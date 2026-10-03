// Package s3 implements storage.Provider on an Amazon S3 bucket, or on an
// S3-compatible endpoint such as LocalStack.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/Rickyxstar/podcast-agent/internal/storage"
)

// ErrNoBucket is returned by New when Config.Bucket is empty.
var ErrNoBucket = errors.New("s3: no bucket configured")

// Config configures the S3 provider.
type Config struct {
	// Bucket holds the objects. Required.
	Bucket string
	// Region is the bucket's AWS region. Empty uses AWS_REGION.
	Region string
	// Endpoint overrides the S3 endpoint URL, e.g. "http://localhost:4566"
	// for LocalStack. Empty uses AWS_ENDPOINT_URL_S3 or AWS_ENDPOINT_URL if
	// set, else AWS. With any custom endpoint, requests use path-style
	// addressing (endpoint/bucket/key), which LocalStack needs.
	Endpoint string
}

// Provider stores objects in one bucket. It is safe for concurrent use.
type Provider struct {
	client *awss3.Client
	bucket string
}

var _ storage.Provider = (*Provider)(nil)

// New returns a Provider for cfg.Bucket. Credentials come from the default
// AWS chain (env vars, shared profile, EKS Pod Identity or IRSA). optFns
// adjust the client after Config is applied.
func New(ctx context.Context, cfg Config, optFns ...func(*awss3.Options)) (*Provider, error) {
	if cfg.Bucket == "" {
		return nil, ErrNoBucket
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("s3: %w", err)
	}
	optFns = append([]func(*awss3.Options){func(o *awss3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		// BaseEndpoint already holds any endpoint from the environment.
		o.UsePathStyle = o.BaseEndpoint != nil
	}}, optFns...)
	return &Provider{client: awss3.NewFromConfig(awsCfg, optFns...), bucket: cfg.Bucket}, nil
}

// Name returns "s3".
func (p *Provider) Name() string { return "s3" }

// Bucket returns the bucket name.
func (p *Provider) Bucket() string { return p.bucket }

// Get streams the object at key.
func (p *Provider) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := storage.CheckKey(key); err != nil {
		return nil, fmt.Errorf("s3: get: %w", err)
	}
	out, err := p.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, wrapErr("get", key, err)
	}
	return out.Body, nil
}

// Put uploads body in a single request; S3 replaces objects atomically. A
// body that isn't an io.ReadSeeker is read into memory first, because the
// SDK must know the length and, over plain HTTP, hash the payload before
// sending it. Empty contentType is derived from the key's extension.
func (p *Provider) Put(ctx context.Context, key string, body io.Reader, contentType string) error {
	if err := storage.CheckKey(key); err != nil {
		return fmt.Errorf("s3: put: %w", err)
	}
	if _, ok := body.(io.ReadSeeker); !ok {
		b, err := io.ReadAll(body)
		if err != nil {
			return fmt.Errorf("s3: put %s: %w", key, err)
		}
		body = bytes.NewReader(b)
	}
	if contentType == "" {
		contentType = mime.TypeByExtension(path.Ext(key))
	}
	in := &awss3.PutObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(key),
		Body:   body,
	}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	if _, err := p.client.PutObject(ctx, in); err != nil {
		return wrapErr("put", key, err)
	}
	return nil
}

// Stat issues a HEAD request for key.
func (p *Provider) Stat(ctx context.Context, key string) (*storage.ObjectInfo, error) {
	if err := storage.CheckKey(key); err != nil {
		return nil, fmt.Errorf("s3: stat: %w", err)
	}
	out, err := p.client.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, wrapErr("stat", key, err)
	}
	return &storage.ObjectInfo{
		Key:     key,
		Size:    aws.ToInt64(out.ContentLength),
		ModTime: aws.ToTime(out.LastModified),
		// S3 quotes ETags on the wire.
		ETag:        strings.Trim(aws.ToString(out.ETag), `"`),
		ContentType: aws.ToString(out.ContentType),
	}, nil
}

// wrapErr maps a missing key to storage.ErrNotFound. GET reports it as
// NoSuchKey; HEAD has no body, so the SDK reports a bare NotFound. A missing
// bucket also gives NotFound on HEAD, but then every Put fails too.
func wrapErr(op, key string, err error) error {
	var noKey *types.NoSuchKey
	var notFound *types.NotFound
	if errors.As(err, &noKey) || errors.As(err, &notFound) {
		return fmt.Errorf("s3: %s %s: %w", op, key, storage.ErrNotFound)
	}
	return fmt.Errorf("s3: %s %s: %w", op, key, err)
}
