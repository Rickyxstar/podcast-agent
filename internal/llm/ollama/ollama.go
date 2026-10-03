// Package ollama implements llm.Provider on a local Ollama server's native
// /api/chat endpoint.
package ollama

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
)

const (
	// DefaultHost is used when Config.Host is empty.
	DefaultHost = "http://localhost:11434"
	// DefaultModel is a small model that handles tool calls reasonably well.
	DefaultModel = "qwen2.5:7b"
	// DefaultContextWindow fits an hour-long transcript plus a tool loop.
	// Ollama's own default is a few thousand tokens and silently truncates
	// anything longer.
	DefaultContextWindow = 32768
)

// Config sets per-provider defaults. Fields left zero use the package defaults.
type Config struct {
	// Host is the server URL, with or without a scheme. Empty uses DefaultHost.
	Host  string
	Model string
	// MaxTokens caps output tokens when a request doesn't set its own. Zero
	// leaves it to Ollama, which generates until the model stops.
	MaxTokens int
	// ContextWindow is the context length (num_ctx) the model is loaded with.
	ContextWindow int
	// HTTPClient sends requests. Nil uses a client without a timeout, so
	// slow local generation is bounded only by the request context.
	HTTPClient *http.Client
}

// Provider talks to Ollama. Ollama has no strict tool mode, prompt caching or
// effort setting, so ToolDef.Strict and ChatRequest.Effort are ignored.
type Provider struct {
	url  string
	http *http.Client
	cfg  Config
}

var _ llm.Provider = (*Provider)(nil)

// New returns a Provider for the Ollama server at cfg.Host.
func New(cfg Config) *Provider {
	host := strings.TrimRight(cmp.Or(cfg.Host, DefaultHost), "/")
	// OLLAMA_HOST-style values are often a bare host:port, such as "0.0.0.0:11434".
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	cfg.Model = cmp.Or(cfg.Model, DefaultModel)
	cfg.ContextWindow = cmp.Or(cfg.ContextWindow, DefaultContextWindow)
	return &Provider{
		url:  host + "/api/chat",
		http: cmp.Or(cfg.HTTPClient, &http.Client{}),
		cfg:  cfg,
	}
}

// Name implements llm.Provider.
func (p *Provider) Name() string { return "ollama" }

// Chat implements llm.Provider.
func (p *Provider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	body, err := p.newRequest(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	defer httpResp.Body.Close()
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("ollama: read response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, newStatusError(httpResp, respBody)
	}

	var resp chatResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}
	return toResponse(&resp), nil
}

// StatusError is a non-200 reply from the Ollama server, such as a 404 for a
// model that hasn't been pulled.
type StatusError struct {
	StatusCode int
	Message    string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("ollama: %d %s: %s", e.StatusCode, http.StatusText(e.StatusCode), e.Message)
}

func newStatusError(resp *http.Response, body []byte) *StatusError {
	var e struct {
		Error string `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	return &StatusError{StatusCode: resp.StatusCode, Message: msg}
}

// Wire types for /api/chat. Only the fields this provider uses are declared.

type chatRequest struct {
	Model    string          `json:"model"`
	Messages []message       `json:"messages"`
	Tools    []tool          `json:"tools,omitempty"`
	Format   json.RawMessage `json:"format,omitempty"`
	Stream   bool            `json:"stream"`
	Options  map[string]any  `json:"options,omitempty"`
}

type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Thinking   string     `json:"thinking,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string           `json:"id,omitempty"`
	Function toolCallFunction `json:"function"`
}

type toolCallFunction struct {
	Name string `json:"name"`
	// Arguments is a JSON object, not a string as in OpenAI-style APIs.
	Arguments json.RawMessage `json:"arguments"`
}

type tool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatResponse struct {
	Model           string  `json:"model"`
	Message         message `json:"message"`
	DoneReason      string  `json:"done_reason"`
	PromptEvalCount int     `json:"prompt_eval_count"`
	EvalCount       int     `json:"eval_count"`
}

func (p *Provider) newRequest(req llm.ChatRequest) (*chatRequest, error) {
	out := &chatRequest{
		Model:   cmp.Or(req.Model, p.cfg.Model),
		Format:  req.ResponseSchema,
		Options: map[string]any{"num_ctx": p.cfg.ContextWindow},
	}
	if n := cmp.Or(req.MaxTokens, p.cfg.MaxTokens); n > 0 {
		out.Options["num_predict"] = n
	}
	if req.System != "" {
		out.Messages = append(out.Messages, message{Role: "system", Content: req.System})
	}
	for i, m := range req.Messages {
		msgs, err := toMessages(m)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}
		out.Messages = append(out.Messages, msgs...)
	}
	for _, t := range req.Tools {
		if !json.Valid(t.InputSchema) {
			return nil, fmt.Errorf("tool %s: input schema is not valid JSON", t.Name)
		}
		out.Tools = append(out.Tools, tool{
			Type:     "function",
			Function: toolFunction{Name: t.Name, Description: t.Description, Parameters: t.InputSchema},
		})
	}
	return out, nil
}

// toMessages converts one neutral message. A user message carrying tool
// results becomes one "tool" message per result, followed by any text.
func toMessages(m llm.Message) ([]message, error) {
	switch m.Role {
	case llm.RoleUser:
		var out []message
		for _, r := range m.ToolResults {
			content := r.Content
			// Ollama has no error flag on tool results, so say it in the text.
			if r.IsError {
				content = "Error: " + content
			}
			out = append(out, message{Role: "tool", Content: content, ToolName: r.Name, ToolCallID: r.ToolCallID})
		}
		if m.Text != "" {
			out = append(out, message{Role: "user", Content: m.Text})
		}
		if len(out) == 0 {
			return nil, errEmptyMessage
		}
		return out, nil
	case llm.RoleAssistant:
		msg := message{Role: "assistant", Content: m.Text}
		for _, c := range m.ToolCalls {
			args := c.Input
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			msg.ToolCalls = append(msg.ToolCalls, toolCall{
				ID:       c.ID,
				Function: toolCallFunction{Name: c.Name, Arguments: args},
			})
		}
		return []message{msg}, nil
	default:
		return nil, fmt.Errorf("unknown role %q", m.Role)
	}
}

var errEmptyMessage = errors.New("message has no content")

func toResponse(resp *chatResponse) *llm.ChatResponse {
	out := llm.Message{
		Role:     llm.RoleAssistant,
		Text:     resp.Message.Content,
		Thinking: resp.Message.Thinking,
	}
	for i, c := range resp.Message.ToolCalls {
		// Older Ollama versions don't send IDs; results are matched by name there.
		id := cmp.Or(c.ID, fmt.Sprintf("call_%d", i))
		out.ToolCalls = append(out.ToolCalls, llm.ToolCall{ID: id, Name: c.Function.Name, Input: c.Function.Arguments})
	}

	return &llm.ChatResponse{
		Message:    out,
		StopReason: toStopReason(resp.DoneReason, len(out.ToolCalls) > 0),
		Model:      resp.Model,
		Usage:      llm.Usage{InputTokens: resp.PromptEvalCount, OutputTokens: resp.EvalCount},
	}
}

// toStopReason maps done_reason. Ollama reports "stop" even when the model
// called tools, so tool calls take precedence.
func toStopReason(doneReason string, hasToolCalls bool) llm.StopReason {
	switch {
	case doneReason == "length":
		return llm.StopMaxTokens
	case hasToolCalls:
		return llm.StopToolUse
	case doneReason == "stop" || doneReason == "":
		return llm.StopEndTurn
	default:
		return llm.StopReason(doneReason)
	}
}
