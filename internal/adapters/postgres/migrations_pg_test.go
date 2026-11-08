package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/postgres"
)

// These tests (DATABASE_URL-gated) migrate a database that is at an OLD version
// and holds data forward, which is what an upgrade does — the contract suites
// only ever see a database built from scratch. Each works in a throwaway
// schema, so it does not disturb the other tests.

// scopedDB returns a pool whose connections see only a fresh, empty schema.
func scopedDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the Postgres integration test")
	}
	ctx := context.Background()
	admin, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("migration_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	})
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
	t.Cleanup(func() { db.Close() })
	return db
}

// migrateTo brings db to exactly version `last` by migrating from a directory
// holding only the files up to it.
func migrateTo(t *testing.T, db *sql.DB, last int) {
	t.Helper()
	entries, err := os.ReadDir(migrationsPath)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	copied := 0
	for _, e := range entries {
		var version int
		if _, err := fmt.Sscanf(e.Name(), "%d_", &version); err != nil || version > last {
			continue
		}
		src, err := os.ReadFile(filepath.Join(migrationsPath, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), src, 0o600); err != nil {
			t.Fatal(err)
		}
		copied++
	}
	if copied != last {
		t.Fatalf("found %d migrations up to version %d", copied, last)
	}
	if _, err := postgres.Migrate(context.Background(), db, dir); err != nil {
		t.Fatalf("migrate to version %d: %v", last, err)
	}
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func queryStrings(t *testing.T, db *sql.DB, q string, args ...any) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Fixed ids keep the fixture readable: u = user, a = account, e = event, …
func id(kind byte, n int) string {
	return fmt.Sprintf("%c0000000-0000-4000-8000-%012d", kind, n)
}

// Migration 0007 on a database where the select-then-insert race has already
// happened: duplicates are merged into the oldest account, every referencing
// row survives on it, and the unique index goes in.
func TestMigrationMergesDuplicateAccounts(t *testing.T) {
	db := scopedDB(t)
	ctx := context.Background()
	migrateTo(t, db, 5)

	alice, bob := id('a', 1), id('a', 2)
	exec(t, db, `INSERT INTO users (id, email, password_hash) VALUES ($1,'alice@x','h'), ($2,'bob@x','h')`, alice, bob)
	// alice/reddit/main exists three times (c1 oldest), bob/reddit/main twice —
	// same created_at there, so the id decides. alice/reddit/work and
	// alice/twitter/main are no duplicates.
	keep, dup1, dup2 := id('c', 1), id('c', 2), id('c', 3)
	work, twitter := id('c', 4), id('c', 5)
	bobKeep, bobDup := id('c', 6), id('c', 7)
	exec(t, db, `INSERT INTO accounts (id, user_id, source_id, label, created_at) VALUES
  ($1,$8,'reddit','main','2024-01-01'), ($2,$8,'reddit','main','2024-01-02'), ($3,$8,'reddit','main','2024-01-03'),
  ($4,$8,'reddit','work','2023-01-01'), ($5,$8,'twitter','main','2023-01-01'),
  ($6,$9,'reddit','main','2024-05-05'), ($7,$9,'reddit','main','2024-05-05')`,
		keep, dup1, dup2, work, twitter, bobKeep, bobDup, alice, bob)

	// 0001's tables that 0006 keeps because they hold data, with rows that
	// collide once the accounts are one: the same thread / raw record on two of
	// alice's accounts, events pointing at the copies.
	exec(t, db, `INSERT INTO threads (id, account_id, external_id, title) VALUES
  ($1,$4,'t-shared','on keep'), ($2,$5,'t-shared','on dup1'), ($3,$5,'t-only-dup','only on dup1')`,
		id('d', 1), id('d', 2), id('d', 3), keep, dup1)
	exec(t, db, `INSERT INTO raw_records (id, account_id, payload, content_hash) VALUES
  ($1,$3,'{}','hash-shared'), ($2,$4,'{}','hash-shared')`, id('f', 1), id('f', 2), dup1, dup2)
	exec(t, db, `INSERT INTO sync_runs (account_id, mode) VALUES ($1,'full')`, dup2)

	type ev struct{ id, user, account, ext, thread, raw string }
	events := []ev{
		{id('e', 1), alice, keep, "post:1", id('d', 1), ""},
		{id('e', 2), alice, dup1, "post:2", id('d', 2), id('f', 1)},
		{id('e', 3), alice, dup1, "post:3", id('d', 3), ""},
		{id('e', 4), alice, dup2, "post:4", "", id('f', 2)},
		{id('e', 5), alice, work, "post:5", "", ""},
		{id('e', 6), bob, bobKeep, "post:1", "", ""},
		{id('e', 7), bob, bobDup, "post:2", "", ""},
	}
	null := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	for _, e := range events {
		exec(t, db, `INSERT INTO events (id, user_id, account_id, source_id, external_id, type, category, title, thread_id, raw_record_id)
VALUES ($1,$2,$3,'reddit',$4,'reddit.post','content',$4,$5,$6)`, e.id, e.user, e.account, e.ext, null(e.thread), null(e.raw))
		exec(t, db, `INSERT INTO event_targets (event_id, relation) VALUES ($1,'saved')`, e.id)
	}

	if _, err := postgres.Migrate(ctx, db, migrationsPath); err != nil {
		t.Fatalf("migrate a database with duplicate accounts: %v", err)
	}

	if got, want := queryStrings(t, db, `SELECT id::text FROM accounts ORDER BY id`), []string{keep, work, twitter, bobKeep}; !slices.Equal(got, want) {
		t.Errorf("accounts after the merge = %v, want the oldest of each kind %v", got, want)
	}
	wantAccount := map[string]string{
		id('e', 1): keep, id('e', 2): keep, id('e', 3): keep, id('e', 4): keep,
		id('e', 5): work, id('e', 6): bobKeep, id('e', 7): bobKeep,
	}
	rows, err := db.QueryContext(ctx, `SELECT id::text, account_id::text, coalesce(thread_id::text,''), coalesce(raw_record_id::text,'') FROM events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var eid, account, thread, raw string
		if err := rows.Scan(&eid, &account, &thread, &raw); err != nil {
			t.Fatal(err)
		}
		seen++
		if account != wantAccount[eid] {
			t.Errorf("event %s is on account %s, want %s", eid, account, wantAccount[eid])
		}
		switch eid {
		case id('e', 1), id('e', 2): // the colliding thread: both on the surviving copy
			if thread != id('d', 1) {
				t.Errorf("event %s thread = %q, want the kept account's thread %s", eid, thread, id('d', 1))
			}
		case id('e', 3): // a thread only the duplicate had moves over untouched
			if thread != id('d', 3) {
				t.Errorf("event %s thread = %q, want %s", eid, thread, id('d', 3))
			}
		}
		if eid == id('e', 2) || eid == id('e', 4) { // colliding raw records of two duplicates
			if raw != id('f', 1) {
				t.Errorf("event %s raw record = %q, want the surviving %s", eid, raw, id('f', 1))
			}
		}
	}
	if err := rows.Err(); err != nil || seen != len(events) {
		t.Fatalf("%d of %d events survived the merge (err %v)", seen, len(events), err)
	}
	for q, want := range map[string][]string{
		`SELECT count(*)::text FROM event_targets`:         {"7"},
		`SELECT account_id::text FROM threads ORDER BY id`: {keep, keep},
		`SELECT account_id::text FROM raw_records`:         {keep},
		`SELECT account_id::text FROM sync_runs`:           {keep},
		`SELECT indexdef FROM pg_indexes WHERE indexname='accounts_user_source_label_key' AND schemaname=current_schema()`: nil,
	} {
		got := queryStrings(t, db, q)
		if want == nil {
			if len(got) != 1 || !strings.Contains(got[0], "UNIQUE") {
				t.Errorf("unique index = %v, want it created", got)
			}
			continue
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", q, got, want)
		}
	}

	// And the adapter on top of it: Ensure finds the merged account, and a
	// duplicate can no longer be made.
	if got, err := postgres.NewAccounts(db).Ensure(ctx, alice, "reddit", "main"); err != nil || got != keep {
		t.Errorf("Ensure after the merge = %q, %v; want the kept account %s", got, err, keep)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (user_id, source_id, label) VALUES ($1,'reddit','main')`, alice); err == nil {
		t.Error("a duplicate account was accepted after migration 0007")
	}
}

// 0001's never-used tables and columns, as of the full migration of `db`.
func unusedSchemaLeft(t *testing.T, db *sql.DB) []string {
	t.Helper()
	left := queryStrings(t, db, `
SELECT table_name::text FROM information_schema.tables
WHERE table_schema = current_schema()
  AND table_name IN ('encryption_keys','secrets','import_bundles','sync_runs','raw_records','threads','tags','event_tags')
UNION ALL
SELECT table_name || '.' || column_name FROM information_schema.columns
WHERE table_schema = current_schema()
  AND (table_name, column_name) IN (('users','mfa_secret'), ('events','raw_record_id'), ('events','thread_id'))
ORDER BY 1`)
	return left
}

// Migration 0006 on a database nobody touched by hand: everything unused goes.
func TestMigrationDropsUnusedSchema(t *testing.T) {
	db := scopedDB(t)
	migrateTo(t, db, 5)
	if left := unusedSchemaLeft(t, db); len(left) != 11 {
		t.Fatalf("at version 5 the unused schema is %v, want all 8 tables and 3 columns", left)
	}
	// Real data in the tables that stay must survive, of course.
	exec(t, db, `INSERT INTO users (id, email, password_hash) VALUES ($1,'u@x','h')`, id('a', 1))
	exec(t, db, `INSERT INTO accounts (id, user_id, source_id, label) VALUES ($1,$2,'reddit','main')`, id('c', 1), id('a', 1))
	exec(t, db, `INSERT INTO events (user_id, account_id, source_id, external_id, type, category, thread_external_id)
VALUES ($1,$2,'reddit','post:1','reddit.post','content','t1')`, id('a', 1), id('c', 1))
	if _, err := postgres.Migrate(context.Background(), db, migrationsPath); err != nil {
		t.Fatal(err)
	}
	if left := unusedSchemaLeft(t, db); len(left) != 0 {
		t.Errorf("unused schema left after migrating an untouched database: %v", left)
	}
	if got := queryStrings(t, db, `SELECT thread_external_id FROM events`); !slices.Equal(got, []string{"t1"}) {
		t.Errorf("events after 0006 = %v, want the event with its thread_external_id", got)
	}
}

// Migration 0006 where an operator DID use some of it: whatever holds data is
// kept, together with what it depends on; the rest still goes; nothing fails.
func TestMigrationKeepsUnusedSchemaThatHoldsData(t *testing.T) {
	db := scopedDB(t)
	migrateTo(t, db, 5)
	user, account := id('a', 1), id('c', 1)
	exec(t, db, `INSERT INTO users (id, email, password_hash, mfa_secret) VALUES ($1,'u@x','h','\x01')`, user)
	exec(t, db, `INSERT INTO accounts (id, user_id, source_id, label) VALUES ($1,$2,'reddit','main')`, account, user)
	exec(t, db, `INSERT INTO tags (user_id, name) VALUES ($1,'keep me')`, user)                                   // event_tags stays empty
	exec(t, db, `INSERT INTO raw_records (account_id, payload, content_hash) VALUES ($1,'{"k":1}','h')`, account) // import_bundles stays empty
	exec(t, db, `INSERT INTO encryption_keys (id, user_id, wrapped_dek) VALUES ($1,$2,'\x02')`, id('b', 1), user)
	exec(t, db, `INSERT INTO secrets (account_id, dek_id, ciphertext, nonce) VALUES ($1,$2,'\x03','\x04')`, account, id('b', 1))

	if _, err := postgres.Migrate(context.Background(), db, migrationsPath); err != nil {
		t.Fatalf("migrate with data in the unused tables: %v", err)
	}
	want := []string{
		"encryption_keys", "events.raw_record_id",
		"import_bundles", // empty, but raw_records (kept) references it
		"raw_records", "secrets", "tags", "users.mfa_secret",
	}
	if left := unusedSchemaLeft(t, db); !slices.Equal(left, want) {
		t.Errorf("kept = %v\nwant   %v (event_tags, sync_runs, threads + events.thread_id dropped)", left, want)
	}
	for q, want := range map[string]string{
		`SELECT name FROM tags`:                        "keep me",
		`SELECT payload->>'k' FROM raw_records`:        "1",
		`SELECT encode(mfa_secret,'hex') FROM users`:   "01",
		`SELECT encode(ciphertext,'hex') FROM secrets`: "03",
		`SELECT count(*)::text FROM encryption_keys`:   "1",
		`SELECT count(*)::text FROM import_bundles`:    "0",
	} {
		if got := queryStrings(t, db, q); len(got) != 1 || got[0] != want {
			t.Errorf("%s = %v, want %q", q, got, want)
		}
	}
}

// 0009: sessions that exist at upgrade time count as used then — upgrading
// must not log everybody out through the idle timeout.
func TestMigrationKeepsExistingSessionsAlive(t *testing.T) {
	db := scopedDB(t)
	migrateTo(t, db, 5)
	user := id('a', 1)
	exec(t, db, `INSERT INTO users (id, email, password_hash) VALUES ($1,'u@x','h')`, user)
	exec(t, db, `INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES ('old',$1, now() - interval '20 days', now() + interval '10 days')`, user)
	exec(t, db, `INSERT INTO import_jobs (user_id, source, zip_paths) VALUES ($1,'reddit','["u/x.zip"]')`, user)
	if _, err := postgres.Migrate(context.Background(), db, migrationsPath); err != nil {
		t.Fatal(err)
	}
	s := postgres.NewSessions(db, 30*24*time.Hour)
	s.IdleTTL = 14 * 24 * time.Hour
	if uid, ok, err := s.Lookup(context.Background(), "old", time.Now()); err != nil || !ok || uid != user {
		t.Errorf("a 20-day-old session after the upgrade: Lookup = %q,%v,%v; want it alive", uid, ok, err)
	}
	// 0008 / 0010 on existing rows: no part names, never served.
	jobs, err := postgres.NewJobs(db).List(context.Background(), user, 10)
	if err != nil || len(jobs) != 1 || len(jobs[0].PartNames) != 0 || jobs[0].PartName(0) != "" || len(jobs[0].ZipPaths) != 1 {
		t.Errorf("pre-0008 job = %+v, %v", jobs, err)
	}
	if j, ok, err := postgres.NewJobs(db).Claim(context.Background()); err != nil || !ok || j.UserID != user {
		t.Errorf("Claim of a pre-0010 job = %+v,%v,%v", j, ok, err)
	}
}
