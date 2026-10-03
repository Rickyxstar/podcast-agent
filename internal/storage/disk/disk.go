// Package disk implements storage.Provider on a local directory. Keys map to
// files beneath it, and neither ".." nor symlinks can reach outside it.
package disk

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"

	"github.com/Rickyxstar/podcast-agent/internal/storage"
)

// Provider stores objects as files under a directory. It keeps no state
// between calls, so it is safe for concurrent use.
type Provider struct {
	dir string
}

var _ storage.Provider = (*Provider)(nil)

// New returns a Provider rooted at dir, creating the directory if needed.
func New(dir string) (*Provider, error) {
	// Absolute, so a later chdir doesn't move the store.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("disk: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("disk: %w", err)
	}
	return &Provider{dir: abs}, nil
}

// Name returns "disk".
func (p *Provider) Name() string { return "disk" }

// Dir returns the absolute root directory.
func (p *Provider) Dir() string { return p.dir }

// Get opens the file for key.
func (p *Provider) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	f, _, err := p.openFile(ctx, "get", key)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Put writes body to a temporary file next to key's file and renames it into
// place, so readers see the old file or the new one, never a partial write.
// Parent directories are created as needed. contentType is not stored; Stat
// derives it from the key's extension.
func (p *Provider) Put(ctx context.Context, key string, body io.Reader, contentType string) error {
	root, name, err := p.openRoot(ctx, "put", key)
	if err != nil {
		return err
	}
	defer root.Close()

	dir := filepath.Dir(name)
	if err := root.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("disk: put %s: %w", key, err)
	}
	tmp := filepath.Join(dir, ".tmp-"+rand.Text())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("disk: put %s: %w", key, err)
	}
	_, err = io.Copy(f, ctxReader{ctx, body})
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, name)
	}
	if err != nil {
		root.Remove(tmp)
		return fmt.Errorf("disk: put %s: %w", key, err)
	}
	return nil
}

// Stat reads the whole file to compute its ETag, a SHA-256 of the contents.
func (p *Provider) Stat(ctx context.Context, key string) (*storage.ObjectInfo, error) {
	f, fi, err := p.openFile(ctx, "stat", key)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Hash the open file, not the path, so a concurrent Put can't pair this
	// size and time with another version's ETag.
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx, f}); err != nil {
		return nil, fmt.Errorf("disk: stat %s: %w", key, err)
	}
	return &storage.ObjectInfo{
		Key:         key,
		Size:        fi.Size(),
		ModTime:     fi.ModTime(),
		ETag:        hex.EncodeToString(h.Sum(nil)),
		ContentType: mime.TypeByExtension(path.Ext(key)),
	}, nil
}

// openFile opens key's file for reading. A directory counts as not found, as
// it would in S3.
func (p *Provider) openFile(ctx context.Context, op, key string) (*os.File, fs.FileInfo, error) {
	root, name, err := p.openRoot(ctx, op, key)
	if err != nil {
		return nil, nil, err
	}
	// Files opened through root stay open after it closes.
	defer root.Close()

	f, err := root.Open(name)
	if err != nil {
		return nil, nil, wrapErr(op, key, err)
	}
	fi, err := f.Stat()
	if err == nil && fi.IsDir() {
		err = fs.ErrNotExist
	}
	if err != nil {
		f.Close()
		return nil, nil, wrapErr(op, key, err)
	}
	return f, fi, nil
}

// openRoot validates key and opens the root directory. The caller must close
// the returned root.
func (p *Provider) openRoot(ctx context.Context, op, key string) (*os.Root, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if err := storage.CheckKey(key); err != nil {
		return nil, "", fmt.Errorf("disk: %s: %w", op, err)
	}
	root, err := os.OpenRoot(p.dir)
	if err != nil {
		return nil, "", fmt.Errorf("disk: %s %s: %w", op, key, err)
	}
	return root, filepath.FromSlash(key), nil
}

func wrapErr(op, key string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		err = storage.ErrNotFound
	}
	return fmt.Errorf("disk: %s %s: %w", op, key, err)
}

// ctxReader stops a long copy once ctx is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
