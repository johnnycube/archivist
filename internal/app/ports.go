// Package app is the application layer of the hexagon: it defines the driven
// ports (interfaces the outside world must satisfy) and the use cases that
// orchestrate the domain. It depends on the domain only — never on any adapter,
// database, or transport. Adapters depend inward on these interfaces.
package app

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/johnnycube/archivist/internal/domain"
)

// ErrEmailTaken is returned by Users.Create when the email already exists.
var ErrEmailTaken = errors.New("email already registered")

// ErrIdentityTaken is returned by Users.LinkIdentity when the identity is
// linked to a different user.
var ErrIdentityTaken = errors.New("identity already linked to another account")

// ErrNotFound is returned by use cases when the entity does not exist for that
// user. Ports keep reporting absence as (…, false, nil); use cases translate.
var ErrNotFound = errors.New("not found")

// ErrJobFinished is returned when a finished import job is asked to cancel.
var ErrJobFinished = errors.New("import has already finished")

// ErrJobBusy is returned when an import job is queued or running and so cannot
// be reprocessed or deleted right now (or, for account deletion, while any of
// the user's imports is).
var ErrJobBusy = errors.New("import is queued or running")

// ErrInsufficientStorage is returned by Staging.CheckSpace when the staging
// area cannot take an upload of the declared size.
var ErrInsufficientStorage = errors.New("not enough free disk space for this upload")

// Query describes a timeline request. Filters are ANDed; empty filters match
// everything. Pagination is keyset via Cursor (see Cursor / DecodeCursor).
//
//   - From/To bound OccurredAt inclusively; as soon as either is set, undated
//     events are excluded.
//   - Search is word search over title and body: every whitespace-separated
//     term must occur as a whole word, case-insensitively. A search without
//     any word in it matches nothing.
//   - Limit follows ClampLimit. TimelineService normalizes the query
//     (Query.Normalize) before a repository sees it.
//   - Loc is the VIEWER'S time zone: the zone every day boundary of this query
//     is drawn in — the histogram's buckets (HistogramBucket.Day) and the
//     collapse rule's "same day" (CollapseRule.Continues). nil means UTC, which
//     is what a request without a zone gets, so a query that names none behaves
//     exactly as it did before zones existed. From/To are absolute instants and
//     do not depend on it; a transport that accepts a date-only bound resolves
//     it in this zone before filling them in.
//
// Loc must be a location time.LoadLocation can produce (an IANA name such as
// "Europe/Berlin", or time.UTC): a store may hand its name to a database, which
// only knows the IANA zones. A time.FixedZone is not portable across stores.
type Query struct {
	UserID     string
	Sources    []string
	Categories []string
	Types      []string
	Search     string
	From       time.Time
	To         time.Time
	Limit      int
	Cursor     string
	Loc        *time.Location
	// SkipSummary spares the store the Total and Facets of the whole filtered
	// set (a count and three GROUP BYs over it): a page after the first does
	// not need them again. Page.Total is then 0 and the facets empty.
	SkipSummary bool
}

// EmptyFacets are the facets of a query that skipped its summary.
func EmptyFacets() Facets {
	return Facets{Sources: []FacetCount{}, Categories: []FacetCount{}, Types: []FacetCount{}}
}

// Zone is the query's time zone, UTC when it names none. Every day boundary an
// adapter draws goes through this, so "no zone" means UTC in one place only.
func (q Query) Zone() *time.Location { return Zone(q.Loc) }

// Zone returns loc, or UTC when loc is nil.
func Zone(loc *time.Location) *time.Location {
	if loc == nil {
		return time.UTC
	}
	return loc
}

// FacetCount is one value of a facet with its count.
type FacetCount struct {
	Value string
	Count int
}

// Facets are counts for the primary filter dimensions over the filtered set.
type Facets struct {
	Sources    []FacetCount
	Categories []FacetCount
	Types      []FacetCount
}

// Page is one keyset page of the timeline.
type Page struct {
	Events     []domain.StoredEvent
	NextCursor string
	Facets     Facets
	Total      int
}

