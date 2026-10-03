// Package search defines a provider-neutral search interface so the
// fact-check agent can look up evidence in the curated knowledge base or on
// the web without changing the orchestration code.
package search

import (
	"context"
	"time"
)

// Provider is a search backend.
//
//mockery:generate: true
//mockery:filename: mock/mock.go
type Provider interface {
	// Name identifies the provider in logs and reports, e.g. "brave".
	Name() string
	// Search returns results for req, best match first. No matches is an
	// empty slice and a nil error.
	Search(ctx context.Context, req Request) ([]Result, error)
}

// Request is a single search.
type Request struct {
	Query string
	// MaxResults caps the number of results. Zero uses the provider default.
	MaxResults int
}

// Result is one piece of evidence.
type Result struct {
	// ID is a stable identifier: the entry ID for the knowledge base, the
	// URL for web results.
	ID    string
	Title string
	URL   string
	// Snippet is the matching text as plain text, without HTML markup.
	Snippet string
	// Source names who published the evidence, e.g. "FDA" or "www.nasa.gov".
	Source string
	// Kind says where the result came from, for source-quality scoring.
	Kind Kind
	// Published is when the evidence was published or last updated, if known.
	Published *time.Time
	// Score ranks results within one response. Scales differ between
	// providers, so don't compare scores across calls.
	Score float64
}

// Kind is the origin of a Result.
type Kind string

const (
	// KindKB is a curated knowledge-base entry.
	KindKB Kind = "kb"
	// KindWeb is a web search hit of unknown quality.
	KindWeb Kind = "web"
)
