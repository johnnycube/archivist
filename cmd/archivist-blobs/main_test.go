package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/blob"
)

func TestUsageErrors(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run(nil, &out, &errOut); err == nil || !strings.Contains(errOut.String(), "usage:") {
		t.Errorf("no command: err = %v, stderr = %q", err, errOut.String())
	}
	if err := run([]string{"migrate-to-s3", "-bogus"}, &out, &errOut); err == nil {
		t.Error("unknown flag accepted")
	}
	// No endpoint: the adapter's validation, before any network I/O.
	t.Setenv("BLOB_S3_ENDPOINT", "")
	if err := run([]string{"migrate-to-s3", "-access-key", "a", "-secret-key", "b"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Errorf("missing endpoint: err = %v", err)
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 5 << 20: "5.0 MiB", 3 << 30: "3.0 GiB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestMigrateToS3 runs the command end to end against the real MinIO named by
// BLOB_S3_TEST_* (skipped otherwise): a temp filesystem store is copied, the
// blobs are readable by their original keys, and a second run copies nothing.
func TestMigrateToS3(t *testing.T) {
	endpoint := os.Getenv("BLOB_S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("BLOB_S3_TEST_ENDPOINT not set")
	}
	bucket := os.Getenv("BLOB_S3_TEST_BUCKET")
	if bucket == "" {
		bucket = "archivist-test"
	}
	// Unique users keep this run's objects apart from other tests' in the
	// shared bucket.
	userA, userB := fmt.Sprintf("mig-a-%d", time.Now().UnixNano()), fmt.Sprintf("mig-b-%d", time.Now().UnixNano())
	t.Setenv("BLOB_S3_ENDPOINT", endpoint)
	t.Setenv("BLOB_S3_BUCKET", bucket)
	t.Setenv("BLOB_S3_ACCESS_KEY", os.Getenv("BLOB_S3_TEST_ACCESS_KEY"))
	t.Setenv("BLOB_S3_SECRET_KEY", os.Getenv("BLOB_S3_TEST_SECRET_KEY"))
	t.Setenv("BLOB_S3_SSE", "")

	ctx := context.Background()
	src := blob.NewFS(t.TempDir())
	for _, e := range []struct{ user, key, content string }{
		{userA, "archives/j1.zip", strings.Repeat("z", 100_000)},
		{userA, "whatsapp/media/a.jpg", "photo"},
		{userB, "whatsapp/media/a.jpg", "other photo"},
	} {
		if err := src.Put(ctx, e.user, e.key, strings.NewReader(e.content)); err != nil {
			t.Fatal(err)
		}
	}
	s3, err := blob.NewS3(blob.S3Config{
		Endpoint: endpoint, Bucket: bucket,
		AccessKey: os.Getenv("BLOB_S3_TEST_ACCESS_KEY"), SecretKey: os.Getenv("BLOB_S3_TEST_SECRET_KEY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s3.DeleteUser(ctx, userA)
		_ = s3.DeleteUser(ctx, userB)
	})

	var out, errOut bytes.Buffer
	if err := run([]string{"migrate-to-s3", "-from", src.Dir}, &out, &errOut); err != nil {
		t.Fatalf("%v\n%s%s", err, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "copied 3 objects") {
		t.Errorf("first run output:\n%s", out.String())
	}
	rc, err := s3.Open(ctx, userB, "whatsapp/media/a.jpg")
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 11)
	rc.Read(b)
	rc.Close()
	if string(b) != "other photo" {
		t.Errorf("migrated content = %q", b)
	}

	out.Reset()
	if err := run([]string{"migrate-to-s3", "-from", src.Dir, "-quiet"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "copied 0 objects") || !strings.Contains(out.String(), "skipped 3") || !strings.Contains(out.String(), "nothing left to copy") {
		t.Errorf("second run output:\n%s", out.String())
	}

	// Wrong credentials fail fast and say so.
	t.Setenv("BLOB_S3_SECRET_KEY", "nope")
	if err := run([]string{"migrate-to-s3", "-from", src.Dir}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "credentials rejected") {
		t.Errorf("wrong credentials: err = %v", err)
	}
}
