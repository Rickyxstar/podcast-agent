package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
	"github.com/Rickyxstar/podcast-agent/internal/trace"
)

// summarizeJob returns a job whose trace events go to rec.
func summarizeJob(t *testing.T, p llm.Provider, rec *trace.Recorder) *job {
	t.Helper()
	j := newTestJob(t, p, Config{})
	j.sink = rec
	return j
}

// eventsOf returns rec's events of type typ.
func eventsOf(rec *trace.Recorder, typ string) []trace.Event {
	var es []trace.Event
	for _, e := range rec.Events() {
		if e.Type == typ {
			es = append(es, e)
		}
	}
	return es
}

// required returns the required fields of a JSON schema.
func required(t *testing.T, schema json.RawMessage) []string {
	t.Helper()
	var s struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Properties) != len(s.Required) {
		t.Errorf("schema has properties %v but requires %v", s.Properties, s.Required)
	}
	return s.Required
}

func TestSummarizeNoRepair(t *testing.T) {
	p := newMockLLM(t)
	// Any second call fails the test: the mock has no expectation for it.
	p.EXPECT().Chat(mock.Anything, summaryReq).Return(reply(goodNotes(t)), nil).Once()
	var rec trace.Recorder
	j := summarizeJob(t, p, &rec)

	n, err := j.summarize(context.Background(), &meter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Quotes) != 3 || len(j.warnings) != 0 || len(eventsOf(&rec, "repair")) != 0 {
		t.Errorf("quotes %d, warnings %q, %d repair events; want 3, none, none",
			len(n.Quotes), j.warnings, len(eventsOf(&rec, "repair")))
	}
}

