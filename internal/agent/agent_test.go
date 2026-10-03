package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
	"github.com/Rickyxstar/podcast-agent/internal/report"
	"github.com/Rickyxstar/podcast-agent/internal/search"
	"github.com/Rickyxstar/podcast-agent/internal/search/kb"
	"github.com/Rickyxstar/podcast-agent/internal/transcript"
)

var testNow = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

var testEpisode = &transcript.Episode{
	EpisodeID: "ep001",
	Title:     "The Future of Remote Work",
	Host:      "Sarah Johnson",
	Guests:    []string{"Mark Rivera"},
	Transcript: []transcript.Segment{
		{Timestamp: "00:00", Speaker: "Sarah", Section: "Introduction", Text: "Welcome back. Today we’re diving into remote work."},
		{Timestamp: "01:20", Speaker: "Mark", Section: "Deep Dive", Text: "GitLab has been all-remote since day one — no offices at all."},
		{Timestamp: "02:10", Speaker: "Mark", Section: "Deep Dive", Text: "At my last startup we hit break-even in 18 months."},
	},
}

// scriptedLLM answers each stage from a function. The summary and plan calls
// run concurrently, so calls are counted under a lock.
type scriptedLLM struct {
	mu    sync.Mutex
	loops int // fact-check loop calls so far
	// summary, plan and loop return the response text or tool calls for
	// their stage; loop gets the 1-based call number.
	summary func() (string, error)
	plan    func() (string, error)
	loop    func(n int, req llm.ChatRequest) []llm.ToolCall
}

func (s *scriptedLLM) Name() string { return "scripted" }

func (s *scriptedLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	resp := &llm.ChatResponse{
		Model:      "test-model",
		StopReason: llm.StopEndTurn,
		Usage:      llm.Usage{InputTokens: 100, OutputTokens: 10},
		Message:    llm.Message{Role: llm.RoleAssistant},
	}
	var err error
	switch {
	case req.System == summarySystem:
		resp.Message.Text, err = s.summary()
	case len(req.Tools) == 0:
		resp.Message.Text, err = s.plan()
	default:
		s.mu.Lock()
		s.loops++
		n := s.loops
		s.mu.Unlock()
		if calls := s.loop(n, req); len(calls) > 0 {
			resp.Message.ToolCalls = calls
			resp.StopReason = llm.StopToolUse
		}
	}
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func goodNotes() (string, error) {
	n := notes{
		Summary:   strings.Repeat("word ", 250),
		Takeaways: []string{"a", "b", "c", "d", "e"},
		Quotes: []noteQuote{
			// Straight apostrophe and dash differ from the transcript, and
			// the timestamp is wrong; validate should fix both.
			{Text: "GitLab has been all-remote since day one - no offices at all.", Speaker: "Mark", Timestamp: "09:99"},
			{Text: "Something nobody said.", Speaker: "Sarah", Timestamp: "00:00"},
		},
		Topics: []string{"remote-work", "Async Culture"},
	}
	b, err := json.Marshal(n)
	return string(b), err
}

func goodPlan() (string, error) {
	return `{"claims": [
		{"claim": "GitLab has been all-remote since its founding", "speaker": "Mark", "timestamp": "01:20", "type": "factual", "strategy": "search kb for GitLab remote"},
		{"claim": "Reached break-even in 18 months", "speaker": "Mark", "timestamp": "02:10", "type": "anecdote", "strategy": "none"}
	]}`, nil
}

func call(id, name, input string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Input: json.RawMessage(input)}
}

