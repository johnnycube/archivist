package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/johnnycube/archivist/internal/domain"
)

// ErrUnknownSource is returned when an import names a source no provider handles.
var ErrUnknownSource = errors.New("unknown source")

// ErrNoArchive is returned when an import has no archive part.
var ErrNoArchive = errors.New("no archive given")

// ErrQuotaExceeded is the class of per-user quota refusals; match the concrete
// quota with errors.Is on ErrTooManyImports / ErrStorageQuota.
var ErrQuotaExceeded = errors.New("quota exceeded")

// ErrTooManyImports: the user already has the maximum number of queued or
// running imports. Retrying later helps.
var ErrTooManyImports = fmt.Errorf("%w: too many imports in progress, wait for one to finish", ErrQuotaExceeded)

// ErrStorageQuota: the upload would take the user's stored archives over their
// limit. Deleting imports helps.
var ErrStorageQuota = fmt.Errorf("%w: archive storage limit reached, delete an import to free space", ErrQuotaExceeded)

// The ways a run ends early by the import budget (ImportBudget) or its owner.
var (
	// ErrImportCancelled: the owner cancelled the running import.
	ErrImportCancelled = errors.New("import cancelled")
	// ErrImportTimeout: the run exceeded ImportBudget.Timeout.
	ErrImportTimeout = errors.New("import took too long")
	// ErrImportTooManyEvents: the archive yielded more than
	// ImportBudget.MaxEvents events.
	ErrImportTooManyEvents = errors.New("import has too many events")
)

// ImportBudget bounds one run of the worker, so a single archive — hostile or
// just enormous — cannot hold the queue, which every user shares, forever.
// Zero values are unlimited; the composition root supplies the defaults.
type ImportBudget struct {
	// Timeout ends a run that takes longer (IMPORT_TIMEOUT).
	Timeout time.Duration
	// MaxEvents ends a run whose archive yields more events (MAX_IMPORT_EVENTS).
	MaxEvents int
	// CancelPoll is how often a running job is checked for a cancel request
	// (0 = DefaultCancelPoll).
	CancelPoll time.Duration
}

// DefaultCancelPoll is how often the worker checks whether the job it runs
// has been cancelled.
const DefaultCancelPoll = 2 * time.Second

// Limits are the per-user import quotas. A zero value means "unlimited"; the
// composition root supplies the real defaults.
type Limits struct {
	MaxActiveImports int   // queued + running jobs per user
	MaxArchiveBytes  int64 // uploaded archive bytes per user, over all imports
}

// ImportService is the use case for enqueuing imports, reading their status,
// and managing finished ones (reprocess, delete). The actual work runs in the
// background Worker, so uploads return immediately.
type ImportService struct {
	Jobs    Jobs
	Catalog ProviderCatalog
	Events  EventRepository
	Blobs   Blobs
	Staging Staging
	Limits  Limits
	// Gate tracks uploads in flight (Admit); New sets it.
	Gate *uploadGate
	// Clock stamps new jobs (nil = time.Now).
	Clock func() time.Time
}

// StagedPart is one uploaded archive part held in Staging. Name is the file
// name the client gave it (sanitised; "" = none).
type StagedPart struct {
	Ref  string
	Size int64
	Name string
}

// ImportSpec describes an archive to import for one connection. Parts holds
// one staged upload per part of the export.
type ImportSpec struct {
	UserID string
	Source string
	Label  string
	Parts  []StagedPart
}

// StageNamed streams one uploaded archive part into the staging area. name is
// the file name the client sent for it; it is untrusted and kept only in the
// sanitised form of SanitizeUploadName ("" is fine).
func (s ImportService) StageNamed(ctx context.Context, userID, name string, r io.Reader) (StagedPart, error) {
	ref, size, err := s.Staging.Put(ctx, userID, r)
	return StagedPart{Ref: ref, Size: size, Name: SanitizeUploadName(name)}, err
}

// maxUploadName is the longest upload file name kept, in bytes (the usual
// file-system limit).
const maxUploadName = 255