// --- driven ports (implemented by adapters) ---

// HistogramBucket is one day of the archive: event counts per key — per source
// (Histogram) or per category (HistogramByCategory). The day is the calendar
// day in the query's zone (Query.Loc, UTC when unset), so the buckets describe
// the same days the viewer sees on the timeline. Clients aggregate days into
// months/years as needed. Undated events are excluded.
type HistogramBucket struct {
	Day    string // YYYY-MM-DD in the query's zone
	Counts map[string]int
}

// ImportRun is the provenance SaveBatch records: the import job that writes
// the events and the one run of it (a first import or a reprocess) that does.
// Both may be empty (events owned by no import, e.g. seeded ones).
type ImportRun struct {
	JobID string
	RunID string
}

// EventRepository persists and queries Events per user. Save upserts on
// (user_id, account_id, source_id, external_id) — re-importing the same
// archive is idempotent, and two accounts of one source never share an event
// (ADR 0015); the returned bool is true when the event was archived for the
// first time and false when an existing event was refreshed.
//
// SaveBatch is the bulk form used by imports: same upsert semantics, one round
// trip per batch, attributing every event to run.JobID (the last import to
// write an event owns it) and stamping it with run.RunID. It returns how many
// events were new. DeleteByImport removes the events an import owns.
// SweepImport removes the events an import owns that its run run.RunID did
// not write — what a reprocess with an improved provider no longer derives
// from the archive; it does nothing for an empty RunID. Save is the non-import
// form (seeding, tools): the event it writes is owned by no import afterwards.
//
// Timeline orders by (occurred_at desc, id desc) with undated events last and
// issues/accepts cursors in the application's encoding (CursorAfter /
// DecodeCursor); a cursor that does not decode yields ErrBadCursor. Total and
// Facets describe the whole filtered set, not the page — unless q.SkipSummary,
// which leaves Total 0 and the facets empty. Facets, Events and the Histogram
// result are never nil.
//
// Collapsed is the same timeline (same filters, same order, same cursors) with
// runs of repetitive events folded into clusters as CollapseRule defines
// (collapse.go): q.Limit counts items, the cursor continues after the last
// event of the last item, and a store examines at most CollapseWindow events
// per page. Items, Facets and every Cluster.SampleTitles are never nil.
//
// Histogram counts dated events per UTC day and source; HistogramByCategory
// the same per category.
type EventRepository interface {
	Save(ctx context.Context, userID, accountID string, ev domain.Event) (domain.StoredEvent, bool, error)
	SaveBatch(ctx context.Context, userID, accountID string, run ImportRun, evs []domain.Event) (created int, err error)
	DeleteByImport(ctx context.Context, userID, importJobID string) (deleted int, err error)
	SweepImport(ctx context.Context, userID string, run ImportRun) (deleted int, err error)
	Timeline(ctx context.Context, q Query) (Page, error)
	Collapsed(ctx context.Context, q Query, rule CollapseRule) (CollapsedPage, error)
	Histogram(ctx context.Context, q Query) ([]HistogramBucket, error)
	HistogramByCategory(ctx context.Context, q Query) ([]HistogramBucket, error)
	Get(ctx context.Context, userID, id string) (domain.StoredEvent, bool, error)
}

