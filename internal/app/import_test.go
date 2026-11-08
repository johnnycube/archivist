package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/catalog"
	"github.com/johnnycube/archivist/internal/adapters/engine"
	"github.com/johnnycube/archivist/internal/adapters/memory"
	"github.com/johnnycube/archivist/internal/adapters/staging"
	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/domain"
)

var reddit = map[string]string{
	"reddit/comments.csv": "id,permalink,date,subreddit,link,parent,body\n" +
		"x1,http://p/1,2023-01-01 00:00:00 UTC,golang,post:a,post:a,Hello there\n",
	"reddit/posts.csv": "id,permalink,date,subreddit,title,url,body\n" +
		"a,http://p/a,2023-01-02 00:00:00 UTC,golang,Title A,,Body A\n",
	"reddit/post_votes.csv": "id,permalink,direction\na,http://p/a,up\n",
}

func buildZip(t *testing.T) string {
	t.Helper()
	return buildZipOf(t, reddit)
}

func buildZipOf(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := archivetest.WriteFiles(src, files); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "e.zip")
	if err := archivetest.ZipDir(src, out); err != nil {
		t.Fatal(err)
	}
	return out
}

// newStaging returns a filesystem staging area in a temp dir. The free-space
// margin is off: the tests must not depend on how full the machine's disk is.
func newStaging(t *testing.T) *staging.FS {
	t.Helper()
	st, err := staging.NewFS(filepath.Join(t.TempDir(), "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	st.MinFree = 0
	return st
}

// stageZips uploads ZIP files from disk the way the REST adapter does.
func stageZips(t *testing.T, a *app.App, userID string, paths ...string) []app.StagedPart {
	t.Helper()
	var parts []app.StagedPart
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		part, err := a.Import.StageNamed(context.Background(), userID, "", f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, part)
	}
	return parts
}

func TestImportUseCaseAndTimeline(t *testing.T) {
	ctx := context.Background()
	blobs := newMemBlobs()
	a := newImportApp(t, blobs)

	// Enqueue returns immediately with a queued job.
	job, err := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "reddit", Label: "main", Parts: stageZips(t, a, "u1", buildZip(t))})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "queued" {
		t.Fatalf("status = %q, want queued", job.Status)
	}

	// Worker drains the queue.
	did, err := a.Worker.ProcessNext(ctx)
	if err != nil || !did {
		t.Fatalf("ProcessNext did=%v err=%v", did, err)
	}
	if more, _ := a.Worker.ProcessNext(ctx); more {
		t.Error("expected an empty queue after one job")
	}

	done, err := a.Import.GetJob(ctx, "u1", job.ID)
	if err != nil || done.Status != "done" || done.Saved != 3 {
		t.Fatalf("job after worker = %+v, want done/3", done)
	}

	// Ingestion is immutable: the pristine upload is preserved in the blob
	// store before processing, keyed by job.
	if b := blobs.m["u1/"+app.ArchiveKey(done, 0)]; len(b) == 0 {
		t.Errorf("original archive not preserved at %q; stored keys: %v", app.ArchiveKey(done, 0), blobs.keys())
	}

	page, err := a.Timeline.Query(ctx, app.Query{UserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 {
		t.Errorf("total = %d, want 3", page.Total)
	}
	if page.Events[0].Type != "reddit.post" {
		t.Errorf("first = %q, want reddit.post (newest)", page.Events[0].Type)
	}

	react, _ := a.Timeline.Query(ctx, app.Query{UserID: "u1", Categories: []string{"reaction"}})
	if react.Total != 1 {
		t.Errorf("reaction = %d, want 1", react.Total)
	}

	// Tenant isolation.
	other, _ := a.Timeline.Query(ctx, app.Query{UserID: "u2"})
	if other.Total != 0 {
		t.Errorf("other user total = %d, want 0", other.Total)
	}
}

func importDeps(t *testing.T, blobs app.Blobs) app.Deps {
	t.Helper()
	d := app.Deps{
		Events:   memory.NewEvents(),
		Users:    memory.NewUsers(),
		Accounts: memory.NewAccounts(),
		Hasher:   cryptoHasher{},
		Sessions: memory.NewSessions(time.Hour),
		Catalog:  catalog.New(),
		Engine:   engine.NewWith(catalog.New().Bundler),
		Jobs:     memory.NewJobs(),
		Staging:  newStaging(t),
		Blobs:    blobs, // pass a literal nil for "no blob store"
	}
	return d
}

func newImportApp(t *testing.T, blobs app.Blobs) *app.App {
	t.Helper()
	return app.New(importDeps(t, blobs))
}

func runJob(t *testing.T, a *app.App, userID, jobID string) domain.ImportJob {
	t.Helper()
	if did, err := a.Worker.ProcessNext(context.Background()); err != nil || !did {
		t.Fatalf("ProcessNext did=%v err=%v", did, err)
	}
	job, err := a.Import.GetJob(context.Background(), userID, jobID)
	if err != nil || job.Status != domain.JobDone {
		t.Fatalf("job = %+v, want done", job)
	}
	return job
}

// A split export is one import; a finished import can be re-derived from the
// preserved archive after its staging files are gone, and deleted entirely.
func TestMultiPartReprocessAndDelete(t *testing.T) {
	ctx := context.Background()
	blobs := newMemBlobs()
	a := newImportApp(t, blobs)

	part1 := buildZipOf(t, map[string]string{"reddit/comments.csv": reddit["reddit/comments.csv"]})
	part2 := buildZipOf(t, map[string]string{
		"reddit/posts.csv":      reddit["reddit/posts.csv"],
		"reddit/post_votes.csv": reddit["reddit/post_votes.csv"],
	})
	parts := stageZips(t, a, "u1", part1, part2)
	job, err := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "reddit", Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	if job.Parts != 2 {
		t.Errorf("parts = %d, want 2", job.Parts)
	}
	if want := parts[0].Size + parts[1].Size; job.ArchiveBytes != want || want == 0 {
		t.Errorf("archive bytes = %d, want %d", job.ArchiveBytes, want)
	}
	if _, err := a.Import.ReprocessJob(ctx, "u1", job.ID); !errors.Is(err, app.ErrJobBusy) {
		t.Errorf("reprocess of a queued job err = %v, want ErrJobBusy", err)
	}

	done := runJob(t, a, "u1", job.ID)
	if done.Saved != 3 || done.Coverage.New != 3 {
		t.Fatalf("first run saved=%d new=%d, want 3/3", done.Saved, done.Coverage.New)
	}
	if done.BytesTotal == 0 || done.BytesDone != done.BytesTotal {
		t.Errorf("progress = %d/%d, want complete and non-zero", done.BytesDone, done.BytesTotal)
	}
	for i, p := range parts {
		if _, err := a.Import.Staging.Open(ctx, p.Ref, ""); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("staged upload of part %d survived the run (err %v)", i+1, err)
		}
		if len(blobs.m["u1/"+app.ArchiveKey(done, i)]) == 0 {
			t.Errorf("part %d not preserved; keys: %v", i+1, blobs.keys())
		}
	}

	// Reprocess: staging is gone, so the parts come back from the blob store.
	if _, err := a.Import.ReprocessJob(ctx, "u1", job.ID); err != nil {
		t.Fatalf("reprocess err=%v", err)
	}
	if _, err := a.Import.ReprocessJob(ctx, "u2", job.ID); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("another user's reprocess err = %v, want ErrNotFound", err)
	}
	again := runJob(t, a, "u1", job.ID)
	if again.Saved != 3 || again.Coverage.New != 0 || again.Coverage.Reimported != 3 {
		t.Errorf("reprocess saved=%d new=%d reimported=%d, want 3/0/3",
			again.Saved, again.Coverage.New, again.Coverage.Reimported)
	}

	// Delete: events, preserved archive and the job all go.
	if _, err := a.Import.DeleteJob(ctx, "u2", job.ID); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("another user's delete err = %v, want ErrNotFound", err)
	}
	deleted, err := a.Import.DeleteJob(ctx, "u1", job.ID)
	if err != nil || deleted != 3 {
		t.Fatalf("delete = %d,%v; want 3,nil", deleted, err)
	}
	if page, _ := a.Timeline.Query(ctx, app.Query{UserID: "u1"}); page.Total != 0 {
		t.Errorf("events after delete = %d, want 0", page.Total)
	}
	if _, err := a.Import.GetJob(ctx, "u1", job.ID); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("job after delete: err = %v, want ErrNotFound", err)
	}
	for k := range blobs.m {
		if strings.Contains(k, "archives/") {
			t.Errorf("preserved archive %q survived delete", k)
		}
	}
}

