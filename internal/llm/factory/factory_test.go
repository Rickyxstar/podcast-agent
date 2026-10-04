package factory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
	"github.com/Rickyxstar/podcast-agent/internal/llm/anthropic"
	"github.com/Rickyxstar/podcast-agent/internal/llm/bedrock"
	"github.com/Rickyxstar/podcast-agent/internal/llm/ollama"
)

// TestNew builds each provider against a fake server and checks which
// provider came back and which model it asked for. The server answers 400 so
// no provider retries.
func TestNew(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct{ Model string }
		json.Unmarshal(raw, &body)
		gotModel = body.Model
		http.Error(w, `{"error":"stop here"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "test-key")
	t.Setenv("ANTHROPIC_BEDROCK_MANTLE_BASE_URL", srv.URL)

	tests := []struct {
		cfg       Config
		wantName  string
		wantModel string
	}{
		// Credentials and test endpoints for the SDK-backed providers still
		// come from the environment, set above.
		{Config{}, "anthropic", anthropic.DefaultModel},
		{Config{Provider: "anthropic", Model: "claude-sonnet-5-5"}, "anthropic", "claude-sonnet-5-5"},
		{Config{Provider: "bedrock", AWSRegion: "us-east-1"}, "bedrock", bedrock.DefaultModel},
		{Config{Provider: " Bedrock ", Model: "anthropic.claude-opus-5", AWSRegion: "us-east-1"}, "bedrock", "anthropic.claude-opus-5"},
		{Config{Provider: "ollama", OllamaHost: srv.URL}, "ollama", ollama.DefaultModel},
		{Config{Provider: "OLLAMA", Model: "llama3.1:8b", OllamaHost: srv.URL}, "ollama", "llama3.1:8b"},
	}
	for _, tt := range tests {
		p, err := New(context.Background(), tt.cfg)
		if err != nil {
			t.Errorf("New(%+v): %v", tt.cfg, err)
			continue
		}
		if p.Name() != tt.wantName {
			t.Errorf("New(%+v).Name() = %q, want %q", tt.cfg, p.Name(), tt.wantName)
		}
		gotModel = ""
		p.Chat(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}})
		if gotModel != tt.wantModel {
			t.Errorf("New(%+v) sent model %q, want %q", tt.cfg, gotModel, tt.wantModel)
		}
	}
}

func TestNewUnknown(t *testing.T) {
	_, err := New(context.Background(), Config{Provider: "openai"})
	if !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("err = %v, want ErrUnknownProvider", err)
	}
}

func TestModel(t *testing.T) {
	tests := []struct {
		cfg  Config
		want string
	}{
		{Config{}, anthropic.DefaultModel},
		{Config{Provider: "anthropic", Model: "claude-sonnet-5-5"}, "claude-sonnet-5-5"},
		{Config{Provider: " Bedrock "}, bedrock.DefaultModel},
		{Config{Provider: "ollama"}, ollama.DefaultModel},
		{Config{Provider: "openai"}, ""},
	}
	for _, tt := range tests {
		if got := Model(tt.cfg); got != tt.want {
			t.Errorf("Model(%+v) = %q, want %q", tt.cfg, got, tt.want)
		}
	}
}
