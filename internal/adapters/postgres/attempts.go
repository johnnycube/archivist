package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/johnnycube/archivist/internal/app"
)

// Attempts is a PostgreSQL app.AttemptLimiter over auth_failures (migration
// 0011): one row per failure, so the throttle holds across processes and
// restarts. Keys arrive hashed; time is the application's clock.
type Attempts struct{ db *sql.DB }

func NewAttempts(db *sql.DB) Attempts { return Attempts{db: db} }

var _ app.AttemptLimiter = Attempts{}

func (p Attempts) Failures(ctx context.Context, key string, now time.Time, window time.Duration) (int, time.Time, error) {
	var (
		n      int
		oldest sql.NullTime
	)
	err := p.db.QueryRowContext(ctx,
		`SELECT count(*), min(failed_at) FROM auth_failures WHERE key=$1 AND failed_at > $2 AND failed_at <= $3`,
		key, now.Add(-window).UTC(), now.UTC()).Scan(&n, &oldest)
	if err != nil {
		return 0, time.Time{}, err
	}
	return n, oldest.Time.UTC(), nil
}

// RecordFailure prunes on write (one statement): whatever is older than
// app.AttemptRetention goes, of any key, so the table stays small without a
// janitor.
func (p Attempts) RecordFailure(ctx context.Context, key string, now time.Time) error {
	_, err := p.db.ExecContext(ctx, `
WITH pruned AS (DELETE FROM auth_failures WHERE failed_at <= $3)
INSERT INTO auth_failures (key, failed_at) VALUES ($1, $2)`,
		key, now.UTC(), now.Add(-app.AttemptRetention).UTC())
	return err
}

func (p Attempts) Reset(ctx context.Context, key string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM auth_failures WHERE key=$1`, key)
	return err
}