func TestSummarizeRepairsQuote(t *testing.T) {
	first := validNotes()
	first.Quotes[2] = noteQuote{Text: "At our last startup we broke even in 18 months.", Speaker: "Mark", Timestamp: "02:10"}
	firstReply := reply(notesJSON(t, *first))

	p := newMockLLM(t)
	p.EXPECT().Chat(mock.Anything, summaryReq).Return(firstReply, nil).Once()
	p.EXPECT().Chat(mock.Anything, repairReq).
		Run(func(_ context.Context, req llm.ChatRequest) {
			if !slices.Equal(required(t, req.ResponseSchema), []string{"quotes"}) {
				t.Errorf("repair schema requires %v, want only quotes", required(t, req.ResponseSchema))
			}
			if got := req.Messages[1]; got.Role != llm.RoleAssistant || got.Text != firstReply.Message.Text {
				t.Errorf("message 1 = %+v, want the first reply unchanged", got)
			}
			prompt := lastMessage(req).Text
			for _, want := range []string{
				`"At our last startup we broke even in 18 months." was not found in the transcript`,
				`The closest line is [02:10] Mark: "At my last startup we hit break-even in 18 months."`,
				`- "Welcome back."`,
				`Return 1 new quote in "quotes"`,
				"Return only quotes.",
			} {
				if !strings.Contains(prompt, want) {
					t.Errorf("repair prompt missing %q:\n%s", want, prompt)
				}
			}
		}).
		// The summary wasn't asked for, so it must be ignored.
		Return(reply(`{"summary": "short", "quotes": [
			{"text": "At my last startup we hit break-even in 18 months.", "speaker": "?", "timestamp": "?"}
		]}`), nil).Once()
	var rec trace.Recorder
	j := summarizeJob(t, p, &rec)
	m := &meter{}

	n, err := j.summarize(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if want := validNotes().Quotes; !slices.Equal(n.Quotes, want) {
		t.Errorf("quotes = %+v, want %+v", n.Quotes, want)
	}
	if n.Summary != first.Summary {
		t.Errorf("summary = %q, want the first one", n.Summary)
	}
	if len(j.warnings) != 0 {
		t.Errorf("warnings = %q, want none", j.warnings)
	}
	if m.calls != 2 || m.usage.InputTokens != 200 {
		t.Errorf("meter = %+v, want both calls counted", m)
	}
	es := eventsOf(&rec, "repair")
	if len(es) != 1 || es[0].Value("quotes_requested").Int64() != 1 || es[0].Value("quotes_accepted").Int64() != 1 {
		t.Errorf("repair events = %+v, want 1 quote requested and accepted", es)
	}
}

func TestSummarizeRepairStillFails(t *testing.T) {
	first := validNotes()
	first.Quotes = append(first.Quotes[:2], noteQuote{Text: "Never said."})

	p := newMockLLM(t)
	p.EXPECT().Chat(mock.Anything, summaryReq).Return(reply(notesJSON(t, *first)), nil).Once()
	// One replacement still isn't in the transcript, the other repeats a
	// quote already kept.
	p.EXPECT().Chat(mock.Anything, repairReq).Return(reply(`{"quotes": [
		{"text": "Still never said.", "speaker": "Mark", "timestamp": "00:00"},
		{"text": "Welcome back.", "speaker": "Sarah", "timestamp": "00:00"}
	]}`), nil).Once()
	var rec trace.Recorder
	j := summarizeJob(t, p, &rec)

	n, err := j.summarize(context.Background(), &meter{})
	if err != nil {
		t.Fatal(err)
	}
	if want := validNotes().Quotes[:2]; !slices.Equal(n.Quotes, want) {
		t.Errorf("quotes = %+v, want %+v", n.Quotes, want)
	}
	for _, w := range []string{
		`dropped quote: "Still never said." was not found`,
		`dropped quote: "Welcome back." repeats another quote`,
		"got 2 quotes, want 3-5",
	} {
		if !containsPrefix(j.warnings, w) {
			t.Errorf("warnings %q missing %q", j.warnings, w)
		}
	}
	if containsPrefix(j.warnings, `dropped quote: "Never said."`) {
		t.Errorf("warnings %q mention the replaced quote", j.warnings)
	}
	es := eventsOf(&rec, "repair")
	if len(es) != 1 || es[0].Value("quotes_accepted").Int64() != 0 || es[0].Value("quotes_rejected").Int64() != 2 {
		t.Errorf("repair events = %+v, want 0 accepted, 2 rejected", es)
	}
}

func TestSummarizeRepairsSchema(t *testing.T) {
	first := validNotes()
	first.Summary = strings.Repeat("word ", 150)
	first.Takeaways = first.Takeaways[:4]
	first.Topics = []string{"remote-work"}

	p := newMockLLM(t)
	p.EXPECT().Chat(mock.Anything, summaryReq).Return(reply(notesJSON(t, *first)), nil).Once()
	p.EXPECT().Chat(mock.Anything, repairReq).
		Run(func(_ context.Context, req llm.ChatRequest) {
			if got := required(t, req.ResponseSchema); !slices.Equal(got, []string{"summary", "takeaways", "topics"}) {
				t.Errorf("repair schema requires %v", got)
			}
			prompt := lastMessage(req).Text
			if strings.Contains(prompt, "Quotes must") {
				t.Errorf("repair prompt asks about quotes:\n%s", prompt)
			}
			for _, want := range []string{"summary is 150 words, want 200-300", "got 4 takeaways, want 5", "got 1 topics, want 3-8"} {
				if !strings.Contains(prompt, want) {
					t.Errorf("repair prompt missing %q:\n%s", want, prompt)
				}
			}
		}).
		// The summary is fixed and the topics need tidying; the takeaways
		// are still short, so the originals stay.
		Return(reply(notesJSON(t, notes{
			Summary:   strings.Repeat("fixed ", 220),
			Takeaways: []string{"x", "y", "z"},
			Topics:    []string{"Remote Work", "async_culture", "hiring"},
		})), nil).Once()
	var rec trace.Recorder
	j := summarizeJob(t, p, &rec)

	n, err := j.summarize(context.Background(), &meter{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(n.Summary, "fixed") {
		t.Errorf("summary = %.20q..., want the repaired one", n.Summary)
	}
	if !slices.Equal(n.Takeaways, first.Takeaways) {
		t.Errorf("takeaways = %q, want the originals", n.Takeaways)
	}
	if want := []string{"remote-work", "async-culture", "hiring"}; !slices.Equal(n.Topics, want) {
		t.Errorf("topics = %q, want %q", n.Topics, want)
	}
	if !slices.Equal(n.Quotes, first.Quotes) {
		t.Errorf("quotes = %+v, want the originals", n.Quotes)
	}
	if !slices.Equal(j.warnings, []string{"got 4 takeaways, want 5"}) {
		t.Errorf("warnings = %q, want only the takeaways", j.warnings)
	}
	es := eventsOf(&rec, "repair")
	if len(es) != 1 || es[0].Value("fixes_requested").Int64() != 3 || es[0].Value("fixes_accepted").Int64() != 2 ||
		es[0].Value("fields").String() != "summary,takeaways,topics" {
		t.Errorf("repair events = %+v, want 2 of 3 fixes accepted", es)
	}
}

func TestSummarizeRepairFails(t *testing.T) {
	refusal := reply("")
	refusal.StopReason = llm.StopRefusal
	tests := []struct {
		name string
		resp *llm.ChatResponse
		err  error
		want string
	}{
		{"call error", nil, errors.New("boom"), "repair failed: boom"},
		{"refusal", refusal, nil, "repair failed: model refused"},
		{"bad json", reply(`{"quotes": [`), nil, "repair failed: decode:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := validNotes()
			first.Summary = "too short"
			first.Quotes[2] = noteQuote{Text: "Never said."}

			p := newMockLLM(t)
			p.EXPECT().Chat(mock.Anything, summaryReq).Return(reply(notesJSON(t, *first)), nil).Once()
			p.EXPECT().Chat(mock.Anything, repairReq).Return(tt.resp, tt.err).Once()
			var rec trace.Recorder
			j := summarizeJob(t, p, &rec)

			n, err := j.summarize(context.Background(), &meter{})
			if err != nil {
				t.Fatalf("summarize failed: %v; a failed repair should only warn", err)
			}
			if n.Summary != "too short" || !slices.Equal(n.Quotes, validNotes().Quotes[:2]) {
				t.Errorf("notes = %+v, want the first answer minus the bad quote", n)
			}
			for _, w := range []string{tt.want, `dropped quote: "Never said."`, "got 2 quotes", "summary is 2 words"} {
				if !containsPrefix(j.warnings, w) {
					t.Errorf("warnings %q missing %q", j.warnings, w)
				}
			}
			if es := eventsOf(&rec, "repair"); len(es) != 1 || es[0].Value("error").String() == "" {
				t.Errorf("repair events = %+v, want one with an error", es)
			}
		})
	}
}

func TestRepairSchema(t *testing.T) {
	got := repairSchema([]string{"summary", "quotes"})
	var s struct {
		Type                 string                     `json:"type"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	if err := json.Unmarshal(got, &s); err != nil {
		t.Fatal(err)
	}
	if s.Type != "object" || s.AdditionalProperties || !slices.Equal(s.Required, []string{"summary", "quotes"}) {
		t.Errorf("schema = %s", got)
	}
	var want bytes.Buffer
	if err := json.Compact(&want, notesProperties["quotes"]); err != nil {
		t.Fatal(err)
	}
	if string(s.Properties["quotes"]) != want.String() || len(s.Properties) != 2 {
		t.Errorf("properties = %v, want summary and quotes from notesSchema", s.Properties)
	}
}
