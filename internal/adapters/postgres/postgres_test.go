package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/porttest"
	"github.com/johnnycube/archivist/internal/adapters/postgres"
	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// Everything in this file runs only when DATABASE_URL is set. It REBUILDS the
// public schema of that database — point it at a throwaway one.

const migrationsPath = "../../../migrations"

var (
	setupOnce sync.Once
	setupDB   *sql.DB
	setupErr  error
	applied   []string
)

// openMigrated returns a pool on a database whose public schema was dropped and
// rebuilt from migrations/ (once per test binary).
func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the Postgres integration tests")
	}
	setupOnce.Do(func() {
		ctx := context.Background()
		if setupDB, setupErr = postgres.Open(ctx, dsn); setupErr != nil {
			return
		}
		if _, setupErr = setupDB.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); setupErr != nil {
			return
		}
		applied, setupErr = postgres.Migrate(ctx, setupDB, migrationsPath)
	})
	if setupErr != nil {
		t.Fatalf("set up test database: %v", setupErr)
	}
	return setupDB
}

// emptyDB is openMigrated with every table emptied: all data hangs off users,
// except the login throttle's (which must not reference them).
func emptyDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openMigrated(t)
	if _, err := db.ExecContext(context.Background(), `TRUNCATE users, auth_failures CASCADE`); err != nil {
		t.Fatalf("empty the database: %v", err)
	}
	return db
}

func envOf(db *sql.DB) porttest.Env {
	return porttest.EnvFrom(postgres.NewUsers(db), postgres.NewAccounts(db), postgres.NewJobs(db))
}

func TestMigrateRealDatabase(t *testing.T) {
	db := openMigrated(t)
	files, err := os.ReadDir(migrationsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != len(files) || len(applied) < 5 {
		t.Fatalf("migrate applied %v, want all %d files", applied, len(files))
	}
	// A second run must be a no-op (versioned, not re-applied).
	if again, err := postgres.Migrate(context.Background(), db, migrationsPath); err != nil || len(again) != 0 {
		t.Fatalf("re-migrate = %v, %v; want none, nil", again, err)
	}
}

// Migration 0005: the RLS policies of 0001 were never in effect (the app
// connects as the table owner and never sets app.user_id) and are gone, so
// nobody mistakes them for tenant isolation — and so that connecting as a
// non-owner role does not silently turn every query into "no rows".
func TestDecorativeRLSIsGone(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()
	var policies int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_policies WHERE schemaname = current_schema()`).Scan(&policies); err != nil {
		t.Fatal(err)
	}
	if policies != 0 {
		t.Errorf("pg_policies has %d policies, want none", policies)
	}
	rows, err := db.QueryContext(ctx, `
SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND c.relname IN ('events', 'accounts', 'event_targets')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name string
		var enabled, forced bool
		if err := rows.Scan(&name, &enabled, &forced); err != nil {
			t.Fatal(err)
		}
		seen++
		if enabled || forced {
			t.Errorf("table %s: row security enabled=%v forced=%v, want both off", name, enabled, forced)
		}
	}
	if err := rows.Err(); err != nil || seen != 3 {
		t.Errorf("checked %d tables (err %v), want events, accounts, event_targets", seen, err)
	}
}

// --- the shared port contract (internal/adapters/porttest) ---

func TestUsersContract(t *testing.T) {
	openMigrated(t)
	porttest.RunUsers(t, func(t *testing.T) app.Users { return postgres.NewUsers(emptyDB(t)) })
}

func TestAccountsContract(t *testing.T) {
	openMigrated(t)
	porttest.RunAccounts(t, func(t *testing.T) (app.Accounts, porttest.Env) {
		db := emptyDB(t)
		return postgres.NewAccounts(db), envOf(db)
	})
}

func TestSessionsContract(t *testing.T) {
	openMigrated(t)
	porttest.RunSessions(t, func(t *testing.T, ttl, idle time.Duration) (app.Sessions, porttest.Env) {
		db := emptyDB(t)
		s := postgres.NewSessions(db, ttl)
		s.IdleTTL = idle
		return s, envOf(db)
	})
}

func TestAttemptLimiterContract(t *testing.T) {
	openMigrated(t)
	porttest.RunAttemptLimiter(t, func(t *testing.T) app.AttemptLimiter { return postgres.NewAttempts(emptyDB(t)) })
}

func TestUserDeletionContract(t *testing.T) {
	openMigrated(t)
	porttest.RunUserDeletion(t, func(t *testing.T) porttest.Store {
		db := emptyDB(t)
		return porttest.Store{
			Users: postgres.NewUsers(db), Accounts: postgres.NewAccounts(db),
			Events: postgres.NewEvents(db), Jobs: postgres.NewJobs(db), Sessions: postgres.NewSessions(db, time.Hour),
		}
	})
}

func TestJobsContract(t *testing.T) {
	openMigrated(t)
	porttest.RunJobs(t, func(t *testing.T) (app.Jobs, porttest.Env) {
		db := emptyDB(t)
		return postgres.NewJobs(db), envOf(db)
	})
}

func TestEventRepositoryContract(t *testing.T) {
	openMigrated(t)
	porttest.RunEventRepository(t, func(t *testing.T) (app.EventRepository, porttest.Env) {
		db := emptyDB(t)
		return postgres.NewEvents(db), envOf(db)
	})
}

