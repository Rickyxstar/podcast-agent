package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
	"github.com/Rickyxstar/podcast-agent/internal/report"
)

// factCheckResult is the fact-check stage's output. It is returned even
// when the stage fails, so the report can carry whatever was checked.
type factCheckResult struct {
	status report.FactCheckStatus
	claims []report.Claim
}

// factCheck runs the fact-check agent in three steps:
//
//  1. Plan: one structured call extracts every claim from the transcript
//     and classifies it as factual, anecdote, prediction or opinion.
//  2. Triage: anecdotes, predictions and opinions are settled in code as
//     unverifiable, since no search can check them.
//  3. Verify: a tool loop in which the model searches for evidence and
//     calls submit_verdict for each factual claim.
//
// Verify ends when every claim has a verdict, or when it runs out of model
// calls or tokens, in which case the claims still open are marked
// unverifiable. If a model call fails, the stage returns the verdicts
// reached so far.
func (j *job) factCheck(ctx context.Context, m *meter) (*factCheckResult, error) {
	system := fmt.Sprintf(factCheckSystem, j.cfg.Now().Format(time.DateOnly), j.recordedAt())

	plan, err := j.planClaims(ctx, m, system)
	if err != nil {
		return &factCheckResult{status: report.StatusFailed}, fmt.Errorf("plan: %w", err)
	}
	st := j.triage(ctx, plan)
	if err := j.verifyClaims(ctx, m, system, st); err != nil {
		return st.result(report.StatusPartial), fmt.Errorf("verify: %w", err)
	}
	return st.result(report.StatusCompleted), nil
}

// planClaims is step 1: it asks the model for every claim in the
// transcript, with its type and a strategy for checking it.
func (j *job) planClaims(ctx context.Context, m *meter, system string) ([]planClaim, error) {
	resp, err := j.chat(ctx, "plan", m, llm.ChatRequest{
		System:         system,
		Messages:       []llm.Message{{Role: llm.RoleUser, Text: j.transcriptPrompt() + planPrompt}},
		ResponseSchema: planSchema,
		Effort:         j.cfg.FactCheckEffort,
	})
	if err != nil {
		return nil, err
	}
	var plan struct {
		Claims []planClaim `json:"claims"`
	}
	if err := json.Unmarshal([]byte(resp.Message.Text), &plan); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return plan.Claims, nil
}

// uncheckable gives, for each claim type that no evidence can check, the
// reason recorded with its unverifiable verdict.
var uncheckable = map[report.ClaimType]string{
	report.ClaimAnecdote:   "Personal anecdote; there is no public record to check it against.",
	report.ClaimPrediction: "Prediction about the future; it can't be checked yet.",
	report.ClaimOpinion:    "Opinion, not a factual claim.",
}

// triage is step 2: it assigns claim IDs, settles the uncheckable claims,
// and leaves the factual ones open for verifyClaims.
func (j *job) triage(ctx context.Context, plan []planClaim) *factCheckState {
	st := newFactCheckState()
	counts := map[report.ClaimType]int{}
	for i, p := range plan {
		c := report.Claim{
			ID:        fmt.Sprintf("c%d", i+1),
			Claim:     p.Claim,
			Speaker:   p.Speaker,
			Timestamp: p.Timestamp,
			Type:      p.Type,
		}
		if reason, ok := uncheckable[c.Type]; ok {
			markUnverifiable(&c, reason)
			st.addDecided(c)
		} else {
			if c.Type != report.ClaimFactual {
				j.warnf("claim %s: unknown type %q, checking it as factual", c.ID, c.Type)
				c.Type = report.ClaimFactual
			}
			st.addOpen(c, p.Strategy)
		}
		counts[c.Type]++
		j.emit(ctx, "claim",
			slog.String("id", c.ID),
			slog.String("type", string(c.Type)),
			slog.String("claim", c.Claim),
			slog.String("strategy", p.Strategy),
		)
	}
	j.emit(ctx, "plan",
		slog.Int("claims", len(plan)),
		slog.Int("factual", counts[report.ClaimFactual]),
		slog.Int("predictions", counts[report.ClaimPrediction]),
		slog.Int("anecdotes", counts[report.ClaimAnecdote]),
		slog.Int("opinions", counts[report.ClaimOpinion]),
	)
	return st
}

// verifyClaims is step 3: the tool loop. Each turn the model searches or
// submits verdicts; the loop answers with the tool results and goes again
// until no claims are open or the budget runs out. It returns an error only
// when a model call fails.
func (j *job) verifyClaims(ctx context.Context, m *meter, system string, st *factCheckState) error {
	// This is a new conversation rather than a continuation of the plan:
	// Anthropic binds thinking blocks to the request's tool list, so the
	// plan turn (no tools) can't be replayed into a request with tools. The
	// plan's strategies are carried over in the prompt instead.
	tools := j.factCheckTools(st)
	msgs := []llm.Message{{Role: llm.RoleUser, Text: j.transcriptPrompt() + verifyPrompt(st.pending())}}
	for iter := 1; ; iter++ {
		open := st.pending()
		if len(open) == 0 {
			return nil
		}
		if j.outOfBudget(iter, m, len(open)) {
			st.closeOpen("budget_exhausted: the fact-check stopped before this claim was checked")
			return nil
		}

		resp, err := j.chat(ctx, "fact_check", m, llm.ChatRequest{
			System:   system,
			Messages: msgs,
			Tools:    tools.defs(),
			Effort:   j.cfg.FactCheckEffort,
		})
		if err != nil {
			return err
		}
		msgs = append(msgs, resp.Message, j.reply(ctx, tools, resp.Message, open))
	}
}

