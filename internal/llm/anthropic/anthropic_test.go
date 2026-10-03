package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
)

// toolUseStream is a streamed reply with a thinking block, text, and one tool call.
var toolUseStream = []string{
	`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":80,"cache_creation_input_tokens":20}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Check the KB first."}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Looking "}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"it up."}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"search_knowledge_base","input":{}}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"GitLab\"}"}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null,"stop_details":null},"usage":{"output_tokens":42}}`,
	`{"type":"message_stop"}`,
}

// fakeAPI serves events as an SSE stream and records each request.
type fakeAPI struct {
	events   []string
	bodies   []map[string]any
	headers  []http.Header
	provider *Provider
}

func newFakeAPI(t *testing.T, events []string, cfg Config) *fakeAPI {
	t.Helper()
	f := &fakeAPI{events: events}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		f.bodies = append(f.bodies, body)
		f.headers = append(f.headers, r.Header.Clone())

		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range f.events {
			var typ struct{ Type string }
			json.Unmarshal([]byte(e), &typ)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ.Type, e)
		}
	}))
	t.Cleanup(srv.Close)
	f.provider = New(cfg, option.WithBaseURL(srv.URL), option.WithAPIKey("test-key"), option.WithMaxRetries(0))
	return f
}

func TestChatResponse(t *testing.T) {
	f := newFakeAPI(t, toolUseStream, Config{})
	resp, err := f.provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Check this claim."}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.StopReason != llm.StopToolUse {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, llm.StopToolUse)
	}
	if resp.Model != "claude-opus-5-5" {
		t.Errorf("Model = %q", resp.Model)
	}
	if got := resp.Message.Text; got != "Looking it up." {
		t.Errorf("Text = %q", got)
	}
	if got := resp.Message.Thinking; got != "Check the KB first." {
		t.Errorf("Thinking = %q", got)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.Message.ToolCalls))
	}
	call := resp.Message.ToolCalls[0]
	if call.ID != "toolu_1" || call.Name != "search_knowledge_base" || string(call.Input) != `{"query":"GitLab"}` {
		t.Errorf("ToolCall = %+v (input %s)", call, call.Input)
	}
	want := llm.Usage{InputTokens: 100, OutputTokens: 42, CacheReadTokens: 80, CacheWriteTokens: 20}
	if resp.Usage != want {
		t.Errorf("Usage = %+v, want %+v", resp.Usage, want)
	}
	if _, ok := resp.Message.Raw.(sdk.BetaMessageParam); !ok {
		t.Errorf("Raw is %T, want sdk.BetaMessageParam", resp.Message.Raw)
	}
}

func TestChatRequest(t *testing.T) {
	f := newFakeAPI(t, toolUseStream, Config{Effort: llm.EffortHigh})
	_, err := f.provider.Chat(context.Background(), llm.ChatRequest{
		System:   "You are a fact checker.",
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
		Tools: []llm.ToolDef{{
			Name:        "search_knowledge_base",
			Description: "Search the curated KB.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
			Strict:      true,
		}},
		ResponseSchema: json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string"}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := f.bodies[0]

	checks := map[string]any{
		"model":      "claude-opus-5-5",
		"max_tokens": float64(DefaultMaxTokens),
		"fallbacks":  "default",
		"thinking":   map[string]any{"type": "adaptive", "display": "summarized"},
		"system":     []any{map[string]any{"type": "text", "text": "You are a fact checker."}},
	}
	for k, want := range checks {
		if got := body[k]; !jsonEqual(got, want) {
			t.Errorf("body[%q] = %v, want %v", k, got, want)
		}
	}
	if _, ok := body["cache_control"]; !ok {
		t.Error("body has no top-level cache_control")
	}

	out := body["output_config"].(map[string]any)
	if out["effort"] != "high" {
		t.Errorf("output_config.effort = %v, want high", out["effort"])
	}
	format := out["format"].(map[string]any)
	if format["type"] != "json_schema" || format["schema"] == nil {
		t.Errorf("output_config.format = %v", format)
	}

	tool := body["tools"].([]any)[0].(map[string]any)
	if tool["strict"] != true || tool["description"] != "Search the curated KB." {
		t.Errorf("tool = %v", tool)
	}
	wantSchema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"query": map[string]any{"type": "string"}},
		"required":             []any{"query"},
		"additionalProperties": false,
	}
	if !jsonEqual(tool["input_schema"], wantSchema) {
		t.Errorf("input_schema = %v, want %v", tool["input_schema"], wantSchema)
	}

	if beta := f.headers[0].Get("anthropic-beta"); !strings.Contains(beta, "server-side-fallback-2026-07-01") {
		t.Errorf("anthropic-beta = %q, want server-side-fallback-2026-07-01", beta)
	}
}

