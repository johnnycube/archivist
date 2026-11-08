package blob

import (
	"context"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
)

// MigrateStats summarises one MigrateFS run.
type MigrateStats struct {
	Copied  int   // objects uploaded by this run
	Skipped int   // objects the bucket already had (an earlier run, or live writes)
	Ignored int   // files under the store root that are not blobs (temp files, strays)
	Bytes   int64 // bytes uploaded by this run
}

// MigrateProgress is reported after every blob MigrateFS looks at.
type MigrateProgress struct {
	Object string // object name, e.g. <user>/<hh>/<sha256>
	Size   int64
	Copied bool // false = already present, skipped
	Stats  MigrateStats
}

// MigrateFS copies every blob of a filesystem store into the S3 store, so a
// single-node install can move to object storage. The two stores share one
// layout — <user>/<hh>/<sha256(key)> — so files are copied under their exact
// path as object name; the original keys are not needed (and not known).
//
// Write-once is respected on both ends: an object the bucket already has is
// never rewritten, which also makes the run idempotent and resumable — an
// interrupted migration is simply started again and skips what is done. Both
// stores may stay live: the application reads whichever store it is
// configured with, so switch it over to S3 once MigrateFS reports zero copies
// on a re-run. Files that are not blobs (Put's ".blob-*" temp files, anything
// outside the three-level layout) are ignored and counted.
func MigrateFS(ctx context.Context, src *FS, dst *S3, progress func(MigrateProgress)) (MigrateStats, error) {
	var stats MigrateStats
	root := src.Dir
	if _, err := os.Stat(root); err != nil {
		return stats, fmt.Errorf("migrate blobs: source %s: %w", root, err)
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if !isBlobObjectName(name) {
			stats.Ignored++
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		copied, err := dst.copyFile(ctx, name, p, info.Size())
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if copied {
			stats.Copied++
			stats.Bytes += info.Size()
		} else {
			stats.Skipped++
		}
		if progress != nil {
			progress(MigrateProgress{Object: name, Size: info.Size(), Copied: copied, Stats: stats})
		}
		return nil
	})
	if err != nil {
		return stats, fmt.Errorf("migrate blobs: %w", err)
	}
	return stats, nil
}

// isBlobObjectName reports whether name has the store layout
// <user>/<hh>/<64 hex> with <hh> the hash's first two characters.
func isBlobObjectName(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) != 3 || parts[0] == "" || len(parts[1]) != 2 || len(parts[2]) != 64 {
		return false
	}
	if _, err := hex.DecodeString(parts[2]); err != nil {
		return false
	}
	return parts[2][:2] == parts[1] && parts[0] == sanitize(parts[0])
}

// copyFile uploads the file at path as the named object unless the object
// exists. The file is a known-size io.ReaderAt, so minio-go uploads large
// files as multipart straight from the file — no read-ahead buffer.
func (s *S3) copyFile(ctx context.Context, name, path string, size int64) (copied bool, err error) {
	exists, err := s.exists(ctx, name)
	if err != nil {
		return false, fmt.Errorf("stat: %w", err)
	}
	if exists {
		return false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	opts := s.putOptions()
	opts.SetMatchETagExcept("*") // a concurrent writer (the live app) wins
	if _, err := s.client.PutObject(ctx, s.bucket, name, f, size, opts); err != nil {
		if minio.ToErrorResponse(err).Code == minio.PreconditionFailed {
			return false, nil
		}
		return false, fmt.Errorf("put: %w", err)
	}
	return true, nil
}
