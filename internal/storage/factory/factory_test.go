package factory

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Rickyxstar/podcast-agent/internal/storage/s3"
)

func TestNew(t *testing.T) {
	ctx := context.Background()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "config"))
	t.Setenv("AWS_REGION", "us-east-1")
	dir := t.TempDir()

	tests := []struct {
		cfg      Config
		wantName string
	}{
		{Config{Dir: dir}, "disk"},
		{Config{Provider: " Disk ", Dir: dir}, "disk"},
		{Config{Provider: "s3", Bucket: "podcasts"}, "s3"},
		{Config{Provider: "S3", Bucket: "podcasts", S3Endpoint: "http://localhost:4566"}, "s3"},
	}
	for _, tt := range tests {
		p, err := New(ctx, tt.cfg)
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
	ctx := context.Background()
	if _, err := New(ctx, Config{Provider: "gcs"}); !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("unknown: err = %v, want ErrUnknownProvider", err)
	}
	if p, err := New(ctx, Config{Provider: "s3"}); !errors.Is(err, s3.ErrNoBucket) || p != nil {
		t.Errorf("s3 without bucket: p = %v, err = %v; want nil, ErrNoBucket", p, err)
	}
}
