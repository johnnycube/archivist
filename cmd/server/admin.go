package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/johnnycube/archivist/internal/app"
)

// adminUsage is what `archivist admin` prints for help or a wrong command.
const adminUsage = `usage: archivist admin <command> [flags]

Operator commands against the server's database (the same DATABASE_URL,
DATA_DIR and BLOB_S3_* settings as the server; in Docker:
docker compose exec app /app/archivist admin ...).

  create-user    -email E [-password-stdin]   create an account (also while signup is closed)
  reset-password -email E [-password-stdin]   set a new password, end the account's sessions
  list-users                                  every account, what it stores, its imports in progress
  delete-user    -email E -yes                delete an account and all its data
  list-imports   -email E [-n 20]             an account's recent imports
  cancel-import  -email E -id JOB             cancel a queued or running import

Without -password-stdin a random password is generated and printed once.
`

// runAdmin runs an admin command against the configured database and returns
// the process exit code.
func runAdmin(args []string) int {
	cfg, err := loadConfig()
	if err != nil {
		say(os.Stderr, "invalid configuration: %v\n", err)
		return 2
	}
	if cfg.DatabaseURL == "" {
		say(os.Stderr, "admin: DATABASE_URL is not set — the admin commands work on the server's database\n")
		return 2
	}
	setupLogging("text")
	deps, _, _ := wire(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return adminCommand(ctx, app.New(deps).Admin, args, os.Stdin, os.Stdout, os.Stderr)
}

// adminOpts are the flags of every admin command (each uses some of them).
type adminOpts struct {
	cmd, email, jobID string
	fromStdin, yes    bool
	limit             int
	stdin             io.Reader
	stdout, stderr    io.Writer
}

// errUsage is a command line the command cannot run with; the message has
// been printed already.
var errUsage = errors.New("usage")

// adminCommands are the admin commands by name.
var adminCommands = map[string]func(context.Context, app.AdminService, adminOpts) error{
	"create-user":    setPassword,
	"reset-password": setPassword,
	"list-users":     func(ctx context.Context, a app.AdminService, o adminOpts) error { return listUsers(ctx, a, o.stdout) },
	"delete-user":    deleteUser,
	"list-imports":   listImportsCmd,
	"cancel-import":  cancelImport,
}

// adminCommand parses and runs one admin command (testable without a
// database: any app.AdminService will do) and returns the exit code.
func adminCommand(ctx context.Context, admin app.AdminService, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		say(stdout, "%s", adminUsage)
		return 0
	}
	run, known := adminCommands[args[0]]
	if !known {
		say(stderr, "unknown command %q\n\n%s", args[0], adminUsage)
		return 2
	}
	o := adminOpts{cmd: args[0], stdin: stdin, stdout: stdout, stderr: stderr}
	fs := flag.NewFlagSet("archivist admin "+o.cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.email, "email", "", "the account's email")
	fs.BoolVar(&o.fromStdin, "password-stdin", false, "read the password from the first line of standard input")
	fs.BoolVar(&o.yes, "yes", false, "confirm the deletion")
	fs.IntVar(&o.limit, "n", 20, "how many imports to list")
	fs.StringVar(&o.jobID, "id", "", "the import's id (list-imports shows it)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	switch err := run(ctx, admin, o); {
	case errors.Is(err, errUsage):
		return 2
	case err != nil:
		say(stderr, "%s: %s\n", o.cmd, adminError(err))
		return 1
	}
	return 0
}

