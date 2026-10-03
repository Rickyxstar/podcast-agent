// Package factory builds the configured llm.Provider. It lives outside
// package llm because the provider packages import llm.
package factory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
	"github.com/Rickyxstar/podcast-agent/internal/llm/anthropic"
	"github.com/Rickyxstar/podcast-agent/internal/llm/bedrock"
	"github.com/Rickyxstar/podcast-agent/internal/llm/ollama"
)

// ErrUnknownProvider is returned by New for a provider name it doesn't know.
var ErrUnknownProvider = errors.New("unknown LLM provider")

// Provider names accepted in Config.Provider.
const (
	Anthropic = "anthropic"
	Bedrock   = "bedrock"
	Ollama    = "ollama"
)

// Config selects and configures a provider. Credentials are not here: the
// SDKs resolve them (ANTHROPIC_API_KEY or an `ant` profile; the AWS
// credential chain, including EKS Pod Identity).
type Config struct {
	// Provider is one of the names above, case-insensitive. Empty means Anthropic.
	Provider string
	// Model overrides the provider's default model. Use the provider's own
	// naming, e.g. "anthropic.claude-opus-5-5" on Bedrock or "qwen2.5:7b" on Ollama.
	Model string
	// AWSRegion is the Bedrock region. Bedrock only.
	AWSRegion string
	// OllamaHost is the Ollama server URL. Empty uses ollama.DefaultHost.
	OllamaHost string
}

// New returns the provider named by cfg.Provider.
func New(ctx context.Context, cfg Config) (llm.Provider, error) {
	switch name := strings.ToLower(strings.TrimSpace(cfg.Provider)); name {
	case "", Anthropic:
		return anthropic.New(anthropic.Config{Model: cfg.Model}), nil
	case Bedrock:
		bc := bedrock.Config{Region: cfg.AWSRegion}
		bc.Model = cfg.Model
		return bedrock.New(ctx, bc)
	case Ollama:
		return ollama.New(ollama.Config{Host: cfg.OllamaHost, Model: cfg.Model}), nil
	default:
		return nil, fmt.Errorf("%q: %w (want %s, %s or %s)", cfg.Provider, ErrUnknownProvider, Anthropic, Bedrock, Ollama)
	}
}
