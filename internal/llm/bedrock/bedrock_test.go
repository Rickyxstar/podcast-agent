package bedrock

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
)

// refusedStream is a refusal before any output.
var refusedStream = []string{
	`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"anthropic.claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":50,"output_tokens":0}}}`,
	`{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"cyber","explanation":"declined"}},"usage":{"output_tokens":0}}`,
	`{"type":"message_stop"}`,
}

func textStream(model, text string) []string {
	return []string{
		fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":50,"output_tokens":1}}}`, model),
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, text),
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null,"stop_details":null},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	}
}

// fakeMantle replies to the nth request with streams[n] and records each body.
type fakeMantle struct {
	streams [][]string
	bodies  []map[string]any
	paths   []string
}

func (f *fakeMantle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(raw, &body)
	f.bodies = append(f.bodies, body)
	f.paths = append(f.paths, r.URL.Path)

	n := len(f.bodies) - 1
	if n >= len(f.streams) {
		http.Error(w, "unexpected request", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range f.streams[n] {
		var typ struct{ Type string }
		json.Unmarshal([]byte(e), &typ)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ.Type, e)
	}
}

func newTestProvider(t *testing.T, f *fakeMantle, cfg Config) llm.Provider {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	// An API key skips SigV4 so the test needs no AWS credentials.
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "test-key")
	t.Setenv("ANTHROPIC_BEDROCK_MANTLE_BASE_URL", srv.URL)
	cfg.Region = cmp.Or(cfg.Region, "us-east-1")

	p, err := New(context.Background(), cfg, option.WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func chat(t *testing.T, p llm.Provider) *llm.ChatResponse {
	t.Helper()
	resp, err := p.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Is this claim true?"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestDefaults(t *testing.T) {
	f := &fakeMantle{streams: [][]string{textStream(DefaultModel, "Yes.")}}
	p := newTestProvider(t, f, Config{})

	resp := chat(t, p)
	if p.Name() != "bedrock" {
		t.Errorf("Name = %q, want bedrock", p.Name())
	}
	if resp.Message.Text != "Yes." || resp.StopReason != llm.StopEndTurn {
		t.Errorf("resp = %q / %q", resp.Message.Text, resp.StopReason)
	}
	body := f.bodies[0]
	if body["model"] != DefaultModel {
		t.Errorf("model = %v, want %s", body["model"], DefaultModel)
	}
	if _, ok := body["fallbacks"]; ok {
		t.Error("server-side fallbacks sent to Bedrock")
	}
	if f.paths[0] != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", f.paths[0])
	}
}

func TestRefusalFallsBack(t *testing.T) {
	f := &fakeMantle{streams: [][]string{refusedStream, textStream(DefaultFallbackModel, "Yes, it's true.")}}
	p := newTestProvider(t, f, Config{})

	resp := chat(t, p)
	if len(f.bodies) != 2 {
		t.Fatalf("got %d requests, want 2 (original + fallback)", len(f.bodies))
	}
	if got := f.bodies[1]["model"]; got != DefaultFallbackModel {
		t.Errorf("retry model = %v, want %s", got, DefaultFallbackModel)
	}
	if resp.StopReason != llm.StopEndTurn || resp.Message.Text != "Yes, it's true." {
		t.Errorf("resp = %q / %q", resp.Message.Text, resp.StopReason)
	}
	if resp.Model != DefaultFallbackModel {
		t.Errorf("Model = %q, want %s", resp.Model, DefaultFallbackModel)
	}
}

func TestConfigOverrides(t *testing.T) {
	f := &fakeMantle{streams: [][]string{refusedStream, textStream("anthropic.claude-opus-5", "ok")}}
	cfg := Config{FallbackModel: "anthropic.claude-opus-5"}
	cfg.Model = "anthropic.claude-sonnet-5-5"
	p := newTestProvider(t, f, cfg)

	chat(t, p)
	if got := f.bodies[0]["model"]; got != "anthropic.claude-sonnet-5-5" {
		t.Errorf("model = %v", got)
	}
	if got := f.bodies[1]["model"]; got != "anthropic.claude-opus-5" {
		t.Errorf("fallback model = %v", got)
	}
}
