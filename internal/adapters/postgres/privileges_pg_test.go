package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/postgres"
	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// restrictedDB is a fresh database the returned pool reaches as a role that
// may create nothing but objects in its own schema: no CREATE on the
// database, so no extension either — what a managed PostgreSQL often hands
// out. It is dropped (with the role) when the test ends.
func restrictedDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the Postgres integration test")
	}
	base, err := url.Parse(dsn)
	if err != nil || base.Scheme == "" {
		t.Skip("DATABASE_URL is not a URL; the restricted-role test needs one")
	}
	ctx := context.Background()
	admin, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	dbName, role := "archivist_restricted_"+suffix, "archivist_restricted_"+suffix
	for _, stmt := range []string{
		`CREATE ROLE ` + role + ` LOGIN PASSWORD 'restricted' NOSUPERUSER NOCREATEDB NOCREATEROLE`,
		`CREATE DATABASE ` + dbName, // owned by the admin: the role has no CREATE on it
	} {
		if _, err := admin.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`)
		_, _ = admin.ExecContext(ctx, `DROP ROLE IF EXISTS `+role)
	})

	// The role's own schema in the new database, created by the admin.
	adminURL := *base
	adminURL.Path = "/" + dbName
	inDB, err := postgres.Open(ctx, adminURL.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inDB.ExecContext(ctx, `CREATE SCHEMA archive AUTHORIZATION `+role); err != nil {
		t.Fatal(err)
	}
	_ = inDB.Close()

	roleURL := *base
	roleURL.Path = "/" + dbName
	roleURL.User = url.UserPassword(role, "restricted")
	q := roleURL.Query()
	q.Set("search_path", "archive")
	roleURL.RawQuery = q.Encode()
	db, err := postgres.Open(ctx, roleURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// The whole migration chain runs for a role that cannot create extensions,
// and the application works on the result: gen_random_uuid() is built into
// PostgreSQL 13+, and an extension a later migration would like (btree_gin)
// is optional.
func TestMigrateNeedsNoExtensionPrivileges(t *testing.T) {
	db := restrictedDB(t)
	ctx := context.Background()
	if _, err := postgres.Migrate(ctx, db, migrationsPath); err != nil {
		t.Fatalf("migrate as a role without extension privileges: %v", err)
	}
	var exts []string
	rows, err := db.QueryContext(ctx, `SELECT extname FROM pg_extension ORDER BY extname`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			t.Fatal(err)
		}
		exts = append(exts, e)
	}
	_ = rows.Close()
	if !slices.Equal(exts, []string{"plpgsql"}) {
		t.Errorf("extensions = %v; want none besides plpgsql", exts)
	}
	// Without btree_gin, search keeps the index over all users (0024).
	if got := indexes(t, db); !slices.Contains(got, "events_fts_idx") || slices.Contains(got, "events_user_fts_idx") {
		t.Errorf("indexes = %v; want the global events_fts_idx", got)
	}

	env := envOf(db)
	user := env.NewUser(t)
	acct := env.NewAccount(t, user, "reddit")
	events := postgres.NewEvents(db)
	if _, err := events.SaveBatch(ctx, user, acct, app.ImportRun{}, []domain.Event{{
		SourceID: "reddit", ExternalID: "post:1", Type: "reddit.post", Category: domain.CategoryContent,
		Title: "restricted roles welcome", OccurredAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}}); err != nil {
		t.Fatal(err)
	}
	if p, err := events.Timeline(ctx, app.Query{UserID: user, Search: "restricted", Limit: 10}); err != nil || p.Total != 1 {
		t.Errorf("search on the restricted database = %d, %v; want 1", p.Total, err)
	}
}

// indexes lists the index names on events.
func indexes(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT indexname FROM pg_indexes WHERE tablename = 'events' AND schemaname = current_schema() ORDER BY indexname`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// Where btree_gin can be created, search gets one GIN index per user's
// postings (0024): (user_id, search_vector), and the global one goes.
func TestSearchIndexLeadsWithTheUser(t *testing.T) {
	db := openMigrated(t)
	got := indexes(t, db)
	if !slices.Contains(got, "events_user_fts_idx") || slices.Contains(got, "events_fts_idx") {
		t.Fatalf("indexes = %v; want events_user_fts_idx instead of events_fts_idx", got)
	}
	var def string
	if err := db.QueryRowContext(context.Background(),
		`SELECT indexdef FROM pg_indexes WHERE indexname = 'events_user_fts_idx'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	if want := "USING gin (user_id, search_vector)"; !strings.Contains(def, want) {
		t.Errorf("index = %q; want %q", def, want)
	}
}