// SanitizeUploadName reduces a client-supplied file name to something safe to
// store and show: the base name only (both / and \ separate directories —
// browsers on Windows have sent full paths), no control characters, valid
// UTF-8, at most 255 bytes. Anything that is not a name yields "".
func SanitizeUploadName(name string) string {
	name = strings.ToValidUTF8(name, "")
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name))
	if name == "." || name == ".." || name == "/" {
		return ""
	}
	for len(name) > maxUploadName { // cut on a rune boundary
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

// Discard removes staged parts of an upload that will not become an import.
func (s ImportService) Discard(ctx context.Context, parts []StagedPart) {
	for _, p := range parts {
		_ = s.Staging.Remove(ctx, p.Ref)
	}
}

// Enqueue validates the import, enforces the user's quotas, and creates a
// queued import job. Errors are typed: ErrUnknownSource and ErrNoArchive are
// the caller's fault, ErrQuotaExceeded is a refusal; anything else is an
// infrastructure failure. On error the staged parts are left to the caller.
func (s ImportService) Enqueue(ctx context.Context, spec ImportSpec) (domain.ImportJob, error) {
	if !s.Catalog.Known(spec.Source) {
		return domain.ImportJob{}, fmt.Errorf("%w %q", ErrUnknownSource, spec.Source)
	}
	if len(spec.Parts) == 0 {
		return domain.ImportJob{}, ErrNoArchive
	}
	refs := make([]string, len(spec.Parts))
	names := make([]string, len(spec.Parts))
	var total int64
	for i, p := range spec.Parts {
		refs[i] = p.Ref
		names[i] = SanitizeUploadName(p.Name)
		total += p.Size
	}
	label := spec.Label
	if label == "" {
		label = "default"
	}
	now := clockOrNow(s.Clock).UTC()
	// The quota check and the insert are one atomic step per user.
	return s.Jobs.CreateWithin(ctx, domain.ImportJob{
		UserID:       spec.UserID,
		Source:       spec.Source,
		Label:        label,
		ZipPaths:     refs,
		PartNames:    names,
		Parts:        len(refs),
		ArchiveBytes: total,
		Status:       domain.JobQueued,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, s.Limits)
}

// ReprocessJob puts a finished import back on the queue. The worker re-derives
// its events from the preserved original archive — this is how an improved
// provider reaches data that was imported before the improvement. The user
// having no such job yields ErrNotFound; a queued or running job, ErrJobBusy.
func (s ImportService) ReprocessJob(ctx context.Context, userID, id string) (domain.ImportJob, error) {
	job, ok, err := s.Jobs.Requeue(ctx, userID, id)
	if err == nil && !ok {
		err = ErrNotFound
	}
	if err != nil {
		return domain.ImportJob{}, err
	}
	return job, nil
}

// DeleteJob removes an import: the events it owns (those no later import has
// refreshed), its preserved archive, its staged uploads, and the job itself.
// The user having no such job yields ErrNotFound; a running job, ErrJobBusy.
// deleted counts the events removed, also when a later step failed. Extracted
// media is shared between imports of the same account data and is
// kept.
//
// Order, and why — the job row goes LAST:
//
//  1. Events the import owns.
//  2. Preserved archive parts and staged uploads.
//  3. The job row, atomically guarded against a running job.
//
// The job row is the only handle to the rest: the archive keys derive from its
// id, the staging refs live on it, and events point at it (the store nulls
// that pointer when the row goes, after which they could never be attributed
// to an import again). Removing it first and then failing on the blob store
// would leave an archive — private data — that no user action can reach. With
// the row last, every partial failure leaves the import listed; steps 1 and 2
// are idempotent, so deleting again finishes the job.
//
// A queued job can still be claimed by a worker between the check below and
// step 3. Step 3 then reports the job busy and nothing is orphaned: the worker
// may re-create the archive or events, but the row is still there, and
// deleting once more after the run removes them.
func (s ImportService) DeleteJob(ctx context.Context, userID, id string) (deleted int, err error) {
	job, err := s.GetJob(ctx, userID, id)
	if err != nil {
		return 0, err
	}
	if !job.CanDelete() {
		return 0, ErrJobBusy
	}
	if deleted, err = s.Events.DeleteByImport(ctx, userID, id); err != nil {
		return 0, err
	}
	// Media this import stored that no other import of the user did. (A run of
	// another import that started meanwhile and found such a blob already in
	// place would lose it; reprocessing that import stores it again.)
	if s.Blobs != nil {
		keys, err := s.Jobs.ExclusiveBlobs(ctx, userID, id)
		if err != nil {
			return deleted, err
		}
		for _, key := range keys {
			if err := s.Blobs.Delete(ctx, userID, key); err != nil {
				return deleted, fmt.Errorf("delete media: %w", err)
			}
		}
	}
	for i, ref := range job.ZipPaths {
		if s.Blobs != nil {
			if err := s.Blobs.Delete(ctx, userID, ArchiveKey(job, i)); err != nil {
				return deleted, fmt.Errorf("delete archive: %w", err)
			}
		}
		if s.Staging != nil && ref != "" {
			if err := s.Staging.Remove(ctx, ref); err != nil {
				return deleted, fmt.Errorf("delete staged upload: %w", err)
			}
		}
	}
	switch outcome, err := s.Jobs.Delete(ctx, userID, id); {
	case err != nil:
		return deleted, err
	case outcome == JobBusy:
		return deleted, ErrJobBusy
	default:
		// JobNotFound here means a concurrent delete won; the import is gone
		// either way.
		return deleted, nil
	}
}

// GetJob returns one job of the user, or ErrNotFound.
func (s ImportService) GetJob(ctx context.Context, userID, id string) (domain.ImportJob, error) {
	job, ok, err := s.Jobs.Get(ctx, userID, id)
	if err == nil && !ok {
		err = ErrNotFound
	}
	if err != nil {
		return domain.ImportJob{}, err
	}
	return job, nil
}

// DefaultStagingMaxAge is how old an unreferenced staged upload must be before
// SweepStaging removes it. Far longer than any upload takes: a part being
// received right now is unreferenced too (its job does not exist yet).
const DefaultStagingMaxAge = 24 * time.Hour

// SweepStaging is the janitor for uploads that never became an import (the
// request died mid-upload, the process crashed before Enqueue, a restored copy
// outlived a crashed reprocess). It removes staged files that were last written
// more than maxAge ago (<= 0: DefaultStagingMaxAge) AND that no import job —
// queued, running or finished — points at, and returns how many it removed.
//
// It errs on the side of keeping: a file is kept when it is young, when any
// job's ref names it (also in the path form old jobs carry, hence the suffix
// match), or when its owner has an import queued or running — a reprocess
// works on restored copies that no job row references. A failure to remove one
// file does not stop the sweep; the first such error is returned at the end.
func (s ImportService) SweepStaging(ctx context.Context, maxAge time.Duration) (removed int, err error) {
	if s.Staging == nil {
		return 0, nil
	}
	if maxAge <= 0 {
		maxAge = DefaultStagingMaxAge
	}
	staged, err := s.Staging.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list staged uploads: %w", err)
	}
	cutoff := clockOrNow(s.Clock).Add(-maxAge)
	var old []StagedRef
	for _, f := range staged {
		if f.ModTime.Before(cutoff) {
			old = append(old, f)
		}
	}
	if len(old) == 0 {
		return 0, nil
	}
	// Read the jobs AFTER the listing: a file old enough to be a candidate
	// whose job was created in between is then seen as referenced.
	refs, err := s.Jobs.StagedRefs(ctx)
	if err != nil {
		return 0, fmt.Errorf("list referenced uploads: %w", err)
	}
	referenced := func(ref string) bool {
		for _, r := range refs {
			r = strings.ReplaceAll(r, `\`, "/")
			if r == ref || strings.HasSuffix(r, "/"+ref) {
				return true
			}
		}
		return false
	}
	busy := map[string]bool{} // user id → has an active import
	for _, f := range old {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if referenced(f.Ref) {
			continue
		}
		if f.UserID != "" {
			active, seen := busy[f.UserID]
			if !seen {
				usage, uerr := s.Jobs.Usage(ctx, f.UserID)
				if uerr != nil {
					return removed, uerr
				}
				active = usage.Active > 0
				busy[f.UserID] = active
			}
			if active {
				continue
			}
		}
		if rerr := s.Staging.Remove(ctx, f.Ref); rerr != nil {
			if err == nil {
				err = fmt.Errorf("remove staged upload: %w", rerr)
			}
			continue
		}
		removed++
	}
	return removed, err
}

// CancelJob cancels the user's import: a queued one at once, a running one as
// soon as its worker notices (within ImportBudget.CancelPoll). No such job is
// ErrNotFound; a finished one, ErrJobFinished.
func (s ImportService) CancelJob(ctx context.Context, userID, id string) (domain.ImportJob, error) {
	job, ok, err := s.Jobs.RequestCancel(ctx, userID, id)
	if err == nil && !ok {
		err = ErrNotFound
	}
	if err != nil {
		return domain.ImportJob{}, err
	}
	return job, nil
}

// RecentJobs lists a user's recent import jobs.
func (s ImportService) RecentJobs(ctx context.Context, userID string, limit int) ([]domain.ImportJob, error) {
	if limit <= 0 {
		limit = 20
	}
	return s.Jobs.List(ctx, userID, limit)
}

// saveBatchSize is how many events the worker buffers per repository round
// trip, and saveBatchBytes roughly how large they may be together: a batch is
// written (and marshalled in one piece by the store) when either is reached,
// so 500 events of a megabyte each never sit in memory at once.
const (
	saveBatchSize  = 500
	saveBatchBytes = 16 << 20
)

// ledgerBatch is how many stored blobs the worker records per round trip.
const ledgerBatch = 1000

// countingReader counts the bytes read through it and remembers the first
// read error other than EOF.
type countingReader struct {
	r   io.Reader
	n   int64
	err error
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if err != nil && err != io.EOF && c.err == nil {
		c.err = err
	}
	return n, err
}

// serverFault marks a run's failure as the server's — the database, the blob
// store, the staging area — not the archive's. The job then records
// serverFaultMessage and the cause goes to the log only: it can name internal
// hosts, addresses and SQL, which are none of the user's business.
type serverFault struct{ err error }

func (f serverFault) Error() string { return f.err.Error() }
func (f serverFault) Unwrap() error { return f.err }

// serverFaultMessage is what a job records when the server failed it.
const serverFaultMessage = "The import failed because of a problem on the server, not with your archive. " +
	"The details are in the server log. Reprocess the import to try again."

// approxEventBytes estimates the size of an event as the store will encode
// it — for batching only, so cheap beats exact. Event.Capped bounds every
// part of it.
func approxEventBytes(ev domain.Event) int {
	n := 256 + len(ev.ExternalID) + len(ev.Type) + len(ev.Actor) + len(ev.Title) + len(ev.Body) +
		len(ev.ThreadExternalID) + len(ev.ParentExternalID)
	for _, t := range ev.Targets {
		n += 64 + len(t.TargetSource) + len(t.TargetExternalID) + len(t.TargetURL)
	}
	for _, m := range ev.Media {
		n += 64 + len(m.BlobKey) + len(m.MIME) + len(m.URL)
	}
	return n + approxJSONBytes(ev.Metadata, 0)
}

// approxJSONBytes estimates the encoded size of a metadata value.
func approxJSONBytes(v any, depth int) int {
	if depth > 16 {
		return 64
	}
	switch x := v.(type) {
	case string:
		return len(x) + 2
	case []byte:
		return len(x)*4/3 + 2
	case map[string]any:
		n := 2
		for k, e := range x {
			n += len(k) + 4 + approxJSONBytes(e, depth+1)
		}
		return n
	case []any:
		n := 2
		for _, e := range x {
			n += 1 + approxJSONBytes(e, depth+1)
		}
		return n
	case []string:
		n := 2
		for _, e := range x {
			n += len(e) + 3
		}
		return n
	}
	return 24
}

// progressInterval is the least time between two progress writes of a job.
const progressInterval = time.Second

// Worker drains the import queue: claim a job, parse the archive, persist every
// Event, and record the outcome. Run it in the background (one or more).
type Worker struct {
	Jobs     Jobs
	Catalog  ProviderCatalog
	Engine   ArchiveEngine
	Events   EventRepository
	Accounts Accounts
	// Staging is where the uploaded parts wait, and where parts of a reprocessed
	// job are restored to from the blob store.
	Staging Staging
	// Blobs, if set, preserves the original archive and receives its media.
	Blobs Blobs
	// Log receives job failures and queue errors (nil = discard).
	Log *slog.Logger
	// Clock paces the progress reports (nil = time.Now).
	Clock func() time.Time
	// Budget bounds every run (zero = unbounded).
	Budget ImportBudget
	// Observer is told how each run ended (nil = nobody).
	Observer ImportObserver
}

func (w Worker) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.New(slog.DiscardHandler)
}

// ProcessNext claims and runs a single job. It returns false when the queue is
// empty. A processing failure is recorded on the job and logged (not returned
// as error); an error return means the queue itself failed.
//
// Crash safety: staged uploads are only removed once they are preserved in the
// blob store AND the job's outcome is recorded. If ctx is cancelled mid-run
// (shutdown), no outcome is recorded: the job stays 'running' with its uploads
// intact and Jobs.Recover requeues it on the next boot.
func (w Worker) ProcessNext(ctx context.Context) (bool, error) {
	job, ok, err := w.Jobs.Claim(ctx)
	if err != nil || !ok {
		return false, err
	}
	log := w.log().With("job", job.ID, "user", job.UserID, "source", job.Source)
	st := &staged{}
	// Cleanup must also happen when ctx is already cancelled.
	defer st.release(context.WithoutCancel(ctx), w.Staging)
	started := time.Now()
	jobCtx, endRun := w.budgeted(ctx, job)
	saved, cov, runErr := w.runGuarded(jobCtx, job, st)
	stoppedBy := endRun()
	if ctx.Err() != nil {
		log.Warn("import interrupted; it will be requeued on the next start", "err", runErr)
		return true, ctx.Err()
	}
	if runErr != nil && stoppedBy != nil {
		runErr = stoppedBy // the budget or the owner ended the run, not the archive
	}
	if w.Observer != nil {
		w.Observer.ImportFinished(job.Source, outcome(runErr), time.Since(started), saved)
	}
	status := domain.JobDone
	msg := ""
	if runErr != nil {
		status = domain.JobError
		msg = w.budgetMessage(runErr)
		log.Warn("import failed", "err", runErr, "saved", saved)
	}
	if err := w.Jobs.Complete(ctx, job.ID, status, saved, cov, msg); err != nil {
		// Outcome not recorded: the job is requeued on the next boot, so its
		// uploads must survive.
		log.Error("import outcome not recorded; uploads kept for a retry", "err", err)
		return true, err
	}
	st.outcomeRecorded = true
	return true, nil
}

// budgeted derives the context one run of job works under: cancelled when
// the run exceeds Budget.Timeout, or when the job's owner cancels it (polled
// every Budget.CancelPoll). end stops the poller and returns why the run was
// ended early — ErrImportTimeout or ErrImportCancelled — or nil.
func (w Worker) budgeted(ctx context.Context, job domain.ImportJob) (_ context.Context, end func() error) {
	runCtx, cancel := context.WithCancelCause(ctx)
	stopTimer := func() bool { return false }
	if w.Budget.Timeout > 0 {
		t := time.AfterFunc(w.Budget.Timeout, func() { cancel(ErrImportTimeout) })
		stopTimer = t.Stop
	}
	poll := w.Budget.CancelPoll
	if poll <= 0 {
		poll = DefaultCancelPoll
	}
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(poll)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-runCtx.Done():
				return
			case <-tick.C:
				cur, ok, err := w.Jobs.Get(runCtx, job.UserID, job.ID)
				if err == nil && ok && cur.CancelRequested {
					cancel(ErrImportCancelled)
					return
				}
			}
		}
	}()
	return runCtx, func() error {
		close(done)
		stopTimer()
		cause := context.Cause(runCtx)
		cancel(nil)
		if errors.Is(cause, ErrImportCancelled) || errors.Is(cause, ErrImportTimeout) {
			return cause
		}
		return nil
	}
}

// outcome names how a run ended, for ImportObserver.
func outcome(err error) string {
	switch {
	case err == nil:
		return "done"
	case errors.Is(err, ErrImportCancelled):
		return "cancelled"
	case errors.Is(err, ErrImportTimeout):
		return "timeout"
	case errors.Is(err, ErrImportTooManyEvents):
		return "too_many_events"
	case errors.As(err, new(serverFault)):
		return "server_fault"
	}
	return "archive_error"
}

// budgetMessage is what the job records for a run that failed with err: the
// budget's refusals in words the owner can act on, everything else as is.
func (w Worker) budgetMessage(err error) string {
	switch {
	case errors.Is(err, ErrImportCancelled):
		return domain.JobCancelledMessage
	case errors.Is(err, ErrImportTimeout):
		return fmt.Sprintf("Stopped after %s: the import took longer than this server allows (IMPORT_TIMEOUT). "+
			"Events saved so far are kept; delete the import to remove them.", w.Budget.Timeout)
	case errors.Is(err, ErrImportTooManyEvents):
		return fmt.Sprintf("Stopped after %d events: the archive holds more than this server allows (MAX_IMPORT_EVENTS). "+
			"Events saved so far are kept; delete the import to remove them.", w.Budget.MaxEvents)
	case errors.As(err, new(serverFault)):
		return serverFaultMessage
	}
	return err.Error()
}

// runGuarded is run with a panic turned into a job failure. Providers parse
// untrusted uploads; a bug in one must fail that import, not take the server
// (and every other tenant's imports) down with it.
func (w Worker) runGuarded(ctx context.Context, job domain.ImportJob, st *staged) (saved int, cov *domain.Coverage, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("import crashed while parsing the archive: %v", r)
		}
	}()
	return w.run(ctx, job, st)
}

// staged tracks a job's opened archive parts. Restored copies are disposable;
// uploads are the only copy until they are preserved in the blob store, so they
// are removed only once they are safe and the job's outcome is recorded.
type staged struct {
	files           []StagedFile // every part, in order
	uploads         []string     // refs staged by the upload
	restored        []string     // refs restored from the blob store
	preserved       bool         // uploads are safely in the blob store
	noBlobs         bool         // no blob store: uploads are disposable after the run
	outcomeRecorded bool         // Jobs.Complete succeeded
}

func (s *staged) parts() []ArchivePart {
	out := make([]ArchivePart, len(s.files))
	for i, f := range s.files {
		out[i] = f
	}
	return out
}

// release closes every part, drops the restored copies, and drops the uploads
// if (and only if) that is safe.
func (s *staged) release(ctx context.Context, staging Staging) {
	for _, f := range s.files {
		_ = f.Close()
	}
	for _, ref := range s.restored {
		_ = staging.Remove(ctx, ref)
	}
	if s.outcomeRecorded && (s.preserved || s.noBlobs) {
		for _, ref := range s.uploads {
			_ = staging.Remove(ctx, ref)
		}
	}
}

// ArchiveKey is where part i of a job's original upload is preserved in the
// blob store (user-namespaced). Ingestion is immutable: the pristine archive is
// kept write-once, so every import can be audited or re-processed later.
func ArchiveKey(job domain.ImportJob, part int) string {
	if part == 0 {
		return "archives/" + job.ID + ".zip"
	}
	return fmt.Sprintf("archives/%s.part%d.zip", job.ID, part+1)
}

// stage opens every archive part into st: the staged upload on a first run, or
// a copy restored from the blob store on a reprocess.
func (w Worker) stage(ctx context.Context, job domain.ImportJob, st *staged) error {
	st.noBlobs = w.Blobs == nil
	for i, ref := range job.ZipPaths {
		f, err := w.Staging.Open(ctx, ref, job.PartName(i))
		switch {
		case err == nil:
			st.files = append(st.files, f)
			st.uploads = append(st.uploads, ref)
			continue
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("open archive part %d: %w", i+1, err)
		case w.Blobs == nil:
			return fmt.Errorf("archive part %d is no longer available", i+1)
		}
		if err := w.restore(ctx, job, i, st); err != nil {
			return fmt.Errorf("restore archive part %d: %w", i+1, err)
		}
	}
	if len(st.files) == 0 {
		return errors.New("job has no archive")
	}
	return nil
}

func (w Worker) restore(ctx context.Context, job domain.ImportJob, part int, st *staged) error {
	rc, err := w.Blobs.Open(ctx, job.UserID, ArchiveKey(job, part))
	if err != nil {
		return err
	}
	defer rc.Close()
	ref, _, err := w.Staging.Put(ctx, job.UserID, rc)
	if err != nil {
		return err
	}
	st.restored = append(st.restored, ref)
	f, err := w.Staging.Open(ctx, ref, job.PartName(part))
	if err != nil {
		return err
	}
	st.files = append(st.files, f)
	return nil
}

func (w Worker) run(ctx context.Context, job domain.ImportJob, st *staged) (int, *domain.Coverage, error) {
	if !w.Catalog.Known(job.Source) {
		return 0, nil, fmt.Errorf("unknown source %q", job.Source)
	}
	accountID, err := w.Accounts.Ensure(ctx, job.UserID, job.Source, job.Label)
	if err != nil {
		return 0, nil, serverFault{fmt.Errorf("provision account: %w", err)}
	}
	if err := w.stage(ctx, job, st); err != nil {
		return 0, nil, serverFault{err}
	}
	// Preserve the original archive before touching it. If this fails, the
	// job fails: importing without provenance would break the immutability
	// guarantee. (Write-once, so a reprocess leaves the preserved copy alone;
	// the staged uploads are removed only afterwards.)
	if w.Blobs != nil {
		for i, f := range st.files {
			err := w.Blobs.Put(ctx, job.UserID, ArchiveKey(job, i), io.NewSectionReader(f, 0, f.Size()))
			if err != nil {
				return 0, nil, serverFault{fmt.Errorf("preserve archive: %w", err)}
			}
		}
		st.preserved = true
	}
	// Every blob the run stores goes into the job's ledger (Jobs.RecordBlobs),
	// which the storage quota counts and DeleteJob cleans up after; recorded
	// in batches as the run goes, so a crash loses at most one batch.
	var ledger []JobBlob
	recordLedger := func(ctx context.Context) error {
		if len(ledger) == 0 {
			return nil
		}
		orphaned, err := w.Jobs.RecordBlobs(ctx, job.UserID, job.ID, ledger)
		ledger = ledger[:0]
		if err != nil {
			return serverFault{err}
		}
		// Media this job had recorded under another key (its path, before
		// content addressing) that nothing refers to any more.
		for _, key := range orphaned {
			if err := w.Blobs.Delete(ctx, job.UserID, key); err != nil {
				return serverFault{fmt.Errorf("delete superseded media: %w", err)}
			}
		}
		return nil
	}
	var putBlob func(path, sha256 string, r io.Reader) error
	if w.Blobs != nil {
		// Media is stored under its content (MediaKey): blobs are write-once,
		// so under its path the first export with a file of that name would
		// decide what every later one shows. The ledger maps the path events
		// use to the key.
		putBlob = func(path, sum string, r io.Reader) error {
			key := MediaKey(job.Source, sum)
			cr := &countingReader{r: r}
			if err := w.Blobs.Put(ctx, job.UserID, key, cr); err != nil {
				if cr.err != nil {
					return err // the member could not be read: the archive's fault
				}
				return serverFault{fmt.Errorf("store media: %w", err)}
			}
			// A blob that already existed was not read (write-once): read the
			// member through to learn its size for the ledger.
			if _, err := io.Copy(io.Discard, cr); err != nil {
				return err
			}
			ledger = append(ledger, JobBlob{Path: job.Source + "/" + path, Key: key, Bytes: cr.n})
			if len(ledger) >= ledgerBatch {
				return recordLedger(ctx)
			}
			return nil
		}
	}

	// This run's id: every event it writes carries it, so a successful run can
	// sweep the import's events it did not write (a reprocess with improved
	// providers no longer derives them).
	run := ImportRun{JobID: job.ID, RunID: newSessionToken()}
	saved, created, batchBytes := 0, 0, 0
	batch := make([]domain.Event, 0, saveBatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, err := w.Events.SaveBatch(ctx, job.UserID, accountID, run, batch)
		if err != nil {
			return serverFault{fmt.Errorf("save events: %w", err)}
		}
		saved += len(batch)
		created += n
		clear(batch) // drop the references: the backing array is reused
		batch, batchBytes = batch[:0], 0
		return nil
	}
	// Progress fires per record; write it through at most once a second.
	var lastReport time.Time
	progress := func(done, total int64) {
		now := clockOrNow(w.Clock)
		if now.Sub(lastReport) < progressInterval {
			return
		}
		lastReport = now
		if err := w.Jobs.Progress(ctx, job.ID, saved, done, total); err != nil {
			w.log().Warn("import progress not recorded", "job", job.ID, "err", err)
		}
	}

	cov, err := w.Engine.Import(ctx, job.Source, ArchiveInput{
		Parts: st.parts(),
		Emit: func(ev domain.Event) error {
			if w.Budget.MaxEvents > 0 && saved+len(batch) >= w.Budget.MaxEvents {
				return ErrImportTooManyEvents
			}
			batch = append(batch, ev)
			batchBytes += approxEventBytes(ev)
			if len(batch) >= saveBatchSize || batchBytes >= saveBatchBytes {
				return flush()
			}
			return nil
		},
		PutBlob:  putBlob,
		Progress: progress,
	})
	// What was stored is recorded whatever the outcome — also when the run was
	// cancelled — so a failed import's media is deleted with it too.
	if lerr := recordLedger(context.WithoutCancel(ctx)); lerr != nil && err == nil {
		err = fmt.Errorf("record stored media: %w", lerr)
	}
	switch {
	case err == nil:
		err = flush()
	case errors.Is(err, ErrImportTooManyEvents):
		// Keep exactly MaxEvents: the events before the cap are as good as
		// any import's, and the message says how many there are.
		if ferr := flush(); ferr != nil {
			err = ferr
		}
	}
	// Only a complete run knows every event the archive yields: then the
	// import's events it did not write are stale (an earlier run, an older
	// provider) and go.
	removed := 0
	if err == nil {
		if removed, err = w.Events.SweepImport(ctx, job.UserID, run); err != nil {
			err = serverFault{fmt.Errorf("remove superseded events: %w", err)}
		}
	}
	if err == nil && w.Blobs != nil {
		w.releaseLegacyMedia(ctx, job)
	}
	// Imports are idempotent (upsert on user/account/source/external id):
	// record how many events were genuinely new vs. refreshed from an earlier
	// import.
	if cov != nil {
		cov.New = created
		cov.Reimported = saved - created
		cov.Superseded = removed
		if err := w.Jobs.Progress(ctx, job.ID, saved, cov.Bytes, cov.Bytes); err != nil {
			w.log().Warn("import progress not recorded", "job", job.ID, "err", err)
		}
	}
	return saved, cov, err
}

// releaseLegacyMedia deletes the copies an import from before content
// addressing stored under media paths, now that its completed run stored the
// files under content keys, as far as nothing shows the old copies any more
// (Jobs.ReleaseLegacyMedia). The import itself succeeded, so a failure here
// is logged and leaves the copies in the blob store.
func (w Worker) releaseLegacyMedia(ctx context.Context, job domain.ImportJob) {
	keys, err := w.Jobs.ReleaseLegacyMedia(ctx, job.UserID, job.ID)
	if err != nil {
		w.log().Warn("media stored by path not released", "job", job.ID, "err", err)
		return
	}
	for _, key := range keys {
		if err := w.Blobs.Delete(ctx, job.UserID, key); err != nil {
			w.log().Warn("media stored by path not deleted", "job", job.ID, "key", key, "err", err)
		}
	}
}

// Run processes jobs until it is told to stop, polling the queue when idle.
// There are two ways to stop it, which is what makes a graceful shutdown
// possible:
//
//   - closing stop is graceful: the in-flight job runs to completion, then Run
//     returns without claiming another;
//   - cancelling ctx is hard: the in-flight job is interrupted, no outcome is
//     recorded, and Jobs.Recover requeues it on the next boot.
//
// A nil stop channel never fires.
func (w Worker) Run(ctx context.Context, stop <-chan struct{}, poll time.Duration) {
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	stopped := func() bool {
		select {
		case <-stop:
			return true
		default:
			return ctx.Err() != nil
		}
	}
	for {
		// Drain everything currently queued, then wait for the next tick.
		for !stopped() {
			did, err := w.ProcessNext(ctx)
			if err != nil && ctx.Err() == nil {
				w.log().Error("import queue", "err", err)
			}
			if err != nil || !did {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-t.C:
		}
	}
}
