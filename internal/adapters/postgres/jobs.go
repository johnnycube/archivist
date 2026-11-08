package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lib/pq"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// Jobs is a durable PostgreSQL app.Jobs. Claim uses FOR UPDATE SKIP LOCKED so
// multiple workers can drain the queue concurrently without double-processing
// (the same pattern River uses).
type Jobs struct{ db *sql.DB }

func NewJobs(db *sql.DB) *Jobs { return &Jobs{db: db} }

var _ app.Jobs = (*Jobs)(nil)

const jobCols = `id, user_id, source, label, zip_paths, part_names, archive_bytes, status, attempts, saved, bytes_done, bytes_total, coverage, error, cancel_requested, created_at, updated_at`

// queryer is what Create needs: the pool, or the transaction of CreateWithin.
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (j *Jobs) Create(ctx context.Context, job domain.ImportJob) (domain.ImportJob, error) {
	return insertJob(ctx, j.db, job)
}

func insertJob(ctx context.Context, q queryer, job domain.ImportJob) (domain.ImportJob, error) {
	if job.Status == "" {
		job.Status = domain.JobQueued
	}
	// The timestamps are the application's (its clock decides queue order);
	// a zero one falls back to the database's now().
	row := q.QueryRowContext(ctx, `
INSERT INTO import_jobs (user_id, source, label, zip_paths, part_names, archive_bytes, status, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7, COALESCE($8, now()), COALESCE($9, now()))
RETURNING `+jobCols,
		job.UserID, job.Source, job.Label, jsonStrings(job.ZipPaths), jsonStrings(job.PartNames),
		job.ArchiveBytes, string(job.Status), nullTime(job.CreatedAt), nullTime(job.UpdatedAt))
	return scanJob(row)
}

// jsonStrings encodes a string list as a jsonb array (nil = []).
func jsonStrings(v []string) []byte {
	if len(v) == 0 {
		return []byte("[]")
	}
	b, err := json.Marshal(v)
	if err != nil { // cannot happen for []string
		return []byte("[]")
	}
	return b
}

