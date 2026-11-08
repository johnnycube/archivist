package app_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// endlessEngine emits events until its context ends, the way a provider
// stuck on an enormous (or hostile) member would.
type endlessEngine struct{ started chan struct{} }

func (e endlessEngine) Import(ctx context.Context, _ string, in app.ArchiveInput) (*domain.Coverage, error) {
	close(e.started)
	for i := 0; ; i++ {
		if err := ctx.Err(); err != nil {
			return &domain.Coverage{}, err
		}
		ev := domain.Event{
			SourceID: "reddit", ExternalID: "e" + strconv.Itoa(i),
			Type: "reddit.post", Category: domain.CategoryContent, OccurredAt: time.Unix(int64(i), 0),
		}
		if err := in.Emit(ev); err != nil {
			return &domain.Coverage{}, err
		}
		time.Sleep(time.Millisecond)
	}
}

func budgetApp(t *testing.T, budget app.ImportBudget) (*app.App, endlessEngine, domain.ImportJob) {
	t.Helper()
	deps := importDeps(t, nil)
	eng := endlessEngine{started: make(chan struct{})}
	deps.Engine = eng
	deps.ImportBudget = budget
	a := app.New(deps)
	job, err := a.Import.Enqueue(context.Background(), app.ImportSpec{UserID: "u1", Source: "reddit", Parts: stageZips(t, a, "u1", buildZip(t))})
	if err != nil {
		t.Fatal(err)
	}
	return a, eng, job
}

// finish runs the worker and returns the job once it has an outcome.
func finish(t *testing.T, a *app.App, id string, during func()) domain.ImportJob {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := a.Worker.ProcessNext(context.Background())
		done <- err
	}()
	if during != nil {
		during()
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ProcessNext: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the import did not stop")
	}
	job, err := a.Import.GetJob(context.Background(), "u1", id)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestOwnerCancelsARunningImport(t *testing.T) {
	a, eng, job := budgetApp(t, app.ImportBudget{CancelPoll: 10 * time.Millisecond})
	got := finish(t, a, job.ID, func() {
		<-eng.started
		if _, err := a.Import.CancelJob(context.Background(), "u2", job.ID); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("another user cancelling = %v; want ErrNotFound", err)
		}
		j, err := a.Import.CancelJob(context.Background(), "u1", job.ID)
		if err != nil || !j.CancelRequested {
			t.Errorf("CancelJob(running) = %+v, %v", j, err)
		}
	})
	if got.Status != domain.JobError || got.Error != domain.JobCancelledMessage || got.CancelRequested {
		t.Errorf("cancelled job = %s / %q / requested %v", got.Status, got.Error, got.CancelRequested)
	}
	if _, err := a.Import.CancelJob(context.Background(), "u1", job.ID); !errors.Is(err, app.ErrJobFinished) {
		t.Errorf("cancelling a finished job = %v; want ErrJobFinished", err)
	}
	if !got.CanRequeue() {
		t.Error("a cancelled import must be reprocessable")
	}
}

func TestOwnerCancelsAQueuedImport(t *testing.T) {
	a, _, job := budgetApp(t, app.ImportBudget{})
	j, err := a.Import.CancelJob(context.Background(), "u1", job.ID)
	if err != nil || j.Status != domain.JobError || j.Error != domain.JobCancelledMessage {
		t.Fatalf("CancelJob(queued) = %+v, %v", j, err)
	}
	if did, err := a.Worker.ProcessNext(context.Background()); did || err != nil {
		t.Errorf("the worker claimed a cancelled job: %v, %v", did, err)
	}
}

func TestImportTimeout(t *testing.T) {
	a, _, job := budgetApp(t, app.ImportBudget{Timeout: 50 * time.Millisecond})
	got := finish(t, a, job.ID, nil)
	if got.Status != domain.JobError || !strings.Contains(got.Error, "IMPORT_TIMEOUT") || !strings.Contains(got.Error, "50ms") {
		t.Errorf("timed-out job = %s / %q; want an error naming IMPORT_TIMEOUT", got.Status, got.Error)
	}
}