// say writes to the terminal; a failed write has nowhere better to go.
func say(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

// requireEmail refuses a command line without -email.
func (o adminOpts) requireEmail() error {
	if o.email == "" {
		say(o.stderr, "%s: -email is required\n", o.cmd)
		return errUsage
	}
	return nil
}

func setPassword(ctx context.Context, admin app.AdminService, o adminOpts) error {
	if err := o.requireEmail(); err != nil {
		return err
	}
	password, generated, err := readOrGeneratePassword(o.fromStdin, o.stdin)
	if err != nil {
		return err
	}
	if o.cmd == "create-user" {
		_, err = admin.CreateUser(ctx, o.email, password)
	} else {
		err = admin.ResetPassword(ctx, o.email, password)
	}
	if err != nil {
		return err
	}
	say(o.stdout, "%s: done for %s\n", o.cmd, o.email)
	if generated {
		say(o.stdout, "password: %s\n(shown once; the user can change it on the account page)\n", password)
	}
	return nil
}

func deleteUser(ctx context.Context, admin app.AdminService, o adminOpts) error {
	if err := o.requireEmail(); err != nil {
		return err
	}
	if !o.yes {
		say(o.stderr, "delete-user: deletes the account and ALL its data; add -yes to confirm\n")
		return errUsage
	}
	if err := admin.DeleteUser(ctx, o.email); err != nil {
		return err
	}
	say(o.stdout, "deleted %s and all its data\n", o.email)
	return nil
}

func listImportsCmd(ctx context.Context, admin app.AdminService, o adminOpts) error {
	if err := o.requireEmail(); err != nil {
		return err
	}
	return listImports(ctx, admin, o.email, o.limit, o.stdout)
}

func cancelImport(ctx context.Context, admin app.AdminService, o adminOpts) error {
	if o.email == "" || o.jobID == "" {
		say(o.stderr, "cancel-import: -email and -id are required\n")
		return errUsage
	}
	job, err := admin.CancelImport(ctx, o.email, o.jobID)
	if err != nil {
		return err
	}
	state := "cancelled"
	if job.CancelRequested {
		state = "stopping (the worker ends it within seconds)"
	}
	say(o.stdout, "import %s: %s\n", job.ID, state)
	return nil
}

// readOrGeneratePassword reads the first line of stdin, or makes a random
// password (crypto/rand.Text: 26 base32 characters, 130 bits).
func readOrGeneratePassword(fromStdin bool, stdin io.Reader) (password string, generated bool, err error) {
	if !fromStdin {
		return rand.Text(), true, nil
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false, err
	}
	return strings.TrimRight(line, "\r\n"), false, nil
}

// adminError words the use cases' refusals for the operator.
func adminError(err error) string {
	switch {
	case errors.Is(err, app.ErrNoSuchUser):
		return "no account with that email"
	case errors.Is(err, app.ErrEmailTaken):
		return "an account with that email exists already"
	case errors.Is(err, app.ErrJobBusy):
		return "the account has an import queued or running; cancel it first (list-imports, cancel-import)"
	case errors.Is(err, app.ErrNotFound):
		return "no such import for that account"
	}
	return err.Error()
}

func listUsers(ctx context.Context, admin app.AdminService, stdout io.Writer) error {
	users, err := admin.Users(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	say(tw, "EMAIL\tPASSWORD\tPROVIDERS\tIMPORTS ACTIVE\tSTORED\n")
	for _, u := range users {
		password := "yes"
		if u.User.PasswordHash == "" {
			password = "no"
		}
		providers := strings.Join(u.LinkedProviders, ",")
		if providers == "" {
			providers = "-"
		}
		say(tw, "%s\t%s\t%s\t%d\t%s\n", u.User.Email, password, providers, u.Usage.Active, humanBytes(u.Usage.Stored()))
	}
	return tw.Flush()
}

func listImports(ctx context.Context, admin app.AdminService, email string, limit int, stdout io.Writer) error {
	jobs, err := admin.Imports(ctx, email, limit)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	say(tw, "ID\tCREATED\tSOURCE\tACCOUNT\tSTATUS\tEVENTS\n")
	for _, j := range jobs {
		say(tw, "%s\t%s\t%s\t%s\t%s\t%d\n", j.ID, j.CreatedAt.Format(time.RFC3339), j.Source, j.Label, j.Status, j.Saved)
	}
	return tw.Flush()
}

// humanBytes formats a byte count with a binary unit.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
