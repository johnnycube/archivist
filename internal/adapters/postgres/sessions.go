package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/johnnycube/archivist/internal/app"
)

// Sessions is a PostgreSQL app.Sessions. It stores what the application layer
// gives it — the SHA-256 of the opaque token, never the token — so a database
// leak does not leak live sessions. Time comes from the application's clock,
// not from now(), so expiry is the same rule in every store.
type Sessions struct {
	// IdleTTL ends sessions no Lookup has used for that long (<= 0: never).
	// Set it before the store is used.
	IdleTTL time.Duration

	db  *sql.DB
	ttl time.Duration
}

// NewSessions wraps a pool as a session store with the given TTL.
func NewSessions(db *sql.DB, ttl time.Duration) *Sessions {
	if ttl <= 0 {
		ttl = app.DefaultSessionTTL
	}
	return &Sessions{db: db, ttl: ttl}
}

var _ app.Sessions = (*Sessions)(nil)

// idleCutoff is the last_seen_at a session must be NEWER than to be live; with
// no idle timeout that is a time before every session.
func (s *Sessions) idleCutoff(now time.Time) time.Time {
	if s.IdleTTL <= 0 {
		return time.Time{}
	}
	return now.Add(-s.IdleTTL).UTC()
}

func (s *Sessions) Create(ctx context.Context, tokenHash, userID string, now time.Time) error {
	return s.CreateReplacing(ctx, tokenHash, userID, "", now)
}

// CreateReplacing inserts the session and revokes the previous one — if it is
// the same user's — in a single statement, hence atomically.
func (s *Sessions) CreateReplacing(ctx context.Context, tokenHash, userID, previousHash string, now time.Time) error {
	// Opportunistic sweep keeps the table from accumulating dead rows.
	_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= $1 OR last_seen_at <= $2`,
		now.UTC(), s.idleCutoff(now))
	_, err := s.db.ExecContext(ctx, `
WITH revoked AS (
  DELETE FROM sessions WHERE token_hash = $5 AND user_id = $2 AND token_hash <> $1
)
INSERT INTO sessions (token_hash, user_id, created_at, expires_at, last_seen_at) VALUES ($1, $2, $3, $4, $3)`,
		tokenHash, userID, now.UTC(), now.Add(s.ttl).UTC(), previousHash)
	return err
}

// Lookup is a read. Only when the recorded use is app.SessionTouchInterval old
// or older does it write the new one — at most one write per session per
// interval, however many requests the session authenticates.
func (s *Sessions) Lookup(ctx context.Context, tokenHash string, now time.Time) (string, bool, error) {
	var (
		userID   string
		lastSeen time.Time
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, last_seen_at FROM sessions WHERE token_hash=$1 AND expires_at > $2 AND last_seen_at > $3`,
		tokenHash, now.UTC(), s.idleCutoff(now)).Scan(&userID, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if now.Sub(lastSeen) >= app.SessionTouchInterval {
		// The guard keeps a concurrent, later touch from being overwritten.
		if _, err := s.db.ExecContext(ctx,
			`UPDATE sessions SET last_seen_at=$2 WHERE token_hash=$1 AND last_seen_at < $2`,
			tokenHash, now.UTC()); err != nil {
			return "", false, err
		}
	}
	return userID, true, nil
}

func (s *Sessions) Revoke(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=$1`, tokenHash)
	return err
}

func (s *Sessions) RevokeUser(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID)
	return err
}
