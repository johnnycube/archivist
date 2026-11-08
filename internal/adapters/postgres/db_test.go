package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3/database"
)

// These tests run Migrate's real code path — goose provider, SQL parser,
// the untracked-schema guard — against a recording database/sql driver and an in-memory
// goose Store, so they need no PostgreSQL. What they cannot prove is that
// PostgreSQL accepts the statements; the DATABASE_URL-gated tests do that.

const migrationsDir = "../../../migrations"

// ---- recording driver -------------------------------------------------------

type fakePG struct {
	mu      sync.Mutex
	tables  map[string]bool // what to_regclass() reports
	stmts   []fakeStmt      // every Exec, in order
	inTx    bool
	failOn  string // Exec containing this fails
	txCount int
}

type fakeStmt struct {
	sql  string
	inTx bool // executed inside a database/sql transaction
}

func (f *fakePG) Open(string) (driver.Conn, error) { return &fakeConn{f}, nil }

type fakeConn struct{ f *fakePG }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("fake: no prepare") }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.inTx = true
	c.f.txCount++
	return fakeTx{c.f}, nil
}

type fakeTx struct{ f *fakePG }

func (t fakeTx) Commit() error   { t.f.mu.Lock(); t.f.inTx = false; t.f.mu.Unlock(); return nil }
func (t fakeTx) Rollback() error { return t.Commit() }

func (c *fakeConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if c.f.failOn != "" && strings.Contains(q, c.f.failOn) {
		return nil, errors.New("fake: induced failure")
	}
	c.f.stmts = append(c.f.stmts, fakeStmt{sql: q, inTx: c.f.inTx})
	return driver.RowsAffected(0), nil
}

func (c *fakeConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	switch {
	case strings.Contains(q, "to_regclass"):
		return &fakeRows{vals: []driver.Value{c.f.tables[args[0].Value.(string)]}}, nil
	}
	return nil, fmt.Errorf("fake: unexpected query %q", q)
}

type fakeRows struct {
	vals []driver.Value
	done bool
}

