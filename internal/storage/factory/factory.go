// Package factory builds the configured storage.Provider. It lives outside
// package storage because the provider packages import storage.
package factory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Rickyxstar/podcast-agent/internal/storage"
	"github.com/Rickyxstar/podcast-agent/internal/storage/disk"
	"github.com/Rickyxstar/podcast-agent/internal/storage/s3"
)

// ErrUnknownProvider is returned by New for a provider name it doesn't know.
var ErrUnknownProvider = errors.New("unknown storage provider")

// Provider names accepted in Config.Provider.
const (
	Disk = "disk"
	S3   = "s3"
)

// DefaultDir is the disk root used when Config.Dir is empty, so keys such as
// "results/ep001/report.json" land under the working directory.
const DefaultDir = "."

// Config selects and configures a provider. AWS credentials are not here:
// the SDK resolves them from the default chain.
type Config struct {
	// Provider is one of the names above, case-insensitive. Empty means Disk.
	Provider string
	// Dir is the root directory. Disk only.
	Dir string
	// Bucket is the S3 bucket. Required for S3.
	Bucket string
	// AWSRegion is the bucket's region. Empty uses AWS_REGION. S3 only.
	AWSRegion string
	// S3Endpoint overrides the S3 endpoint, e.g. for LocalStack. Empty uses
	// AWS_ENDPOINT_URL. S3 only.
	S3Endpoint string
}

// New returns the provider named by cfg.Provider.
func New(ctx context.Context, cfg Config) (storage.Provider, error) {
	// Each case checks err itself: returning a nil *disk.Provider directly
	// would give the caller a non-nil storage.Provider.
	switch name := strings.ToLower(strings.TrimSpace(cfg.Provider)); name {
	case "", Disk:
		p, err := disk.New(cmp.Or(cfg.Dir, DefaultDir))
		if err != nil {
			return nil, err
		}
		return p, nil
	case S3:
		p, err := s3.New(ctx, s3.Config{Bucket: cfg.Bucket, Region: cfg.AWSRegion, Endpoint: cfg.S3Endpoint})
		if err != nil {
			return nil, err
		}
		return p, nil
	default:
		return nil, fmt.Errorf("%q: %w (want %s or %s)", cfg.Provider, ErrUnknownProvider, Disk, S3)
	}
}
