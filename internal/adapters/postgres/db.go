// Package postgres implements the app's driven ports on PostgreSQL using
// database/sql + lib/pq. See migrations/ for the schema (goose migrations).
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"time"

	_ "github.com/lib/pq"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"

	"github.com/google/uuid"
)

// Open opens and pings a connection pool from a DSN/URL.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return db, nil
}

// Migrate applies the pending goose migrations (numbered *.sql files) from dir
// and returns the file names it applied, in order. Versions are tracked in
// goose's goose_db_version table; concurrent starters serialize on a
// session-level advisory lock.
//
// A database that holds tables but no goose_db_version is refused (see
// refuseUntrackedSchema) instead of being treated as empty.
func Migrate(ctx context.Context, db *sql.DB, dir string) (applied []string, err error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	store, err := database.NewStore(goose.DialectPostgres, goose.DefaultTablename)
	if err != nil {
		return nil, err
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, err
	}
	return migrate(ctx, db, os.DirFS(dir), store, locker)
}

// migrate is Migrate with its collaborators injected (tests run it without a
// PostgreSQL server).
func migrate(ctx context.Context, db *sql.DB, fsys fs.FS, store database.Store, locker lock.SessionLocker) (applied []string, err error) {
	opts := []goose.ProviderOption{
		goose.WithStore(store),
		goose.WithSlog(slog.Default()),
		goose.WithDisableGlobalRegistry(true), // SQL files only
	}
	if locker != nil {
		opts = append(opts, goose.WithSessionLocker(locker))
	}
	// The store is always passed explicitly (goose: "custom" dialect), so the
	// bridge below and the provider are guaranteed to share one version table.
	provider, err := goose.NewProvider(goose.DialectCustom, db, fsys, opts...)
	if err != nil {
		return nil, fmt.Errorf("load migrations: %w", err)
	}

	if err := refuseUntrackedSchema(ctx, db, store); err != nil {
		return nil, err
	}

	results, err := provider.Up(ctx)
	var partial *goose.PartialError
	if errors.As(err, &partial) {
		results = partial.Applied
	}
	for _, r := range results {
		applied = append(applied, path.Base(r.Source.Path))
	}
	if err != nil {
		return applied, fmt.Errorf("migrate: %w", err)
	}
	return applied, nil
}

// refuseUntrackedSchema stops a migration that would run 0001 over existing
// tables. goose takes a database without its version table for an empty one;
// one that already has Archivist's tables then is either from a pre-release
// build from before goose or another application's schema. Either way, running 0001 would fail halfway.
func refuseUntrackedSchema(ctx context.Context, db *sql.DB, store database.Store) error {
	exists := func(table string) (ok bool, err error) {
		err = db.QueryRowContext(ctx, `SELECT to_regclass($1::text) IS NOT NULL`, table).Scan(&ok)
		return ok, err
	}
	if ok, err := exists(store.Tablename()); err != nil || ok {
		return err // goose tracks this database (or the check failed)
	}
	for _, table := range []string{"schema_migrations", "users", "events"} {
		ok, err := exists(table)
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("the database has a %q table but no %s: it belongs to another application or a pre-release build, "+
				"which this release cannot upgrade — use an empty database", table, store.Tablename())
		}
	}
	return nil
}

// isUUID reports whether s is a UUID in the canonical 8-4-4-4-12 form. Ids
// arrive from URLs; a malformed one is simply "not found", not a uuid cast error.
func isUUID(s string) bool {
	return len(s) == 36 && uuid.Validate(s) == nil
}