// The last import to write an event owns it: deleting an older import of the
// same data must not take away events a newer import also contains.
func TestDeleteImportKeepsEventsOwnedByLaterImport(t *testing.T) {
	ctx := context.Background()
	a := newImportApp(t, newMemBlobs())

	first, _ := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "reddit", Parts: stageZips(t, a, "u1", buildZip(t))})
	runJob(t, a, "u1", first.ID)
	second, _ := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "reddit", Parts: stageZips(t, a, "u1", buildZip(t))})
	runJob(t, a, "u1", second.ID)

	if deleted, err := a.Import.DeleteJob(ctx, "u1", first.ID); err != nil || deleted != 0 {
		t.Errorf("deleting the superseded import removed %d events (err %v), want 0", deleted, err)
	}
	if page, _ := a.Timeline.Query(ctx, app.Query{UserID: "u1"}); page.Total != 3 {
		t.Errorf("events = %d, want 3", page.Total)
	}
	if deleted, _ := a.Import.DeleteJob(ctx, "u1", second.ID); deleted != 3 {
		t.Errorf("deleting the owning import removed %d events, want 3", deleted)
	}
}

func TestSignupLoginUseCase(t *testing.T) {
	ctx := context.Background()
	a := app.New(app.Deps{
		Events: memory.NewEvents(), Users: memory.NewUsers(), Accounts: memory.NewAccounts(),
		Hasher: cryptoHasher{}, Sessions: memory.NewSessions(time.Hour), Catalog: catalog.New(), Engine: engine.NewWith(catalog.New().Bundler),
	})
	if _, _, err := a.Auth.SignUp(ctx, "bad", "short"); err == nil {
		t.Error("expected weak-signup rejection")
	}
	u, tok, err := a.Auth.SignUp(ctx, "me@x.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if uid, ok, _ := a.Auth.Authenticate(ctx, tok); !ok || uid != u.ID {
		t.Errorf("authenticate = %q,%v want %q", uid, ok, u.ID)
	}
	if _, _, err := a.Auth.LogInGuarded(ctx, "me@x.com", "password123", "", ""); err != nil {
		t.Errorf("login failed: %v", err)
	}
	if _, _, err := a.Auth.LogInGuarded(ctx, "me@x.com", "nope", "", ""); err == nil {
		t.Error("expected invalid credentials")
	}

	// Logout revokes the session server-side.
	if err := a.Auth.LogOut(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := a.Auth.Authenticate(ctx, tok); ok {
		t.Error("revoked token still authenticates")
	}

	// Password change revokes all sessions and returns a working new token.
	_, tok2, _ := a.Auth.LogInGuarded(ctx, "me@x.com", "password123", "", "")
	tok3, err := a.Auth.ChangePassword(ctx, u.ID, "password123", "password456")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := a.Auth.Authenticate(ctx, tok2); ok {
		t.Error("old session survived password change")
	}
	if _, ok, _ := a.Auth.Authenticate(ctx, tok3); !ok {
		t.Error("fresh session after password change does not authenticate")
	}
	if _, _, err := a.Auth.LogInGuarded(ctx, "me@x.com", "password456", "", ""); err != nil {
		t.Errorf("login with new password failed: %v", err)
	}

	// Account deletion removes the user and kills sessions.
	if err := a.Auth.DeleteAccount(ctx, u.ID, "wrong"); err == nil {
		t.Error("delete with wrong password accepted")
	}
	if err := a.Auth.DeleteAccount(ctx, u.ID, "password456"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Auth.LogInGuarded(ctx, "me@x.com", "password456", "", ""); err == nil {
		t.Error("login succeeded after account deletion")
	}
}

// memBlobs is a write-once in-memory app.Blobs for tests. failDelete makes
// Delete and DeleteUser fail, to exercise partial failures.
type memBlobs struct {
	m          map[string][]byte
	failDelete error
}

func newMemBlobs() *memBlobs { return &memBlobs{m: map[string][]byte{}} }

func (b *memBlobs) Put(_ context.Context, userID, key string, r io.Reader) error {
	k := userID + "/" + key
	if _, ok := b.m[k]; ok {
		return nil // write-once
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.m[k] = data
	return nil
}

func (b *memBlobs) Open(_ context.Context, userID, key string) (io.ReadCloser, error) {
	data, ok := b.m[userID+"/"+key]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *memBlobs) Delete(_ context.Context, userID, key string) error {
	if b.failDelete != nil {
		return b.failDelete
	}
	delete(b.m, userID+"/"+key)
	return nil
}

func (b *memBlobs) DeleteUser(_ context.Context, userID string) error {
	if b.failDelete != nil {
		return b.failDelete
	}
	for k := range b.m {
		if strings.HasPrefix(k, userID+"/") {
			delete(b.m, k)
		}
	}
	return nil
}

func (b *memBlobs) keys() []string {
	out := make([]string, 0, len(b.m))
	for k := range b.m {
		out = append(out, k)
	}
	return out
}

// tiny fakes so the auth use-case test doesn't depend on crypto specifics
type cryptoHasher struct{}

func (cryptoHasher) Hash(pw string) (string, error) { return "H:" + pw, nil }
func (cryptoHasher) Verify(enc, pw string) bool     { return enc == "H:"+pw }
func (cryptoHasher) NeedsRehash(string) bool        { return false }

type panickingEngine struct{}

func (panickingEngine) Import(context.Context, string, app.ArchiveInput) (*domain.Coverage, error) {
	panic("provider bug on hostile input")
}

// Providers parse untrusted uploads: a panic in one must fail that import and
// leave the worker (and the process) alive for every other tenant.
func TestWorkerSurvivesProviderPanic(t *testing.T) {
	deps := importDeps(t, nil)
	deps.Engine = panickingEngine{}
	a := app.New(deps)
	ctx := context.Background()
	job, err := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "reddit", Parts: stageZips(t, a, "u1", buildZip(t))})
	if err != nil {
		t.Fatal(err)
	}
	if did, err := a.Worker.ProcessNext(ctx); err != nil || !did {
		t.Fatalf("ProcessNext did=%v err=%v", did, err)
	}
	got, err := a.Import.GetJob(ctx, "u1", job.ID)
	if err != nil || got.Status != domain.JobError || !strings.Contains(got.Error, "crashed") {
		t.Errorf("job = %+v, want status error mentioning the crash", got)
	}
}

// Media an import stores is in its ledger: it counts towards the storage
// quota once per key, survives the deletion of one of two imports that share
// it, and goes with the last one.
func TestStoredMediaIsCountedAndDeletedWithItsImports(t *testing.T) {
	ctx := context.Background()
	blobs := newMemBlobs()
	a := newImportApp(t, blobs)
	zipA := buildZipOf(t, map[string]string{"shared.jpg": "SHARED-PHOTO", "only-a.jpg": "PHOTO-A"})
	zipB := buildZipOf(t, map[string]string{"shared.jpg": "SHARED-PHOTO", "only-b.jpg": "PHOTO-BB"})
	enqueue := func(path string) domain.ImportJob {
		t.Helper()
		job, err := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "reddit", Parts: stageZips(t, a, "u1", path)})
		if err != nil {
			t.Fatal(err)
		}
		return runJob(t, a, "u1", job.ID)
	}
	jobA := enqueue(zipA)
	jobB := enqueue(zipB)

	// Media lies under its content's hash.
	content := map[string]string{"shared.jpg": "SHARED-PHOTO", "only-a.jpg": "PHOTO-A", "only-b.jpg": "PHOTO-BB"}
	has := func(name string) bool {
		sum := sha256.Sum256([]byte(content[name]))
		_, err := blobs.Open(ctx, "u1", app.MediaKey("reddit", hex.EncodeToString(sum[:])))
		return err == nil
	}
	for _, k := range []string{"shared.jpg", "only-a.jpg", "only-b.jpg"} {
		if !has(k) {
			t.Fatalf("media %s not stored", k)
		}
	}
	use, err := a.Import.Jobs.Usage(ctx, "u1")
	if err != nil || use.BlobBytes != int64(len("SHARED-PHOTO")+len("PHOTO-A")+len("PHOTO-BB")) {
		t.Errorf("BlobBytes = %d, %v; want each stored key once", use.BlobBytes, err)
	}

	if _, err := a.Import.DeleteJob(ctx, "u1", jobA.ID); err != nil {
		t.Fatal(err)
	}
	if has("only-a.jpg") || !has("shared.jpg") || !has("only-b.jpg") {
		t.Errorf("after deleting A: only-a %v, shared %v, only-b %v; want false, true, true",
			has("only-a.jpg"), has("shared.jpg"), has("only-b.jpg"))
	}
	if _, err := a.Import.DeleteJob(ctx, "u1", jobB.ID); err != nil {
		t.Fatal(err)
	}
	if has("shared.jpg") || has("only-b.jpg") {
		t.Error("media of the last import survived its deletion")
	}
	if use, _ := a.Import.Jobs.Usage(ctx, "u1"); use.BlobBytes != 0 {
		t.Errorf("BlobBytes after deleting everything = %d", use.BlobBytes)
	}
}

