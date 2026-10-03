package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
	llmfactory "github.com/Rickyxstar/podcast-agent/internal/llm/factory"
	"github.com/Rickyxstar/podcast-agent/internal/search"
	searchfactory "github.com/Rickyxstar/podcast-agent/internal/search/factory"
	"github.com/Rickyxstar/podcast-agent/internal/storage"
	storagefactory "github.com/Rickyxstar/podcast-agent/internal/storage/factory"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// config holds the settings shared by every subcommand. Each field is a flag
// whose default comes from the environment variable named in its help text,
// so the container can be configured with env alone.
type config struct {
	LLM     llmfactory.Config
	Search  searchfactory.Config
	Storage storagefactory.Config
	Timeout time.Duration
	Debug   bool
}

func newRootCmd() *cobra.Command {
	cfg := &config{}

	cmd := &cobra.Command{
		Use:           "podcast-agent",
		Short:         "Summarize, annotate and fact-check podcast transcripts",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			level := slog.LevelInfo
			if cfg.Debug {
				level = slog.LevelDebug
			}
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
		},
	}

	f := cmd.PersistentFlags()
	f.StringVar(&cfg.LLM.Provider, "llm", envOr("LLM_PROVIDER", llmfactory.Anthropic), "LLM provider: anthropic, bedrock or ollama [LLM_PROVIDER]")
	f.StringVar(&cfg.LLM.Model, "model", os.Getenv("LLM_MODEL"), "model override in the provider's naming [LLM_MODEL]")
	f.StringVar(&cfg.LLM.OllamaHost, "ollama-host", os.Getenv("OLLAMA_HOST"), "Ollama server URL [OLLAMA_HOST]")

	f.StringVar(&cfg.Search.Provider, "search", envOr("SEARCH_PROVIDER", searchfactory.KB), "search provider: kb or brave [SEARCH_PROVIDER]")
	f.StringVar(&cfg.Search.KBDir, "kb-dir", envOr("KB_DIR", searchfactory.DefaultKBDir), "knowledge base directory [KB_DIR]")

	f.StringVar(&cfg.Storage.Provider, "storage", envOr("STORAGE", storagefactory.Disk), "storage provider: disk or s3 [STORAGE]")
	f.StringVar(&cfg.Storage.Dir, "out", envOr("OUT_DIR", storagefactory.DefaultDir), "disk storage root [OUT_DIR]")
	f.StringVar(&cfg.Storage.Bucket, "bucket", os.Getenv("S3_BUCKET"), "S3 bucket [S3_BUCKET]")
	f.StringVar(&cfg.Storage.S3Endpoint, "s3-endpoint", os.Getenv("AWS_ENDPOINT_URL"), "S3 endpoint override, e.g. LocalStack [AWS_ENDPOINT_URL]")

	f.StringVar(&cfg.LLM.AWSRegion, "aws-region", os.Getenv("AWS_REGION"), "AWS region for Bedrock and S3 [AWS_REGION]")

	f.DurationVar(&cfg.Timeout, "timeout", envDuration("TIMEOUT", 10*time.Minute), "per-episode timeout [TIMEOUT]")
	f.BoolVar(&cfg.Debug, "debug", os.Getenv("DEBUG") != "", "enable debug logging [DEBUG]")

	cmd.AddCommand(newRunCmd(cfg), newWorkerCmd(cfg))
	return cmd
}

// deps are the providers built from config.
type deps struct {
	llm     llm.Provider
	search  search.Provider
	storage storage.Provider
}

// build constructs the configured providers.
func (c *config) build(ctx context.Context) (*deps, error) {
	// One --aws-region flag serves both Bedrock and S3.
	c.Storage.AWSRegion = c.LLM.AWSRegion

	var d deps
	var err error
	if d.llm, err = llmfactory.New(ctx, c.LLM); err != nil {
		return nil, err
	}
	if d.search, err = searchfactory.New(c.Search); err != nil {
		return nil, err
	}
	if d.storage, err = storagefactory.New(ctx, c.Storage); err != nil {
		return nil, err
	}
	return &d, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return d
	}
	return def
}
