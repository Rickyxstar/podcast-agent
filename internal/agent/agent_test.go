package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
	llmmock "github.com/Rickyxstar/podcast-agent/internal/llm/mock"
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

// Matchers for the three model stages. The summary and plan calls run
// concurrently, so expectations match on the request rather than call order.
var (
	summaryReq = mock.MatchedBy(func(r llm.ChatRequest) bool { return r.System == summarySystem })
	planReq    = mock.MatchedBy(func(r llm.ChatRequest) bool { return r.System != summarySystem && len(r.Tools) == 0 })
	loopReq    = mock.MatchedBy(func(r llm.ChatRequest) bool { return r.System != summarySystem && len(r.Tools) > 0 })
)

func newMockLLM(t *testing.T) *llmmock.MockProvider {
	p := llmmock.NewMockProvider(t)
	p.EXPECT().Name().Return("mock").Maybe()
	return p
}

// reply returns a final text response; toolUse returns one that calls tools.
func reply(text string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Model:      "test-model",
		StopReason: llm.StopEndTurn,
		Usage:      llm.Usage{InputTokens: 100, OutputTokens: 10},
		Message:    llm.Message{Role: llm.RoleAssistant, Text: text},
	}
}

func toolUse(calls ...llm.ToolCall) *llm.ChatResponse {
	resp := reply("")
	resp.StopReason = llm.StopToolUse
	resp.Message.ToolCalls = calls
	return resp
}

func goodNotes(t *testing.T) string {
	t.Helper()
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
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const goodPlan = `{"claims": [
		{"claim": "GitLab has been all-remote since its founding", "speaker": "Mark", "timestamp": "01:20", "type": "factual", "strategy": "search kb for GitLab remote"},
		{"claim": "Reached break-even in 18 months", "speaker": "Mark", "timestamp": "02:10", "type": "anecdote", "strategy": "none"}
	]}`

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
	p := newMockLLM(t)
	p.EXPECT().Chat(mock.Anything, summaryReq).Return(reply(goodNotes(t)), nil).Once()
	p.EXPECT().Chat(mock.Anything, planReq).Return(reply(goodPlan), nil).Once()
	// Loop expectations are consumed in order.
	p.EXPECT().Chat(mock.Anything, loopReq).
		Return(toolUse(call("t1", "search_kb", `{"query": "GitLab all-remote founding"}`)), nil).Once()
	// The first verdict cites a made-up result and is rejected; the model is
	// expected to retry.
	p.EXPECT().Chat(mock.Anything, loopReq).
		Return(toolUse(call("t2", "submit_verdict", `{"claim_id": "c1", "verdict": "verified",
			"evidence": [{"result_id": "invented", "stance": "supports"}], "reasoning": "x", "self_rating": 0.8}`)), nil).Once()
	p.EXPECT().Chat(mock.Anything, loopReq).
		Run(func(_ context.Context, req llm.ChatRequest) {
			last := req.Messages[len(req.Messages)-1]
			if len(last.ToolResults) != 1 || !last.ToolResults[0].IsError {
				t.Errorf("want an error result for the invented evidence, got %+v", last.ToolResults)
			}
		}).
		Return(toolUse(call("t3", "submit_verdict", `{"claim_id": "c1", "verdict": "verified",
			"evidence": [{"result_id": "gitlab-all-remote", "stance": "supports"}],
			"reasoning": "The GitLab Handbook says so.", "self_rating": 0.8}`)), nil).Once()
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
	if rep.Run.Provider != "mock" || rep.Run.Model != "test-model" || rep.Run.TraceID == "" {
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
	p := newMockLLM(t)
	p.EXPECT().Chat(mock.Anything, summaryReq).Return(reply(goodNotes(t)), nil).Once()
	p.EXPECT().Chat(mock.Anything, planReq).Return(reply(goodPlan), nil).Once()
	p.EXPECT().Chat(mock.Anything, loopReq).
		Return(toolUse(call("t", "get_current_date", `{}`)), nil).Times(3)
	rep, err := newTestAgent(t, p, Config{MaxIterations: 3}).Run(context.Background(), testEpisode)
	if err != nil {
		t.Fatal(err)
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
	p := newMockLLM(t)
	p.EXPECT().Chat(mock.Anything, summaryReq).Return(reply(goodNotes(t)), nil).Once()
	p.EXPECT().Chat(mock.Anything, planReq).Return(nil, errors.New("boom")).Once()
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
	p := newMockLLM(t)
	p.EXPECT().Chat(mock.Anything, summaryReq).Return(nil, errors.New("boom")).Once()
	p.EXPECT().Chat(mock.Anything, planReq).Return(reply(`{"claims": []}`), nil).Once()
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
