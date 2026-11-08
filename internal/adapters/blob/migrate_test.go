package blob

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedFS fills a filesystem store with a few blobs of two users — one larger
// than a (small) S3 part so the copy exercises multipart — and returns the
// content by user and key.
func seedFS(t *testing.T, src *FS, bigSize int) map[[2]string][]byte {
	t.Helper()
	ctx := context.Background()
	want := map[[2]string][]byte{}
	big := make([]byte, bigSize)
	rand.Read(big)
	for _, e := range []struct {
		user, key string
		content   []byte
	}{
		{"user-1", "archives/j1.zip", big},
		{"user-1", "whatsapp/media/a.jpg", []byte("photo a")},
		{"user-2", "whatsapp/media/a.jpg", []byte("photo a of user 2")},
		{"user-2", "empty", nil},
	} {
		if err := src.Put(ctx, e.user, e.key, bytes.NewReader(e.content)); err != nil {
			t.Fatal(err)
		}
		want[[2]string{e.user, e.key}] = e.content
	}
	// Strays that must be ignored, not uploaded: a temp file Put left behind
	// and a file outside the layout.
	os.MkdirAll(filepath.Join(src.Dir, "user-1", "ab"), 0o755)
	if err := os.WriteFile(filepath.Join(src.Dir, "user-1", "ab", ".blob-123"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src.Dir, "README"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	return want
}

// checkMigrated reads every seeded blob back through the S3 store by its
// original user and key.
func checkMigrated(t *testing.T, s3 *S3, want map[[2]string][]byte) {
	t.Helper()
	for uk, content := range want {
		if got := readAll(t, s3, uk[0], uk[1]); !bytes.Equal(got, content) {
			t.Errorf("%s/%s: %d bytes after migration, want %d", uk[0], uk[1], len(got), len(content))
		}
	}
}

func TestMigrateFS(t *testing.T) {
	ctx := context.Background()
	s3, fake := newTestS3(t)
	s3.partSize = 5 << 20
	src := NewFS(t.TempDir())
	want := seedFS(t, src, 5<<20+1)

	var seen []MigrateProgress
	stats, err := MigrateFS(ctx, src, s3, func(p MigrateProgress) { seen = append(seen, p) })
	if err != nil {
		t.Fatal(err)
	}
	if stats.Copied != 4 || stats.Skipped != 0 || stats.Ignored != 2 || stats.Bytes != int64(5<<20+1+7+17) {
		t.Errorf("first run stats = %+v", stats)
	}
	if len(seen) != 4 || !seen[3].Copied || seen[3].Stats != stats {
		t.Errorf("progress = %+v", seen)
	}
	if puts, multiparts, objects := fake.snapshot(); puts != 4 || multiparts != 1 || objects != 4 {
		t.Errorf("puts %d (multipart %d), objects %d; want 4 objects, one via multipart", puts, multiparts, objects)
	}
	checkMigrated(t, s3, want)

	// Resumable / idempotent: a second run copies nothing and rewrites nothing.
	stats, err = MigrateFS(ctx, src, s3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Copied != 0 || stats.Skipped != 4 || stats.Bytes != 0 {
		t.Errorf("second run stats = %+v", stats)
	}
	if puts, _, _ := fake.snapshot(); puts != 4 {
		t.Errorf("second run wrote %d objects in total, want the 4 from the first run", puts)
	}

	// Write-once on the target: an object the bucket already has (a live
	// import that raced the migration) is never replaced by the file.
	name := objectName("user-1", "whatsapp/media/a.jpg")
	fake.mu.Lock()
	fake.objects["/test-bucket/"+name] = []byte("live")
	fake.mu.Unlock()
	if _, err := MigrateFS(ctx, src, s3, nil); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, s3, "user-1", "whatsapp/media/a.jpg"); string(got) != "live" {
		t.Errorf("migration overwrote a live object: %q", got)
	}

	// A missing source directory is an error, not an empty success.
	if _, err := MigrateFS(ctx, NewFS(filepath.Join(t.TempDir(), "nope")), s3, nil); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing source err = %v", err)
	}
	// Cancellation stops the walk.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := MigrateFS(cancelled, src, s3, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled err = %v", err)
	}
}

func TestIsBlobObjectName(t *testing.T) {
	h := strings.Repeat("ab", 32)
	for name, want := range map[string]bool{
		"user-1/ab/" + h:             true,
		"user-1/ab/" + h[:63]:        false, // short hash
		"user-1/cd/" + h:             false, // shard does not match the hash
		"user-1/ab/.blob-123":        false, // Put's temp file
		"README":                     false,
		"user-1/ab/x/" + h:           false, // too deep
		"user-1/ab/" + h[:62] + "zz": false, // not hex
		"us er/ab/" + h:              false, // unsanitised user id
	} {
		if got := isBlobObjectName(name); got != want {
			t.Errorf("isBlobObjectName(%q) = %t, want %t", name, got, want)
		}
	}
}

// TestMigrateFSRealEndpoint copies a temp filesystem store into the real
// MinIO (gated like TestS3RealEndpoint).
func TestMigrateFSRealEndpoint(t *testing.T) {
	s3 := realS3(t)
	ctx := context.Background()
	src := NewFS(t.TempDir())
	want := seedFS(t, src, s3PartSize+99)
	// Every object of this run lives under unique users; clean them up.
	t.Cleanup(func() {
		for _, u := range []string{"user-1", "user-2"} {
			_ = s3.DeleteUser(ctx, u)
		}
	})
	for _, u := range []string{"user-1", "user-2"} {
		if err := s3.DeleteUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := MigrateFS(ctx, src, s3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Copied != 4 || stats.Ignored != 2 {
		t.Errorf("stats = %+v", stats)
	}
	checkMigrated(t, s3, want)
	if stats, err := MigrateFS(ctx, src, s3, nil); err != nil || stats.Copied != 0 || stats.Skipped != 4 {
		t.Errorf("second run = %+v, %v", stats, err)
	}
	rc, err := s3.Open(ctx, "user-1", "archives/j1.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if n, err := rc.(io.Seeker).Seek(0, io.SeekEnd); err != nil || n != int64(s3PartSize+99) {
		t.Errorf("migrated object size = %d, %v", n, err)
	}
}