func TestImportEventCap(t *testing.T) {
	a, _, job := budgetApp(t, app.ImportBudget{MaxEvents: 1200})
	got := finish(t, a, job.ID, nil)
	if got.Status != domain.JobError || !strings.Contains(got.Error, "MAX_IMPORT_EVENTS") {
		t.Errorf("capped job = %s / %q; want an error naming MAX_IMPORT_EVENTS", got.Status, got.Error)
	}
	if got.Saved != 1200 {
		t.Errorf("saved = %d; want exactly the cap (1200)", got.Saved)
	}
}

// bigEngine emits n events of size bytes each.
type bigEngine struct{ n, size int }

func (e bigEngine) Import(_ context.Context, _ string, in app.ArchiveInput) (*domain.Coverage, error) {
	body := strings.Repeat("x", e.size)
	for i := range e.n {
		if err := in.Emit(domain.Event{
			SourceID: "reddit", ExternalID: strconv.Itoa(i), Type: "reddit.post",
			Category: domain.CategoryContent, Body: body,
		}); err != nil {
			return nil, err
		}
	}
	return &domain.Coverage{}, nil
}

// countingEvents records the size of every batch the worker writes.
type countingEvents struct {
	app.EventRepository
	batches []int
}

func (c *countingEvents) SaveBatch(ctx context.Context, userID, accountID string, run app.ImportRun, evs []domain.Event) (int, error) {
	c.batches = append(c.batches, len(evs))
	return c.EventRepository.SaveBatch(ctx, userID, accountID, run, evs)
}

// Large events are written in smaller batches: 500 events of a megabyte
// would otherwise be marshalled as one half-gigabyte statement.
func TestBatchesAreBoundedByBytes(t *testing.T) {
	deps := importDeps(t, nil)
	deps.Engine = bigEngine{n: 100, size: 1 << 20}
	events := &countingEvents{EventRepository: deps.Events}
	deps.Events = events
	a := app.New(deps)
	job, err := a.Import.Enqueue(context.Background(), app.ImportSpec{UserID: "u1", Source: "reddit", Parts: stageZips(t, a, "u1", buildZip(t))})
	if err != nil {
		t.Fatal(err)
	}
	if got := finish(t, a, job.ID, nil); got.Status != domain.JobDone || got.Saved != 100 {
		t.Fatalf("job = %s, saved %d", got.Status, got.Saved)
	}
	for _, n := range events.batches {
		if n > 16 {
			t.Fatalf("a batch held %d events of 1 MiB; want at most ~16 MiB per batch (%v)", n, events.batches)
		}
	}
}

// slowEvents blocks every read until its context ends, like a pathological
// query, and then fails the way a driver may: with its own error.
type slowEvents struct{ app.EventRepository }

func (slowEvents) Timeline(ctx context.Context, _ app.Query) (app.Page, error) {
	<-ctx.Done()
	return app.Page{}, errors.New("pq: canceling statement due to user request")
}

