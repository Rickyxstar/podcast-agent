// Package factory builds the configured search.Provider. It lives outside
// package search because the provider packages import search.
package factory

import (
	"cmp"
	"errors"
	"fmt"
	"strings"

	"github.com/Rickyxstar/podcast-agent/internal/search"
	"github.com/Rickyxstar/podcast-agent/internal/search/brave"
	"github.com/Rickyxstar/podcast-agent/internal/search/kb"
)

// ErrUnknownProvider is returned by New for a provider name it doesn't know.
var ErrUnknownProvider = errors.New("unknown search provider")

// Provider names accepted in Config.Provider.
const (
	KB    = "kb"
	Brave = "brave"
)

// DefaultKBDir is the knowledge-base directory used when Config.KBDir is empty.
const DefaultKBDir = "kb"

// Config selects and configures a provider.
type Config struct {
	// Provider is one of the names above, case-insensitive. Empty means KB.
	Provider string
	// KBDir holds the knowledge base's *.json files. KB only.
	KBDir string
	// BraveAPIKey is the Brave subscription token. Empty reads
	// brave.APIKeyEnv. Brave only.
	BraveAPIKey string
}

// New returns the provider named by cfg.Provider.
func New(cfg Config) (search.Provider, error) {
	// Each case checks err itself: returning a nil *kb.Provider directly would
	// give the caller a non-nil search.Provider.
	switch name := strings.ToLower(strings.TrimSpace(cfg.Provider)); name {
	case "", KB:
		p, err := kb.LoadDir(cmp.Or(cfg.KBDir, DefaultKBDir))
		if err != nil {
			return nil, err
		}
		return p, nil
	case Brave:
		p, err := brave.New(brave.Config{APIKey: cfg.BraveAPIKey})
		if err != nil {
			return nil, err
		}
		return p, nil
	default:
		return nil, fmt.Errorf("%q: %w (want %s or %s)", cfg.Provider, ErrUnknownProvider, KB, Brave)
	}
}