// outOfBudget reports whether the verify loop has used its allowance of
// model calls or tokens before call number iter, and warns if so.
func (j *job) outOfBudget(iter int, m *meter, open int) bool {
	if iter > j.cfg.MaxIterations {
		j.warnf("fact-check stopped after %d model calls with %d claims open", j.cfg.MaxIterations, open)
		return true
	}
	if b := j.cfg.TokenBudget; b > 0 && totalTokens(m.usage) >= b {
		j.warnf("fact-check stopped at its %d-token budget with %d claims open", b, open)
		return true
	}
	return false
}

// reply builds the user turn that answers the model's last message: the
// results of its tool calls, or, if it ended its turn with claims still
// open, a reminder to check them.
func (j *job) reply(ctx context.Context, tools toolset, msg llm.Message, open []trackedClaim) llm.Message {
	if len(msg.ToolCalls) == 0 {
		return llm.Message{Role: llm.RoleUser, Text: verifyPrompt(open)}
	}
	for _, c := range msg.ToolCalls {
		j.emit(ctx, "tool_call", slog.String("tool", c.Name), slog.String("input", string(c.Input)))
	}
	results := tools.runAll(ctx, msg.ToolCalls)
	for _, r := range results {
		j.emit(ctx, "tool_result", slog.String("tool", r.Name), slog.Bool("error", r.IsError), slog.Int("bytes", len(r.Content)))
	}
	return llm.Message{Role: llm.RoleUser, ToolResults: results}
}

func (j *job) transcriptPrompt() string {
	return "<transcript>\n" + j.lines + "</transcript>\n\n"
}

func (j *job) recordedAt() string {
	if t := j.cfg.RecordedAt; t != nil {
		return "on " + t.Format(time.DateOnly)
	}
	return "on an unknown date; infer the era from the content if you need it"
}

func totalTokens(u llm.Usage) int {
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// planClaim is one claim the model extracted in the plan step.
type planClaim struct {
	Claim     string           `json:"claim"`
	Speaker   string           `json:"speaker"`
	Timestamp string           `json:"timestamp"`
	Type      report.ClaimType `json:"type"`
	// Strategy is how the model intends to check the claim, for the trace.
	Strategy string `json:"strategy"`
}

var planSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "claims": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "claim": {"type": "string"},
          "speaker": {"type": "string"},
          "timestamp": {"type": "string"},
          "type": {"type": "string", "enum": ["factual", "prediction", "anecdote", "opinion"]},
          "strategy": {"type": "string"}
        },
        "required": ["claim", "speaker", "timestamp", "type", "strategy"],
        "additionalProperties": false
      }
    }
  },
  "required": ["claims"],
  "additionalProperties": false
}`)

const factCheckSystem = `You are a fact-checker for an ad agency that publishes podcast highlights. Nothing should be published that a reader could show to be false.

Today's date is %s. The episode was recorded %s. A claim that was true when recorded but has since been overtaken by events is outdated_or_inaccurate, not verified.

Claim types:
- factual: a checkable statement about the world (companies, numbers, dates, laws, studies, events).
- anecdote: the speaker's own experience, e.g. "we reached break-even in 18 months".
- prediction: a statement about the future.
- opinion: a judgment or preference.

Verdicts:
- verified: current evidence from a trusted source supports the claim.
- outdated_or_inaccurate: evidence contradicts the claim, or it was once true but no longer is.
- unverifiable: the evidence you found is insufficient either way.

Search results carry a kind: "kb" is a curated knowledge base and is trusted; "web" is of unknown quality. Prefer the knowledge base, then the web. Cite evidence only by the result ids the search tools returned.`

const planPrompt = `List every claim in the transcript that a listener might repeat as fact, with the speaker and timestamp of the line it comes from. Classify each one, and for factual claims say briefly how you would check it (what to search for, and where).`

// verifyPrompt asks the model to check the open claims.
func verifyPrompt(open []trackedClaim) string {
	var b strings.Builder
	b.WriteString("Check each of these factual claims. Search for evidence, then call submit_verdict once per claim. You may call several tools at once.\n\n")
	for _, c := range open {
		fmt.Fprintf(&b, "- %s (%s, %s): %s\n", c.claim.ID, c.claim.Speaker, c.claim.Timestamp, c.claim.Claim)
		if c.strategy != "" {
			fmt.Fprintf(&b, "  Plan: %s\n", c.strategy)
		}
	}
	return b.String()
}
