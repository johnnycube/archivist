// Package blob implements app.Blobs on the local filesystem. Keys are hashed
// into per-user directories, so hostile archive member paths can never escape
// the store root. Single-node by design; an S3/MinIO adapter can replace it
// behind the same port for multi-node deploys.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/johnnycube/archivist/internal/app"
)

// FS is a filesystem-backed app.Blobs rooted at Dir.
type FS struct{ Dir string }

// NewFS returns a blob store rooted at dir (created on demand).
func NewFS(dir string) *FS { return &FS{Dir: dir} }

var _ app.Blobs = (*FS)(nil)

func (f *FS) path(userID, key string) string {
	sum := sha256.Sum256([]byte(key))
	h := hex.EncodeToString(sum[:])
	// Shard by hash prefix to keep directories small at scale.
	return filepath.Join(f.Dir, sanitize(userID), h[:2], h)
}

// sanitize keeps user IDs path-safe (they are UUIDs, but never trust input).
func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// Put stores the object once. If the key already exists the call is a no-op:
// ingested artifacts are immutable (see the app.Blobs contract).
func (f *FS) Put(ctx context.Context, userID, key string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dst := f.path(userID, key)
	if _, err := os.Stat(dst); err == nil {
		return nil // write-once: first import wins, re-imports never rewrite
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".blob-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return fmt.Errorf("write blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Re-check to narrow the overwrite race, then make the object read-only.
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	if err := os.Chmod(tmp.Name(), 0o444); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func (f *FS) Open(ctx context.Context, userID, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return os.Open(f.path(userID, key))
}

// Delete removes the object; a missing key is not an error.
func (f *FS) Delete(ctx context.Context, userID, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(f.path(userID, key)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DeleteUser removes everything stored for the user — their whole directory —
// and is a no-op when there is none.
func (f *FS) DeleteUser(ctx context.Context, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if userID == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(f.Dir, sanitize(userID)))
}
