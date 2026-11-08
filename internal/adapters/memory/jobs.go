package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// Jobs is an in-memory app.Jobs. Claim is fair across users (see app.Jobs).
type Jobs struct {
	mu sync.Mutex
	m  map[string]domain.ImportJob
	// served is, per user, the number of the claim that last served them
	// (claims counts them); a user without an entry was never served. It is
	// the memory form of users.last_import_claim in PostgreSQL.
	served map[string]uint64
	claims uint64
	clock  func() time.Time
	// blobs is each job's ledger (app.Jobs.RecordBlobs): path → entry.
	blobs map[string]map[string]ledgerEntry
}

func NewJobs() *Jobs {
	return &Jobs{m: map[string]domain.ImportJob{}, served: map[string]uint64{}, clock: time.Now, blobs: map[string]map[string]ledgerEntry{}}
}

var _ app.Jobs = (*Jobs)(nil)

func (j *Jobs) Create(_ context.Context, job domain.ImportJob) (domain.ImportJob, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.create(job), nil
}

// CreateWithin checks the quotas and creates the job under one lock.
func (j *Jobs) CreateWithin(_ context.Context, job domain.ImportJob, limits app.Limits) (domain.ImportJob, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	usage := j.usage(job.UserID)
	if limits.MaxActiveImports > 0 && usage.Active >= limits.MaxActiveImports {
		return domain.ImportJob{}, app.ErrTooManyImports
	}
	if limits.MaxArchiveBytes > 0 && usage.Stored()+job.ArchiveBytes > limits.MaxArchiveBytes {
		return domain.ImportJob{}, app.ErrStorageQuota
	}
	return j.create(job), nil
}

// create stores a new job; the caller holds the lock.
func (j *Jobs) create(job domain.ImportJob) domain.ImportJob {
	job.ID = newID()
	job.Parts = len(job.ZipPaths)
	if job.Status == "" {
		job.Status = domain.JobQueued
	}
	now := j.clock().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = now
	}
	j.m[job.ID] = job
	return job
}

func (j *Jobs) Get(_ context.Context, userID, id string) (domain.ImportJob, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.m[id]
	if !ok || job.UserID != userID {
		return domain.ImportJob{}, false, nil
	}
	return job, true, nil
}

func (j *Jobs) List(_ context.Context, userID string, limit int) ([]domain.ImportJob, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := []domain.ImportJob{}
	for _, job := range j.m {
		if job.UserID == userID {
			out = append(out, job)
		}
	}
	// Newest first; the id breaks ties so the order is total.
	sort.Slice(out, func(a, b int) bool {
		if c := out[a].CreatedAt.Compare(out[b].CreatedAt); c != 0 {
			return c > 0
		}
		return out[a].ID > out[b].ID
	})
	if len(out) > max(limit, 0) {
		out = out[:max(limit, 0)]
	}
	return out, nil
}

func (j *Jobs) Usage(_ context.Context, userID string) (app.JobUsage, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.usage(userID), nil
}

func (j *Jobs) StagedRefs(context.Context) ([]string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	refs := []string{}
	for _, job := range j.m {
		refs = append(refs, job.ZipPaths...)
	}
	return refs, nil
}

func (j *Jobs) deleteUser(userID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for id, job := range j.m {
		if job.UserID == userID {
			delete(j.m, id)
			delete(j.blobs, id)
		}
	}
	delete(j.served, userID)
}

// usage sums up a user's jobs; the caller holds the lock.
func (j *Jobs) usage(userID string) app.JobUsage {
	var u app.JobUsage
	for _, job := range j.m {
		if job.UserID != userID {
			continue
		}
		if job.Active() {
			u.Active++
		}
		u.ArchiveBytes += job.ArchiveBytes
	}
	for _, bytes := range j.userBlobs(userID) {
		u.BlobBytes += bytes
	}
	return u
}

// ledgerEntry is what a job's ledger holds for one path.
type ledgerEntry struct {
	key   string
	bytes int64
}

