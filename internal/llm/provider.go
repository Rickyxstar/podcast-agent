// Package llm defines a provider-neutral chat interface so the agent can run
// against the Anthropic API, Amazon Bedrock, or a local Ollama model without
// changing the orchestration code.
package llm

import (
	"context"
	"encoding/json"
)

// Provider is a chat backend. Implementations translate the neutral request
// and response types to and from their own wire format.
//
// Chat performs a single model turn and never executes tools itself. To run a
// tool loop the caller appends the returned assistant Message to the history,
// runs the requested ToolCalls, appends a user Message carrying the
// ToolResults, and calls Chat again.
type Provider interface {
	// Name identifies the provider in logs and reports, e.g. "anthropic".
	Name() string
	// Chat sends the conversation in req and returns the model's next turn.
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// ChatRequest is a single model call.
type ChatRequest struct {
	// Model overrides the provider's configured default when non-empty.
	Model string
	// System is the system prompt.
	System string
	// Messages is the conversation so far, oldest first, starting with a user
	// message. Callers should treat it as append-only: providers rely on an
	// unchanged prefix for prompt caching and for echoing back Message.Raw.
	Messages []Message
	// Tools the model may call. Empty disables tool use.
	Tools []ToolDef
	// ResponseSchema, when set, is a JSON Schema the model's final text must
	// conform to (structured output).
	ResponseSchema json.RawMessage
	// MaxTokens caps output tokens for this call. Zero uses the provider default.
	MaxTokens int
	// Effort controls how much reasoning the model spends. Empty uses the
	// provider default.
	Effort Effort
}

// ChatResponse is the model's reply to a ChatRequest.
type ChatResponse struct {
	// Message is the assistant turn. Append it to the history unchanged.
	Message Message
	// StopReason says why the model stopped. Check it before trusting Message.
	StopReason StopReason
	Usage      Usage
	// Model is the model that served the call, as reported by the provider.
	Model string
}

// Role is the author of a Message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn of the conversation.
type Message struct {
	Role Role
	// Text is the message's text content, concatenated if the provider
	// returned several text parts.
	Text string
	// Thinking is the model's readable reasoning, if the provider returned
	// any. It is for traces only; providers never send it back (Raw does that).
	Thinking string
	// ToolCalls are the tools the assistant asked to run. Assistant messages only.
	ToolCalls []ToolCall
	// ToolResults answer the ToolCalls of the preceding assistant message.
	// User messages only; include one result per call, in a single message.
	ToolResults []ToolResult
	// Raw is the provider's native form of an assistant message, such as
	// Anthropic content blocks with signed thinking that must be echoed back
	// verbatim. Callers must not modify it. A provider uses Raw in place of the
	// neutral fields when it recognizes the type and ignores it otherwise.
	Raw any
}

// ToolDef describes a tool the model may call.
type ToolDef struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema object describing the tool's arguments.
	InputSchema json.RawMessage
	// Strict asks the provider to guarantee calls match InputSchema exactly.
	// Providers without strict tool use ignore it, so callers must still
	// validate ToolCall.Input.
	Strict bool
}

// ToolCall is a request from the model to run a tool.
type ToolCall struct {
	// ID links the call to its ToolResult. Providers that don't issue IDs
	// (Ollama) generate one.
	ID   string
	Name string
	// Input is the tool's arguments as a JSON object.
	Input json.RawMessage
}

// ToolResult is the outcome of running a ToolCall.
type ToolResult struct {
	// ToolCallID is the ID of the ToolCall this answers.
	ToolCallID string
	// Name is the tool's name. Ollama matches results to calls by name, not ID.
	Name    string
	Content string
	// IsError marks Content as an error message rather than a tool result.
	IsError bool
}

// StopReason says why the model stopped generating. Providers map their native
// reasons onto these constants and pass any others through verbatim.
type StopReason string

const (
	// StopEndTurn means the model finished its turn normally.
	StopEndTurn StopReason = "end_turn"
	// StopToolUse means the model is waiting on results for Message.ToolCalls.
	StopToolUse StopReason = "tool_use"
	// StopMaxTokens means output was cut off; Message may be incomplete.
	StopMaxTokens StopReason = "max_tokens"
	// StopRefusal means the model declined to answer; don't use Message content.
	StopRefusal StopReason = "refusal"
)

// Effort is how much reasoning the model should spend. Providers without an
// equivalent setting ignore it.
type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortMax    Effort = "max"
)

// Usage counts the tokens a call consumed. InputTokens excludes cached
// tokens, so total input is InputTokens + CacheReadTokens + CacheWriteTokens.
// Cache fields are zero for providers without prompt caching.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

// Add accumulates o into u, for per-episode totals.
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.CacheWriteTokens += o.CacheWriteTokens
}
