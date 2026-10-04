package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/option"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
)

// toolCallResponse is a reply with a reasoning summary, text, and one tool call.
const toolCallResponse = `{
  "id": "resp_1", "object": "response", "status": "completed", "model": "gpt-6.1-sol",
  "output": [
    {"type": "reasoning", "id": "rs_1", "summary": [{"type": "summary_text", "text": "Check the KB first."}], "encrypted_content": "enc123"},
    {"type": "message", "id": "msg_1", "role": "assistant", "status": "completed", "content": [{"type": "output_text", "text": "Looking it up.", "annotations": []}]},
    {"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "search_kb", "arguments": "{\"query\":\"GitLab\"}", "status": "completed"}
  ],
  "usage": {"input_tokens": 200, "input_tokens_details": {"cached_tokens": 80, "cache_write_tokens": 20}, "output_tokens": 42, "output_tokens_details": {"reasoning_tokens": 30}, "total_tokens": 242}
}`

// fakeAPI answers every request with reply and records each request body.
type fakeAPI struct {
	reply    string
	status   int
	bodies   []map[string]any
	provider *Provider
}

func newFakeAPI(t *testing.T, reply string, cfg Config) *fakeAPI {
	t.Helper()
	f := &fakeAPI{reply: reply, status: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %s, want /responses", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		f.bodies = append(f.bodies, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		io.WriteString(w, f.reply)
	}))
	t.Cleanup(srv.Close)
	f.provider = New(cfg, option.WithBaseURL(srv.URL), option.WithAPIKey("test-key"), option.WithMaxRetries(0))
	return f
}

func TestChatResponse(t *testing.T) {
	f := newFakeAPI(t, toolCallResponse, Config{})
	resp, err := f.provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Check this claim."}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.StopReason != llm.StopToolUse {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, llm.StopToolUse)
	}
	if resp.Model != "gpt-6.1-sol" {
		t.Errorf("Model = %q", resp.Model)
	}
	if resp.Message.Text != "Looking it up." {
		t.Errorf("Text = %q", resp.Message.Text)
	}
	if resp.Message.Thinking != "Check the KB first." {
		t.Errorf("Thinking = %q", resp.Message.Thinking)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v, want 1", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "search_kb" || string(call.Input) != `{"query":"GitLab"}` {
		t.Errorf("ToolCall = %+v", call)
	}
	want := llm.Usage{InputTokens: 100, OutputTokens: 42, CacheReadTokens: 80, CacheWriteTokens: 20}
	if resp.Usage != want {
		t.Errorf("Usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestChatRequest(t *testing.T) {
	f := newFakeAPI(t, toolCallResponse, Config{MaxTokens: 1000})
	_, err := f.provider.Chat(context.Background(), llm.ChatRequest{
		System:         "Be brief.",
		Messages:       []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
		Effort:         llm.EffortHigh,
		ResponseSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":false}`),
		Tools: []llm.ToolDef{{
			Name:        "search_kb",
			Description: "Search the knowledge base.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
			Strict:      true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := f.bodies[0]

	checks := map[string]any{
		"model":             DefaultModel,
		"instructions":      "Be brief.",
		"store":             false,
		"max_output_tokens": float64(1000),
	}
	for k, want := range checks {
		if body[k] != want {
			t.Errorf("%s = %v, want %v", k, body[k], want)
		}
	}
	if inc, _ := body["include"].([]any); len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v", body["include"])
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" || reasoning["summary"] != "auto" {
		t.Errorf("reasoning = %v", reasoning)
	}
	format, _ := body["text"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["strict"] != true || format["schema"] == nil {
		t.Errorf("text.format = %v", format)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", body["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "search_kb" || tool["strict"] != true || tool["parameters"] == nil {
		t.Errorf("tool = %v", tool)
	}
}

// TestRoundTrip checks that output items come back verbatim, encrypted
// reasoning included, and that tool results become function_call_output items.
func TestRoundTrip(t *testing.T) {
	f := newFakeAPI(t, toolCallResponse, Config{})
	first, err := f.provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Check this claim."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Text: "Check this claim."},
			first.Message,
			{Role: llm.RoleUser, ToolResults: []llm.ToolResult{{ToolCallID: "call_1", Name: "search_kb", Content: "no results", IsError: true}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	input, _ := f.bodies[1]["input"].([]any)
	var types []string
	for _, item := range input {
		typ, _ := item.(map[string]any)["type"].(string)
		types = append(types, typ)
	}
	// The first user message has no explicit type, as the SDK's easy-message form omits it.
	if got, want := strings.Join(types[1:], ","), "reasoning,message,function_call,function_call_output"; got != want {
		t.Fatalf("input types = %s, want %s", got, want)
	}
	if enc := input[1].(map[string]any)["encrypted_content"]; enc != "enc123" {
		t.Errorf("reasoning encrypted_content = %v, want enc123", enc)
	}
	out := input[4].(map[string]any)
	if out["call_id"] != "call_1" || out["output"] != "Error: no results" {
		t.Errorf("function_call_output = %v", out)
	}
}

// TestNeutralHistory converts an assistant message from another provider,
// which has no Raw.
func TestNeutralHistory(t *testing.T) {
	f := newFakeAPI(t, toolCallResponse, Config{})
	_, err := f.provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Text: "hi"},
			{Role: llm.RoleAssistant, Text: "Searching.", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "search_kb"}}},
			{Role: llm.RoleUser, ToolResults: []llm.ToolResult{{ToolCallID: "c1", Content: "ok"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := f.bodies[0]["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("input = %v, want 4 items", input)
	}
	call := input[2].(map[string]any)
	if call["type"] != "function_call" || call["call_id"] != "c1" || call["arguments"] != "{}" {
		t.Errorf("function_call = %v", call)
	}
}

func TestStopReason(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		want  llm.StopReason
	}{
		{"end turn", `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`, llm.StopEndTurn},
		{"max tokens", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`, llm.StopMaxTokens},
		{"content filter", `{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}`, llm.StopRefusal},
		{"refusal", `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"I can't help with that."}]}]}`, llm.StopRefusal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t, tt.reply, Config{})
			resp, err := f.provider.Chat(context.Background(), llm.ChatRequest{
				Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if resp.StopReason != tt.want {
				t.Errorf("StopReason = %q, want %q", resp.StopReason, tt.want)
			}
		})
	}
}

func TestChatErrors(t *testing.T) {
	t.Run("failed response", func(t *testing.T) {
		f := newFakeAPI(t, `{"status":"failed","error":{"code":"server_error","message":"boom"},"output":[]}`, Config{})
		_, err := f.provider.Chat(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}})
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Errorf("err = %v, want failure message", err)
		}
	})
	t.Run("HTTP error", func(t *testing.T) {
		f := newFakeAPI(t, `{"error":{"message":"bad model","type":"invalid_request_error"}}`, Config{})
		f.status = http.StatusBadRequest
		_, err := f.provider.Chat(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}})
		if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "bad model") {
			t.Errorf("err = %v, want a 400", err)
		}
	})
	t.Run("empty message", func(t *testing.T) {
		f := newFakeAPI(t, toolCallResponse, Config{})
		_, err := f.provider.Chat(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser}}})
		if !errors.Is(err, errEmptyMessage) {
			t.Errorf("err = %v, want errEmptyMessage", err)
		}
		if len(f.bodies) != 0 {
			t.Error("request was sent")
		}
	})
}
