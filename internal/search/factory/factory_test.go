package factory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rickyxstar/podcast-agent/internal/search/brave"
)

func TestNew(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "facts.json"), []byte(`[{"id":"a","statement":"x"}]`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(brave.APIKeyEnv, "test-key")

	tests := []struct {
		cfg      Config
		wantName string
	}{
		{Config{KBDir: dir}, "kb"},
		{Config{Provider: " KB ", KBDir: dir}, "kb"},
		{Config{Provider: "brave"}, "brave"},
		{Config{Provider: "Brave", BraveAPIKey: "explicit"}, "brave"},
	}
	for _, tt := range tests {
		p, err := New(tt.cfg)
		if err != nil {
			t.Errorf("New(%+v): %v", tt.cfg, err)
			continue
		}
		if p.Name() != tt.wantName {
			t.Errorf("New(%+v).Name() = %q, want %q", tt.cfg, p.Name(), tt.wantName)
		}
	}
}

func TestNewErrors(t *testing.T) {
	t.Setenv(brave.APIKeyEnv, "")
	if _, err := New(Config{Provider: "tavily"}); !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("unknown: err = %v, want ErrUnknownProvider", err)
	}
	if p, err := New(Config{Provider: "brave"}); !errors.Is(err, brave.ErrNoAPIKey) || p != nil {
		t.Errorf("brave without key: p = %v, err = %v; want nil, ErrNoAPIKey", p, err)
	}
	if p, err := New(Config{KBDir: t.TempDir()}); err == nil || p != nil {
		t.Errorf("empty KB dir: p = %v, err = %v; want nil and an error", p, err)
	}
}
