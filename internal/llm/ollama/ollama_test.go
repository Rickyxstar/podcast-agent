package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
)

// fakeOllama replies with reply and records each request body.
type fakeOllama struct {
	status int
	reply  string
	bodies []map[string]any
	paths  []string
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(raw, &body)
	f.bodies = append(f.bodies, body)
	f.paths = append(f.paths, r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	if f.status != 0 {
		w.WriteHeader(f.status)
	}
	io.WriteString(w, f.reply)
}

func newTestProvider(t *testing.T, f *fakeOllama, cfg Config) *Provider {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg.Host = srv.URL
	return New(cfg)
}

const toolCallReply = `{
	"model": "qwen2.5:7b",
	"message": {
		"role": "assistant",
		"content": "",
		"thinking": "Need the KB.",
		"tool_calls": [{"id": "call_abc", "function": {"index": 0, "name": "search_knowledge_base", "arguments": {"query": "GitLab"}}}]
	},
	"done": true,
	"done_reason": "stop",
	"prompt_eval_count": 120,
	"eval_count": 18
}`

func TestChatResponse(t *testing.T) {
	f := &fakeOllama{reply: toolCallReply}
	p := newTestProvider(t, f, Config{})

	resp, err := p.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Check this claim."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.paths[0] != "/api/chat" {
		t.Errorf("path = %q, want /api/chat", f.paths[0])
	}
	if resp.StopReason != llm.StopToolUse {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, llm.StopToolUse)
	}
	if resp.Model != "qwen2.5:7b" || resp.Message.Thinking != "Need the KB." {
		t.Errorf("Model = %q, Thinking = %q", resp.Model, resp.Message.Thinking)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.Message.ToolCalls))
	}
	call := resp.Message.ToolCalls[0]
	if call.ID != "call_abc" || call.Name != "search_knowledge_base" || !jsonEqual(call.Input, `{"query":"GitLab"}`) {
		t.Errorf("ToolCall = %+v (input %s)", call, call.Input)
	}
	if want := (llm.Usage{InputTokens: 120, OutputTokens: 18}); resp.Usage != want {
		t.Errorf("Usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestChatRequest(t *testing.T) {
	f := &fakeOllama{reply: `{"model":"m","message":{"role":"assistant","content":"{}"},"done_reason":"stop"}`}
	p := newTestProvider(t, f, Config{MaxTokens: 500})

	schema := `{"type":"object","properties":{"summary":{"type":"string"}}}`
	toolSchema := `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`
	_, err := p.Chat(context.Background(), llm.ChatRequest{
		System: "You are a fact checker.",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Text: "q"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "c1", Name: "search_knowledge_base", Input: json.RawMessage(`{"query":"x"}`)},
				{ID: "c2", Name: "get_current_date"},
			}},
			{Role: llm.RoleUser, Text: "go on", ToolResults: []llm.ToolResult{
				{ToolCallID: "c1", Name: "search_knowledge_base", Content: "found"},
				{ToolCallID: "c2", Name: "get_current_date", Content: "timeout", IsError: true},
			}},
		},
		Tools:          []llm.ToolDef{{Name: "search_knowledge_base", Description: "Search.", InputSchema: json.RawMessage(toolSchema), Strict: true}},
		ResponseSchema: json.RawMessage(schema),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := f.bodies[0]

	wantMessages := `[
		{"role":"system","content":"You are a fact checker."},
		{"role":"user","content":"q"},
		{"role":"assistant","content":"","tool_calls":[
			{"id":"c1","function":{"name":"search_knowledge_base","arguments":{"query":"x"}}},
			{"id":"c2","function":{"name":"get_current_date","arguments":{}}}
		]},
		{"role":"tool","content":"found","tool_name":"search_knowledge_base","tool_call_id":"c1"},
		{"role":"tool","content":"Error: timeout","tool_name":"get_current_date","tool_call_id":"c2"},
		{"role":"user","content":"go on"}
	]`
	if !jsonEqual(body["messages"], wantMessages) {
		got, _ := json.Marshal(body["messages"])
		t.Errorf("messages = %s\nwant %s", got, wantMessages)
	}
	wantTools := `[{"type":"function","function":{"name":"search_knowledge_base","description":"Search.","parameters":` + toolSchema + `}}]`
	if !jsonEqual(body["tools"], wantTools) {
		t.Errorf("tools = %v", body["tools"])
	}
	if !jsonEqual(body["format"], schema) {
		t.Errorf("format = %v", body["format"])
	}
	if body["stream"] != false || body["model"] != DefaultModel {
		t.Errorf("stream = %v, model = %v", body["stream"], body["model"])
	}
	if !jsonEqual(body["options"], `{"num_ctx":32768,"num_predict":500}`) {
		t.Errorf("options = %v", body["options"])
	}
}