// Users persists user accounts. Emails are identified by
// domain.NormalizeEmail: Create stores the normalized form and refuses a second
// account for it with ErrEmailTaken; ByEmail looks up case-insensitively.
// UpdatePassword and Delete of an unknown id are no-ops. Delete removes the
// user and everything they own in the sibling ports of the same store: events,
// accounts, import jobs and sessions (PostgreSQL: ON DELETE CASCADE; memory:
// memory.Store wires the adapters together). porttest.RunUserDeletion checks it.
type Users interface {
	Create(ctx context.Context, email, passwordHash string) (domain.User, error)
	ByEmail(ctx context.Context, email string) (domain.User, bool, error)
	ByID(ctx context.Context, id string) (domain.User, bool, error)
	UpdatePassword(ctx context.Context, id, passwordHash string) error
	Delete(ctx context.Context, id string) error
	// ByIdentity finds the user an external identity (an OIDC provider's id
	// and its subject claim) is linked to.
	ByIdentity(ctx context.Context, provider, subject string) (domain.User, bool, error)
	// LinkIdentity attaches (provider, subject) to the user; email is the
	// address the provider reported ("" = none), kept for the record. Linking
	// the same identity to the same user again is a no-op; to another user it
	// fails with ErrIdentityTaken; to an unknown user with ErrNotFound. A
	// user's identities go with the user on Delete.
	LinkIdentity(ctx context.Context, userID, provider, subject, email string) error
	// LinkedProviders lists the provider ids the user has an identity at,
	// sorted, each once; never nil.
	LinkedProviders(ctx context.Context, userID string) ([]string, error)
	// MarkEmailVerified records that an identity provider vouched for the
	// user's email (domain.User.EmailVerified). Unknown ids are a no-op.
	MarkEmailVerified(ctx context.Context, id string) error
	// List returns every user, ordered by email; never nil. For the operator
	// (AdminService), not for any request path.
	List(ctx context.Context) ([]domain.User, error)
}

// Accounts provisions per-source connections and returns their id. There is
// exactly one account per (user, source, label) — an empty label means
// "default" — also when many callers Ensure the same one concurrently: all of
// them get the same id.
type Accounts interface {
	Ensure(ctx context.Context, userID, source, label string) (accountID string, err error)
}

// PasswordHasher hashes and verifies passwords. NeedsRehash reports whether a
// stored hash was made with an outdated algorithm or parameters; a login then
// replaces it while the plaintext is at hand.
type PasswordHasher interface {
	Hash(password string) (string, error)
	Verify(encoded, password string) bool
	NeedsRehash(encoded string) bool
}

// Sessions is the revocable server-side session store. It only ever sees token
// HASHES: AuthService generates the opaque token, hands the client the token
// and the store its hash, so a leaked store does not leak live sessions.
// Logging out (or a password change) actually invalidates a session, unlike a
// stateless signed token.
//
// Time is passed in (the application's clock), never read by the store: a
// session created at now lives for the store's configured TTL, and Lookup
// reports ok only while now is before its expiry. Revoke of an unknown hash
// and RevokeUser of a user without sessions are no-ops.
//
// Idle timeout: a store configured with an idle TTL (> 0) also ends a session
// that no Lookup has used for that long, whatever its absolute expiry. A
// successful Lookup counts as use, but is written through at most once per
// SessionTouchInterval per session, so authenticating a request is normally a
// read. (The recorded "last seen" may therefore lag by up to that interval;
// the idle TTL must be well above it.)
//
// CreateReplacing is Create plus, in the same atomic step, the revocation of
// previousHash — but only if that session belongs to userID. A previous hash
// that is empty, unknown or somebody else's is left alone. It is what a login
// from a browser that already holds a session uses.
type Sessions interface {
	Create(ctx context.Context, tokenHash, userID string, now time.Time) error
	CreateReplacing(ctx context.Context, tokenHash, userID, previousHash string, now time.Time) error
	Lookup(ctx context.Context, tokenHash string, now time.Time) (userID string, ok bool, err error)
	Revoke(ctx context.Context, tokenHash string) error
	RevokeUser(ctx context.Context, userID string) error
}

// SessionTouchInterval is the least time between two "last seen" writes of one
// session (see Sessions).
const SessionTouchInterval = 5 * time.Minute

// AttemptRetention is how long an AttemptLimiter must remember a failure.
// Windows passed to Failures must not exceed it.
const AttemptRetention = time.Hour