func TestRequestOverridesConfig(t *testing.T) {
	f := newFakeAPI(t, toolUseStream, Config{Model: "claude-sonnet-5-5", MaxTokens: 1000, Effort: llm.EffortLow})
	_, err := f.provider.Chat(context.Background(), llm.ChatRequest{
		Model:     "claude-haiku-4-5",
		MaxTokens: 2000,
		Effort:    llm.EffortMax,
		Messages:  []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := f.bodies[0]
	if body["model"] != "claude-haiku-4-5" || body["max_tokens"] != float64(2000) {
		t.Errorf("model = %v, max_tokens = %v", body["model"], body["max_tokens"])
	}
	if effort := body["output_config"].(map[string]any)["effort"]; effort != "max" {
		t.Errorf("effort = %v, want max", effort)
	}
}

// TestRoundTrip checks that a returned assistant message goes back with its
// signed thinking block intact, followed by the caller's tool results.
func TestRoundTrip(t *testing.T) {
	f := newFakeAPI(t, toolUseStream, Config{})
	history := []llm.Message{{Role: llm.RoleUser, Text: "Check this claim."}}
	resp, err := f.provider.Chat(context.Background(), llm.ChatRequest{Messages: history})
	if err != nil {
		t.Fatal(err)
	}
	call := resp.Message.ToolCalls[0]
	history = append(history, resp.Message, llm.Message{
		Role:        llm.RoleUser,
		ToolResults: []llm.ToolResult{{ToolCallID: call.ID, Name: call.Name, Content: "GitLab is all-remote."}},
	})
	if _, err := f.provider.Chat(context.Background(), llm.ChatRequest{Messages: history}); err != nil {
		t.Fatal(err)
	}

	msgs := f.bodies[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("sent %d messages, want 3", len(msgs))
	}
	assistant := msgs[1].(map[string]any)["content"].([]any)
	wantAssistant := []any{
		map[string]any{"type": "thinking", "thinking": "Check the KB first.", "signature": "sig123"},
		map[string]any{"type": "text", "text": "Looking it up."},
		map[string]any{"type": "tool_use", "id": "toolu_1", "name": "search_knowledge_base", "input": map[string]any{"query": "GitLab"}},
	}
	if !jsonEqual(assistant, wantAssistant) {
		t.Errorf("assistant content = %v\nwant %v", assistant, wantAssistant)
	}
	result := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if result["type"] != "tool_result" || result["tool_use_id"] != "toolu_1" {
		t.Errorf("tool result = %v", result)
	}
}

func TestToMessageParamsWithoutRaw(t *testing.T) {
	msgs, err := toMessageParams([]llm.Message{
		{Role: llm.RoleUser, Text: "q"},
		{Role: llm.RoleAssistant, Text: "a", ToolCalls: []llm.ToolCall{{ID: "t1", Name: "get_current_date"}}},
		{Role: llm.RoleUser, Text: "and then?", ToolResults: []llm.ToolResult{{ToolCallID: "t1", Content: "boom", IsError: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(msgs)
	want := `[
		{"role":"user","content":[{"type":"text","text":"q"}]},
		{"role":"assistant","content":[{"type":"text","text":"a"},{"type":"tool_use","id":"t1","name":"get_current_date","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"boom"}],"is_error":true},{"type":"text","text":"and then?"}]}
	]`
	if !jsonEqual(json.RawMessage(got), json.RawMessage(want)) {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestToMessageParamsErrors(t *testing.T) {
	if _, err := toMessageParams([]llm.Message{{Role: llm.RoleUser}}); !errors.Is(err, errEmptyMessage) {
		t.Errorf("empty message: err = %v, want errEmptyMessage", err)
	}
	if _, err := toMessageParams([]llm.Message{{Role: "system", Text: "x"}}); err == nil {
		t.Error("unknown role: want error")
	}
}

func TestBadSchema(t *testing.T) {
	f := newFakeAPI(t, toolUseStream, Config{})
	_, err := f.provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
		Tools:    []llm.ToolDef{{Name: "bad", InputSchema: json.RawMessage(`not json`)}},
	})
	if err == nil || !strings.Contains(err.Error(), "tool bad") {
		t.Errorf("err = %v, want tool schema error", err)
	}
	if len(f.bodies) != 0 {
		t.Error("request was sent despite invalid schema")
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"nope"}}`)
	}))
	defer srv.Close()
	p := New(Config{}, option.WithBaseURL(srv.URL), option.WithAPIKey("k"), option.WithMaxRetries(0))

	_, err := p.Chat(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}})
	var apiErr *sdk.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("err = %v, want *sdk.Error with status 400", err)
	}
}

func TestStopReason(t *testing.T) {
	tests := map[sdk.BetaStopReason]llm.StopReason{
		sdk.BetaStopReasonEndTurn:                    llm.StopEndTurn,
		sdk.BetaStopReasonStopSequence:               llm.StopEndTurn,
		sdk.BetaStopReasonToolUse:                    llm.StopToolUse,
		sdk.BetaStopReasonMaxTokens:                  llm.StopMaxTokens,
		sdk.BetaStopReasonRefusal:                    llm.StopRefusal,
		sdk.BetaStopReasonModelContextWindowExceeded: "model_context_window_exceeded",
	}
	for in, want := range tests {
		if got := toStopReason(in); got != want {
			t.Errorf("toStopReason(%q) = %q, want %q", in, got, want)
		}
	}
}

// jsonEqual compares two values by their JSON encoding, after normalizing
// raw JSON so key order and whitespace don't matter.
func jsonEqual(a, b any) bool {
	norm := func(v any) string {
		raw, ok := v.(json.RawMessage)
		if !ok {
			raw, _ = json.Marshal(v)
		}
		var x any
		json.Unmarshal(raw, &x)
		out, _ := json.Marshal(x)
		return string(out)
	}
	return norm(a) == norm(b)
}
