package app

import (
	"context"
	"fmt"

	"github.com/johnnycube/archivist/internal/domain"
)

// AdminService is the operator's use case — what `archivist admin` drives.
// There is no session and no transport: whoever can run the server binary
// against its database is the operator. It is how the first account comes
// into being when signup is closed (ALLOW_SIGNUP=0, the default), and how a
// forgotten password is reset on a server that sends no email.
type AdminService struct {
	Auth   AuthService
	Import ImportService
}

// UserSummary is one line of the operator's user list.
type UserSummary struct {
	User domain.User
	// LinkedProviders are the identity providers the user has logged in
	// through (configured or not).
	LinkedProviders []string
	Usage           JobUsage
}

// ErrNoSuchUser is returned when no account has the email.
var ErrNoSuchUser = fmt.Errorf("%w: no account with that email", ErrNotFound)

// CreateUser creates an account with a password, whatever ALLOW_SIGNUP says.
// The password policy (CheckPassword) applies.
func (s AdminService) CreateUser(ctx context.Context, email, password string) (domain.User, error) {
	email = domain.NormalizeEmail(email)
	if !validEmail(email) {
		return domain.User{}, ErrInvalidEmail
	}
	if err := CheckPassword(email, password); err != nil {
		return domain.User{}, err
	}
	hash, err := s.Auth.Hasher.Hash(password)
	if err != nil {
		return domain.User{}, err
	}
	return s.Auth.Users.Create(ctx, email, hash)
}

// ResetPassword sets the account's password and ends all its sessions.
func (s AdminService) ResetPassword(ctx context.Context, email, password string) error {
	u, err := s.user(ctx, email)
	if err != nil {
		return err
	}
	if err := CheckPassword(u.Email, password); err != nil {
		return err
	}
	hash, err := s.Auth.Hasher.Hash(password)
	if err != nil {
		return err
	}
	if err := s.Auth.Users.UpdatePassword(ctx, u.ID, hash); err != nil {
		return err
	}
	return s.Auth.Sessions.RevokeUser(ctx, u.ID)
}

// Users lists every account with what it holds.
func (s AdminService) Users(ctx context.Context) ([]UserSummary, error) {
	users, err := s.Auth.Users.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]UserSummary, 0, len(users))
	for _, u := range users {
		linked, err := s.Auth.Users.LinkedProviders(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		usage, err := s.Import.Jobs.Usage(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, UserSummary{User: u, LinkedProviders: linked, Usage: usage})
	}
	return out, nil
}

// DeleteUser deletes the account and all its data, as DeleteAccount does
// without the password; refused (ErrJobBusy) while one of its imports is
// queued or running — cancel those first.
func (s AdminService) DeleteUser(ctx context.Context, email string) error {
	u, err := s.user(ctx, email)
	if err != nil {
		return err
	}
	return s.Auth.deleteUser(ctx, u.ID)
}

// Imports lists the account's most recent imports.
func (s AdminService) Imports(ctx context.Context, email string, limit int) ([]domain.ImportJob, error) {
	u, err := s.user(ctx, email)
	if err != nil {
		return nil, err
	}
	return s.Import.RecentJobs(ctx, u.ID, limit)
}

// CancelImport cancels one of the account's imports (ImportService.CancelJob).
func (s AdminService) CancelImport(ctx context.Context, email, jobID string) (domain.ImportJob, error) {
	u, err := s.user(ctx, email)
	if err != nil {
		return domain.ImportJob{}, err
	}
	return s.Import.CancelJob(ctx, u.ID, jobID)
}

func (s AdminService) user(ctx context.Context, email string) (domain.User, error) {
	u, ok, err := s.Auth.Users.ByEmail(ctx, email)
	if err == nil && !ok {
		err = ErrNoSuchUser
	}
	return u, err
}
