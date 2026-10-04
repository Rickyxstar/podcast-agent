package trace

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

var testTime = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func event(typ string, attrs ...slog.Attr) Event {
	return Event{Time: testTime, TraceID: "abc", Episode: "ep001", Type: typ, Attrs: attrs}
}

func TestRecorderWriteJSONL(t *testing.T) {
	var r Recorder
	r.Emit(event("plan", slog.Int("claims", 2), slog.String("note", "a<b")))
	r.Emit(event("done", slog.Float64("cost_usd", 0.5), slog.Group("tokens", slog.Int("input", 10))))

	var b bytes.Buffer
	if err := r.WriteJSONL(&b); err != nil {
		t.Fatal(err)
	}
	want := `{"ts":"2026-10-03T12:00:00Z","trace_id":"abc","episode":"ep001","event":"plan","claims":2,"note":"a<b"}
{"ts":"2026-10-03T12:00:00Z","trace_id":"abc","episode":"ep001","event":"done","cost_usd":0.5,"tokens":{"input":10}}
`
	if got := b.String(); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestContext(t *testing.T) {
	if s := FromContext(context.Background()); s != nil {
		t.Errorf("FromContext of a bare context = %v, want nil", s)
	}
	var a, b Recorder
	ctx := WithSink(context.Background(), Tee(&a, &b))
	FromContext(ctx).Emit(event("plan"))
	if len(a.Events()) != 1 || len(b.Events()) != 1 {
		t.Errorf("tee delivered %d and %d events, want 1 each", len(a.Events()), len(b.Events()))
	}
}

func TestPretty(t *testing.T) {
	tests := []struct {
		e    Event
		want string
	}{
		{
			event("plan", slog.Int("claims", 6), slog.Int("factual", 3), slog.Int("predictions", 2),
				slog.Int("anecdotes", 1), slog.Int("opinions", 0)),
			"🧭 Plan: 6 claims → 3 factual, 2 predictions, 1 anecdote",
		},
		{
			event("llm_call", slog.String("stage", "plan"), slog.Int("tool_calls", 0),
				slog.Int("input_tokens", 12345), slog.Int("output_tokens", 678), slog.Int64("duration_ms", 2140)),
			"🤖 plan call: 12,345 in / 678 out tokens, 2.1s",
		},
		{
			event("tool_call", slog.String("tool", "search_kb"), slog.String("input", `{"query": "GitLab"}`)),
			`🔎 search_kb {"query": "GitLab"}`,
		},
		{event("tool_call", slog.String("tool", "submit_verdict")), ""},
		{
			event("tool_result", slog.String("tool", "submit_verdict"), slog.Bool("error", true)),
			"   ↳ ⚠ submit_verdict returned an error",
		},
		{
			event("verdict", slog.String("id", "c1"), slog.String("verdict", "verified"),
				slog.Float64("confidence", 0.97), slog.String("reasoning", "The handbook\nsays so.")),
			"✅ c1 verified (confidence 0.97): The handbook says so.",
		},
		// Missing attributes render as zero instead of panicking.
		{event("validation"), "🧪 Validation: 0 of 0 quotes found verbatim, 0 takeaways, 0-word summary"},
		{event("custom", slog.Int("n", 1)), "• custom n=1"},
	}
	for _, tt := range tests {
		var b bytes.Buffer
		NewPretty(&b).Emit(tt.e)
		if got := strings.TrimSuffix(b.String(), "\n"); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.e.Type, got, tt.want)
		}
	}
}
