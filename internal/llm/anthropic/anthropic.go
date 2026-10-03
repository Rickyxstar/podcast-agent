// Package anthropic implements llm.Provider on the Claude Messages API using
// the official Anthropic Go SDK.
package anthropic

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
)

const (
	// DefaultModel is used when neither Config nor the request names a model.
	DefaultModel = "claude-opus-5-5"
	// DefaultMaxTokens leaves room for adaptive thinking plus a long answer.
	// Requests are streamed, so a large cap doesn't risk HTTP timeouts.
	DefaultMaxTokens = 64000
)

// Config sets per-provider defaults. Fields left zero use the package defaults.
type Config struct {
	Model     string
	MaxTokens int
	// Effort applies when a request doesn't set its own. Empty uses the
	// model's default, which is "medium" on claude-opus-5-5.
	Effort llm.Effort
}

// Provider talks to Claude through the SDK's beta Messages service. The beta
// surface is used so refusal fallbacks work on the Claude API and Bedrock alike.
type Provider struct {
	name           string
	messages       *sdk.BetaMessageService
	cfg            Config
	serverFallback bool
}

var _ llm.Provider = (*Provider)(nil)

// New returns a Provider for the Claude API. Credentials come from the
// environment (ANTHROPIC_API_KEY or an `ant auth login` profile) unless opts
// say otherwise.
//
// Refused requests are retried server-side on Anthropic's recommended
// fallback model for the refusal category, so ChatResponse.Model may differ
// from the requested model.
func New(cfg Config, opts ...option.RequestOption) *Provider {
	client := sdk.NewClient(opts...)
	p := NewWithService("anthropic", &client.Beta.Messages, cfg)
	p.serverFallback = true
	return p
}

// NewWithService returns a Provider backed by any Messages-compatible
// service, such as the Beta.Messages of a Bedrock Mantle client. Server-side
// refusal fallback is Claude API only, so it is off here.
func NewWithService(name string, messages *sdk.BetaMessageService, cfg Config) *Provider {
	cfg.Model = cmp.Or(cfg.Model, DefaultModel)
	cfg.MaxTokens = cmp.Or(cfg.MaxTokens, DefaultMaxTokens)
	return &Provider{name: name, messages: messages, cfg: cfg}
}

// Name implements llm.Provider.
func (p *Provider) Name() string { return p.name }

// Chat implements llm.Provider.
func (p *Provider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	params, err := p.newParams(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.name, err)
	}
	msg, err := p.stream(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.name, err)
	}
	return toResponse(msg), nil
}

func (p *Provider) stream(ctx context.Context, params sdk.BetaMessageNewParams) (*sdk.BetaMessage, error) {
	stream := p.messages.NewStreaming(ctx, params)
	defer stream.Close()

	var msg sdk.BetaMessage
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return nil, fmt.Errorf("accumulate stream: %w", err)
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (p *Provider) newParams(req llm.ChatRequest) (sdk.BetaMessageNewParams, error) {
	messages, err := toMessageParams(req.Messages)
	if err != nil {
		return sdk.BetaMessageNewParams{}, err
	}
	params := sdk.BetaMessageNewParams{
		Model:     cmp.Or(req.Model, p.cfg.Model),
		MaxTokens: int64(cmp.Or(req.MaxTokens, p.cfg.MaxTokens)),
		Messages:  messages,
		// Summarized thinking gives the trace readable reasoning; the default
		// ("omitted") returns empty thinking text.
		Thinking: sdk.BetaThinkingConfigParamUnion{
			OfAdaptive: &sdk.BetaThinkingConfigAdaptiveParam{
				Display: sdk.BetaThinkingConfigAdaptiveDisplaySummarized,
			},
		},
		// Automatic caching moves the breakpoint to the end of the history on
		// each call, so a tool loop re-reads its prefix from cache.
		CacheControl: sdk.NewBetaCacheControlEphemeralParam(),
	}
	if req.System != "" {
		params.System = []sdk.BetaTextBlockParam{{Text: req.System}}
	}
	if effort := cmp.Or(req.Effort, p.cfg.Effort); effort != "" {
		params.OutputConfig.Effort = sdk.BetaOutputConfigEffort(effort)
	}
	if len(req.ResponseSchema) > 0 {
		var schema map[string]any
		if err := json.Unmarshal(req.ResponseSchema, &schema); err != nil {
			return sdk.BetaMessageNewParams{}, fmt.Errorf("response schema: %w", err)
		}
		params.OutputConfig.Format = sdk.BetaJSONOutputFormatParam{Schema: schema}
	}
	for _, t := range req.Tools {
		tool, err := toToolParam(t)
		if err != nil {
			return sdk.BetaMessageNewParams{}, err
		}
		params.Tools = append(params.Tools, sdk.BetaToolUnionParam{OfTool: tool})
	}
	if p.serverFallback {
		params.Fallbacks = sdk.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
		params.Betas = append(params.Betas, sdk.AnthropicBetaServerSideFallback2026_07_01)
	}
	return params, nil
}

// toToolParam splits a JSON Schema object into the SDK's typed fields
// (properties, required) and passes every other keyword through, such as
// additionalProperties, which strict tools require.
func toToolParam(t llm.ToolDef) (*sdk.BetaToolParam, error) {
	var schema map[string]any
	if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
		return nil, fmt.Errorf("tool %s: input schema: %w", t.Name, err)
	}
	input := sdk.BetaToolInputSchemaParam{
		Properties:  schema["properties"],
		ExtraFields: map[string]any{},
	}
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			s, ok := r.(string)
			if !ok {
				return nil, fmt.Errorf("tool %s: input schema: required entry %v is not a string", t.Name, r)
			}
			input.Required = append(input.Required, s)
		}
	}
	for k, v := range schema {
		switch k {
		case "type", "properties", "required":
		default:
			input.ExtraFields[k] = v
		}
	}

	tool := &sdk.BetaToolParam{Name: t.Name, InputSchema: input}
	if t.Description != "" {
		tool.Description = sdk.String(t.Description)
	}
	if t.Strict {
		tool.Strict = sdk.Bool(true)
	}
	return tool, nil
}

