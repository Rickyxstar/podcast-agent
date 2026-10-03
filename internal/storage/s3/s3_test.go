package s3

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rickyxstar/podcast-agent/internal/storage"
)

const testBucket = "podcasts"

type object struct {
	body        []byte
	contentType string
}

// fakeS3 serves GET, PUT and HEAD for one bucket with path-style addressing.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string]object
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key, ok := strings.CutPrefix(r.URL.Path, "/"+testBucket+"/")
	if !ok {
		http.Error(w, "want path-style /"+testBucket+"/key, got "+r.URL.Path, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.objects[key] = object{b, r.Header.Get("Content-Type")}
		w.Header().Set("ETag", etag(b))
	case http.MethodGet, http.MethodHead:
		obj, ok := f.objects[key]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			if r.Method == http.MethodGet {
				io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
			}
			return
		}
		w.Header().Set("ETag", etag(obj.body))
		w.Header().Set("Content-Type", obj.contentType)
		w.Header().Set("Last-Modified", time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC).Format(http.TimeFormat))
		w.Header().Set("Content-Length", strconv.Itoa(len(obj.body)))
		if r.Method == http.MethodGet {
			w.Write(obj.body)
		}
	default:
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	}
}

func etag(b []byte) string {
	sum := md5.Sum(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// setAWSEnv isolates the SDK from the developer's AWS config.
func setAWSEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ENDPOINT_URL", "")
	t.Setenv("AWS_ENDPOINT_URL_S3", "")
}

func newTestProvider(t *testing.T) (*Provider, *fakeS3) {
	t.Helper()
	setAWSEnv(t)
	fake := &fakeS3{objects: map[string]object{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	p, err := New(context.Background(), Config{Bucket: testBucket, Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return p, fake
}

func TestPutGetStat(t *testing.T) {
	ctx := context.Background()
	p, fake := newTestProvider(t)
	const key = "results/ep001/report.json"
	const body = `{"v":1}`

	// A plain io.Reader, not a ReadSeeker, exercises the buffering path.
	if err := p.Put(ctx, key, io.MultiReader(strings.NewReader(body)), ""); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := fake.objects[key]; string(got.body) != body || got.contentType != "application/json" {
		t.Errorf("stored %q as %q", got.body, got.contentType)
	}

	r, err := p.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil || string(got) != body {
		t.Errorf("Get = %q, %v", got, err)
	}

	info, err := p.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	wantETag := strings.Trim(etag([]byte(body)), `"`)
	if info.Key != key || info.Size != int64(len(body)) || info.ETag != wantETag ||
		info.ContentType != "application/json" || info.ModTime.IsZero() {
		t.Errorf("Stat = %+v", info)
	}
}

func TestExplicitContentType(t *testing.T) {
	p, fake := newTestProvider(t)
	if err := p.Put(context.Background(), "results/ep001/trace.jsonl", strings.NewReader("{}\n"), "application/x-ndjson"); err != nil {
		t.Fatal(err)
	}
	if ct := fake.objects["results/ep001/trace.jsonl"].contentType; ct != "application/x-ndjson" {
		t.Errorf("content type = %q", ct)
	}
}

func TestNotFound(t *testing.T) {
	ctx := context.Background()
	p, _ := newTestProvider(t)
	if _, err := p.Get(ctx, "missing.json"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Get: err = %v, want ErrNotFound", err)
	}
	if _, err := p.Stat(ctx, "missing.json"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Stat: err = %v, want ErrNotFound", err)
	}
}

func TestInvalidKey(t *testing.T) {
	ctx := context.Background()
	p, fake := newTestProvider(t)
	for _, key := range []string{"", "/abs", "a/../b", `a\b`} {
		if err := p.Put(ctx, key, strings.NewReader("x"), ""); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("Put(%q): err = %v, want ErrInvalidKey", key, err)
		}
	}
	if len(fake.objects) != 0 {
		t.Errorf("invalid keys reached the server: %v", fake.objects)
	}
}

func TestEndpointFromEnv(t *testing.T) {
	setAWSEnv(t)
	fake := &fakeS3{objects: map[string]object{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	t.Setenv("AWS_ENDPOINT_URL", srv.URL)

	p, err := New(context.Background(), Config{Bucket: testBucket})
	if err != nil {
		t.Fatal(err)
	}
	// The fake rejects virtual-hosted requests, so success means path style.
	if err := p.Put(context.Background(), "incoming/ep001.json", strings.NewReader("{}"), ""); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

func TestNoBucket(t *testing.T) {
	if _, err := New(context.Background(), Config{}); !errors.Is(err, ErrNoBucket) {
		t.Errorf("err = %v, want ErrNoBucket", err)
	}
}
