// Package openai implements llm.Provider on the OpenAI Responses API using
// the official OpenAI Go SDK.
package openai

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
)

// DefaultModel is used when neither Config nor the request names a model.
const DefaultModel = "gpt-6.1-sol"

// Config sets per-provider defaults. Fields left zero use the package defaults.
type Config struct {
	Model string
	// MaxTokens caps output tokens, reasoning included, when a request
	// doesn't set its own. Zero leaves it to the model's limit.
	MaxTokens int
	// Effort applies when a request doesn't set its own. Empty uses the
	// model's default.
	Effort llm.Effort
}

// Provider talks to OpenAI through the Responses API. It runs statelessly
// (store=false): each call sends the full history, and reasoning carries
// across turns as encrypted reasoning items echoed back in Message.Raw.
type Provider struct {
	responses *responses.ResponseService
	cfg       Config
}

var _ llm.Provider = (*Provider)(nil)

// New returns a Provider for the OpenAI API. Credentials and endpoint come
// from the environment (OPENAI_API_KEY, OPENAI_BASE_URL) unless opts say
// otherwise.
func New(cfg Config, opts ...option.RequestOption) *Provider {
	client := sdk.NewClient(opts...)
	cfg.Model = cmp.Or(cfg.Model, DefaultModel)
	return &Provider{responses: &client.Responses, cfg: cfg}
}

// Name implements llm.Provider.
func (p *Provider) Name() string { return "openai" }

// Chat implements llm.Provider.
func (p *Provider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	params, err := p.newParams(req)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	resp, err := p.responses.New(ctx, params)
	if err != nil {
		// The SDK's Error() hides the API's reason, which is what explains a
		// 403 or 404 (no access to the model, wrong project, and so on).
		var apiErr *sdk.Error
		if errors.As(err, &apiErr) && apiErr.Message != "" {
			return nil, fmt.Errorf("openai: %w: %s: %s", err, cmp.Or(apiErr.Code, apiErr.Type), apiErr.Message)
		}
		return nil, fmt.Errorf("openai: %w", err)
	}
	if resp.Status == responses.ResponseStatusFailed {
		return nil, fmt.Errorf("openai: response failed: %s: %s", resp.Error.Code, resp.Error.Message)
	}
	return toResponse(resp), nil
}

func (p *Provider) newParams(req llm.ChatRequest) (responses.ResponseNewParams, error) {
	input, err := toInput(req.Messages)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	params := responses.ResponseNewParams{
		Model: cmp.Or(req.Model, p.cfg.Model),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: input},
		// Nothing is kept server-side, so encrypted reasoning must come back
		// in the output for the next call to replay.
		Store:   param.NewOpt(false),
		Include: []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
		// Summaries give the trace readable reasoning.
		Reasoning: shared.ReasoningParam{Summary: shared.ReasoningSummaryAuto},
	}
	if req.System != "" {
		params.Instructions = param.NewOpt(req.System)
	}
	if n := cmp.Or(req.MaxTokens, p.cfg.MaxTokens); n > 0 {
		params.MaxOutputTokens = param.NewOpt(int64(n))
	}
	if effort := cmp.Or(req.Effort, p.cfg.Effort); effort != "" {
		params.Reasoning.Effort = shared.ReasoningEffort(effort)
	}
	if len(req.ResponseSchema) > 0 {
		var schema map[string]any
		if err := json.Unmarshal(req.ResponseSchema, &schema); err != nil {
			return responses.ResponseNewParams{}, fmt.Errorf("response schema: %w", err)
		}
		params.Text.Format = responses.ResponseFormatTextConfigUnionParam{
			OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
				Name:   "response",
				Schema: schema,
				Strict: param.NewOpt(true),
			},
		}
	}
	for _, t := range req.Tools {
		var schema map[string]any
		if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
			return responses.ResponseNewParams{}, fmt.Errorf("tool %s: input schema: %w", t.Name, err)
		}
		tool := &responses.FunctionToolParam{Name: t.Name, Parameters: schema, Strict: param.NewOpt(t.Strict)}
		if t.Description != "" {
			tool.Description = param.NewOpt(t.Description)
		}
		params.Tools = append(params.Tools, responses.ToolUnionParam{OfFunction: tool})
	}
	return params, nil
}