// toMessageParams converts the neutral history. Assistant messages this
// provider produced carry their exact SDK form in Raw, which is sent back
// unchanged so thinking signatures stay valid.
func toMessageParams(msgs []llm.Message) ([]sdk.BetaMessageParam, error) {
	out := make([]sdk.BetaMessageParam, 0, len(msgs))
	for i, m := range msgs {
		if raw, ok := m.Raw.(sdk.BetaMessageParam); ok {
			out = append(out, raw)
			continue
		}

		var blocks []sdk.BetaContentBlockParamUnion
		switch m.Role {
		case llm.RoleUser:
			// tool_result blocks must come before any text in the message.
			for _, r := range m.ToolResults {
				blocks = append(blocks, sdk.NewBetaToolResultBlock(r.ToolCallID, r.Content, r.IsError))
			}
			if m.Text != "" {
				blocks = append(blocks, sdk.NewBetaTextBlock(m.Text))
			}
		case llm.RoleAssistant:
			if m.Text != "" {
				blocks = append(blocks, sdk.NewBetaTextBlock(m.Text))
			}
			for _, c := range m.ToolCalls {
				input := c.Input
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, sdk.NewBetaToolUseBlock(c.ID, input, c.Name))
			}
		default:
			return nil, fmt.Errorf("message %d: unknown role %q", i, m.Role)
		}
		if len(blocks) == 0 {
			return nil, fmt.Errorf("message %d: %w", i, errEmptyMessage)
		}
		out = append(out, sdk.BetaMessageParam{Role: sdk.BetaMessageParamRole(m.Role), Content: blocks})
	}
	return out, nil
}

var errEmptyMessage = errors.New("message has no content")

func toResponse(msg *sdk.BetaMessage) *llm.ChatResponse {
	out := llm.Message{Role: llm.RoleAssistant, Raw: msg.ToParam()}
	var text, thinking strings.Builder
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "thinking":
			if block.Thinking == "" {
				continue
			}
			if thinking.Len() > 0 {
				thinking.WriteString("\n\n")
			}
			thinking.WriteString(block.Thinking)
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, llm.ToolCall{
				ID:    block.ID,
				Name:  block.Name,
				Input: block.Input,
			})
		}
	}
	out.Text = text.String()
	out.Thinking = thinking.String()

	return &llm.ChatResponse{
		Message:    out,
		StopReason: toStopReason(msg.StopReason),
		Model:      msg.Model,
		Usage: llm.Usage{
			InputTokens:      int(msg.Usage.InputTokens),
			OutputTokens:     int(msg.Usage.OutputTokens),
			CacheReadTokens:  int(msg.Usage.CacheReadInputTokens),
			CacheWriteTokens: int(msg.Usage.CacheCreationInputTokens),
		},
	}
}

func toStopReason(r sdk.BetaStopReason) llm.StopReason {
	switch r {
	case sdk.BetaStopReasonEndTurn, sdk.BetaStopReasonStopSequence:
		return llm.StopEndTurn
	case sdk.BetaStopReasonToolUse:
		return llm.StopToolUse
	case sdk.BetaStopReasonMaxTokens:
		return llm.StopMaxTokens
	case sdk.BetaStopReasonRefusal:
		return llm.StopRefusal
	default:
		return llm.StopReason(r)
	}
}