// --- PostgreSQL specifics the port contract does not cover ---

// Exports contain NUL bytes and absurd dates now and then; neither text/jsonb
// nor timestamptz can hold them. They are cleaned, not rejected — on both write
// paths.
func TestEventsSurviveUnstorableInput(t *testing.T) {
	db := emptyDB(t)
	ctx := context.Background()
	env := envOf(db)
	user := env.NewUser(t)
	acct := env.NewAccount(t, user, "reddit")
	events := postgres.NewEvents(db)

	nul := domain.Event{
		SourceID: "reddit", ExternalID: "post:nul", Type: "reddit.post", Category: domain.CategoryContent,
		Title: "nul\x00byte", Metadata: map[string]any{"k": "v\x00"},
		Targets: []domain.Target{{Relation: domain.RelationSaved, TargetURL: "http://x\x00"}},
	}
	farFuture := domain.Event{
		SourceID: "reddit", ExternalID: "post:year", Type: "reddit.post", Category: domain.CategoryContent,
		Title: "far future", OccurredAt: time.Date(20000, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	if _, _, err := events.Save(ctx, user, acct, nul); err != nil {
		t.Fatalf("Save with NUL bytes: %v", err)
	}
	if _, err := events.SaveBatch(ctx, user, acct, app.ImportRun{}, []domain.Event{nul, farFuture}); err != nil {
		t.Fatalf("SaveBatch with NUL bytes and a year-20000 date: %v", err)
	}
	p, err := events.Timeline(ctx, app.Query{UserID: user, Search: "nulbyte"})
	if err != nil || p.Total != 1 || p.Events[0].Metadata["k"] != "v" || p.Events[0].Targets[0].TargetURL != "http://x" {
		t.Errorf("NUL-cleaned event = %+v, %v", p.Events, err)
	}
	p, err = events.Timeline(ctx, app.Query{UserID: user, Search: "future"})
	if err != nil || p.Total != 1 || !p.Events[0].OccurredAt.IsZero() {
		t.Errorf("out-of-range date = %+v, %v; want the event kept, undated", p.Events, err)
	}

	// Ids that differ only by NUL bytes are one row after cleaning: in one
	// batch they must not make ON CONFLICT touch a row twice.
	twinA, twinB := nul, nul
	twinA.ExternalID, twinB.ExternalID = "post:twin", "post:\x00twin"
	if _, err := events.SaveBatch(ctx, user, acct, app.ImportRun{}, []domain.Event{twinA, twinB}); err != nil {
		t.Errorf("SaveBatch with ids equal after cleaning: %v", err)
	}

	// A body of a megabyte of distinct words: its full-text vector would
	// exceed PostgreSQL's 1 MiB tsvector limit; the trigger bounds its input.
	var words strings.Builder
	for i := 0; words.Len() < 1<<20; i++ {
		fmt.Fprintf(&words, "w%dx%d ", i, i*7919)
	}
	long := domain.Event{
		SourceID: "reddit", ExternalID: "post:long", Type: "reddit.post",
		Category: domain.CategoryContent, Title: "long one", Body: words.String(),
	}
	if _, err := events.SaveBatch(ctx, user, acct, app.ImportRun{}, []domain.Event{long}); err != nil {
		t.Fatalf("SaveBatch with a megabyte of distinct words: %v", err)
	}
	if p, err := events.Timeline(ctx, app.Query{UserID: user, Search: "w5x39595"}); err != nil || p.Total != 1 {
		t.Errorf("a word near the start is not found: %d, %v", p.Total, err)
	}
	if got, ok, _ := events.Get(ctx, user, mustID(t, events, user, "post:long")); !ok || got.Body != long.Body {
		t.Error("the stored body was cut, not just its search vector")
	}
}

func mustID(t *testing.T, events *postgres.Events, user, externalID string) string {
	t.Helper()
	p, err := events.Timeline(context.Background(), app.Query{UserID: user, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range p.Events {
		if e.ExternalID == externalID {
			return e.ID
		}
	}
	t.Fatalf("no event %q", externalID)
	return ""
}

// Deleting a user takes everything they own with it (FK cascades).
func TestDeleteUserCascades(t *testing.T) {
	db := emptyDB(t)
	ctx := context.Background()
	users := postgres.NewUsers(db)
	env := envOf(db)
	user, other := env.NewUser(t), env.NewUser(t)
	for _, u := range []string{user, other} {
		acct := env.NewAccount(t, u, "reddit")
		job := env.NewJob(t, u)
		if _, err := postgres.NewEvents(db).SaveBatch(ctx, u, acct, app.ImportRun{JobID: job}, []domain.Event{
			{
				SourceID: "reddit", ExternalID: "post:1", Type: "reddit.post", Category: domain.CategoryContent,
				Targets: []domain.Target{{Relation: domain.RelationSaved, TargetURL: "http://x"}},
				Media:   []domain.Media{{BlobKey: "m.jpg"}},
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := postgres.NewSessions(db, time.Hour).Create(ctx, "hash-"+u, u, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := users.Delete(ctx, user); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"users", "accounts", "import_jobs", "events", "event_targets", "media", "sessions"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s has %d rows after deleting one of two users, want the other user's 1", table, n)
		}
	}
}