// CreateWithin checks the user's quotas and inserts the job in ONE transaction
// that first takes the user's row lock. Every admission of that user queues up
// behind it, so the usage it reads cannot change before its own insert commits:
// concurrent uploads cannot overshoot. (The lock is on users — the row every
// one of the user's jobs hangs off — and is held for two short statements;
// other users are not affected. FOR NO KEY UPDATE, so that it does not block
// the FOR KEY SHARE locks the user's other inserts take for their foreign key.
// A missing user yields the INSERT's FK error.)
func (j *Jobs) CreateWithin(ctx context.Context, job domain.ImportJob, limits app.Limits) (domain.ImportJob, error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ImportJob{}, err
	}
	defer tx.Rollback()
	if isUUID(job.UserID) {
		var locked string
		err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR NO KEY UPDATE`, job.UserID).Scan(&locked)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return domain.ImportJob{}, err
		}
	}
	var usage app.JobUsage
	if err := tx.QueryRowContext(ctx, usageSQL, job.UserID).Scan(&usage.Active, &usage.ArchiveBytes, &usage.BlobBytes); err != nil {
		return domain.ImportJob{}, err
	}
	if limits.MaxActiveImports > 0 && usage.Active >= limits.MaxActiveImports {
		return domain.ImportJob{}, app.ErrTooManyImports
	}
	if limits.MaxArchiveBytes > 0 && usage.Stored()+job.ArchiveBytes > limits.MaxArchiveBytes {
		return domain.ImportJob{}, app.ErrStorageQuota
	}
	created, err := insertJob(ctx, tx, job)
	if err != nil {
		return domain.ImportJob{}, err
	}
	return created, tx.Commit()
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func (j *Jobs) Get(ctx context.Context, userID, id string) (domain.ImportJob, bool, error) {
	if !isUUID(id) {
		return domain.ImportJob{}, false, nil
	}
	row := j.db.QueryRowContext(ctx, `
SELECT `+jobCols+`
FROM import_jobs WHERE user_id=$1 AND id=$2`, userID, id)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ImportJob{}, false, nil
	}
	if err != nil {
		return domain.ImportJob{}, false, err
	}
	return job, true, nil
}

func (j *Jobs) List(ctx context.Context, userID string, limit int) ([]domain.ImportJob, error) {
	rows, err := j.db.QueryContext(ctx, `
SELECT `+jobCols+`
FROM import_jobs WHERE user_id=$1 ORDER BY created_at DESC, id DESC LIMIT $2`, userID, max(limit, 0))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ImportJob{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

// usageSQL is one index scan over the user's jobs (import_jobs_user_idx) and
// one over their blob ledger (import_job_blobs_user_key_idx), each distinct
// key counted once.
const usageSQL = `
SELECT count(*) FILTER (WHERE status IN ('queued','running')),
       coalesce(sum(archive_bytes), 0)::bigint,
       (SELECT coalesce(sum(b), 0)::bigint FROM (
          SELECT max(bytes) AS b FROM import_job_blobs WHERE user_id=$1 GROUP BY key) k)
FROM import_jobs WHERE user_id=$1`

func (j *Jobs) Usage(ctx context.Context, userID string) (app.JobUsage, error) {
	var u app.JobUsage
	if !isUUID(userID) {
		return u, nil
	}
	err := j.db.QueryRowContext(ctx, usageSQL, userID).Scan(&u.Active, &u.ArchiveBytes, &u.BlobBytes)
	return u, err
}

// Stats counts queued and running jobs (the queue index covers queued ones).
func (j *Jobs) Stats(ctx context.Context) (app.JobStats, error) {
	var st app.JobStats
	err := j.db.QueryRowContext(ctx, `
SELECT count(*) FILTER (WHERE status='queued'), count(*) FILTER (WHERE status='running')
FROM import_jobs WHERE status IN ('queued','running')`).Scan(&st.Queued, &st.Running)
	return st, err
}

// RecordBlobs upserts the entries in one statement (one jsonb parameter),
// only if the job is the user's, inside a transaction that first reads the
// keys the entries replace and afterwards reports those now unreferenced.
func (j *Jobs) RecordBlobs(ctx context.Context, userID, jobID string, blobs []app.JobBlob) ([]string, error) {
	orphaned := []string{}
	if len(blobs) == 0 || !isUUID(userID) || !isUUID(jobID) {
		return orphaned, nil
	}
	type entry struct {
		Path  string `json:"path"`
		Key   string `json:"key"`
		Bytes int64  `json:"bytes"`
	}
	rows := make([]entry, len(blobs))
	for i, b := range blobs {
		rows[i] = entry{Path: clean(b.Path), Key: clean(b.Key), Bytes: b.Bytes}
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() // a no-op after Commit
	// The keys this upsert replaces (an entry of the same path, another key).
	replaced, err := queryStrings(ctx, tx, `
SELECT DISTINCT b.key FROM import_job_blobs b, jsonb_to_recordset($3::jsonb) AS x(path text, key text)
WHERE b.user_id=$1 AND b.job_id=$2 AND b.path=x.path AND b.key<>x.key`, userID, jobID, payload)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO import_job_blobs (job_id, user_id, path, key, bytes)
SELECT j.id, j.user_id, x.path, x.key, x.bytes
FROM import_jobs j, (SELECT DISTINCT ON (path) * FROM jsonb_to_recordset($3::jsonb) AS r(path text, key text, bytes bigint)) AS x
WHERE j.id=$2 AND j.user_id=$1
ON CONFLICT (job_id, path) DO UPDATE SET (key, bytes) = (EXCLUDED.key, EXCLUDED.bytes)`, userID, jobID, payload); err != nil {
		return nil, err
	}
	if len(replaced) > 0 {
		if orphaned, err = queryStrings(ctx, tx, `
SELECT k FROM unnest($2::text[]) AS k
WHERE NOT EXISTS (SELECT 1 FROM import_job_blobs b WHERE b.user_id=$1 AND b.key=k)`, userID, pq.Array(replaced)); err != nil {
			return nil, err
		}
	}
	return orphaned, tx.Commit()
}

