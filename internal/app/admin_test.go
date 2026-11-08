package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

func TestAdminCreatesUsersWhileSignupIsClosed(t *testing.T) {
	ctx := context.Background()
	deps := importDeps(t, nil)
	deps.SignupDisabled = true
	a := app.New(deps)

	u, err := a.Admin.CreateUser(ctx, " Owner@Example.com ", "a-long-passphrase")
	if err != nil || u.Email != "owner@example.com" {
		t.Fatalf("CreateUser = %+v, %v", u, err)
	}
	if _, _, err := a.Auth.LogInGuarded(ctx, "owner@example.com", "a-long-passphrase", "", ""); err != nil {
		t.Errorf("login with the created account: %v", err)
	}
	for _, tc := range []struct {
		email, password string
		want            error
	}{
		{"owner@example.com", "another-passphrase", app.ErrEmailTaken},
		{"not an address", "a-long-passphrase", app.ErrInvalidEmail},
		{"Name <x@example.com>", "a-long-passphrase", app.ErrInvalidEmail},
		{"x@example.com", "short", app.ErrWeakPassword},
	} {
		if _, err := a.Admin.CreateUser(ctx, tc.email, tc.password); !errors.Is(err, tc.want) {
			t.Errorf("CreateUser(%q) = %v; want %v", tc.email, err, tc.want)
		}
	}
}

func TestAdminResetsPasswordsAndEndsSessions(t *testing.T) {
	ctx := context.Background()
	a := newImportApp(t, nil)
	if _, err := a.Admin.CreateUser(ctx, "me@example.com", "first-passphrase"); err != nil {
		t.Fatal(err)
	}
	_, token, err := a.Auth.LogInGuarded(ctx, "me@example.com", "first-passphrase", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Admin.ResetPassword(ctx, "ME@example.com", "second-passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := a.Auth.Authenticate(ctx, token); ok {
		t.Error("a session survived the password reset")
	}
	if _, _, err := a.Auth.LogInGuarded(ctx, "me@example.com", "second-passphrase", "", ""); err != nil {
		t.Errorf("login with the new password: %v", err)
	}
	if err := a.Admin.ResetPassword(ctx, "nobody@example.com", "second-passphrase"); !errors.Is(err, app.ErrNoSuchUser) || !errors.Is(err, app.ErrNotFound) {
		t.Errorf("reset of an unknown account = %v; want ErrNoSuchUser", err)
	}
	if err := a.Admin.ResetPassword(ctx, "me@example.com", "x"); !errors.Is(err, app.ErrWeakPassword) {
		t.Errorf("reset to a weak password = %v", err)
	}
}

func TestAdminListsImportsCancelsAndDeletes(t *testing.T) {
	ctx := context.Background()
	a := newImportApp(t, newMemBlobs())
	u, err := a.Admin.CreateUser(ctx, "me@example.com", "a-long-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	job, err := a.Import.Enqueue(ctx, app.ImportSpec{UserID: u.ID, Source: "reddit", Parts: stageZips(t, a, u.ID, buildZip(t))})
	if err != nil {
		t.Fatal(err)
	}
	users, err := a.Admin.Users(ctx)
	if err != nil || len(users) != 1 || users[0].User.ID != u.ID || users[0].Usage.Active != 1 || users[0].LinkedProviders == nil {
		t.Fatalf("Users = %+v, %v", users, err)
	}
	if err := a.Admin.DeleteUser(ctx, "me@example.com"); !errors.Is(err, app.ErrJobBusy) {
		t.Errorf("deleting a user with a queued import = %v; want ErrJobBusy", err)
	}
	jobs, err := a.Admin.Imports(ctx, "me@example.com", 10)
	if err != nil || len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Fatalf("Imports = %+v, %v", jobs, err)
	}
	if j, err := a.Admin.CancelImport(ctx, "me@example.com", job.ID); err != nil || j.Status != domain.JobError {
		t.Errorf("CancelImport = %+v, %v", j, err)
	}
	if err := a.Admin.DeleteUser(ctx, "me@example.com"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if users, _ := a.Admin.Users(ctx); len(users) != 0 {
		t.Errorf("users after deletion = %+v", users)
	}
	if err := a.Admin.DeleteUser(ctx, "me@example.com"); !errors.Is(err, app.ErrNoSuchUser) {
		t.Errorf("deleting again = %v; want ErrNoSuchUser", err)
	}
}