// AttemptLimiter records FAILED attempts per opaque key in a sliding window —
// the shared state behind the login throttle, so it holds across processes and
// restarts. Keys are hashes made by the application (AuthService); no email or
// client address reaches a store.
//
//   - Failures counts the key's failures in (now-window, now] and returns the
//     time of the oldest one among them (zero when there is none), which is
//     when the window starts to free up.
//   - RecordFailure adds one failure at now. Stores prune on write: failures
//     older than AttemptRetention may be discarded (of any key).
//   - Reset forgets the key's failures; an unknown key is a no-op.
//
// Time is the caller's clock, never the store's.
type AttemptLimiter interface {
	Failures(ctx context.Context, key string, now time.Time, window time.Duration) (count int, oldest time.Time, err error)
	RecordFailure(ctx context.Context, key string, now time.Time) error
	Reset(ctx context.Context, key string) error
}

// Blobs stores ingested binary artifacts (original archives, extracted media),
// namespaced per user. The store is WRITE-ONCE: a Put to an existing key is a
// no-op — ingested artifacts are immutable and are never rewritten. Open on a
// missing key returns an error satisfying errors.Is(err, fs.ErrNotExist).
// Delete is the one way an artifact goes away — an explicit user request to
// remove an import — and is a no-op for a missing key. DeleteUser removes
// everything stored for a user (account deletion); it is idempotent, so a user
// with no blobs is not an error.
type Blobs interface {
	Put(ctx context.Context, userID, key string, r io.Reader) error
	Open(ctx context.Context, userID, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, userID, key string) error
	DeleteUser(ctx context.Context, userID string) error
}

// ArchivePart is one ZIP part of an export, opened for random access — what
// archive/zip needs (io.ReaderAt + size). Name is the ORIGINAL file name of the
// upload ("" if unknown); providers may need it (a WhatsApp iOS export carries
// the chat name nowhere else), so every Staging adapter has to supply it.
type ArchivePart interface {
	io.ReaderAt
	Size() int64
	Name() string
}

// StagedFile is an opened staged upload. Name is the ORIGINAL file name of the
// upload as recorded on the import job (sanitised by SanitizeUploadName; "" if
// the client gave none) — never the name or path it is staged under.
type StagedFile interface {
	ArchivePart
	io.Closer
	Name() string
}

// StagedRef describes one file in the staging area (Staging.List).
type StagedRef struct {
	Ref string
	// UserID owns the file ("" if the adapter cannot tell).
	UserID string
	// ModTime is when the file was last written to.
	ModTime time.Time
}

// Staging holds uploaded archive parts between the upload request and the
// worker that imports them. It must survive restarts (queued imports do). Refs
// are opaque, adapter-issued handles: they are stored on the job and never
// leave the server; no server path crosses this port.
//
//   - CheckSpace returns ErrInsufficientStorage when an upload of size bytes
//     (plus the adapter's safety margin) would not fit. size <= 0 checks the
//     margin only.
//   - Put streams r into a new staged file owned by userID.
//   - Open returns an error satisfying errors.Is(err, fs.ErrNotExist) once the
//     ref has been removed (or never existed). name is what the opened file
//     reports from Name(): staging does not store names, the import job does.
//   - Remove and RemoveUser are idempotent.
//   - List enumerates every staged file, in the ref form Put issues, for the
//     janitor (ImportService.SweepStaging). Files being written right now are
//     included; their ModTime is recent.
type Staging interface {
	CheckSpace(ctx context.Context, size int64) error
	Put(ctx context.Context, userID string, r io.Reader) (ref string, size int64, err error)
	Open(ctx context.Context, ref, name string) (StagedFile, error)
	List(ctx context.Context) ([]StagedRef, error)
	Remove(ctx context.Context, ref string) error
	RemoveUser(ctx context.Context, userID string) error
}

// ProviderCatalog lists the available sources. How a source is parsed is the
// archive engine's business; the application only needs to know which exist.
type ProviderCatalog interface {
	Descriptors() []domain.Descriptor
	Known(id string) bool
}

