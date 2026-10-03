// Package bedrock implements llm.Provider on Amazon Bedrock. Bedrock's Mantle
// endpoint speaks the Claude Messages API, so this package only builds the
// client and reuses the anthropic provider for everything else.
package bedrock

import (
	"cmp"
	"context"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	sdkbedrock "github.com/anthropics/anthropic-sdk-go/bedrock"
	"github.com/anthropics/anthropic-sdk-go/lib/betafallback"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/Rickyxstar/podcast-agent/internal/llm/anthropic"
)

const (
	// DefaultModel is the Bedrock ID of claude-opus-5-5.
	DefaultModel = "anthropic.claude-opus-5-5"
	// DefaultFallbackModel serves requests the main model refuses.
	DefaultFallbackModel = "anthropic.claude-opus-4-8"
)

// Config configures the Bedrock provider.
type Config struct {
	// anthropic.Config sets model, token and effort defaults. Model IDs take
	// Bedrock's "anthropic." prefix.
	anthropic.Config
	// Region is the AWS region. Empty uses AWS_REGION.
	Region string
	// FallbackModel retries refused requests. Empty uses DefaultFallbackModel.
	FallbackModel string
}

// New returns a Provider for Claude on Bedrock. Credentials come from the
// default AWS chain (env vars, shared profile, EKS Pod Identity or IRSA) and
// are signed with SigV4, or from AWS_BEARER_TOKEN_BEDROCK when set.
//
// Bedrock has no server-side refusal fallback, so refused requests are retried
// client-side on FallbackModel; ChatResponse.Model reports the model that
// answered.
func New(ctx context.Context, cfg Config, opts ...option.RequestOption) (*anthropic.Provider, error) {
	fallback := sdk.BetaFallbackParam{Model: cmp.Or(cfg.FallbackModel, DefaultFallbackModel)}
	// The fallback middleware must see the request before SigV4 signs it;
	// NewMantleClient runs caller middleware ahead of its signer.
	opts = append([]option.RequestOption{
		option.WithMiddleware(betafallback.BetaRefusalFallbackMiddleware([]sdk.BetaFallbackParam{fallback})),
	}, opts...)

	client, err := sdkbedrock.NewMantleClient(ctx, sdkbedrock.MantleClientConfig{AWSRegion: cfg.Region}, opts...)
	if err != nil {
		return nil, fmt.Errorf("bedrock: %w", err)
	}
	cfg.Model = cmp.Or(cfg.Model, DefaultModel)
	return anthropic.NewWithService("bedrock", &client.Beta.Messages, cfg.Config), nil
}
