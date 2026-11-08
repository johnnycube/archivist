package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"testing"
)

func TestFSWriteOnce(t *testing.T) {
	ctx := context.Background()
	f := NewFS(t.TempDir())

	if err := f.Put(ctx, "user-1", "media/a.jpg", bytes.NewReader([]byte("original"))); err != nil {
		t.Fatal(err)
	}
	if err := f.Put(ctx, "user-1", "media/a.jpg", bytes.NewReader([]byte("MUTATED"))); err != nil {
		t.Fatal(err)
	}
	rc, err := f.Open(ctx, "user-1", "media/a.jpg")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "original" {
		t.Errorf("content = %q, want the original (immutable)", got)
	}
	if _, err := f.Open(ctx, "user-1", "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing blob err = %v, want fs.ErrNotExist", err)
	}
}

// Account deletion: every blob of the user goes, nobody else's does, and a
// second call (the retry after a partial failure) is a no-op.
func TestFSDeleteUser(t *testing.T) {
	ctx := context.Background()
	f := NewFS(t.TempDir())
	for _, user := range []string{"user-1", "user-2"} {
		for _, key := range []string{"archives/j1.zip", "reddit/media/a.jpg"} {
			if err := f.Put(ctx, user, key, bytes.NewReader([]byte("data"))); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < 2; i++ {
		if err := f.DeleteUser(ctx, "user-1"); err != nil {
			t.Fatalf("DeleteUser (call %d): %v", i+1, err)
		}
	}
	if _, err := f.Open(ctx, "user-1", "archives/j1.zip"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("deleted user's blob err = %v, want fs.ErrNotExist", err)
	}
	rc, err := f.Open(ctx, "user-2", "archives/j1.zip")
	if err != nil {
		t.Fatalf("another user's blob was deleted: %v", err)
	}
	rc.Close()
	if err := f.DeleteUser(ctx, ""); err != nil {
		t.Errorf("DeleteUser(\"\") = %v, want a no-op", err)
	}
	if rc, err := f.Open(ctx, "user-2", "reddit/media/a.jpg"); err != nil {
		t.Errorf("DeleteUser(\"\") removed other users' blobs: %v", err)
	} else {
		rc.Close()
	}
}