// ArchiveInput is one archive to parse: its ZIP part(s) and the sinks.
type ArchiveInput struct {
	// Parts are the opened parts of the export, imported as a single archive.
	Parts []ArchivePart
	// Emit receives each normalized Event.
	Emit func(domain.Event) error
	// PutBlob (optional) receives media members so attachments survive the
	// import: the member's archive path, the SHA-256 of its content (hex) and
	// the content.
	PutBlob func(path, sha256 string, r io.Reader) error
	// Progress (optional) reports uncompressed bytes consumed / in total. It
	// fires often; the receiver throttles.
	Progress func(doneBytes, totalBytes int64)
}

// ArchiveEngine parses an export archive of the source with the given id
// (one the ProviderCatalog knows).
type ArchiveEngine interface {
	Import(ctx context.Context, sourceID string, in ArchiveInput) (*domain.Coverage, error)
}

// JobDeletion is the outcome of Jobs.Delete.
type JobDeletion int

const (
	// JobNotFound: the user has no such job.
	JobNotFound JobDeletion = iota
	// JobBusy: the job is running and was left untouched.
	JobBusy
	// JobDeleted: the job is gone.
	JobDeleted
)

// JobUsage is what a user's import jobs currently consume (quota input).
type JobUsage struct {
	Active       int   // queued + running jobs
	ArchiveBytes int64 // uploaded archive bytes over all of the user's jobs
	// BlobBytes are the bytes of the media the user's jobs stored
	// (Jobs.RecordBlobs), each distinct key once.
	BlobBytes int64
}

// Stored is what the user's imports occupy in the blob store: the uploaded
// archives and the media extracted from them. The storage quota counts it.
func (u JobUsage) Stored() int64 { return u.ArchiveBytes + u.BlobBytes }

// JobStats is the state of the import queue over all users (for metrics).
type JobStats struct {
	Queued  int
	Running int
}

// ImportObserver is told how each run of the import worker ended — what a
// metrics adapter counts. Outcomes: done, cancelled, timeout,
// too_many_events, archive_error, server_fault.
type ImportObserver interface {
	ImportFinished(source, outcome string, duration time.Duration, events int)
}

// JobBlob is one media member a job's run stored: Path is "<source>/<archive
// member path>" — what events refer to — and Key the content-addressed key the
// blob lies under (MediaKey), Bytes its size.
type JobBlob struct {
	Path  string
	Key   string
	Bytes int64
}

// MediaKey is the content-addressed blob key of media with the given SHA-256
// (hex): identical files share one blob, whatever export they came in.
func MediaKey(source, sha256 string) string { return source + "/sha256/" + sha256 }

