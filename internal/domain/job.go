package domain

import "time"

// JobStatus is the lifecycle state of an import job.
type JobStatus string

const (
	JobQueued  JobStatus = "queued"
	JobRunning JobStatus = "running"
	JobDone    JobStatus = "done"
	JobError   JobStatus = "error"
)

// DefaultMaxJobAttempts is how many times a job may be claimed without ever
// recording an outcome (i.e. how many crashes it may cause) before crash
// recovery fails it instead of requeueing it.
const DefaultMaxJobAttempts = 3

// JobGaveUpMessage is the error recorded on a job that crash recovery gave up
// on (see ImportJob.Exhausted).
const JobGaveUpMessage = "import crashed repeatedly; giving up"

// JobCancelledMessage is the error recorded on a job its owner cancelled.
const JobCancelledMessage = "Cancelled. Events saved before the cancellation are kept; delete the import to remove them."

// ImportJob is a queued/importing/finished archive import. It is processed by a
// background worker so uploads return immediately and the UI can poll progress.
type ImportJob struct {
	ID     string
	UserID string
	Source string
	Label  string
	// ZipPaths are opaque staging refs (app.Staging) of the uploaded archive,
	// one per part (platforms split large exports into several ZIPs). They are
	// never server paths and never leave the server. The staged uploads are
	// removed after processing; the parts stay recoverable from the blob
	// store, which is what makes a job reprocessable.
	ZipPaths []string
	// PartNames are the file names the client gave the uploaded parts, index-
	// aligned with ZipPaths ("" = not supplied; the slice may be shorter than
	// ZipPaths on jobs that predate it). They are sanitised base names — the
	// user's own data, fine to show — and reach providers, because some exports
	// carry information only there (WhatsApp on iOS: the chat's name).
	PartNames []string
	Parts     int
	// ArchiveBytes is the size of the uploaded archive, all parts together. It
	// is what the per-user storage quota counts.
	ArchiveBytes int64
	Status       JobStatus
	// Attempts counts consecutive claims that never reached a recorded outcome
	// (i.e. crashes). Recording an outcome resets it; reprocessing does not, so
	// an archive that kills the process cannot be retried without bound.
	Attempts int
	Saved    int
	// BytesDone/BytesTotal are uncompressed archive bytes, updated while the
	// job runs so the UI can show progress.
	BytesDone  int64
	BytesTotal int64
	Coverage   *Coverage
	Error      string
	// CancelRequested is set on a running job whose owner asked to cancel it;
	// the worker notices within moments and records it as cancelled.
	CancelRequested bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Active reports whether the job is waiting for or held by a worker.
func (j ImportJob) Active() bool { return j.Status == JobQueued || j.Status == JobRunning }

// CanRequeue reports whether the job may be put back on the queue: only a
// finished job (done or failed) can be reprocessed.
func (j ImportJob) CanRequeue() bool { return j.Status == JobDone || j.Status == JobError }

// CanDelete reports whether the job may be removed: anything a worker is not
// processing right now. A queued job may be deleted (that cancels it).
func (j ImportJob) CanDelete() bool { return j.Status != JobRunning }

// PartName is the original file name of part i ("" when unknown).
func (j ImportJob) PartName(i int) string {
	if i < 0 || i >= len(j.PartNames) {
		return ""
	}
	return j.PartNames[i]
}

// Exhausted reports whether crash recovery must give up on the job: it was
// claimed maxAttempts times or more without ever recording an outcome. This is
// THE rule; a store that evaluates it in a query (attempts >= maxAttempts) must
// mirror it, and records JobGaveUpMessage on the jobs it fails.
func (j ImportJob) Exhausted(maxAttempts int) bool { return j.Attempts >= maxAttempts }
