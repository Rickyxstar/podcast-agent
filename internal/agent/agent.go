// Package agent runs the podcast pipeline for one episode: ingest, then the
// summary + notes stage and the fact-check agent concurrently, then
// validation, producing a report.Report.
//
// Only fact-checking is agentic (plan, then a model-driven tool loop). The
// summary is a single structured call, and ingest and validation are plain
// Go, so the model is used only where it earns its cost.
package agent

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
	"github.com/Rickyxstar/podcast-agent/internal/report"
	"github.com/Rickyxstar/podcast-agent/internal/search"
	"github.com/Rickyxstar/podcast-agent/internal/transcript"
)

// Defaults for fields left zero in Config.
const (
	DefaultSummaryEffort   = llm.EffortMedium
	DefaultFactCheckEffort = llm.EffortHigh
	DefaultMaxIterations   = 20
)

// Config tunes the pipeline. Fields left zero use the defaults above.
type Config struct {
	// Model overrides the provider's default model for every stage.
	Model string
	// SummaryEffort and FactCheckEffort set reasoning effort per stage.
	SummaryEffort   llm.Effort
	FactCheckEffort llm.Effort
	// MaxIterations caps model calls in the fact-check tool loop.
	MaxIterations int
	// TokenBudget caps tokens (input, output and cache) spent by the
	// fact-check stage. Zero means no cap beyond MaxIterations.
	TokenBudget int
	// Price converts token usage to Report.Run.CostUSD. Zero reports no cost.
	Price Price
	// RecordedAt is when the episode was recorded, if known. It grounds
	// "outdated" verdicts alongside today's date.
	RecordedAt *time.Time
	// Now is the clock. Nil uses time.Now.
	Now func() time.Time
	// Logger receives trace events. Nil uses slog.Default().
	Logger *slog.Logger
}

// Price is the cost of a model in USD per million tokens.
type Price struct {
	Input, Output, CacheRead, CacheWrite float64
}

// Cost returns the USD cost of u.
func (p Price) Cost(u llm.Usage) float64 {
	return (float64(u.InputTokens)*p.Input +
		float64(u.OutputTokens)*p.Output +
		float64(u.CacheReadTokens)*p.CacheRead +
		float64(u.CacheWriteTokens)*p.CacheWrite) / 1e6
}

// Agent processes episodes. It is safe for concurrent use; each Run is
// independent.
type Agent struct {
	llm      llm.Provider
	searches []search.Provider
	cfg      Config
}

// New returns an Agent that calls p for every model stage and offers each of
// searches to the fact-check agent as its own tool, e.g. "search_kb".
func New(p llm.Provider, searches []search.Provider, cfg Config) *Agent {
	cfg.SummaryEffort = cmp.Or(cfg.SummaryEffort, DefaultSummaryEffort)
	cfg.FactCheckEffort = cmp.Or(cfg.FactCheckEffort, DefaultFactCheckEffort)
	cfg.MaxIterations = cmp.Or(cfg.MaxIterations, DefaultMaxIterations)
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Agent{llm: p, searches: searches, cfg: cfg}
}

