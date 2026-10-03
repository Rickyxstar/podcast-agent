package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
	"github.com/Rickyxstar/podcast-agent/internal/report"
	"github.com/Rickyxstar/podcast-agent/internal/search"
	searchmock "github.com/Rickyxstar/podcast-agent/internal/search/mock"
)

// newTestJob returns a job for testEpisode, ingested and ready for
// factCheck. searches replaces the default knowledge base when given.
func newTestJob(t *testing.T, p llm.Provider, cfg Config, searches ...search.Provider) *job {
	t.Helper()
	a := newTestAgent(t, p, cfg)
	if len(searches) > 0 {
		a.searches = searches
	}
	j := &job{Agent: a, ep: testEpisode, traceID: "test", log: a.cfg.Logger}
	j.ingest()
	return j
}

func planJSON(t *testing.T, claims ...planClaim) string {
	t.Helper()
	b, err := json.Marshal(map[string][]planClaim{"claims": claims})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var (
	gitlabClaim = planClaim{Claim: "GitLab has been all-remote since its founding", Speaker: "Mark", Timestamp: "01:20", Type: report.ClaimFactual, Strategy: "search kb for GitLab remote"}
	slackClaim  = planClaim{Claim: "Slack was founded in 2009", Speaker: "Sarah", Timestamp: "00:30", Type: report.ClaimFactual, Strategy: "search for Slack founding year"}
	breakEven   = planClaim{Claim: "Reached break-even in 18 months", Speaker: "Mark", Timestamp: "02:10", Type: report.ClaimAnecdote}
)

const searchGitLab = `{"query": "GitLab all-remote founding"}`

const verifyGitLab = `{"claim_id": "c1", "verdict": "verified",
	"evidence": [{"result_id": "gitlab-all-remote", "stance": "supports"}],
	"reasoning": "The GitLab Handbook says so.", "self_rating": 0.8}`

// lastMessage returns the final message of req.
func lastMessage(req llm.ChatRequest) llm.Message {
	return req.Messages[len(req.Messages)-1]
}

func claimIDs(claims []report.Claim) []string {
	ids := make([]string, len(claims))
	for i, c := range claims {
		ids[i] = c.ID
	}
	return ids
}

func TestTriage(t *testing.T) {
	j := newTestJob(t, newMockLLM(t), Config{})
	plan := []planClaim{
		gitlabClaim,
		breakEven,
		{Claim: "Offices will be gone by 2030", Type: report.ClaimPrediction},
		{Claim: "Async is better", Type: report.ClaimOpinion},
		{Claim: "Zoom was founded in 2011", Type: "rumor", Strategy: "search Zoom founding"},
	}
	st := j.triage(context.Background(), plan)

	open := st.pending()
	if len(open) != 2 || open[0].claim.ID != "c1" || open[1].claim.ID != "c5" {
		t.Fatalf("pending = %+v, want c1 and c5", open)
	}
	if open[0].strategy != gitlabClaim.Strategy {
		t.Errorf("c1 strategy = %q, want %q", open[0].strategy, gitlabClaim.Strategy)
	}
	if open[1].claim.Type != report.ClaimFactual {
		t.Errorf("c5 type = %q, want unknown type checked as factual", open[1].claim.Type)
	}
	if !containsPrefix(j.warnings, `claim c5: unknown type "rumor"`) {
		t.Errorf("warnings %q missing unknown type", j.warnings)
	}

	decided := st.result(report.StatusCompleted).claims
	if got := claimIDs(decided); !slices.Equal(got, []string{"c2", "c3", "c4"}) {
		t.Fatalf("decided = %v, want c2 c3 c4", got)
	}
	for _, c := range decided {
		if c.Verdict != report.VerdictUnverifiable || c.Confidence != noEvidenceConfidence || c.Reasoning != uncheckable[c.Type] {
			t.Errorf("%s (%s) = %s %v %q, want unverifiable with the %s reason", c.ID, c.Type, c.Verdict, c.Confidence, c.Reasoning, c.Type)
		}
	}
	if c := decided[0]; c.Claim != breakEven.Claim || c.Speaker != "Mark" || c.Timestamp != "02:10" {
		t.Errorf("c2 = %+v, want the plan's claim, speaker and timestamp", c)
	}
}

func TestFactCheckRequests(t *testing.T) {
	recorded := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	p := newMockLLM(t)
	p.EXPECT().Chat(mock.Anything, planReq).
		Run(func(_ context.Context, req llm.ChatRequest) {
			for _, want := range []string{"Today's date is 2026-10-03.", "recorded on 2025-06-01."} {
				if !strings.Contains(req.System, want) {
					t.Errorf("system prompt missing %q", want)
				}
			}
			if req.Model != "test-model" || req.Effort != llm.EffortHigh || string(req.ResponseSchema) != string(planSchema) {
				t.Errorf("plan request = model %q effort %q, schema set %v", req.Model, req.Effort, req.ResponseSchema != nil)
			}
			if !strings.Contains(req.Messages[0].Text, "[01:20] Mark [Deep Dive]: GitLab") {
				t.Errorf("plan prompt missing the transcript: %q", req.Messages[0].Text)
			}
		}).
		Return(reply(planJSON(t, gitlabClaim)), nil).Once()
	p.EXPECT().Chat(mock.Anything, loopReq).
		Run(func(_ context.Context, req llm.ChatRequest) {
			var names []string
			for _, d := range req.Tools {
				names = append(names, d.Name)
			}
			if want := []string{"get_current_date", "search_kb", "submit_verdict"}; !slices.Equal(names, want) {
				t.Errorf("tools = %v, want %v", names, want)
			}
			if req.Model != "test-model" || req.Effort != llm.EffortHigh || req.ResponseSchema != nil {
				t.Errorf("loop request = model %q effort %q, schema set %v", req.Model, req.Effort, req.ResponseSchema != nil)
			}
			if len(req.Messages) != 1 {
				t.Fatalf("loop starts with %d messages, want 1", len(req.Messages))
			}
			text := req.Messages[0].Text
			for _, want := range []string{"<transcript>", "- c1 (Mark, 01:20): GitLab", "Plan: search kb for GitLab remote"} {
				if !strings.Contains(text, want) {
					t.Errorf("verify prompt missing %q:\n%s", want, text)
				}
			}
		}).
		Return(toolUse(call("t1", "submit_verdict", `{"claim_id": "c1", "verdict": "unverifiable", "evidence": [], "reasoning": "x", "self_rating": 0.5}`)), nil).Once()

	j := newTestJob(t, p, Config{Model: "test-model", RecordedAt: &recorded})
	if _, err := j.factCheck(context.Background(), &meter{}); err != nil {
		t.Fatal(err)
	}
}

func TestFactCheckUnknownRecordingDate(t *testing.T) {
	p := newMockLLM(t)
	p.EXPECT().Chat(mock.Anything, planReq).
		Run(func(_ context.Context, req llm.ChatRequest) {
			if !strings.Contains(req.System, "recorded on an unknown date") {
				t.Errorf("system prompt missing the unknown recording date:\n%s", req.System)
			}
		}).
		Return(reply(`{"claims": []}`), nil).Once()
	if _, err := newTestJob(t, p, Config{}).factCheck(context.Background(), &meter{}); err != nil {
		t.Fatal(err)
	}
}

func TestFactCheckPlanFailure(t *testing.T) {
	refusal := reply("")
	refusal.StopReason = llm.StopRefusal
	truncated := reply(`{"claims": [`)
	truncated.StopReason = llm.StopMaxTokens
	boom := errors.New("boom")

	tests := []struct {
		name    string
		resp    *llm.ChatResponse
		err     error
		wantErr error
		wantMsg string
	}{
		{name: "provider error", err: boom, wantErr: boom},
		{name: "refusal", resp: refusal, wantErr: ErrRefusal},
		{name: "max tokens", resp: truncated, wantErr: ErrMaxTokens},
		{name: "bad json", resp: reply("not json"), wantMsg: "plan: decode:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newMockLLM(t)
			p.EXPECT().Chat(mock.Anything, planReq).Return(tt.resp, tt.err).Once()
			res, err := newTestJob(t, p, Config{}).factCheck(context.Background(), &meter{})
			if err == nil || !strings.HasPrefix(err.Error(), "plan: ") {
				t.Fatalf("err = %v, want a plan error", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantMsg != "" && !strings.HasPrefix(err.Error(), tt.wantMsg) {
				t.Errorf("err = %v, want prefix %q", err, tt.wantMsg)
			}
			if res.status != report.StatusFailed || len(res.claims) != 0 {
				t.Errorf("result = %s with %d claims, want failed with none", res.status, len(res.claims))
			}
		})
	}
}

func TestFactCheckNoFactualClaims(t *testing.T) {
	p := newMockLLM(t)
	// No loopReq expectation: the mock fails the test if the loop runs.
	p.EXPECT().Chat(mock.Anything, planReq).Return(reply(planJSON(t, breakEven)), nil).Once()
	m := &meter{}
	res, err := newTestJob(t, p, Config{}).factCheck(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if res.status != report.StatusCompleted || len(res.claims) != 1 || res.claims[0].Verdict != report.VerdictUnverifiable {
		t.Errorf("result = %s %+v, want completed with one unverifiable claim", res.status, res.claims)
	}
	if m.calls != 1 {
		t.Errorf("model calls = %d, want 1", m.calls)
	}
}

func TestFactCheckVerifyFailureKeepsVerdicts(t *testing.T) {
	p := newMockLLM(t)
	p.EXPECT().Chat(mock.Anything, planReq).Return(reply(planJSON(t, gitlabClaim, slackClaim, breakEven)), nil).Once()
	p.EXPECT().Chat(mock.Anything, loopReq).Return(toolUse(call("t1", "search_kb", searchGitLab)), nil).Once()
	p.EXPECT().Chat(mock.Anything, loopReq).Return(toolUse(call("t2", "submit_verdict", verifyGitLab)), nil).Once()
	p.EXPECT().Chat(mock.Anything, loopReq).Return(nil, errors.New("boom")).Once()

	res, err := newTestJob(t, p, Config{}).factCheck(context.Background(), &meter{})
	if err == nil || err.Error() != "verify: boom" {
		t.Fatalf("err = %v, want verify: boom", err)
	}
	if res.status != report.StatusPartial {
		t.Errorf("status = %s, want partial", res.status)
	}
	// c2 never got a verdict, so it is left out rather than reported open.
	if got := claimIDs(res.claims); !slices.Equal(got, []string{"c1", "c3"}) {
		t.Errorf("claims = %v, want c1 c3", got)
	}
	if res.claims[0].Verdict != report.VerdictVerified {
		t.Errorf("c1 = %s, want verified", res.claims[0].Verdict)
	}
}

func TestFactCheckTokenBudget(t *testing.T) {
	// Every response uses 110 tokens. The plan and two loop calls reach 330,
	// past the 300-token budget, so a third loop call is never made.
	p := newMockLLM(t)
	p.EXPECT().Chat(mock.Anything, planReq).Return(reply(planJSON(t, gitlabClaim, breakEven)), nil).Once()
	p.EXPECT().Chat(mock.Anything, loopReq).Return(toolUse(call("t", "get_current_date", `{}`)), nil).Times(2)

	j := newTestJob(t, p, Config{TokenBudget: 300})
	m := &meter{}
	res, err := j.factCheck(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if res.status != report.StatusCompleted {
		t.Errorf("status = %s, want completed", res.status)
	}
	if c := res.claims[0]; c.Verdict != report.VerdictUnverifiable || !strings.HasPrefix(c.Reasoning, "budget_exhausted") {
		t.Errorf("c1 = %s %q, want unverifiable budget_exhausted", c.Verdict, c.Reasoning)
	}
	if !containsPrefix(j.warnings, "fact-check stopped at its 300-token budget with 1 claims open") {
		t.Errorf("warnings %q missing token budget", j.warnings)
	}
	if got := totalTokens(m.usage); got != 330 {
		t.Errorf("tokens = %d, want 330", got)
	}
}

func TestFactCheckRemindsOfOpenClaims(t *testing.T) {
	p := newMockLLM(t)
	p.EXPECT().Chat(mock.Anything, planReq).Return(reply(planJSON(t, gitlabClaim, slackClaim)), nil).Once()
	p.EXPECT().Chat(mock.Anything, loopReq).Return(toolUse(call("t1", "search_kb", searchGitLab)), nil).Once()
	p.EXPECT().Chat(mock.Anything, loopReq).Return(toolUse(call("t2", "submit_verdict", verifyGitLab)), nil).Once()
	// The model ends its turn with c2 still open.
	p.EXPECT().Chat(mock.Anything, loopReq).Return(reply("All done."), nil).Once()
	p.EXPECT().Chat(mock.Anything, loopReq).
		Run(func(_ context.Context, req llm.ChatRequest) {
			last := lastMessage(req)
			if last.Role != llm.RoleUser || len(last.ToolResults) != 0 {
				t.Fatalf("last message = %+v, want a user reminder", last)
			}
			if !strings.Contains(last.Text, "- c2 (Sarah, 00:30): Slack") || !strings.Contains(last.Text, "Plan: search for Slack founding year") {
				t.Errorf("reminder missing c2:\n%s", last.Text)
			}
			if strings.Contains(last.Text, "- c1 ") {
				t.Errorf("reminder lists c1, which has a verdict:\n%s", last.Text)
			}
			if strings.Contains(last.Text, "<transcript>") {
				t.Errorf("reminder repeats the transcript")
			}
		}).
		Return(toolUse(call("t3", "submit_verdict", `{"claim_id": "c2", "verdict": "unverifiable", "evidence": [], "reasoning": "Nothing found.", "self_rating": 0.3}`)), nil).Once()

	res, err := newTestJob(t, p, Config{}).factCheck(context.Background(), &meter{})
	if err != nil {
		t.Fatal(err)
	}
	if res.status != report.StatusCompleted || len(res.claims) != 2 {
		t.Fatalf("result = %s with %d claims, want completed with 2", res.status, len(res.claims))
	}
	if c := res.claims[1]; c.Verdict != report.VerdictUnverifiable || c.Confidence != noEvidenceConfidence || c.Reasoning != "Nothing found." {
		t.Errorf("c2 = %+v", c)
	}
}

func TestFactCheckToolResults(t *testing.T) {
	web := searchmock.NewMockProvider(t)
	web.EXPECT().Name().Return("web").Maybe()
	web.EXPECT().Search(mock.Anything, search.Request{Query: "GitLab"}).Return(nil, errors.New("rate limited")).Once()

	p := newMockLLM(t)
	p.EXPECT().Chat(mock.Anything, planReq).Return(reply(planJSON(t, gitlabClaim)), nil).Once()
	p.EXPECT().Chat(mock.Anything, loopReq).Return(toolUse(
		call("t1", "search_web", `{"query": " "}`),
		call("t2", "search_web", `{"query": "GitLab"}`),
		call("t3", "search_news", `{"query": "GitLab"}`),
		call("t4", "get_current_date", ``),
		call("t5", "submit_verdict", `{"claim_id": `),
		call("t6", "submit_verdict", `{"claim_id": "c1", "verdict": "verified", "evidence": [], "reasoning": "x", "self_rating": 1}`),
	), nil).Once()
	p.EXPECT().Chat(mock.Anything, loopReq).
		Run(func(_ context.Context, req llm.ChatRequest) {
			// The history is the opening prompt, the tool calls and their results.
			if len(req.Messages) != 3 || req.Messages[1].Role != llm.RoleAssistant {
				t.Fatalf("got %d messages, want prompt, tool calls, results", len(req.Messages))
			}
			want := []struct {
				name    string
				isError bool
				content string
			}{
				{"search_web", true, "query is required"},
				{"search_web", true, "search failed: rate limited"},
				{"search_news", true, `unknown tool "search_news"`},
				{"get_current_date", false, "2026-10-03"},
				{"submit_verdict", true, "invalid arguments:"},
				{"submit_verdict", true, errNoEvidence.Error()},
			}
			got := lastMessage(req).ToolResults
			if len(got) != len(want) {
				t.Fatalf("got %d tool results, want %d: %+v", len(got), len(want), got)
			}
			for i, w := range want {
				r := got[i]
				if id := "t" + string(rune('1'+i)); r.ToolCallID != id || r.Name != w.name {
					t.Errorf("result %d answers %s %s, want %s %s", i, r.ToolCallID, r.Name, id, w.name)
				}
				if r.IsError != w.isError || !strings.HasPrefix(r.Content, w.content) {
					t.Errorf("result %d = error %v %q, want error %v %q", i, r.IsError, r.Content, w.isError, w.content)
				}
			}
		}).
		Return(toolUse(call("t7", "get_current_date", `{}`)), nil).Once()

	res, err := newTestJob(t, p, Config{MaxIterations: 2}, web).factCheck(context.Background(), &meter{})
	if err != nil {
		t.Fatal(err)
	}
	if c := res.claims[0]; !strings.HasPrefix(c.Reasoning, "budget_exhausted") {
		t.Errorf("c1 = %s %q, want budget_exhausted", c.Verdict, c.Reasoning)
	}
}

func TestSubmitVerdict(t *testing.T) {
	published := testNow.AddDate(-2, 0, 0)
	webResult := search.Result{ID: "https://example.com/gitlab", Kind: search.KindWeb, Source: "example.com", Snippet: "GitLab opened an office.", URL: "https://example.com/gitlab", Published: &published}

	newState := func() *factCheckState {
		st := newFactCheckState()
		st.addOpen(report.Claim{ID: "c1", Claim: gitlabClaim.Claim, Type: report.ClaimFactual}, "")
		st.addDecided(report.Claim{ID: "c2", Type: report.ClaimAnecdote, Verdict: report.VerdictUnverifiable})
		st.remember([]search.Result{webResult})
		return st
	}

	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"unknown claim", `{"claim_id": "c9", "verdict": "unverifiable"}`, `unknown claim_id "c9"`},
		{"already decided", `{"claim_id": "c2", "verdict": "unverifiable"}`, "claim c2 already has a verdict"},
		{"unknown verdict", `{"claim_id": "c1", "verdict": "probably"}`, `unknown verdict "probably"`},
		{"verified without evidence", `{"claim_id": "c1", "verdict": "verified"}`, errNoEvidence.Error()},
		{"outdated without evidence", `{"claim_id": "c1", "verdict": "outdated_or_inaccurate"}`, errNoEvidence.Error()},
		{"evidence not shown", `{"claim_id": "c1", "verdict": "verified", "evidence": [{"result_id": "gitlab-all-remote", "stance": "supports"}]}`, `evidence "gitlab-all-remote" is not a search result you were shown`},
		{"unverifiable may cite nothing", `{"claim_id": "c1", "verdict": "unverifiable", "reasoning": "Nothing found."}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newState()
			in, err := decodeInput[verdictInput](json.RawMessage(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			_, err = st.submit(in, testNow)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("submit: %v", err)
				}
				if open := st.pending(); len(open) != 0 {
					t.Errorf("pending = %+v, want none", open)
				}
				return
			}
			if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
			// A rejected verdict leaves the claim open for the model to retry.
			if open := st.pending(); len(open) != 1 || open[0].claim.ID != "c1" {
				t.Errorf("pending = %+v, want c1", open)
			}
		})
	}

	t.Run("records verdict", func(t *testing.T) {
		st := newState()
		c, err := st.submit(verdictInput{
			ClaimID:    "c1",
			Verdict:    report.VerdictOutdatedOrInaccurate,
			Evidence:   []evidenceInput{{ResultID: webResult.ID, Stance: stanceContradicts}},
			Reasoning:  "GitLab has an office now.",
			SelfRating: 1,
		}, testNow)
		if err != nil {
			t.Fatal(err)
		}
		// 0.35·0.4 (web) + 0.30·1 (agrees) + 0.20·0.7 (two years old) + 0.15·1
		if c.Verdict != report.VerdictOutdatedOrInaccurate || c.Confidence != 0.73 || c.Reasoning != "GitLab has an office now." {
			t.Errorf("claim = %s %v %q, want outdated_or_inaccurate 0.73", c.Verdict, c.Confidence, c.Reasoning)
		}
		want := report.Evidence{Source: "example.com", Snippet: webResult.Snippet, URL: webResult.URL, Date: published.Format(time.DateOnly)}
		if len(c.Evidence) != 1 || c.Evidence[0] != want {
			t.Errorf("evidence = %+v, want %+v", c.Evidence, want)
		}
		if res := st.result(report.StatusCompleted); len(res.claims) != 2 || res.claims[0].Verdict != report.VerdictOutdatedOrInaccurate {
			t.Errorf("result = %+v, want c1 recorded in plan order", res.claims)
		}
	})
}