func TestTimelineReadTimesOut(t *testing.T) {
	svc := app.TimelineService{Events: slowEvents{}, QueryTimeout: 50 * time.Millisecond}
	start := time.Now()
	_, err := svc.Query(context.Background(), app.Query{UserID: "u1"})
	if !errors.Is(err, app.ErrQueryTimeout) {
		t.Fatalf("err = %v; want ErrQueryTimeout", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("gave up after %v; want about 50ms", d)
	}
	// A failure that is not the timeout stays what it is.
	if _, err := (app.TimelineService{Events: slowEvents{}}).Query(canceled(), app.Query{UserID: "u1"}); errors.Is(err, app.ErrQueryTimeout) {
		t.Errorf("a cancelled request reported as a timeout: %v", err)
	}
}

func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// failingEvents fails every batch the way a database driver does: with
// internals no user should see.
type failingEvents struct{ app.EventRepository }

func (failingEvents) SaveBatch(context.Context, string, string, app.ImportRun, []domain.Event) (int, error) {
	return 0, errors.New(`pq: could not extend file "base/16384/2619": No space left on device (10.0.4.7)`)
}

func TestServerFailuresAreNotShownToTheUser(t *testing.T) {
	deps := importDeps(t, nil)
	deps.Events = failingEvents{deps.Events}
	a := app.New(deps)
	job, err := a.Import.Enqueue(context.Background(), app.ImportSpec{UserID: "u1", Source: "reddit", Parts: stageZips(t, a, "u1", buildZip(t))})
	if err != nil {
		t.Fatal(err)
	}
	got := finish(t, a, job.ID, nil)
	if got.Status != domain.JobError || !strings.Contains(got.Error, "problem on the server") {
		t.Errorf("job = %s / %q; want the generic server-fault message", got.Status, got.Error)
	}
	for _, leak := range []string{"pq:", "10.0.4.7", "base/16384"} {
		if strings.Contains(got.Error, leak) {
			t.Errorf("job error leaks %q: %q", leak, got.Error)
		}
	}
}

func TestArchiveProblemsAreShownToTheUser(t *testing.T) {
	deps := importDeps(t, nil)
	a := app.New(deps)
	parts, err := a.Import.StageNamed(context.Background(), "u1", "broken.zip", strings.NewReader("this is not a zip archive"))
	if err != nil {
		t.Fatal(err)
	}
	job, err := a.Import.Enqueue(context.Background(), app.ImportSpec{UserID: "u1", Source: "reddit", Parts: []app.StagedPart{parts}})
	if err != nil {
		t.Fatal(err)
	}
	got := finish(t, a, job.ID, nil)
	if got.Status != domain.JobError || strings.Contains(got.Error, "problem on the server") || !strings.Contains(got.Error, "zip") {
		t.Errorf("job = %s / %q; want the archive's own error", got.Status, got.Error)
	}
}

// shrinkingEngine yields fewer events on every run, like a provider that
// learned to map what an older version captured generically.
type shrinkingEngine struct{ runs *int }

func (e shrinkingEngine) Import(_ context.Context, _ string, in app.ArchiveInput) (*domain.Coverage, error) {
	*e.runs++
	n := 4 - *e.runs // run 1: 3 events, run 2: 2, …
	for i := range n {
		if err := in.Emit(domain.Event{
			SourceID: "reddit", ExternalID: "e" + strconv.Itoa(i), Type: "reddit.post",
			Category: domain.CategoryContent, Title: "t",
		}); err != nil {
			return nil, err
		}
	}
	return &domain.Coverage{}, nil
}

// A reprocess replaces what the import holds: events the new run no longer
// derives are removed, and the coverage says how many.
func TestReprocessRemovesSupersededEvents(t *testing.T) {
	ctx := context.Background()
	deps := importDeps(t, newMemBlobs())
	runs := 0
	deps.Engine = shrinkingEngine{runs: &runs}
	a := app.New(deps)
	job, err := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "reddit", Parts: stageZips(t, a, "u1", buildZip(t))})
	if err != nil {
		t.Fatal(err)
	}
	runJob(t, a, "u1", job.ID)
	if p, _ := a.Timeline.Query(ctx, app.Query{UserID: "u1"}); p.Total != 3 {
		t.Fatalf("after the first run: %d events", p.Total)
	}
	if _, err := a.Import.ReprocessJob(ctx, "u1", job.ID); err != nil {
		t.Fatal(err)
	}
	got := runJob(t, a, "u1", job.ID)
	if p, _ := a.Timeline.Query(ctx, app.Query{UserID: "u1"}); p.Total != 2 {
		t.Errorf("after the reprocess: %d events; want 2", p.Total)
	}
	if got.Coverage == nil || got.Coverage.Superseded != 1 {
		t.Errorf("coverage = %+v; want Superseded 1", got.Coverage)
	}
}

// recordingObserver keeps what the worker reports.
type recordingObserver struct{ outcomes []string }

func (r *recordingObserver) ImportFinished(source, outcome string, _ time.Duration, events int) {
	r.outcomes = append(r.outcomes, source+":"+outcome+":"+strconv.Itoa(events))
}

func TestWorkerReportsHowEachRunEnded(t *testing.T) {
	obs := &recordingObserver{}
	deps := importDeps(t, nil)
	deps.ImportObserver = obs
	deps.ImportBudget = app.ImportBudget{MaxEvents: 1200}
	deps.Engine = endlessEngine{started: make(chan struct{})}
	a := app.New(deps)
	job, err := a.Import.Enqueue(context.Background(), app.ImportSpec{UserID: "u1", Source: "reddit", Parts: stageZips(t, a, "u1", buildZip(t))})
	if err != nil {
		t.Fatal(err)
	}
	finish(t, a, job.ID, nil)
	if len(obs.outcomes) != 1 || obs.outcomes[0] != "reddit:too_many_events:1200" {
		t.Errorf("observed %v; want one too_many_events run with 1200 events", obs.outcomes)
	}
}
