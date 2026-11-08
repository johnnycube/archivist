package staging

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/app"
)

func newFS(t *testing.T) *FS {
	t.Helper()
	s, err := NewFS(filepath.Join(t.TempDir(), "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPutOpenRemove(t *testing.T) {
	ctx := context.Background()
	s := newFS(t)

	ref, size, err := s.Put(ctx, "u1", strings.NewReader("zip bytes"))
	if err != nil || size != 9 {
		t.Fatalf("Put = %q, %d, %v", ref, size, err)
	}
	if filepath.IsAbs(ref) || !strings.HasPrefix(ref, "u1/") {
		t.Errorf("ref = %q, want an opaque relative ref under the user", ref)
	}
	f, err := s.Open(ctx, ref, "")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(io.NewSectionReader(f, 0, f.Size()))
	if string(got) != "zip bytes" {
		t.Errorf("content = %q", got)
	}
	// A ref is relative to Dir.
	if _, err := os.Stat(filepath.Join(s.Dir(), ref)); err != nil || !filepath.IsAbs(s.Dir()) {
		t.Errorf("Dir = %q; stat of the staged file: %v", s.Dir(), err)
	}
	f.Close()

	for i := 0; i < 2; i++ { // idempotent
		if err := s.Remove(ctx, ref); err != nil {
			t.Fatalf("Remove (call %d): %v", i+1, err)
		}
	}
	if _, err := s.Open(ctx, ref, ""); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open after Remove err = %v, want fs.ErrNotExist", err)
	}
}

// A failed upload must not leave a partial file behind.
func TestPutFailureLeavesNothing(t *testing.T) {
	s := newFS(t)
	boom := errors.New("client went away")
	if _, _, err := s.Put(context.Background(), "u1", io.MultiReader(strings.NewReader("part"), errReader{boom})); !errors.Is(err, boom) {
		t.Fatalf("Put err = %v, want the read error", err)
	}
	if left, _ := os.ReadDir(filepath.Join(s.dir, "u1")); len(left) != 0 {
		t.Errorf("failed Put left %d file(s) behind", len(left))
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestRemoveUser(t *testing.T) {
	ctx := context.Background()
	s := newFS(t)
	mine, _, _ := s.Put(ctx, "u1", strings.NewReader("a"))
	theirs, _, _ := s.Put(ctx, "u2", strings.NewReader("b"))
	for i := 0; i < 2; i++ { // idempotent, also for a user with nothing staged
		if err := s.RemoveUser(ctx, "u1"); err != nil {
			t.Fatalf("RemoveUser (call %d): %v", i+1, err)
		}
	}
	if _, err := s.Open(ctx, mine, ""); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("removed user's upload err = %v, want fs.ErrNotExist", err)
	}
	if f, err := s.Open(ctx, theirs, ""); err != nil {
		t.Errorf("another user's upload was removed: %v", err)
	} else {
		f.Close()
	}
}

// Refs come from the database, but nothing they contain may reach outside the
// staging directory.
func TestRefsCannotEscape(t *testing.T) {
	ctx := context.Background()
	s := newFS(t)
	outside := filepath.Join(filepath.Dir(s.dir), "secret.zip")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{outside, "../secret.zip", "u1/../../secret.zip", ""} {
		if _, err := s.Open(ctx, ref, ""); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Open(%q) err = %v, want fs.ErrNotExist", ref, err)
		}
		if err := s.Remove(ctx, ref); err != nil {
			t.Errorf("Remove(%q) = %v, want a no-op", ref, err)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("file outside staging was touched: %v", err)
	}
	for _, user := range []string{"", "..", "a/b", `a\b`} {
		if _, _, err := s.Put(ctx, user, strings.NewReader("x")); err == nil {
			t.Errorf("Put accepted user id %q", user)
		}
	}
	if err := s.RemoveUser(ctx, ".."); err != nil {
		t.Errorf("RemoveUser(..) = %v, want a no-op", err)
	}
	if _, err := os.Stat(s.dir); err != nil {
		t.Errorf("RemoveUser(..) removed the staging dir: %v", err)
	}
}

// Jobs queued before refs existed carry the staged file's path.
func TestLegacyPathRef(t *testing.T) {
	ctx := context.Background()
	s := newFS(t)
	legacy := filepath.Join(s.dir, "archivist-123.zip")
	if err := os.WriteFile(legacy, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := s.Open(ctx, legacy, "")
	if err != nil {
		t.Fatalf("Open(legacy path) = %v", err)
	}
	f.Close()
	if err := s.Remove(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("legacy staged file survived Remove")
	}
}

func TestCheckSpace(t *testing.T) {
	ctx := context.Background()
	s := newFS(t)
	if _, ok := freeBytes(s.dir); !ok {
		t.Skip("no statfs on this platform")
	}
	s.MinFree = 0
	if err := s.CheckSpace(ctx, 1); err != nil {
		t.Errorf("1 byte does not fit: %v", err)
	}
	if err := s.CheckSpace(ctx, math.MaxInt64); !errors.Is(err, app.ErrInsufficientStorage) {
		t.Errorf("huge upload err = %v, want ErrInsufficientStorage", err)
	}
	s.MinFree = math.MaxInt64
	if err := s.CheckSpace(ctx, 0); !errors.Is(err, app.ErrInsufficientStorage) {
		t.Errorf("margin not enforced: err = %v", err)
	}
}

// The opened file reports the ORIGINAL upload name it is told, never the random
// name it is staged under (which the embedded os.File would report).
func TestOpenedFileReportsTheUploadName(t *testing.T) {
	ctx := context.Background()
	s := newFS(t)
	ref, _, err := s.Put(ctx, "u1", strings.NewReader("zip"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"WhatsApp Chat - Anna.zip", ""} {
		f, err := s.Open(ctx, ref, name)
		if err != nil {
			t.Fatal(err)
		}
		if got := f.Name(); got != name {
			t.Errorf("Name() = %q, want %q", got, name)
		}
		f.Close()
	}
	// The CLI's files are named after themselves.
	disk := filepath.Join(t.TempDir(), "export 2024.zip")
	if err := os.WriteFile(disk, []byte("zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFile(disk)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.Name() != "export 2024.zip" {
		t.Errorf("OpenFile: Name %q", f.Name())
	}
}

func TestList(t *testing.T) {
	ctx := context.Background()
	s := newFS(t)
	if refs, err := s.List(ctx); err != nil || len(refs) != 0 {
		t.Fatalf("List of an empty staging area = %v, %v", refs, err)
	}
	a1, _, _ := s.Put(ctx, "u1", strings.NewReader("a"))
	a2, _, _ := s.Put(ctx, "u1", strings.NewReader("b"))
	b1, _, _ := s.Put(ctx, "u2", strings.NewReader("c"))
	legacy := "archivist-123.zip" // staged before refs had a user part
	if err := os.WriteFile(filepath.Join(s.dir, legacy), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2020, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(s.dir, filepath.FromSlash(a1)), when, when); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveUser(ctx, "u3"); err != nil { // an empty user dir is not a file
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.dir, "u3"), 0o700); err != nil {
		t.Fatal(err)
	}

	refs, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]app.StagedRef{}
	for _, r := range refs {
		got[r.Ref] = r
	}
	if len(refs) != 4 || len(got) != 4 {
		t.Fatalf("List = %+v, want the 4 files", refs)
	}
	for ref, user := range map[string]string{a1: "u1", a2: "u1", b1: "u2", legacy: ""} {
		r, ok := got[ref]
		if !ok || r.UserID != user {
			t.Errorf("List has %q = %+v (found %v), want owner %q", ref, r, ok, user)
		}
		if r.ModTime.IsZero() {
			t.Errorf("%q has no ModTime", ref)
		}
		// Every listed ref is one the other methods accept.
		if f, err := s.Open(ctx, ref, ""); err != nil {
			t.Errorf("Open(listed ref %q) = %v", ref, err)
		} else {
			f.Close()
		}
	}
	if !got[a1].ModTime.Equal(when) {
		t.Errorf("ModTime = %v, want the file's %v", got[a1].ModTime, when)
	}
	if time.Since(got[a2].ModTime) > time.Minute {
		t.Errorf("a file written just now has ModTime %v", got[a2].ModTime)
	}
	for _, r := range refs {
		if err := s.Remove(ctx, r.Ref); err != nil {
			t.Errorf("Remove(listed ref %q) = %v", r.Ref, err)
		}
	}
	if refs, _ := s.List(ctx); len(refs) != 0 {
		t.Errorf("List after removing everything = %+v", refs)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.List(cancelled); !errors.Is(err, context.Canceled) {
		t.Errorf("List with a cancelled context = %v", err)
	}
}
