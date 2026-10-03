package disk

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rickyxstar/podcast-agent/internal/storage"
)

func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	p, err := New(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func get(t *testing.T, p *Provider, key string) string {
	t.Helper()
	r, err := p.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPutGetStat(t *testing.T) {
	ctx := context.Background()
	p := newTestProvider(t)
	const key = "results/ep001/report.json"

	if err := p.Put(ctx, key, strings.NewReader(`{"v":1}`), ""); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := get(t, p, key); got != `{"v":1}` {
		t.Errorf("Get = %q", got)
	}
	info, err := p.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Key != key || info.Size != 7 || info.ContentType != "application/json" || info.ETag == "" || info.ModTime.IsZero() {
		t.Errorf("Stat = %+v", info)
	}

	// Overwrite: new content, new ETag, and no temp files left behind.
	if err := p.Put(ctx, key, strings.NewReader(`{"v":2}`), ""); err != nil {
		t.Fatalf("Put again: %v", err)
	}
	if got := get(t, p, key); got != `{"v":2}` {
		t.Errorf("Get after overwrite = %q", got)
	}
	info2, err := p.Stat(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if info2.ETag == info.ETag {
		t.Errorf("ETag unchanged after overwrite: %s", info.ETag)
	}
	entries, err := os.ReadDir(filepath.Join(p.Dir(), "results", "ep001"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("results/ep001 holds %d entries, want only report.json", len(entries))
	}
}

func TestNotFound(t *testing.T) {
	ctx := context.Background()
	p := newTestProvider(t)
	if err := p.Put(ctx, "results/ep001/report.md", strings.NewReader("# hi"), ""); err != nil {
		t.Fatal(err)
	}
	// A directory is not an object.
	for _, key := range []string{"missing.json", "results/ep001", "results/ep002/report.md"} {
		if _, err := p.Get(ctx, key); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("Get(%q): err = %v, want ErrNotFound", key, err)
		}
		if _, err := p.Stat(ctx, key); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("Stat(%q): err = %v, want ErrNotFound", key, err)
		}
	}
}

func TestInvalidKey(t *testing.T) {
	ctx := context.Background()
	p := newTestProvider(t)
	for _, key := range []string{"", ".", "/etc/passwd", "../escape", "a/../../escape", "a//b", "a/", `a\b`} {
		if err := p.Put(ctx, key, strings.NewReader("x"), ""); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("Put(%q): err = %v, want ErrInvalidKey", key, err)
		}
		if _, err := p.Get(ctx, key); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("Get(%q): err = %v, want ErrInvalidKey", key, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(p.Dir()), "escape")); !os.IsNotExist(err) {
		t.Errorf("a file escaped the root: %v", err)
	}
}

func TestSymlinkEscape(t *testing.T) {
	ctx := context.Background()
	p := newTestProvider(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(p.Dir(), "link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if _, err := p.Get(ctx, "link/secret"); err == nil {
		t.Error("Get through a symlink out of the root succeeded")
	}
	if err := p.Put(ctx, "link/new", strings.NewReader("x"), ""); err == nil {
		t.Error("Put through a symlink out of the root succeeded")
	}
}

func TestCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := newTestProvider(t)
	if err := p.Put(ctx, "a.txt", strings.NewReader("x"), ""); !errors.Is(err, context.Canceled) {
		t.Errorf("Put: err = %v, want context.Canceled", err)
	}
	if _, err := p.Get(ctx, "a.txt"); !errors.Is(err, context.Canceled) {
		t.Errorf("Get: err = %v, want context.Canceled", err)
	}
}