// userBlobs is the union of the user's job ledgers, each key once with its
// largest recorded size; the caller holds the lock.
func (j *Jobs) userBlobs(userID string) map[string]int64 {
	out := map[string]int64{}
	for id, ledger := range j.blobs {
		if j.m[id].UserID != userID {
			continue
		}
		for _, e := range ledger {
			out[e.key] = max(out[e.key], e.bytes)
		}
	}
	return out
}

// Stats counts queued and running jobs.
func (j *Jobs) Stats(context.Context) (app.JobStats, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var st app.JobStats
	for _, job := range j.m {
		switch job.Status {
		case domain.JobQueued:
			st.Queued++
		case domain.JobRunning:
			st.Running++
		case domain.JobDone, domain.JobError:
		}
	}
	return st, nil
}

// RecordBlobs sets the job's ledger entries if the job is the user's, and
// reports the keys the replaced entries had that nothing refers to any more.
func (j *Jobs) RecordBlobs(_ context.Context, userID, jobID string, blobs []app.JobBlob) ([]string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	orphaned := []string{}
	if job, ok := j.m[jobID]; !ok || job.UserID != userID || len(blobs) == 0 {
		return orphaned, nil
	}
	ledger := j.blobs[jobID]
	if ledger == nil {
		ledger = map[string]ledgerEntry{}
		j.blobs[jobID] = ledger
	}
	replaced := map[string]bool{}
	for _, b := range blobs {
		if old, ok := ledger[b.Path]; ok && old.key != b.Key {
			replaced[old.key] = true
		}
		ledger[b.Path] = ledgerEntry{key: b.Key, bytes: b.Bytes}
	}
	if len(replaced) > 0 {
		inUse := j.userBlobs(userID)
		for k := range replaced {
			if _, used := inUse[k]; !used {
				orphaned = append(orphaned, k)
			}
		}
		sort.Strings(orphaned)
	}
	return orphaned, nil
}

// ReleaseLegacyMedia returns none: this store never held media stored by path.
func (j *Jobs) ReleaseLegacyMedia(context.Context, string, string) ([]string, error) {
	return []string{}, nil
}