// MediaKey looks the path up in the job's ledger, then in the user's most
// recent job that recorded it.
func (j *Jobs) MediaKey(ctx context.Context, userID, jobID, path string) (string, bool, error) {
	if !isUUID(userID) {
		return "", false, nil
	}
	if !isUUID(jobID) {
		jobID = ""
	}
	var key string
	err := j.db.QueryRowContext(ctx, `
SELECT b.key FROM import_job_blobs b JOIN import_jobs j ON j.id = b.job_id
WHERE b.user_id=$1 AND b.path=$3
ORDER BY (b.job_id::text = $2) DESC, j.created_at DESC, j.id DESC
LIMIT 1`, userID, jobID, path).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return key, true, nil
}

// queryStrings runs a query of one text column.
func queryStrings(ctx context.Context, q interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, query string, args ...any,
) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // read errors surface through rows.Err
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ReleaseLegacyMedia clears the job's media_by_path flag (0025) and returns
// the paths in its ledger that no event resolves to by path any more. The
// paths still shown are computed once per call (all of the user's media that
// its own import's ledger does not cover), not once per candidate.
func (j *Jobs) ReleaseLegacyMedia(ctx context.Context, userID, jobID string) ([]string, error) {
	paths := []string{}
	if !isUUID(userID) || !isUUID(jobID) {
		return paths, nil
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() // a no-op after Commit
	var flagged bool
	err = tx.QueryRowContext(ctx, `
UPDATE import_jobs SET media_by_path=false WHERE id=$2 AND user_id=$1 AND media_by_path
RETURNING true`, userID, jobID).Scan(&flagged)
	if errors.Is(err, sql.ErrNoRows) {
		return paths, nil
	}
	if err != nil {
		return nil, err
	}
	if paths, err = queryStrings(ctx, tx, `
WITH shown_by_path AS (
    SELECT DISTINCT e.source_id || '/' || m.blob_key AS path
    FROM events e JOIN media m ON m.event_id=e.id
    WHERE e.user_id=$1
      AND NOT EXISTS (SELECT 1 FROM import_job_blobs o
                      WHERE o.job_id=e.import_job_id AND o.path=e.source_id || '/' || m.blob_key)
)
SELECT b.path FROM import_job_blobs b
WHERE b.job_id=$2
  AND NOT EXISTS (SELECT 1 FROM import_job_blobs k WHERE k.user_id=$1 AND k.key=b.path)
  AND NOT EXISTS (SELECT 1 FROM shown_by_path s WHERE s.path=b.path)
ORDER BY b.path`, userID, jobID); err != nil {
		return nil, err
	}
	return paths, tx.Commit()
}

// ExclusiveBlobs lists the job's keys no other job of the user recorded.
func (j *Jobs) ExclusiveBlobs(ctx context.Context, userID, jobID string) ([]string, error) {
	keys := []string{}
	if !isUUID(userID) || !isUUID(jobID) {
		return keys, nil
	}
	rows, err := j.db.QueryContext(ctx, `
SELECT DISTINCT b.key FROM import_job_blobs b
WHERE b.user_id=$1 AND b.job_id=$2
  AND NOT EXISTS (SELECT 1 FROM import_job_blobs o WHERE o.user_id=$1 AND o.key=b.key AND o.job_id<>$2)
ORDER BY b.key`, userID, jobID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // read errors surface through rows.Err
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (j *Jobs) StagedRefs(ctx context.Context) ([]string, error) {
	rows, err := j.db.QueryContext(ctx,
		`SELECT ref FROM import_jobs, jsonb_array_elements_text(zip_paths) AS ref`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := []string{}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// Claim picks the next job in the fair order of app.Jobs — the user served
// least recently first (users.last_import_claim, NULL = never), then the older
// job — locks it with FOR UPDATE OF j SKIP LOCKED (a job another worker is
// claiming right now is skipped, never shared), marks it running and stamps the
// user with the next value of import_claim_seq; all in one statement.
func (j *Jobs) Claim(ctx context.Context) (domain.ImportJob, bool, error) {
	row := j.db.QueryRowContext(ctx, `
WITH next AS (
  SELECT j.id FROM import_jobs j JOIN users u ON u.id = j.user_id
  WHERE j.status='queued'
  ORDER BY u.last_import_claim NULLS FIRST, j.created_at, j.id
  FOR UPDATE OF j SKIP LOCKED
  LIMIT 1
), claimed AS (
  UPDATE import_jobs SET status='running', attempts=attempts+1, cancel_requested=false,
         saved=0, bytes_done=0, bytes_total=0, updated_at=now()
  WHERE id = (SELECT id FROM next)
  RETURNING `+jobCols+`
), served AS (
  UPDATE users SET last_import_claim = nextval('import_claim_seq')
  WHERE id = (SELECT user_id FROM claimed)
)
SELECT `+jobCols+` FROM claimed`)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ImportJob{}, false, nil
	}
	if err != nil {
		return domain.ImportJob{}, false, err
	}
	return job, true, nil
}

// Complete records the outcome and resets attempts: reaching this point means
// the run did not crash.
func (j *Jobs) Complete(ctx context.Context, id string, status domain.JobStatus, saved int, cov *domain.Coverage, errMsg string) error {
	var covJSON any
	if cov != nil {
		b, _ := json.Marshal(cov)
		covJSON = b
	}
	_, err := j.db.ExecContext(ctx, `
UPDATE import_jobs SET status=$2, attempts=0, saved=$3, coverage=$4, error=$5, cancel_requested=false, updated_at=now()
WHERE id=$1`, id, string(status), saved, covJSON, nullStr(errMsg))
	return err
}

func (j *Jobs) Progress(ctx context.Context, id string, saved int, bytesDone, bytesTotal int64) error {
	_, err := j.db.ExecContext(ctx, `
UPDATE import_jobs SET saved=$2, bytes_done=$3, bytes_total=$4, updated_at=now()
WHERE id=$1 AND status='running'`, id, saved, bytesDone, bytesTotal)
	return err
}

func (j *Jobs) Requeue(ctx context.Context, userID, id string) (domain.ImportJob, bool, error) {
	if !isUUID(id) {
		return domain.ImportJob{}, false, nil
	}
	// The WHERE clause is domain.ImportJob.CanRequeue, evaluated atomically.
	// attempts is deliberately kept: only a recorded outcome resets it.
	row := j.db.QueryRowContext(ctx, `
UPDATE import_jobs SET status='queued', error=NULL, cancel_requested=false, updated_at=now()
WHERE user_id=$1 AND id=$2 AND status IN ('done','error')
RETURNING `+jobCols, userID, id)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		// Either no such job, or it is not finished.
		cur, ok, err := j.Get(ctx, userID, id)
		if err != nil || !ok {
			return domain.ImportJob{}, false, err
		}
		return cur, true, app.ErrJobBusy
	}
	if err != nil {
		return domain.ImportJob{}, false, err
	}
	return job, true, nil
}

// Delete removes the job unless it is running. The status guard (the SQL form
// of domain.ImportJob.CanDelete) is part of the DELETE itself, so a worker
// claiming the job concurrently either wins (busy) or loses (row gone before
// its Claim sees it) — never both.
func (j *Jobs) Delete(ctx context.Context, userID, id string) (app.JobDeletion, error) {
	if !isUUID(id) {
		return app.JobNotFound, nil
	}
	res, err := j.db.ExecContext(ctx,
		`DELETE FROM import_jobs WHERE user_id=$1 AND id=$2 AND status <> 'running'`, userID, id)
	if err != nil {
		return app.JobNotFound, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return app.JobDeleted, nil
	}
	// Nothing deleted: tell "no such job" from "running".
	if _, ok, err := j.Get(ctx, userID, id); err != nil || !ok {
		return app.JobNotFound, err
	}
	return app.JobBusy, nil
}

// RequestCancel is one statement guarded by the status: a queued job is
// failed as cancelled, a running one flagged. Nothing changes otherwise.
func (j *Jobs) RequestCancel(ctx context.Context, userID, id string) (domain.ImportJob, bool, error) {
	if !isUUID(id) {
		return domain.ImportJob{}, false, nil
	}
	// SET expressions see the row as it was, so `status='running'` below is
	// the old status.
	row := j.db.QueryRowContext(ctx, `
UPDATE import_jobs SET
  status = CASE WHEN status='queued' THEN 'error' ELSE status END,
  error = CASE WHEN status='queued' THEN $3 ELSE error END,
  cancel_requested = (status='running'),
  updated_at = now()
WHERE user_id=$1 AND id=$2 AND status IN ('queued','running')
RETURNING `+jobCols, userID, id, domain.JobCancelledMessage)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		cur, ok, err := j.Get(ctx, userID, id)
		if err != nil || !ok {
			return domain.ImportJob{}, false, err
		}
		return cur, true, app.ErrJobFinished
	}
	if err != nil {
		return domain.ImportJob{}, false, err
	}
	return job, true, nil
}

// Recover requeues jobs orphaned in 'running' by a crash; jobs already claimed
// maxAttempts times, and jobs their owner asked to cancel, are failed instead
// (the WHERE clauses are the SQL form of domain.ImportJob.Exhausted). Run at
// startup before the worker.
func (j *Jobs) Recover(ctx context.Context, maxAttempts int) (requeued, failed int, err error) {
	res, err := j.db.ExecContext(ctx, `
UPDATE import_jobs SET status='error', error=$1, cancel_requested=false, attempts=0, updated_at=now()
WHERE status='running' AND cancel_requested`, domain.JobCancelledMessage)
	if err != nil {
		return 0, 0, err
	}
	if n, err := res.RowsAffected(); err == nil {
		failed = int(n)
	}
	res, err = j.db.ExecContext(ctx, `
UPDATE import_jobs SET status='queued', updated_at=now()
WHERE status='running' AND attempts < $1`, maxAttempts)
	if err != nil {
		return 0, 0, err
	}
	if n, err := res.RowsAffected(); err == nil {
		requeued = int(n)
	}
	res, err = j.db.ExecContext(ctx, `
UPDATE import_jobs SET status='error', error=$2, updated_at=now()
WHERE status='running' AND attempts >= $1`, maxAttempts, domain.JobGaveUpMessage)
	if err != nil {
		return requeued, 0, err
	}
	if n, err := res.RowsAffected(); err == nil {
		failed += int(n)
	}
	return requeued, failed, nil
}

func scanJob(sc scanner) (domain.ImportJob, error) {
	var (
		job     domain.ImportJob
		status  string
		covJSON []byte
		errStr  sql.NullString
		zips    []byte
		names   []byte
	)
	if err := sc.Scan(&job.ID, &job.UserID, &job.Source, &job.Label, &zips, &names, &job.ArchiveBytes, &status,
		&job.Attempts, &job.Saved, &job.BytesDone, &job.BytesTotal, &covJSON, &errStr, &job.CancelRequested,
		&job.CreatedAt, &job.UpdatedAt); err != nil {
		return domain.ImportJob{}, err
	}
	_ = json.Unmarshal(zips, &job.ZipPaths)
	_ = json.Unmarshal(names, &job.PartNames)
	if len(job.PartNames) == 0 {
		job.PartNames = nil
	}
	job.Parts = len(job.ZipPaths)
	job.Status = domain.JobStatus(status)
	job.Error = errStr.String
	job.CreatedAt = job.CreatedAt.UTC()
	job.UpdatedAt = job.UpdatedAt.UTC()
	if len(covJSON) > 0 && string(covJSON) != "null" {
		var c domain.Coverage
		if json.Unmarshal(covJSON, &c) == nil {
			job.Coverage = &c
		}
	}
	return job, nil
}