// Jobs is the durable import queue. Create enqueues: the store assigns the ID,
// derives Parts from ZipPaths, keeps the supplied CreatedAt/UpdatedAt (the
// application's clock; zero = the store's own), and defaults Status to queued.
//
// Claim atomically dequeues a queued job for a worker — no two claimers ever
// get the same job (Postgres uses FOR UPDATE SKIP LOCKED). The order is fair
// across users (round-robin): among the queued jobs, the one whose user was
// served least recently wins — users never served first — then the older
// CreatedAt, then the smaller ID. So one user's backlog cannot starve
// everybody else: A1 A2 A3 B1 is claimed as A1, B1, A2, A3.
//
// Progress updates a running job's counters; Complete records the outcome and
// resets the job's crash counter (Attempts). Get/List are read models for the
// UI. Status rules live on domain.ImportJob (CanRequeue, CanDelete).
type Jobs interface {
	Create(ctx context.Context, job domain.ImportJob) (domain.ImportJob, error)
	// CreateWithin is Create with the user's quotas enforced in the same
	// atomic step: admissions of one user are serialised, so concurrent
	// uploads cannot overshoot. It returns ErrTooManyImports when the user
	// already has limits.MaxActiveImports queued or running jobs, and
	// ErrStorageQuota when what they store (JobUsage.Stored) plus
	// job.ArchiveBytes would exceed limits.MaxArchiveBytes. A zero limit is
	// unlimited.
	CreateWithin(ctx context.Context, job domain.ImportJob, limits Limits) (domain.ImportJob, error)
	Get(ctx context.Context, userID, id string) (domain.ImportJob, bool, error)
	List(ctx context.Context, userID string, limit int) ([]domain.ImportJob, error)
	// Usage sums up the user's jobs for quota checks.
	Usage(ctx context.Context, userID string) (JobUsage, error)
	// StagedRefs returns every staging ref any job of any user points at,
	// whatever the job's status (the janitor must not remove those).
	StagedRefs(ctx context.Context) ([]string, error)
	Claim(ctx context.Context) (domain.ImportJob, bool, error)
	Progress(ctx context.Context, id string, saved int, bytesDone, bytesTotal int64) error
	Complete(ctx context.Context, id string, status domain.JobStatus, saved int, cov *domain.Coverage, errMsg string) error
	// Requeue puts a finished (done|error) job back on the queue. Attempts is
	// NOT reset: it only goes back to zero when a run records an outcome, so
	// reprocessing an archive that crashes the process stays bounded by
	// Recover's maxAttempts. ok is false when the user has no such job; a
	// queued or running job yields ErrJobBusy.
	Requeue(ctx context.Context, userID, id string) (job domain.ImportJob, ok bool, err error)
	// Delete removes the job unless it is running. The status check and the
	// removal are one atomic step, so a job a worker claimed a moment ago is
	// never deleted from under it.
	Delete(ctx context.Context, userID, id string) (JobDeletion, error)
	// RequestCancel cancels the user's job, atomically with the status check:
	// a queued job becomes an error job with domain.JobCancelledMessage at
	// once; a running one gets CancelRequested, which its worker acts on. ok
	// is false when the user has no such job; a finished one yields
	// ErrJobFinished. Claim, Requeue and Complete clear CancelRequested;
	// Recover fails (does not requeue) a running job that has it.
	RequestCancel(ctx context.Context, userID, id string) (job domain.ImportJob, ok bool, err error)
	// RecordBlobs notes what a run of the user's job stored (the job's
	// ledger): per path, the key and size. Recording a path again replaces its
	// key and size; a job that is not the user's records nothing. Ledger
	// entries go with the job row. orphaned are the keys the replaced entries
	// had that no ledger entry of the user refers to any more — blobs to
	// delete (a reprocess moving media recorded under its path, before
	// content addressing, to its content key). Never nil.
	RecordBlobs(ctx context.Context, userID, jobID string, blobs []JobBlob) (orphaned []string, err error)
	// ExclusiveBlobs lists the keys in the job's ledger that no other job of
	// the user has: the blobs deleting the job leaves unreferenced. Never nil.
	ExclusiveBlobs(ctx context.Context, userID, jobID string) ([]string, error)
	// MediaKey resolves path ("<source>/<member path>") to the key its blob
	// lies under: in the ledger of the user's job jobID, else in the ledger of
	// the user's most recent job that recorded the path; ok is false when no
	// job of the user did (media stored before the ledger existed, seeded
	// media).
	MediaKey(ctx context.Context, userID, jobID, path string) (key string, ok bool, err error)
	// ReleaseLegacyMedia follows a completed run of the user's job. If the
	// job's earlier runs stored media under its path (before content
	// addressing: an import older than migration 0025), it returns the paths
	// in the job's ledger whose path-keyed blob nothing shows any more —
	// every event of the user that names the path has its own job's ledger
	// entry for it, and no ledger entry uses the path as its key — and the
	// job stops counting as such. The caller deletes those blobs. Any other
	// job, or one that is not the user's, returns none. Never nil.
	ReleaseLegacyMedia(ctx context.Context, userID, jobID string) ([]string, error)
	// Stats counts the queued and running jobs of all users.
	Stats(ctx context.Context) (JobStats, error)
	// Recover requeues jobs orphaned in 'running' by a crash. Jobs claimed
	// maxAttempts times or more without ever recording an outcome are failed
	// instead of requeued (the rule is domain.ImportJob.Exhausted; they get
	// domain.JobGaveUpMessage). Call on startup, before the worker runs.
	Recover(ctx context.Context, maxAttempts int) (requeued, failed int, err error)
}
