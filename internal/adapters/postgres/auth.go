package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"

	"github.com/lib/pq"
)

// Users is a PostgreSQL app.Users.
type Users struct{ db *sql.DB }

func NewUsers(db *sql.DB) Users { return Users{db: db} }

var _ app.Users = Users{}

func (p Users) Create(ctx context.Context, email, hash string) (domain.User, error) {
	email = domain.NormalizeEmail(email)
	var id string
	err := p.db.QueryRowContext(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1,$2) RETURNING id`, email, hash).Scan(&id)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code.Name() == "unique_violation" {
			return domain.User{}, app.ErrEmailTaken
		}
		return domain.User{}, err
	}
	return domain.User{ID: id, Email: email, PasswordHash: hash}, nil
}

func (p Users) ByEmail(ctx context.Context, email string) (domain.User, bool, error) {
	return p.scan(ctx, `SELECT id, email, password_hash, email_verified FROM users WHERE email=$1`, domain.NormalizeEmail(email))
}

func (p Users) ByID(ctx context.Context, id string) (domain.User, bool, error) {
	if !isUUID(id) {
		return domain.User{}, false, nil
	}
	return p.scan(ctx, `SELECT id, email, password_hash, email_verified FROM users WHERE id=$1`, id)
}

func (p Users) UpdatePassword(ctx context.Context, id, hash string) error {
	if !isUUID(id) {
		return nil
	}
	_, err := p.db.ExecContext(ctx, `UPDATE users SET password_hash=$2 WHERE id=$1`, id, hash)
	return err
}

// Delete removes the user; accounts, events, jobs, and sessions cascade via
// their foreign keys (see migrations).
func (p Users) Delete(ctx context.Context, id string) error {
	if !isUUID(id) {
		return nil
	}
	_, err := p.db.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, id)
	return err
}

// ByIdentity finds the user an external identity is linked to.
func (p Users) ByIdentity(ctx context.Context, provider, subject string) (domain.User, bool, error) {
	var u domain.User
	err := p.db.QueryRowContext(ctx, `
SELECT u.id, u.email, u.password_hash, u.email_verified FROM user_identities i JOIN users u ON u.id = i.user_id
WHERE i.provider = $1 AND i.subject = $2`, provider, subject).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.EmailVerified)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, false, nil
	}
	if err != nil {
		return domain.User{}, false, err
	}
	return u, true, nil
}

// LinkIdentity is one statement on the primary key (provider, subject): the
// conflict arm updates only when the row already belongs to this user, so a
// missing RETURNING row means the identity is somebody else's.
func (p Users) LinkIdentity(ctx context.Context, userID, provider, subject, email string) error {
	if !isUUID(userID) {
		return fmt.Errorf("link identity to user %q: %w", userID, app.ErrNotFound)
	}
	var owner string
	err := p.db.QueryRowContext(ctx, `
INSERT INTO user_identities (provider, subject, user_id, email) VALUES ($1, $2, $3, NULLIF($4, ''))
ON CONFLICT (provider, subject) DO UPDATE SET email = EXCLUDED.email
WHERE user_identities.user_id = EXCLUDED.user_id
RETURNING user_id`, provider, subject, userID, email).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return app.ErrIdentityTaken
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code.Name() == "foreign_key_violation" {
		return fmt.Errorf("link identity to user %q: %w", userID, app.ErrNotFound)
	}
	return err
}

// LinkedProviders lists the providers the user has an identity at.
func (p Users) LinkedProviders(ctx context.Context, userID string) ([]string, error) {
	out := []string{}
	if !isUUID(userID) {
		return out, nil
	}
	rows, err := p.db.QueryContext(ctx,
		`SELECT DISTINCT provider FROM user_identities WHERE user_id = $1 ORDER BY provider`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // read errors surface through rows.Err
	for rows.Next() {
		var provider string
		if err := rows.Scan(&provider); err != nil {
			return nil, err
		}
		out = append(out, provider)
	}
	return out, rows.Err()
}

// List returns every user, ordered by email.
func (p Users) List(ctx context.Context) ([]domain.User, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id, email, password_hash, email_verified FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // read errors surface through rows.Err
	out := []domain.User{}
	for rows.Next() {
		var u domain.User
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.EmailVerified); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// MarkEmailVerified records that a provider vouched for the user's email.
func (p Users) MarkEmailVerified(ctx context.Context, id string) error {
	if !isUUID(id) {
		return nil
	}
	_, err := p.db.ExecContext(ctx, `UPDATE users SET email_verified = true WHERE id = $1`, id)
	return err
}

func (p Users) scan(ctx context.Context, q, arg string) (domain.User, bool, error) {
	var u domain.User
	err := p.db.QueryRowContext(ctx, q, arg).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.EmailVerified)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, false, nil
	}
	if err != nil {
		return domain.User{}, false, err
	}
	return u, true, nil
}

// Accounts is a PostgreSQL app.Accounts.
type Accounts struct{ db *sql.DB }

func NewAccounts(db *sql.DB) Accounts { return Accounts{db: db} }

var _ app.Accounts = Accounts{}

// Ensure is one statement on the unique index accounts_user_source_label_key
// (migration 0007), so concurrent callers all get the same account. The
// conflict arm is a no-op UPDATE rather than DO NOTHING on purpose: DO NOTHING
// returns no row for the loser, and a follow-up SELECT in the same statement
// cannot see a row a concurrent transaction has just committed (it runs on the
// statement's snapshot). DO UPDATE waits for the winner and returns its row.
func (p Accounts) Ensure(ctx context.Context, userID, source, label string) (string, error) {
	if label == "" {
		label = "default"
	}
	var id string
	err := p.db.QueryRowContext(ctx, `
INSERT INTO accounts (user_id, source_id, label) VALUES ($1,$2,$3)
ON CONFLICT (user_id, source_id, label) DO UPDATE SET label = EXCLUDED.label
RETURNING id`, userID, source, label).Scan(&id)
	return id, err
}
