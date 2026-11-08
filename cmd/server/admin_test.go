package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/crypto"
	"github.com/johnnycube/archivist/internal/adapters/memory"
	"github.com/johnnycube/archivist/internal/app"
)

func adminFor(t *testing.T) (app.AdminService, *app.App) {
	t.Helper()
	store := memory.NewStore(memory.Options{SessionTTL: time.Hour})
	a := app.New(app.Deps{
		Users: store.Users, Sessions: store.Sessions, Accounts: store.Accounts, Events: store.Events,
		Jobs: store.Jobs, Hasher: crypto.Hasher{}, SignupDisabled: true,
	})
	return a.Admin, a
}

func run(t *testing.T, admin app.AdminService, stdin string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = adminCommand(context.Background(), admin, args, strings.NewReader(stdin), &o, &e)
	return code, o.String(), e.String()
}

func TestAdminCLI(t *testing.T) {
	admin, a := adminFor(t)
	ctx := context.Background()

	if code, out, _ := run(t, admin, ""); code != 0 || !strings.Contains(out, "create-user") {
		t.Errorf("no command = %d %q; want the usage", code, out)
	}
	if code, _, errOut := run(t, admin, "", "frobnicate"); code != 2 || !strings.Contains(errOut, "unknown command") {
		t.Errorf("unknown command = %d %q", code, errOut)
	}
	if code, _, errOut := run(t, admin, "", "create-user"); code != 2 || !strings.Contains(errOut, "-email") {
		t.Errorf("create-user without -email = %d %q", code, errOut)
	}

	// A generated password is printed once and works.
	code, out, _ := run(t, admin, "", "create-user", "-email", "owner@example.com")
	if code != 0 || !strings.Contains(out, "password: ") {
		t.Fatalf("create-user = %d %q", code, out)
	}
	pw := strings.Fields(out[strings.Index(out, "password: ")+len("password: "):])[0]
	if _, _, err := a.Auth.LogInGuarded(ctx, "owner@example.com", pw, "", ""); err != nil {
		t.Errorf("login with the printed password: %v", err)
	}
	// From stdin, nothing is printed.
	if code, out, _ := run(t, admin, "a-chosen-passphrase\n", "reset-password", "-email", "owner@example.com", "-password-stdin"); code != 0 || strings.Contains(out, "\npassword: ") {
		t.Errorf("reset-password from stdin = %d %q", code, out)
	}
	if _, _, err := a.Auth.LogInGuarded(ctx, "owner@example.com", "a-chosen-passphrase", "", ""); err != nil {
		t.Errorf("login with the password from stdin: %v", err)
	}
	if code, _, errOut := run(t, admin, "", "create-user", "-email", "owner@example.com"); code != 1 || !strings.Contains(errOut, "exists already") {
		t.Errorf("duplicate create-user = %d %q", code, errOut)
	}
	if code, _, errOut := run(t, admin, "x\n", "reset-password", "-email", "owner@example.com", "-password-stdin"); code != 1 || !strings.Contains(errOut, "password") {
		t.Errorf("weak password = %d %q", code, errOut)
	}

	if code, out, _ := run(t, admin, "", "list-users"); code != 0 || !strings.Contains(out, "owner@example.com") || !strings.Contains(out, "STORED") {
		t.Errorf("list-users = %d %q", code, out)
	}
	if code, out, _ := run(t, admin, "", "list-imports", "-email", "owner@example.com"); code != 0 || !strings.HasPrefix(out, "ID") {
		t.Errorf("list-imports = %d %q", code, out)
	}
	if code, _, errOut := run(t, admin, "", "cancel-import", "-email", "owner@example.com", "-id", "nope"); code != 1 || !strings.Contains(errOut, "no such import") {
		t.Errorf("cancel-import of nothing = %d %q", code, errOut)
	}
	if code, _, errOut := run(t, admin, "", "delete-user", "-email", "owner@example.com"); code != 2 || !strings.Contains(errOut, "-yes") {
		t.Errorf("delete-user without -yes = %d %q", code, errOut)
	}
	if code, _, _ := run(t, admin, "", "delete-user", "-email", "owner@example.com", "-yes"); code != 0 {
		t.Errorf("delete-user = %d", code)
	}
	if code, _, errOut := run(t, admin, "", "delete-user", "-email", "owner@example.com", "-yes"); code != 1 || !strings.Contains(errOut, "no account") {
		t.Errorf("delete-user of a deleted account = %d %q", code, errOut)
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 50 << 30: "50.0 GiB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