// Run produces the report for ep.
//
// A fact-check failure still returns a report, with FactCheck.Status set to
// partial or failed and the error in Run.Warnings. A summary failure returns
// an error, since a report without a summary isn't worth publishing; the
// worker then leaves the message for SQS to retry.
func (a *Agent) Run(ctx context.Context, ep *transcript.Episode) (*report.Report, error) {
	start := time.Now()
	j := &job{
		Agent:   a,
		ep:      ep,
		traceID: newTraceID(),
	}
	j.log = a.cfg.Logger.With("trace_id", j.traceID, "episode", ep.EpisodeID)
	j.ingest()

	var (
		wg                sync.WaitGroup
		n                 *notes
		fc                *factCheckResult
		notesM, factM     meter
		notesErr, factErr error
	)
	// The stages are independent, so neither cancels the other on failure.
	wg.Go(func() { n, notesErr = j.summarize(ctx, &notesM) })
	wg.Go(func() { fc, factErr = j.factCheck(ctx, &factM) })
	wg.Wait()

	if notesErr != nil {
		return nil, fmt.Errorf("agent: %s: summary: %w", ep.EpisodeID, notesErr)
	}
	if factErr != nil {
		j.warnf("fact-check %s: %v", fc.status, factErr)
	}

	rep := &report.Report{
		Episode: report.Episode{
			ID:       ep.EpisodeID,
			Title:    ep.Title,
			Host:     ep.Host,
			Guests:   ep.Guests,
			Duration: j.duration(),
		},
		Summary:   n.Summary,
		Takeaways: n.Takeaways,
		Topics:    n.Topics,
		FactCheck: report.FactCheck{Status: fc.status, Claims: fc.claims},
	}
	rep.Quotes = j.validate(ctx, n)

	usage := notesM.usage
	usage.Add(factM.usage)
	rep.Run = report.Run{
		Provider: a.llm.Name(),
		Model:    cmp.Or(notesM.model, factM.model, a.cfg.Model),
		Tokens: report.Tokens{
			Input:      usage.InputTokens,
			Output:     usage.OutputTokens,
			CacheRead:  usage.CacheReadTokens,
			CacheWrite: usage.CacheWriteTokens,
		},
		CostUSD:    a.cfg.Price.Cost(usage),
		DurationMS: time.Since(start).Milliseconds(),
		TraceID:    j.traceID,
		Warnings:   j.warnings,
	}
	j.emit(ctx, "done",
		slog.String("fact_check", string(fc.status)),
		slog.Int("claims", len(fc.claims)),
		slog.Int("warnings", len(j.warnings)),
		slog.Float64("cost_usd", rep.Run.CostUSD),
		slog.Int64("duration_ms", rep.Run.DurationMS),
	)
	return rep, nil
}

// job is the state of one Run.
type job struct {
	*Agent
	ep      *transcript.Episode
	traceID string
	log     *slog.Logger
	// lines is the transcript rendered for prompts, set by ingest.
	lines string

	mu       sync.Mutex // guards warnings
	warnings []string
}

// warnf records a warning for the report and logs it.
func (j *job) warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	j.log.Warn(msg)
	j.mu.Lock()
	j.warnings = append(j.warnings, msg)
	j.mu.Unlock()
}

// emit logs a trace event: plan, llm_call, tool_call, tool_result, verdict,
// validation or done. These logs are the agent-reasoning deliverable.
//
// TODO: route through internal/trace so events also land in trace.jsonl and
// the --pretty console view.
func (j *job) emit(ctx context.Context, event string, attrs ...slog.Attr) {
	j.log.LogAttrs(ctx, slog.LevelInfo, event, attrs...)
}

// Stop reasons that leave a response unusable.
var (
	ErrRefusal   = errors.New("model refused")
	ErrMaxTokens = errors.New("model output hit max_tokens")
)

// meter totals the usage of one stage's model calls.
type meter struct {
	usage llm.Usage
	model string
	calls int
}

// chat makes one model call for stage, records its usage in m, and returns
// ErrRefusal or ErrMaxTokens when the response can't be used.
func (j *job) chat(ctx context.Context, stage string, m *meter, req llm.ChatRequest) (*llm.ChatResponse, error) {
	req.Model = j.cfg.Model
	start := time.Now()
	resp, err := j.llm.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	m.calls++
	m.usage.Add(resp.Usage)
	if m.model == "" {
		m.model = resp.Model
	}

	j.emit(ctx, "llm_call",
		slog.String("stage", stage),
		slog.String("model", resp.Model),
		slog.String("stop", string(resp.StopReason)),
		slog.Int("tool_calls", len(resp.Message.ToolCalls)),
		slog.Int("input_tokens", resp.Usage.InputTokens),
		slog.Int("output_tokens", resp.Usage.OutputTokens),
		slog.Int("cache_read_tokens", resp.Usage.CacheReadTokens),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()),
	)
	if resp.Message.Thinking != "" {
		j.log.Debug("thinking", "stage", stage, "text", resp.Message.Thinking)
	}

	switch resp.StopReason {
	case llm.StopRefusal:
		return nil, ErrRefusal
	case llm.StopMaxTokens:
		return nil, ErrMaxTokens
	}
	return resp, nil
}

func newTraceID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
