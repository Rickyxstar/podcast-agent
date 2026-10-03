// Package storage defines a provider-neutral object store so the agent can
// read transcripts and write reports on local disk or in S3 without changing
// the orchestration code.
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound is returned, wrapped, by Get and Stat when key doesn't exist.
// Check it with errors.Is.
var ErrNotFound = errors.New("object not found")

// Provider is an object store addressed by key.
//
// Keys are slash-separated paths relative to the provider's root, such as
// "incoming/ep001.json" or "results/ep001/report.json". They never start
// with a slash, on every platform; local disk maps them onto its directory
// and S3 uses them as object keys under its bucket.
//
//mockery:generate: true
//mockery:filename: mock/mock.go
type Provider interface {
	// Name identifies the provider in logs and reports, e.g. "s3".
	Name() string
	// Get opens the object at key for reading. The caller must close the
	// returned reader.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Put writes body to key, replacing any existing object. Readers never
	// see a partially written object. contentType is the MIME type, e.g.
	// "application/json"; empty lets the provider choose.
	Put(ctx context.Context, key string, body io.Reader, contentType string) error
	// Stat returns the object's metadata without reading its contents.
	Stat(ctx context.Context, key string) (*ObjectInfo, error)
}

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Key  string
	Size int64
	// ModTime is when the object was last written.
	ModTime time.Time
	// ETag is an opaque version tag that changes whenever the content does.
	// Compare it only with ETags from the same provider.
	ETag        string
	ContentType string
}