func newTestAgent(t *testing.T, p llm.Provider, cfg Config) *Agent {
	t.Helper()
	k, err := kb.New([]kb.Entry{{
		ID:        "gitlab-all-remote",
		Statement: "GitLab has been an all-remote company since its founding, with no company offices.",
		Source:    "GitLab Handbook",
		URL:       "https://handbook.gitlab.com/",
		Date:      "2026-01",
	}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Now = func() time.Time { return testNow }
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(p, []search.Provider{k}, cfg)
}

func TestRun(t *testing.T) {
	p := &scriptedLLM{
		summary: goodNotes,
		plan:    goodPlan,
		loop: func(n int, req llm.ChatRequest) []llm.ToolCall {
			switch n {
			case 1:
				return []llm.ToolCall{call("t1", "search_kb", `{"query": "GitLab all-remote founding"}`)}
			case 2:
				// The first verdict cites a made-up result and is rejected;
				// the model is expected to retry.
				return []llm.ToolCall{call("t2", "submit_verdict", `{"claim_id": "c1", "verdict": "verified",
					"evidence": [{"result_id": "invented", "stance": "supports"}], "reasoning": "x", "self_rating": 0.8}`)}
			case 3:
				last := req.Messages[len(req.Messages)-1]
				if len(last.ToolResults) != 1 || !last.ToolResults[0].IsError {
					t.Errorf("want an error result for the invented evidence, got %+v", last.ToolResults)
				}
				return []llm.ToolCall{call("t3", "submit_verdict", `{"claim_id": "c1", "verdict": "verified",
					"evidence": [{"result_id": "gitlab-all-remote", "stance": "supports"}],
					"reasoning": "The GitLab Handbook says so.", "self_rating": 0.8}`)}
			}
			t.Errorf("unexpected loop call %d", n)
			return nil
		},
	}
	rep, err := newTestAgent(t, p, Config{}).Run(context.Background(), testEpisode)
	if err != nil {
		t.Fatal(err)
	}

	if rep.Episode.Duration != "02:10" {
		t.Errorf("Duration = %q, want 02:10", rep.Episode.Duration)
	}
	if len(rep.Quotes) != 2 {
		t.Fatalf("got %d quotes, want 2", len(rep.Quotes))
	}
	if q := rep.Quotes[0]; !q.Verified || q.Timestamp != "01:20" {
		t.Errorf("quote 0 = %+v, want verified at 01:20", q)
	}
	if rep.Quotes[1].Verified {
		t.Errorf("quote 1 verified, want not found")
	}

	fc := rep.FactCheck
	if fc.Status != report.StatusCompleted || len(fc.Claims) != 2 {
		t.Fatalf("fact check = %s with %d claims, want completed with 2", fc.Status, len(fc.Claims))
	}
	c1 := fc.Claims[0]
	if c1.ID != "c1" || c1.Verdict != report.VerdictVerified {
		t.Errorf("c1 = %s %s, want c1 verified", c1.ID, c1.Verdict)
	}
	// 0.35·1.0 (kb) + 0.30·1 (agrees) + 0.20·1.0 (under a year old) + 0.15·0.8
	if c1.Confidence != 0.97 {
		t.Errorf("c1 confidence = %v, want 0.97", c1.Confidence)
	}
	if len(c1.Evidence) != 1 || c1.Evidence[0].Source != "kb:gitlab-all-remote" || c1.Evidence[0].Date != "2026-01-01" {
		t.Errorf("c1 evidence = %+v", c1.Evidence)
	}
	if c2 := fc.Claims[1]; c2.Verdict != report.VerdictUnverifiable || c2.Confidence != noEvidenceConfidence {
		t.Errorf("anecdote = %s %v, want unverifiable %v", c2.Verdict, c2.Confidence, noEvidenceConfidence)
	}

	// summary + plan + 3 loop calls
	if rep.Run.Tokens.Input != 500 || rep.Run.Tokens.Output != 50 {
		t.Errorf("tokens = %+v, want 500 in / 50 out", rep.Run.Tokens)
	}
	if rep.Run.Provider != "scripted" || rep.Run.Model != "test-model" || rep.Run.TraceID == "" {
		t.Errorf("run = %+v", rep.Run)
	}
	wantWarnings := []string{"quote not found", `topic "Async Culture"`}
	for _, w := range wantWarnings {
		if !containsPrefix(rep.Run.Warnings, w) {
			t.Errorf("warnings %q missing %q", rep.Run.Warnings, w)
		}
	}
}

func TestRunLoopCap(t *testing.T) {
	p := &scriptedLLM{
		summary: goodNotes,
		plan:    goodPlan,
		loop: func(int, llm.ChatRequest) []llm.ToolCall {
			return []llm.ToolCall{call("t", "get_current_date", `{}`)}
		},
	}
	rep, err := newTestAgent(t, p, Config{MaxIterations: 3}).Run(context.Background(), testEpisode)
	if err != nil {
		t.Fatal(err)
	}
	if p.loops != 3 {
		t.Errorf("loop calls = %d, want 3", p.loops)
	}
	c1 := rep.FactCheck.Claims[0]
	if c1.Verdict != report.VerdictUnverifiable || !strings.HasPrefix(c1.Reasoning, "budget_exhausted") {
		t.Errorf("c1 = %s %q, want unverifiable budget_exhausted", c1.Verdict, c1.Reasoning)
	}
	if !containsPrefix(rep.Run.Warnings, "fact-check stopped after 3") {
		t.Errorf("warnings %q missing loop cap", rep.Run.Warnings)
	}
}

func TestRunFactCheckFailureKeepsSummary(t *testing.T) {
	p := &scriptedLLM{
		summary: goodNotes,
		plan:    func() (string, error) { return "", errors.New("boom") },
	}
	rep, err := newTestAgent(t, p, Config{}).Run(context.Background(), testEpisode)
	if err != nil {
		t.Fatal(err)
	}
	if rep.FactCheck.Status != report.StatusFailed || rep.Summary == "" {
		t.Errorf("status = %s, summary %d bytes; want failed with a summary", rep.FactCheck.Status, len(rep.Summary))
	}
	if !containsPrefix(rep.Run.Warnings, "fact-check failed: plan: boom") {
		t.Errorf("warnings %q missing fact-check failure", rep.Run.Warnings)
	}
}

func TestRunSummaryFailure(t *testing.T) {
	p := &scriptedLLM{
		summary: func() (string, error) { return "", errors.New("boom") },
		plan:    func() (string, error) { return `{"claims": []}`, nil },
	}
	if _, err := newTestAgent(t, p, Config{}).Run(context.Background(), testEpisode); err == nil {
		t.Fatal("Run succeeded, want summary error")
	}
}

func containsPrefix(ss []string, prefix string) bool {
	for _, s := range ss {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
