package app_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/memory"
	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

func enqueue(t *testing.T, a *app.App, userID string) domain.ImportJob {
	t.Helper()
	job, err := a.Import.Enqueue(context.Background(), app.ImportSpec{
		UserID: userID, Source: "reddit", Parts: stageZips(t, a, userID, buildZip(t)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func stagedGone(t *testing.T, a *app.App, ref string) bool {
	t.Helper()
	f, err := a.Import.Staging.Open(context.Background(), ref, "")
	if err == nil {
		f.Close()
		return false
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return true
}

// ---- account deletion (privacy) ----

func TestDeleteAccountRemovesBlobsAndStagedUploads(t *testing.T) {
	ctx := context.Background()
	blobs := newMemBlobs()
	a := newImportApp(t, blobs)
	u, _, err := a.Auth.SignUp(ctx, "me@x.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	other, _, _ := a.Auth.SignUp(ctx, "other@x.com", "password123")

	done := enqueue(t, a, u.ID)
	runJob(t, a, u.ID, done.ID)
	kept := enqueue(t, a, other.ID)
	runJob(t, a, other.ID, kept.ID)
	// An upload that never became an import (e.g. the request died).
	stray := stageZips(t, a, u.ID, buildZip(t))[0]

	// While an import is queued the account cannot go: the worker would write
	// blobs for a user who no longer exists. The refusal has no side effects.
	queued := enqueue(t, a, u.ID)
	_, tok, _ := a.Auth.LogInGuarded(ctx, "me@x.com", "password123", "", "")
	if err := a.Auth.DeleteAccount(ctx, u.ID, "password123"); !errors.Is(err, app.ErrJobBusy) {
		t.Fatalf("delete with a queued import err = %v, want ErrJobBusy", err)
	}
	if _, ok, _ := a.Auth.Authenticate(ctx, tok); !ok {
		t.Error("a refused deletion logged the user out")
	}
	runJob(t, a, u.ID, queued.ID)

	// A blob-store failure must leave the account intact and deletable.
	blobs.failDelete = errors.New("object store down")
	if err := a.Auth.DeleteAccount(ctx, u.ID, "password123"); err == nil {
		t.Fatal("delete succeeded although the blobs could not be removed")
	}
	if _, _, err := a.Auth.LogInGuarded(ctx, "me@x.com", "password123", "", ""); err != nil {
		t.Fatalf("account is gone after a failed deletion (not retryable): %v", err)
	}
	blobs.failDelete = nil
	if err := a.Auth.DeleteAccount(ctx, u.ID, "password123"); err != nil {
		t.Fatalf("retry: %v", err)
	}

	for k := range blobs.m {
		if strings.HasPrefix(k, u.ID+"/") {
			t.Errorf("blob %q survived account deletion", k)
		}
	}
	if len(blobs.m) == 0 {
		t.Error("another user's blobs were deleted too")
	}
	if !stagedGone(t, a, stray.Ref) {
		t.Error("staged upload survived account deletion")
	}
	if _, _, err := a.Auth.LogInGuarded(ctx, "me@x.com", "password123", "", ""); !errors.Is(err, app.ErrInvalidCredentials) {
		t.Errorf("login after deletion err = %v, want ErrInvalidCredentials", err)
	}
}

// ---- import deletion (consistency) ----

// A failure half-way through must leave the import listed, so the archive stays
// reachable and deleting again finishes the job.
func TestDeleteImportIsRetryableAfterPartialFailure(t *testing.T) {
	ctx := context.Background()
	blobs := newMemBlobs()
	a := newImportApp(t, blobs)
	job := enqueue(t, a, "u1")
	runJob(t, a, "u1", job.ID)

	blobs.failDelete = errors.New("object store down")
	if _, err := a.Import.DeleteJob(ctx, "u1", job.ID); err == nil || errors.Is(err, app.ErrNotFound) {
		t.Fatalf("delete err = %v; want a failure", err)
	}
	if _, err := a.Import.GetJob(ctx, "u1", job.ID); err != nil {
		t.Fatal("job row is gone although its archive could not be deleted: the archive is orphaned")
	}
	if len(blobs.m["u1/"+app.ArchiveKey(job, 0)]) == 0 {
		t.Fatal("test setup: archive should still exist")
	}

	blobs.failDelete = nil
	if _, err := a.Import.DeleteJob(ctx, "u1", job.ID); err != nil {
		t.Fatalf("retry err = %v", err)
	}
	if _, err := a.Import.GetJob(ctx, "u1", job.ID); !errors.Is(err, app.ErrNotFound) {
		t.Error("job survived the retry")
	}
	if _, ok := blobs.m["u1/"+app.ArchiveKey(job, 0)]; ok {
		t.Error("archive survived the retry")
	}
}

func TestDeleteImportRunningIsBusyQueuedIsCancelled(t *testing.T) {
	ctx := context.Background()
	a := newImportApp(t, newMemBlobs())

	running := enqueue(t, a, "u1")
	if _, ok, err := a.Worker.Jobs.Claim(ctx); err != nil || !ok {
		t.Fatal("claim failed")
	}
	if _, err := a.Import.DeleteJob(ctx, "u1", running.ID); !errors.Is(err, app.ErrJobBusy) {
		t.Errorf("delete of a running job err = %v, want ErrJobBusy", err)
	}
	if stagedGone(t, a, running.ZipPaths[0]) {
		t.Error("delete of a running job removed its upload")
	}
	// The port itself is atomic about it, whatever the caller checked before.
	if out, err := a.Worker.Jobs.Delete(ctx, "u1", running.ID); err != nil || out != app.JobBusy {
		t.Errorf("Jobs.Delete(running) = %v, %v; want JobBusy", out, err)
	}
	if out, _ := a.Worker.Jobs.Delete(ctx, "u2", running.ID); out != app.JobNotFound {
		t.Errorf("Jobs.Delete(other user) = %v, want JobNotFound", out)
	}

	queued := enqueue(t, a, "u1")
	if _, err := a.Import.DeleteJob(ctx, "u1", queued.ID); err != nil {
		t.Fatalf("delete of a queued job err = %v", err)
	}
	if !stagedGone(t, a, queued.ZipPaths[0]) {
		t.Error("cancelled import left its staged upload behind")
	}
}

func TestJobStatusRules(t *testing.T) {
	for status, want := range map[domain.JobStatus][3]bool{
		// active, canRequeue, canDelete
		domain.JobQueued:  {true, false, true},
		domain.JobRunning: {true, false, false},
		domain.JobDone:    {false, true, true},
		domain.JobError:   {false, true, true},
	} {
		j := domain.ImportJob{Status: status}
		if got := [3]bool{j.Active(), j.CanRequeue(), j.CanDelete()}; got != want {
			t.Errorf("%s: active/requeue/delete = %v, want %v", status, got, want)
		}
	}
}

// ---- typed errors, signup switch, quotas ----

func TestEnqueueTypedErrors(t *testing.T) {
	ctx := context.Background()
	a := newImportApp(t, nil)
	if _, err := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "nope", Parts: []app.StagedPart{{Ref: "x"}}}); !errors.Is(err, app.ErrUnknownSource) {
		t.Errorf("unknown source err = %v, want ErrUnknownSource", err)
	}
	if _, err := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "reddit"}); !errors.Is(err, app.ErrNoArchive) {
		t.Errorf("no parts err = %v, want ErrNoArchive", err)
	}
	if _, err := a.Import.Admit(ctx, "u1", "nope", 10); !errors.Is(err, app.ErrUnknownSource) {
		t.Errorf("admit unknown source err = %v, want ErrUnknownSource", err)
	}
}

func TestSignupCanBeDisabled(t *testing.T) {
	deps := importDeps(t, nil)
	deps.SignupDisabled = true
	a := app.New(deps)
	if _, _, err := a.Auth.SignUp(context.Background(), "me@x.com", "password123"); !errors.Is(err, app.ErrSignupDisabled) {
		t.Errorf("signup err = %v, want ErrSignupDisabled", err)
	}
}

func TestImportQuotas(t *testing.T) {
	ctx := context.Background()
	deps := importDeps(t, newMemBlobs())
	deps.Limits = app.Limits{MaxActiveImports: 2}
	a := app.New(deps)

	first := enqueue(t, a, "u1")
	enqueue(t, a, "u1")
	parts := stageZips(t, a, "u1", buildZip(t))
	_, err := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "reddit", Parts: parts})
	if !errors.Is(err, app.ErrTooManyImports) || !errors.Is(err, app.ErrQuotaExceeded) {
		t.Fatalf("third active import err = %v, want ErrTooManyImports (an ErrQuotaExceeded)", err)
	}
	if _, err := a.Import.Admit(ctx, "u1", "reddit", 1); !errors.Is(err, app.ErrTooManyImports) {
		t.Errorf("admit err = %v, want ErrTooManyImports", err)
	}
	// Quotas are per user, and a finished import frees its slot.
	enqueue(t, a, "u2")
	runJob(t, a, "u1", first.ID)
	if _, err := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "reddit", Parts: parts}); err != nil {
		t.Errorf("enqueue after a slot freed up: %v", err)
	}

	// Storage: finished imports keep counting until they are deleted.
	size := parts[0].Size
	deps = importDeps(t, newMemBlobs())
	deps.Limits = app.Limits{MaxArchiveBytes: 2*size + size/2}
	a = app.New(deps)
	one := enqueue(t, a, "u1")
	runJob(t, a, "u1", one.ID)
	two := enqueue(t, a, "u1")
	runJob(t, a, "u1", two.ID)
	parts = stageZips(t, a, "u1", buildZip(t))
	if _, err := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "reddit", Parts: parts}); !errors.Is(err, app.ErrStorageQuota) || !errors.Is(err, app.ErrQuotaExceeded) {
		t.Fatalf("over-quota import err = %v, want ErrStorageQuota (an ErrQuotaExceeded)", err)
	}
	// Admit refuses on the declared size, before the upload is received.
	if _, err := a.Import.Admit(ctx, "u1", "reddit", size); !errors.Is(err, app.ErrStorageQuota) {
		t.Errorf("admit err = %v, want ErrStorageQuota", err)
	}
	if release, err := a.Import.Admit(ctx, "u1", "reddit", -1); err != nil {
		t.Errorf("admit with an undeclared size: %v", err)
	} else {
		release()
	}
	if _, err := a.Import.DeleteJob(ctx, "u1", one.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "reddit", Parts: parts}); err != nil {
		t.Errorf("enqueue after deleting an import: %v", err)
	}
}

