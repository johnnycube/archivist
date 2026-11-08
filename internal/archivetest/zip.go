// Package archivetest provides helpers for building export-archive ZIPs from a
// directory of fixtures. It is stdlib-only and used by provider golden tests.
package archivetest

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
)

// WriteFiles writes name→content files under dir, creating parent directories.
// Names may contain forward-slash subpaths (e.g. "data/tweets.js").
func WriteFiles(dir string, files map[string]string) error {
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ZipDir writes every file under srcDir into outZip, preserving relative paths
// (with forward slashes) so nested layouts like data/tweets.js are kept intact.
func ZipDir(srcDir, outZip string) error {
	f, err := os.Create(outZip)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()

	return filepath.WalkDir(srcDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(w, src)
		return err
	})
}

// Zip returns an in-memory ZIP of name→content members, for tests that import
// without touching the filesystem (bundle.Part over a bytes.Reader).
func Zip(files map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, content); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ZipReader is Zip opened for random access: the (io.ReaderAt, size) pair an
// archive part is made of.
func ZipReader(files map[string]string) (*bytes.Reader, int64, error) {
	b, err := Zip(files)
	if err != nil {
		return nil, 0, err
	}
	return bytes.NewReader(b), int64(len(b)), nil
}
