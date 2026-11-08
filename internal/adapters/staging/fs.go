// Package staging implements app.Staging on the local filesystem: uploaded
// archive parts wait here (DATA_DIR/uploads) until the worker has imported
// them. All file access goes through an os.Root, so no ref — whatever it
// contains — can reach outside the staging directory.
package staging

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/johnnycube/archivist/internal/app"
)

// DefaultMinFree is the free space CheckSpace insists on keeping beyond the
// upload itself, so one import cannot fill the disk under everything else.
const DefaultMinFree = 1 << 30

// FS is a filesystem-backed app.Staging. Refs are "<userID>/<random>.zip",
// relative to the staging directory.
type FS struct {
	// MinFree is the safety margin of CheckSpace, in bytes.
	MinFree int64

	dir  string // absolute
	root *os.Root
}

// NewFS returns a staging area rooted at dir, creating it if needed.
func NewFS(dir string) (*FS, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	return &FS{MinFree: DefaultMinFree, dir: abs, root: root}, nil
}

var _ app.Staging = (*FS)(nil)

// Dir is the staging directory (absolute). Refs are relative to it.
func (s *FS) Dir() string { return s.dir }

// File is an opened archive part (app.StagedFile). Where it lives on disk is
// nobody's business: the archive engine reads it through io.ReaderAt.
type File struct {
	*os.File
	size int64
	name string
}

var _ app.StagedFile = (*File)(nil)

func (f *File) Size() int64 { return f.size }

// Name is the ORIGINAL file name of the upload ("" if unknown) — not the name
// it is staged under, which is what the embedded os.File would report.
func (f *File) Name() string { return f.name }

// OpenFile opens an archive part that lives outside any staging area (the CLI
// imports ZIPs straight from disk). Its Name is the file's own base name.
func OpenFile(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return newFile(f, filepath.Base(path))
}

func newFile(f *os.File, name string) (*File, error) {
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &File{File: f, size: info.Size(), name: name}, nil
}

// CheckSpace compares the free space of the staging filesystem with
// size + MinFree. Platforms without statfs skip the check.
func (s *FS) CheckSpace(ctx context.Context, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	free, ok := freeBytes(s.dir)
	if !ok {
		return nil
	}
	if size < 0 {
		size = 0
	}
	if need := uint64(size) + uint64(max(s.MinFree, 0)); free < need {
		return fmt.Errorf("%w (%d MiB free, %d MiB needed)", app.ErrInsufficientStorage, free>>20, need>>20)
	}
	return nil
}

func (s *FS) Put(ctx context.Context, userID string, r io.Reader) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if userID == "" || userID == "." || userID == ".." || strings.ContainsAny(userID, `/\`) {
		return "", 0, fmt.Errorf("staging: invalid user id %q", userID)
	}
	if err := s.root.Mkdir(userID, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", 0, err
	}
	ref := userID + "/" + rand.Text() + ".zip"
	f, err := s.root.OpenFile(ref, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = s.root.Remove(ref)
		return "", 0, err
	}
	return ref, n, nil
}

func (s *FS) Open(ctx context.Context, ref, name string) (app.StagedFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rel, ok := s.rel(ref)
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: ref, Err: fs.ErrNotExist}
	}
	f, err := s.root.Open(rel)
	if err != nil {
		return nil, err
	}
	return newFile(f, name)
}

// List walks the staging directory: "<userID>/<file>" is what Put creates;
// files directly in the directory (staged before refs had a user part) are
// listed without an owner. Deeper levels do not exist.
func (s *FS) List(ctx context.Context) ([]app.StagedRef, error) {
	refs := []app.StagedRef{}
	err := fs.WalkDir(s.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) { // removed while walking
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		userID := ""
		if dir := path.Dir(p); dir != "." {
			userID = strings.SplitN(dir, "/", 2)[0]
		}
		refs = append(refs, app.StagedRef{Ref: p, UserID: userID, ModTime: info.ModTime()})
		return nil
	})
	return refs, err
}

func (s *FS) Remove(ctx context.Context, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rel, ok := s.rel(ref)
	if !ok {
		return nil
	}
	if err := s.root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *FS) RemoveUser(ctx context.Context, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsLocal(userID) {
		return nil
	}
	return s.root.RemoveAll(userID)
}

// rel maps a ref to a path relative to the staging directory. Jobs queued
// before refs existed carry the staged file's path instead (absolute, or
// relative to the working directory); those resolve as long as they point
// into the staging directory. Anything else does not exist as far as staging
// is concerned.
func (s *FS) rel(ref string) (string, bool) {
	if abs, err := filepath.Abs(ref); err == nil {
		if r, err := filepath.Rel(s.dir, abs); err == nil && r != "." && filepath.IsLocal(r) {
			return r, true
		}
	}
	if filepath.IsLocal(ref) {
		return filepath.FromSlash(ref), true
	}
	return "", false
}