// whatsAppChat is a one-message Android chat export with a photo of the given
// content, as WhatsApp names it (the counter restarts on every phone).
func whatsAppChat(t *testing.T, chat, photo string) string {
	t.Helper()
	return buildZipOf(t, map[string]string{
		"WhatsApp Chat with " + chat + ".txt": "4/15/22, 2:03 PM - " + chat + ": IMG-20220415-WA0001.jpg (file attached)\n",
		"IMG-20220415-WA0001.jpg":             photo,
	})
}

// Two exports with a file of the same name but other content each show their
// own: blobs are write-once, and under its path the first file stored would
// stand in for every later one.
func TestSameFileNameInTwoExportsKeepsBothContents(t *testing.T) {
	ctx := context.Background()
	a := newImportApp(t, newMemBlobs())
	for _, chat := range []struct{ name, photo string }{{"Alice", "PHOTO-FROM-ALICES-PHONE"}, {"Bob", "PHOTO-FROM-BOBS-PHONE"}} {
		job, err := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "whatsapp", Parts: stageZips(t, a, "u1", whatsAppChat(t, chat.name, chat.photo))})
		if err != nil {
			t.Fatal(err)
		}
		runJob(t, a, "u1", job.ID)
	}
	page, err := a.Timeline.Query(ctx, app.Query{UserID: "u1", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, ev := range page.Events {
		if len(ev.Media) == 0 {
			continue
		}
		rc, path, err := a.Timeline.Media(ctx, "u1", ev.ID, 0)
		if err != nil {
			t.Fatalf("media of %s: %v", ev.Title, err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		if path != "whatsapp/IMG-20220415-WA0001.jpg" {
			t.Errorf("media path = %q", path)
		}
		seen[ev.Actor] = string(b)
	}
	if seen["Alice"] != "PHOTO-FROM-ALICES-PHONE" || seen["Bob"] != "PHOTO-FROM-BOBS-PHONE" {
		t.Errorf("photos by sender = %v; want each chat's own", seen)
	}
}

// legacyJobs hands back, for the jobs in release, the path keys a store
// upgraded from before content addressing would (Jobs.ReleaseLegacyMedia),
// and notes which jobs asked.
type legacyJobs struct {
	app.Jobs
	release map[string][]string
	asked   []string
}

func (l *legacyJobs) ReleaseLegacyMedia(ctx context.Context, userID, jobID string) ([]string, error) {
	l.asked = append(l.asked, jobID)
	if keys, ok := l.release[jobID]; ok {
		delete(l.release, jobID)
		return keys, nil
	}
	return l.Jobs.ReleaseLegacyMedia(ctx, userID, jobID)
}

// An import from before content addressing stored its media under its path.
// Its next completed run stores the media under its content and deletes the
// path-keyed copies the store releases. A failed run releases nothing, and a
// copy that cannot be deleted does not fail the import.
func TestCompletedRunDeletesMediaStoredByPath(t *testing.T) {
	ctx := context.Background()
	blobs := newMemBlobs()
	d := importDeps(t, blobs)
	jobs := &legacyJobs{Jobs: d.Jobs, release: map[string][]string{}}
	d.Jobs = jobs
	a := app.New(d)
	enqueue := func(zip string) domain.ImportJob {
		t.Helper()
		job, err := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "whatsapp", Parts: stageZips(t, a, "u1", zip)})
		if err != nil {
			t.Fatal(err)
		}
		return job
	}

	const byPath = "whatsapp/IMG-20220415-WA0001.jpg"
	if err := blobs.Put(ctx, "u1", byPath, strings.NewReader("PHOTO")); err != nil {
		t.Fatal(err)
	}
	job := enqueue(whatsAppChat(t, "Alice", "PHOTO"))
	jobs.release[job.ID] = []string{byPath}
	runJob(t, a, "u1", job.ID)
	if _, err := blobs.Open(ctx, "u1", byPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the copy stored by path is still there (%v)", err)
	}
	sum := sha256.Sum256([]byte("PHOTO"))
	if _, err := blobs.Open(ctx, "u1", app.MediaKey("whatsapp", hex.EncodeToString(sum[:]))); err != nil {
		t.Errorf("the copy under its content: %v", err)
	}

	garbage := filepath.Join(t.TempDir(), "broken.zip")
	if err := os.WriteFile(garbage, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	failed := enqueue(garbage)
	if did, err := a.Worker.ProcessNext(ctx); err != nil || !did {
		t.Fatalf("ProcessNext did=%v err=%v", did, err)
	}
	if got, _ := a.Import.GetJob(ctx, "u1", failed.ID); got.Status != domain.JobError {
		t.Fatalf("broken archive: status %s, want error", got.Status)
	}
	if slices.Contains(jobs.asked, failed.ID) {
		t.Error("a failed run released media stored by path")
	}

	blobs.failDelete = errors.New("store unavailable")
	again := enqueue(whatsAppChat(t, "Bob", "OTHER PHOTO"))
	jobs.release[again.ID] = []string{"whatsapp/IMG-20220415-WA0002.jpg"}
	runJob(t, a, "u1", again.ID) // done, although the copy stays
}