// ExclusiveBlobs lists the job's keys no other job of the user recorded.
func (j *Jobs) ExclusiveBlobs(_ context.Context, userID, jobID string) ([]string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	keys := []string{}
	if job, ok := j.m[jobID]; !ok || job.UserID != userID {
		return keys, nil
	}
	others := map[string]bool{}
	for id, ledger := range j.blobs {
		if id == jobID || j.m[id].UserID != userID {
			continue
		}
		for _, e := range ledger {
			others[e.key] = true
		}
	}
	seen := map[string]bool{}
	for _, e := range j.blobs[jobID] {
		if !others[e.key] && !seen[e.key] {
			seen[e.key] = true
			keys = append(keys, e.key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// MediaKey looks the path up in the job's ledger, then in the user's most
// recent job that recorded it.
func (j *Jobs) MediaKey(_ context.Context, userID, jobID, path string) (string, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if job, ok := j.m[jobID]; ok && job.UserID == userID {
		if e, ok := j.blobs[jobID][path]; ok {
			return e.key, true, nil
		}
	}
	var best domain.ImportJob
	key, found := "", false
	for id, ledger := range j.blobs {
		job := j.m[id]
		e, ok := ledger[path]
		if !ok || job.UserID != userID {
			continue
		}
		if !found || job.CreatedAt.After(best.CreatedAt) || job.CreatedAt.Equal(best.CreatedAt) && job.ID > best.ID {
			best, key, found = job, e.key, true
		}
	}
	return key, found, nil
}

func (j *Jobs) Claim(_ context.Context) (domain.ImportJob, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	// before is the queue order: the user served least recently (never = 0)
	// first, then the older job; the id breaks ties so the order is total.
	before := func(a, b domain.ImportJob) bool {
		if sa, sb := j.served[a.UserID], j.served[b.UserID]; sa != sb {
			return sa < sb
		}
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c < 0
		}
		return a.ID < b.ID
	}
	var next *domain.ImportJob
	for _, job := range j.m {
		if job.Status == domain.JobQueued && (next == nil || before(job, *next)) {
			next = &job
		}
	}
	if next == nil {
		return domain.ImportJob{}, false, nil
	}
	j.claims++
	j.served[next.UserID] = j.claims
	next.Status = domain.JobRunning
	next.CancelRequested = false
	next.Attempts++
	next.Saved, next.BytesDone, next.BytesTotal = 0, 0, 0
	next.UpdatedAt = j.clock().UTC()
	j.m[next.ID] = *next
	return *next, true, nil
}

func (j *Jobs) Recover(_ context.Context, maxAttempts int) (requeued, failed int, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for id, job := range j.m {
		if job.Status != domain.JobRunning {
			continue
		}
		if job.CancelRequested {
			job.Status, job.Error, job.CancelRequested, job.Attempts = domain.JobError, domain.JobCancelledMessage, false, 0
			failed++
		} else if job.Exhausted(maxAttempts) {
			job.Status = domain.JobError
			job.Error = domain.JobGaveUpMessage
			failed++
		} else {
			job.Status = domain.JobQueued
			requeued++
		}
		job.UpdatedAt = j.clock().UTC()
		j.m[id] = job
	}
	return requeued, failed, nil
}

func (j *Jobs) Progress(_ context.Context, id string, saved int, bytesDone, bytesTotal int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.m[id]
	if !ok || job.Status != domain.JobRunning {
		return nil
	}
	job.Saved, job.BytesDone, job.BytesTotal = saved, bytesDone, bytesTotal
	job.UpdatedAt = j.clock().UTC()
	j.m[id] = job
	return nil
}

func (j *Jobs) Requeue(_ context.Context, userID, id string) (domain.ImportJob, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.m[id]
	if !ok || job.UserID != userID {
		return domain.ImportJob{}, false, nil
	}
	if !job.CanRequeue() {
		return job, true, app.ErrJobBusy
	}
	// Attempts is deliberately kept: only a recorded outcome resets it.
	job.Status = domain.JobQueued
	job.Error = ""
	job.CancelRequested = false
	job.UpdatedAt = j.clock().UTC()
	j.m[id] = job
	return job, true, nil
}

func (j *Jobs) Delete(_ context.Context, userID, id string) (app.JobDeletion, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.m[id]
	if !ok || job.UserID != userID {
		return app.JobNotFound, nil
	}
	if !job.CanDelete() {
		return app.JobBusy, nil
	}
	delete(j.m, id)
	delete(j.blobs, id)
	return app.JobDeleted, nil
}

// RequestCancel fails a queued job as cancelled and flags a running one.
func (j *Jobs) RequestCancel(_ context.Context, userID, id string) (domain.ImportJob, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.m[id]
	if !ok || job.UserID != userID {
		return domain.ImportJob{}, false, nil
	}
	switch job.Status {
	case domain.JobQueued:
		job.Status, job.Error = domain.JobError, domain.JobCancelledMessage
	case domain.JobRunning:
		job.CancelRequested = true
	case domain.JobDone, domain.JobError:
		return job, true, app.ErrJobFinished
	}
	job.UpdatedAt = j.clock().UTC()
	j.m[id] = job
	return job, true, nil
}

func (j *Jobs) Complete(_ context.Context, id string, status domain.JobStatus, saved int, cov *domain.Coverage, errMsg string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.m[id]
	if !ok {
		return nil
	}
	job.Status = status
	job.CancelRequested = false
	job.Attempts = 0 // an outcome was recorded: the run did not crash
	job.Saved = saved
	job.Coverage = cov
	job.Error = errMsg
	job.UpdatedAt = j.clock().UTC()
	j.m[id] = job
	return nil
}