func (r *fakeRows) Columns() []string { return []string{"v"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(dest, r.vals)
	return nil
}

var fakeSeq int

func openFake(t *testing.T, f *fakePG) *sql.DB {
	t.Helper()
	if f.tables == nil {
		f.tables = map[string]bool{}
	}
	fakeSeq++
	name := "fakepg-" + strconv.Itoa(fakeSeq)
	sql.Register(name, f)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// ---- in-memory goose store --------------------------------------------------

type memStore struct {
	f        *fakePG
	versions map[int64]bool
}

func (s *memStore) Tablename() string { return "goose_db_version" }
func (s *memStore) CreateVersionTable(context.Context, database.DBTxConn) error {
	if s.f.tables[s.Tablename()] {
		return errors.New("memStore: version table already exists")
	}
	s.f.tables[s.Tablename()] = true
	s.versions = map[int64]bool{}
	return nil
}

func (s *memStore) TableExists(context.Context, database.DBTxConn) (bool, error) {
	return s.f.tables[s.Tablename()], nil
}

func (s *memStore) Insert(_ context.Context, _ database.DBTxConn, req database.InsertRequest) error {
	if s.versions[req.Version] {
		return fmt.Errorf("memStore: version %d inserted twice", req.Version)
	}
	s.versions[req.Version] = true
	return nil
}

func (s *memStore) Delete(_ context.Context, _ database.DBTxConn, v int64) error {
	delete(s.versions, v)
	return nil
}

func (s *memStore) GetMigration(_ context.Context, _ database.DBTxConn, v int64) (*database.GetMigrationResult, error) {
	if !s.versions[v] {
		return nil, database.ErrVersionNotFound
	}
	return &database.GetMigrationResult{Timestamp: time.Now(), IsApplied: true}, nil
}

func (s *memStore) GetLatestVersion(context.Context, database.DBTxConn) (int64, error) {
	if len(s.versions) == 0 {
		return -1, database.ErrVersionNotFound
	}
	var m int64
	for v := range s.versions {
		m = max(m, v)
	}
	return m, nil
}

func (s *memStore) ListMigrations(context.Context, database.DBTxConn) ([]*database.ListMigrationsResult, error) {
	var out []*database.ListMigrationsResult
	for v := range s.versions {
		out = append(out, &database.ListMigrationsResult{Version: v, IsApplied: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version }) // goose: newest first
	return out, nil
}

func (s *memStore) applied() []int64 {
	var out []int64
	for v := range s.versions {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// ---- helpers ----------------------------------------------------------------

// migrationFiles returns the numbered .sql files in migrations/, sorted.
func migrationFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) < 3 {
		t.Fatalf("found only %v in %s", names, migrationsDir)
	}
	return names
}

func versionOf(t *testing.T, name string) int64 {
	t.Helper()
	v, err := strconv.ParseInt(strings.SplitN(name, "_", 2)[0], 10, 64)
	if err != nil {
		t.Fatalf("migration %q has no numeric version prefix", name)
	}
	return v
}

func runFake(t *testing.T, f *fakePG) (*memStore, []string, error) {
	t.Helper()
	db := openFake(t, f)
	store := &memStore{f: f}
	applied, err := migrate(context.Background(), db, os.DirFS(migrationsDir), store, nil)
	return store, applied, err
}

// ---- tests ------------------------------------------------------------------

// Every file in migrations/ must be a valid goose migration, apply in version
// order on a fresh database, and be recorded.
func TestMigrateFreshDatabase(t *testing.T) {
	files := migrationFiles(t)
	f := &fakePG{}
	store, applied, err := runFake(t, f)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !slices.Equal(applied, files) {
		t.Errorf("applied = %v, want every file in order %v", applied, files)
	}
	want := []int64{0}
	for _, name := range files {
		want = append(want, versionOf(t, name))
	}
	if got := store.applied(); !slices.Equal(got, want) {
		t.Errorf("recorded versions = %v, want %v", got, want)
	}
	if !slices.IsSorted(want) || len(slices.Compact(slices.Clone(want))) != len(want) {
		t.Errorf("migration versions are not strictly increasing: %v", want)
	}

	// Second run: nothing to do.
	before := len(f.stmts)
	db := openFake(t, f)
	again, err := migrate(context.Background(), db, os.DirFS(migrationsDir), store, nil)
	if err != nil || len(again) != 0 || len(f.stmts) != before {
		t.Errorf("re-run applied %v (err %v, %d new statements); want a no-op", again, err, len(f.stmts)-before)
	}
}

// goose splits files into statements at line-ending semicolons. PL/pgSQL
// bodies must travel as one statement, and nothing may be lost in the split.
func TestMigrationStatementSplitting(t *testing.T) {
	f := &fakePG{}
	if _, _, err := runFake(t, f); err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, s := range f.stmts {
		all = append(all, s.sql)
		if strings.Contains(s.sql, "$$") && strings.Count(s.sql, "$$")%2 != 0 {
			t.Errorf("dollar-quoted body was split across statements:\n%s", s.sql)
		}
	}
	joined := strings.Join(all, "\n")
	for _, must := range []string{"CREATE FUNCTION events_tsv()", "END $$ LANGUAGE plpgsql;", "CREATE TRIGGER events_tsv_trg"} {
		if !strings.Contains(joined, must) {
			t.Errorf("statement containing %q was not executed", must)
		}
	}

	// Cross-check against the files: every statement keyword occurrence in the
	// source must show up in what was executed (comment-only lines aside).
	kw := regexp.MustCompile(`(?m)^\s*(CREATE|ALTER|UPDATE|INSERT|DROP|DELETE)\b`)
	for _, name := range migrationFiles(t) {
		src, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if n := len(kw.FindAll(src, -1)); n == 0 {
			t.Errorf("%s: no statements found", name)
		}
	}
	if got, want := len(kw.FindAllString(joined, -1)), countInFiles(t, kw); got != want {
		t.Errorf("executed %d top-level statements, the files contain %d", got, want)
	}
}

func countInFiles(t *testing.T, re *regexp.Regexp) int {
	n := 0
	for _, name := range migrationFiles(t) {
		src, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			t.Fatal(err)
		}
		n += len(re.FindAll(src, -1))
	}
	return n
}

// A file either brings its own BEGIN/COMMIT and is marked NO TRANSACTION, or
// has neither and runs inside goose's transaction. Mixing the two would nest
// transactions (COMMIT inside goose's tx ends it early).
func TestMigrationTransactionDiscipline(t *testing.T) {
	f := &fakePG{}
	if _, _, err := runFake(t, f); err != nil {
		t.Fatal(err)
	}
	txControl := regexp.MustCompile(`(?i)^\s*(BEGIN|COMMIT|START TRANSACTION|ROLLBACK)\s*;`)
	open := false
	for _, s := range f.stmts {
		switch {
		case txControl.MatchString(s.sql):
			if s.inTx {
				t.Errorf("%q ran inside goose's transaction: mark the file '-- +goose NO TRANSACTION' or drop its BEGIN/COMMIT", s.sql)
			}
			open = strings.HasPrefix(strings.ToUpper(strings.TrimSpace(s.sql)), "BEGIN") ||
				strings.HasPrefix(strings.ToUpper(strings.TrimSpace(s.sql)), "START")
		case !s.inTx && !open:
			t.Errorf("statement runs outside any transaction:\n%s", s.sql)
		}
	}
	if open {
		t.Error("a migration leaves its own transaction open (BEGIN without COMMIT)")
	}
}

// A database with Archivist's tables but no goose bookkeeping is from a
// pre-release build (or another application's): refused with what to do, nothing run.
func TestMigrateRefusesUntrackedSchema(t *testing.T) {
	for _, table := range []string{"schema_migrations", "users", "events"} {
		t.Run(table, func(t *testing.T) {
			f := &fakePG{tables: map[string]bool{table: true}}
			_, applied, err := runFake(t, f)
			if err == nil || !strings.Contains(err.Error(), table) || !strings.Contains(err.Error(), "empty database") {
				t.Fatalf("err = %v; want a refusal naming %q and what to do", err, table)
			}
			if len(applied) != 0 || len(f.stmts) != 0 {
				t.Errorf("applied %v / executed %d statements on an untracked schema", applied, len(f.stmts))
			}
		})
	}
}

// Once goose tracks the database, existing tables are expected.
func TestMigrateTrackedDatabaseRunsOnlyNewFiles(t *testing.T) {
	f := &fakePG{}
	store, applied, err := runFake(t, f)
	if err != nil || !slices.Equal(applied, migrationFiles(t)) {
		t.Fatalf("first run = %v, %v", applied, err)
	}
	f.tables["users"], f.tables["events"] = true, true
	db := openFake(t, f)
	if again, err := migrate(context.Background(), db, os.DirFS(migrationsDir), store, nil); err != nil || len(again) != 0 {
		t.Errorf("second run = %v, %v; want none, nil", again, err)
	}
}

// A failing migration reports what was applied before it and records nothing
// for the failed file.
func TestMigrateReportsPartialProgress(t *testing.T) {
	files := migrationFiles(t)
	f := &fakePG{failOn: "CREATE TABLE sessions"} // in 0002
	store, applied, err := runFake(t, f)
	if err == nil {
		t.Fatal("expected the induced failure")
	}
	if !slices.Equal(applied, files[:1]) {
		t.Errorf("applied = %v, want %v", applied, files[:1])
	}
	if got := store.applied(); !slices.Equal(got, []int64{0, 1}) {
		t.Errorf("recorded versions = %v, want [0 1]", got)
	}
}

func TestMigrateMissingDir(t *testing.T) {
	if _, err := Migrate(context.Background(), openFake(t, &fakePG{}), filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("a missing migrations dir must be an error")
	}
}

// Creating an extension needs CREATE on the database, which roles on managed
// PostgreSQL often lack (TestMigrateNeedsNoExtensionPrivileges runs the chain
// as such a role). A migration may still ask for an optional extension, in a
// statement block that catches the refusal (EXCEPTION ... RAISE NOTICE).
func TestMigrationsCreateNoExtensionUnguarded(t *testing.T) {
	for _, name := range migrationFiles(t) {
		src, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			t.Fatal(err)
		}
		inBlock, blockAsks, blockCatches := false, false, false
		for _, line := range strings.Split(string(src), "\n") {
			code := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(code, "-- +GOOSE STATEMENTBEGIN"):
				inBlock, blockAsks, blockCatches = true, false, false
			case strings.HasPrefix(code, "-- +GOOSE STATEMENTEND"):
				if blockAsks && !blockCatches {
					t.Errorf("%s: a block creates an extension without an EXCEPTION handler", name)
				}
				inBlock = false
			case strings.HasPrefix(code, "--"):
			case strings.Contains(code, "CREATE EXTENSION") && !inBlock:
				t.Errorf("%s: %q outside a guarded block", name, strings.TrimSpace(line))
			case strings.Contains(code, "CREATE EXTENSION"):
				blockAsks = true
			case strings.Contains(code, "EXCEPTION"):
				blockCatches = true
			}
		}
	}
}