// toInput converts the neutral history to input items. Assistant messages
// this provider produced carry their output items in Raw, which are sent
// back unchanged so encrypted reasoning stays attached to its tool calls.
func toInput(msgs []llm.Message) (responses.ResponseInputParam, error) {
	var out responses.ResponseInputParam
	for i, m := range msgs {
		if raw, ok := m.Raw.(responses.ResponseInputParam); ok {
			out = append(out, raw...)
			continue
		}

		n := len(out)
		switch m.Role {
		case llm.RoleUser:
			for _, r := range m.ToolResults {
				content := r.Content
				// function_call_output has no error flag, so say it in the text.
				if r.IsError {
					content = "Error: " + content
				}
				item := responses.ResponseInputItemParamOfFunctionCallOutput(content)
				item.OfFunctionCallOutput.CallID = param.NewOpt(r.ToolCallID)
				out = append(out, item)
			}
			if m.Text != "" {
				out = append(out, responses.ResponseInputItemParamOfMessage(m.Text, responses.EasyInputMessageRoleUser))
			}
		case llm.RoleAssistant:
			if m.Text != "" {
				out = append(out, responses.ResponseInputItemParamOfMessage(m.Text, responses.EasyInputMessageRoleAssistant))
			}
			for _, c := range m.ToolCalls {
				args := string(c.Input)
				if args == "" {
					args = "{}"
				}
				out = append(out, responses.ResponseInputItemParamOfFunctionCall(args, c.ID, c.Name))
			}
		default:
			return nil, fmt.Errorf("message %d: unknown role %q", i, m.Role)
		}
		if len(out) == n {
			return nil, fmt.Errorf("message %d: %w", i, errEmptyMessage)
		}
	}
	return out, nil
}

var errEmptyMessage = errors.New("message has no content")

func toResponse(resp *responses.Response) *llm.ChatResponse {
	out := llm.Message{Role: llm.RoleAssistant}
	var raw responses.ResponseInputParam
	var text, thinking strings.Builder
	refused := false
	for _, item := range resp.Output {
		raw = append(raw, param.Override[responses.ResponseInputItemUnionParam](json.RawMessage(item.RawJSON())))
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				switch c.Type {
				case "output_text":
					text.WriteString(c.Text)
				case "refusal":
					refused = true
					text.WriteString(c.Refusal)
				}
			}
		case "reasoning":
			for _, s := range item.AsReasoning().Summary {
				if thinking.Len() > 0 {
					thinking.WriteString("\n\n")
				}
				thinking.WriteString(s.Text)
			}
		case "function_call":
			call := item.AsFunctionCall()
			out.ToolCalls = append(out.ToolCalls, llm.ToolCall{
				ID:    call.CallID,
				Name:  call.Name,
				Input: json.RawMessage(call.Arguments),
			})
		}
	}
	out.Text = text.String()
	out.Thinking = thinking.String()
	out.Raw = raw

	// input_tokens counts cached and cache-written tokens too; llm.Usage
	// keeps them apart.
	details := resp.Usage.InputTokensDetails
	return &llm.ChatResponse{
		Message:    out,
		StopReason: toStopReason(resp, refused, len(out.ToolCalls) > 0),
		Model:      resp.Model,
		Usage: llm.Usage{
			InputTokens:      int(max(resp.Usage.InputTokens-details.CachedTokens-details.CacheWriteTokens, 0)),
			OutputTokens:     int(resp.Usage.OutputTokens),
			CacheReadTokens:  int(details.CachedTokens),
			CacheWriteTokens: int(details.CacheWriteTokens),
		},
	}
}

// toStopReason derives a stop reason, which the Responses API doesn't
// report directly: from incomplete_details, a refusal part, or the presence
// of function calls.
func toStopReason(resp *responses.Response, refused, hasToolCalls bool) llm.StopReason {
	if resp.Status == responses.ResponseStatusIncomplete {
		switch reason := resp.IncompleteDetails.Reason; reason {
		case "max_output_tokens":
			return llm.StopMaxTokens
		case "content_filter":
			return llm.StopRefusal
		default:
			return llm.StopReason(reason)
		}
	}
	switch {
	case refused:
		return llm.StopRefusal
	case hasToolCalls:
		return llm.StopToolUse
	default:
		return llm.StopEndTurn
	}
}
