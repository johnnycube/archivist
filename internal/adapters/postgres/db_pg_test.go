package postgres_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/postgres"
)

// TestMigrateRefusesPreGooseDatabase (DATABASE_URL-gated) rebuilds what the
// pre-goose runner left behind — schema from 0001..0003 executed as plain SQL
// plus its schema_migrations bookkeeping — and checks that Migrate refuses it
// without touching it. It works in a throwaway schema, so it does not disturb
// the other tests.
func TestMigrateRefusesPreGooseDatabase(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the Postgres integration test")
	}
	ctx := context.Background()
	admin, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	schema := fmt.Sprintf("bridge_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) })

	// A pool whose connections see only the throwaway schema.
	scoped := dsn + " search_path=" + schema
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scoped = u.String()
	}
	db, err := postgres.Open(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const dir = "../../../migrations"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	if len(files) < 3 {
		t.Fatalf("migrations = %v", files)
	}

	// The old runner: execute each file whole (goose annotations are comments),
	// then record it.
	if _, err := db.ExecContext(ctx, `
CREATE TABLE schema_migrations (
    version    int PRIMARY KEY,
    name       text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		t.Fatal(err)
	}
	for i, name := range files[:3] {
		sqlText, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(sqlText)); err != nil {
			t.Fatalf("legacy apply %s: %v", name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, i+1, name); err != nil {
			t.Fatal(err)
		}
	}

	applied, err := postgres.Migrate(ctx, db, dir)
	if err == nil || !strings.Contains(err.Error(), "schema_migrations") {
		t.Fatalf("migrate over a pre-goose database = %v, %v; want a refusal", applied, err)
	}
	if len(applied) != 0 {
		t.Errorf("applied %v", applied)
	}
	var tracked bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('goose_db_version') IS NOT NULL`).Scan(&tracked); err != nil || tracked {
		t.Errorf("goose_db_version created = %v, %v; want the database left alone", tracked, err)
	}
}