// ---- login timing ----

// countingHasher records what Verify was asked to check.
type countingHasher struct {
	cryptoHasher
	verified []string
}

func (h *countingHasher) Verify(enc, pw string) bool {
	h.verified = append(h.verified, enc)
	return h.cryptoHasher.Verify(enc, pw)
}

// An unknown email must cost the same hash verification as a known one.
func TestLoginUnknownEmailDoesTheHashWork(t *testing.T) {
	ctx := context.Background()
	h := &countingHasher{}
	deps := importDeps(t, nil)
	deps.Hasher = h
	a := app.New(deps)
	if _, _, err := a.Auth.SignUp(ctx, "me@x.com", "password123"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := a.Auth.LogInGuarded(ctx, "nobody@x.com", "password123", "", ""); !errors.Is(err, app.ErrInvalidCredentials) {
		t.Fatalf("unknown email err = %v, want ErrInvalidCredentials", err)
	}
	if len(h.verified) != 1 || !strings.HasPrefix(h.verified[0], "H:") {
		t.Fatalf("Verify calls for an unknown email = %q, want one against a real (dummy) hash", h.verified)
	}
	// The dummy password itself must not log anyone in.
	dummyPassword := strings.TrimPrefix(h.verified[0], "H:")
	if _, _, err := a.Auth.LogInGuarded(ctx, "nobody@x.com", dummyPassword, "", ""); !errors.Is(err, app.ErrInvalidCredentials) {
		t.Errorf("login as an unknown email with the dummy password err = %v", err)
	}
}

// ---- attempts / recover ----

func TestReprocessDoesNotResetCrashCounter(t *testing.T) {
	ctx := context.Background()
	a := newImportApp(t, newMemBlobs())
	jobs := a.Worker.Jobs
	job := enqueue(t, a, "u1")
	const maxAttempts = 3

	crash := func() (requeued, failed int) { // claim, then "crash" before an outcome
		t.Helper()
		if _, ok, err := jobs.Claim(ctx); err != nil || !ok {
			t.Fatalf("claim ok=%v err=%v", ok, err)
		}
		requeued, failed, err := jobs.Recover(ctx, maxAttempts)
		if err != nil {
			t.Fatal(err)
		}
		return requeued, failed
	}
	for i := 1; i < maxAttempts; i++ {
		if rq, _ := crash(); rq != 1 {
			t.Fatalf("crash %d was not requeued", i)
		}
	}
	if _, failed := crash(); failed != 1 {
		t.Fatalf("crash %d was requeued again, want the job failed", maxAttempts)
	}

	// Reprocessing a job that keeps killing the process buys exactly one more
	// try, not a fresh budget.
	rq, err := a.Import.ReprocessJob(ctx, "u1", job.ID)
	if err != nil {
		t.Fatalf("reprocess err=%v", err)
	}
	if rq.Attempts != maxAttempts {
		t.Errorf("attempts after reprocess = %d, want %d (not reset)", rq.Attempts, maxAttempts)
	}
	if _, failed := crash(); failed != 1 {
		t.Error("a reprocessed crasher was requeued again after crashing")
	}

	// A run that records an outcome did not crash: the counter starts over, so
	// healthy imports can be reprocessed any number of times.
	if _, err := a.Import.ReprocessJob(ctx, "u1", job.ID); err != nil {
		t.Fatal(err)
	}
	after := runJob(t, a, "u1", job.ID)
	if after.Attempts != 0 {
		t.Errorf("attempts after a completed run = %d, want 0", after.Attempts)
	}
}

// ---- worker lifecycle, logging, crash safety ----

type failingComplete struct {
	app.Jobs
	err error
}

func (f failingComplete) Complete(context.Context, string, domain.JobStatus, int, *domain.Coverage, string) error {
	return f.err
}

// Staged uploads are only removed once preserved AND the outcome is recorded.
func TestUploadsSurviveUnrecordedOutcome(t *testing.T) {
	ctx := context.Background()
	var logs syncBuffer
	deps := importDeps(t, newMemBlobs())
	deps.Jobs = failingComplete{Jobs: deps.Jobs, err: errors.New("db down")}
	deps.Log = slog.New(slog.NewTextHandler(&logs, nil))
	a := app.New(deps)
	job := enqueue(t, a, "u1")

	if did, err := a.Worker.ProcessNext(ctx); !did || err == nil {
		t.Fatalf("ProcessNext did=%v err=%v, want the Complete error", did, err)
	}
	if stagedGone(t, a, job.ZipPaths[0]) {
		t.Error("upload removed although the outcome was not recorded")
	}
	if !strings.Contains(logs.String(), "outcome not recorded") || !strings.Contains(logs.String(), "db down") {
		t.Errorf("Complete error was not logged; log:\n%s", logs.String())
	}
}

func TestWorkerLogsJobFailure(t *testing.T) {
	var logs syncBuffer
	deps := importDeps(t, nil)
	deps.Engine = panickingEngine{}
	deps.Log = slog.New(slog.NewTextHandler(&logs, nil))
	a := app.New(deps)
	job := enqueue(t, a, "u1")
	if _, err := a.Worker.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if out := logs.String(); !strings.Contains(out, "import failed") || !strings.Contains(out, job.ID) {
		t.Errorf("job failure was not logged; log:\n%s", out)
	}
}

// blockingEngine parks inside Import until released or interrupted.
type blockingEngine struct {
	started chan struct{}
	release chan struct{}
}

func (e blockingEngine) Import(ctx context.Context, _ string, _ app.ArchiveInput) (*domain.Coverage, error) {
	close(e.started)
	select {
	case <-e.release:
		return domain.NewCoverage(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Graceful stop: the in-flight import finishes, then Run returns.
func TestWorkerStopWaitsForInFlightJob(t *testing.T) {
	eng := blockingEngine{started: make(chan struct{}), release: make(chan struct{})}
	deps := importDeps(t, nil)
	deps.Engine = eng
	a := app.New(deps)
	job := enqueue(t, a, "u1")
	second := enqueue(t, a, "u1")

	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		a.Worker.Run(context.Background(), stop, time.Millisecond)
	}()
	<-eng.started
	close(stop)
	select {
	case <-done:
		t.Fatal("Run returned while an import was still in flight")
	case <-time.After(20 * time.Millisecond):
	}
	close(eng.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the in-flight import finished")
	}
	if got, _ := a.Import.GetJob(context.Background(), "u1", job.ID); got.Status != domain.JobDone {
		t.Errorf("in-flight job = %s, want done", got.Status)
	}
	if got, _ := a.Import.GetJob(context.Background(), "u1", second.ID); got.Status != domain.JobQueued {
		t.Errorf("next job = %s, want still queued (no new claims after stop)", got.Status)
	}
}

// Hard stop: the import is interrupted, no outcome is recorded, the upload
// survives, and Recover puts the job back on the queue.
func TestWorkerInterruptLeavesJobRecoverable(t *testing.T) {
	eng := blockingEngine{started: make(chan struct{}), release: make(chan struct{})}
	deps := importDeps(t, newMemBlobs())
	deps.Engine = eng
	a := app.New(deps)
	job := enqueue(t, a, "u1")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.Worker.Run(ctx, nil, time.Millisecond)
	}()
	<-eng.started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
	got, _ := a.Import.GetJob(context.Background(), "u1", job.ID)
	if got.Status != domain.JobRunning {
		t.Errorf("interrupted job = %s (%q), want still running — an interruption is not an import error", got.Status, got.Error)
	}
	if stagedGone(t, a, job.ZipPaths[0]) {
		t.Error("upload of an interrupted import was removed")
	}
	if rq, _, err := a.Worker.Jobs.Recover(context.Background(), 3); err != nil || rq != 1 {
		t.Errorf("Recover requeued %d (err %v), want 1", rq, err)
	}
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// The password asked to confirm a sensitive change is throttled per account:
// a stolen session must not be a way to guess it.
func TestPasswordRechecksAreThrottled(t *testing.T) {
	ctx := context.Background()
	deps := importDeps(t, nil)
	deps.Attempts = memory.NewAttempts()
	a := app.New(deps)
	u, _, err := a.Auth.SignUp(ctx, "me@x.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < app.MaxReauthFailures; i++ {
		if _, err := a.Auth.ChangePassword(ctx, u.ID, "wrong", "new-password-1"); !errors.Is(err, app.ErrInvalidCredentials) {
			t.Fatalf("attempt %d = %v; want ErrInvalidCredentials", i, err)
		}
	}
	var throttled *app.TooManyAttemptsError
	if _, err := a.Auth.ChangePassword(ctx, u.ID, "password123", "new-password-1"); !errors.As(err, &throttled) || throttled.RetryAfter <= 0 {
		t.Errorf("after %d wrong passwords, even the right one = %v; want TooManyAttemptsError", app.MaxReauthFailures, err)
	}
	if err := a.Auth.DeleteAccount(ctx, u.ID, "password123"); !errors.As(err, &throttled) {
		t.Errorf("account deletion shares the throttle: %v", err)
	}
	// Logging in is a different budget.
	if _, _, err := a.Auth.LogInGuarded(ctx, "me@x.com", "password123", "", "client"); err != nil {
		t.Errorf("login after re-check failures = %v", err)
	}
}

// An upload still streaming would become a job after the account's data is
// gone: account deletion waits for it.
func TestAccountDeletionWaitsForUploadsInFlight(t *testing.T) {
	ctx := context.Background()
	a := newImportApp(t, nil)
	u, _, err := a.Auth.SignUp(ctx, "me@x.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	release, err := a.Import.Admit(ctx, u.ID, "reddit", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Auth.DeleteAccount(ctx, u.ID, "password123"); !errors.Is(err, app.ErrJobBusy) {
		t.Errorf("deleting during an upload = %v; want ErrJobBusy", err)
	}
	release()
	if err := a.Auth.DeleteAccount(ctx, u.ID, "password123"); err != nil {
		t.Errorf("deleting after the upload ended = %v", err)
	}
}