func TestRequestOverridesConfig(t *testing.T) {
	f := &fakeOllama{reply: `{"model":"m","message":{"role":"assistant","content":"ok"},"done_reason":"stop"}`}
	p := newTestProvider(t, f, Config{Model: "llama3.1:8b", MaxTokens: 500, ContextWindow: 8192})

	_, err := p.Chat(context.Background(), llm.ChatRequest{
		Model:     "qwen3:8b",
		MaxTokens: 900,
		Messages:  []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := f.bodies[0]
	if body["model"] != "qwen3:8b" {
		t.Errorf("model = %v", body["model"])
	}
	if !jsonEqual(body["options"], `{"num_ctx":8192,"num_predict":900}`) {
		t.Errorf("options = %v", body["options"])
	}
	if _, ok := body["format"]; ok {
		t.Error("format sent without a ResponseSchema")
	}
}

func TestMissingToolCallID(t *testing.T) {
	f := &fakeOllama{reply: `{"model":"m","message":{"role":"assistant","content":"","tool_calls":[
		{"function":{"name":"a","arguments":{}}},{"function":{"name":"b","arguments":{}}}]},"done_reason":"stop"}`}
	p := newTestProvider(t, f, Config{})

	resp, err := p.Chat(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.Message.ToolCalls
	if len(calls) != 2 || calls[0].ID == "" || calls[0].ID == calls[1].ID {
		t.Errorf("tool call IDs = %+v, want distinct non-empty IDs", calls)
	}
}

func TestStatusError(t *testing.T) {
	f := &fakeOllama{status: http.StatusNotFound, reply: `{"error":"model \"qwen2.5:7b\" not found, try pulling it first"}`}
	p := newTestProvider(t, f, Config{})

	_, err := p.Chat(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}})
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %v, want *StatusError", err)
	}
	if statusErr.StatusCode != http.StatusNotFound || statusErr.Message != `model "qwen2.5:7b" not found, try pulling it first` {
		t.Errorf("StatusError = %+v", statusErr)
	}
}

func TestBadInput(t *testing.T) {
	f := &fakeOllama{}
	p := newTestProvider(t, f, Config{})
	tests := map[string]llm.ChatRequest{
		"empty message": {Messages: []llm.Message{{Role: llm.RoleUser}}},
		"unknown role":  {Messages: []llm.Message{{Role: "system", Text: "x"}}},
		"bad schema": {
			Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
			Tools:    []llm.ToolDef{{Name: "bad", InputSchema: json.RawMessage(`not json`)}},
		},
	}
	for name, req := range tests {
		if _, err := p.Chat(context.Background(), req); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if len(f.bodies) != 0 {
		t.Errorf("sent %d requests for invalid input, want 0", len(f.bodies))
	}
}

func TestHost(t *testing.T) {
	tests := []struct{ cfg, env, want string }{
		{"", "", "http://localhost:11434/api/chat"},
		{"", "0.0.0.0:11434", "http://0.0.0.0:11434/api/chat"},
		{"", "https://ollama.internal/", "https://ollama.internal/api/chat"},
		{"http://ollama:11434", "other:1", "http://ollama:11434/api/chat"},
	}
	for _, tt := range tests {
		t.Setenv("OLLAMA_HOST", tt.env)
		if got := New(Config{Host: tt.cfg}).url; got != tt.want {
			t.Errorf("Host %q, OLLAMA_HOST %q: url = %q, want %q", tt.cfg, tt.env, got, tt.want)
		}
	}
}

func TestStopReason(t *testing.T) {
	tests := []struct {
		reason   string
		hasCalls bool
		want     llm.StopReason
	}{
		{"stop", false, llm.StopEndTurn},
		{"", false, llm.StopEndTurn},
		{"stop", true, llm.StopToolUse},
		{"length", false, llm.StopMaxTokens},
		{"length", true, llm.StopMaxTokens},
		{"load", false, "load"},
	}
	for _, tt := range tests {
		if got := toStopReason(tt.reason, tt.hasCalls); got != tt.want {
			t.Errorf("toStopReason(%q, %v) = %q, want %q", tt.reason, tt.hasCalls, got, tt.want)
		}
	}
}

// jsonEqual reports whether a (a decoded value or raw JSON) and the JSON
// text b encode the same value, ignoring key order and whitespace.
func jsonEqual(a any, b string) bool {
	norm := func(raw []byte) string {
		var v any
		if json.Unmarshal(raw, &v) != nil {
			return "invalid: " + string(raw)
		}
		out, _ := json.Marshal(v)
		return string(out)
	}
	raw, ok := a.(json.RawMessage)
	if !ok {
		raw, _ = json.Marshal(a)
	}
	return norm(raw) == norm([]byte(b))
}
