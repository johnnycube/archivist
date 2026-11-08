package postgres_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/postgres"
	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// Imports from before content addressing stored media under its path.
// Migration 0025 flags every import that exists when it runs; new ones are
// not flagged.
func TestMigrationFlagsImportsThatStoredMediaByPath(t *testing.T) {
	db := scopedDB(t)
	migrateTo(t, db, 24)
	env := envOf(db)
	u := env.NewUser(t)
	before := env.NewJob(t, u)
	if _, err := postgres.Migrate(context.Background(), db, migrationsPath); err != nil {
		t.Fatal(err)
	}
	after := env.NewJob(t, u)
	for job, want := range map[string]string{before: "true", after: "false"} {
		if got := queryStrings(t, db, `SELECT media_by_path::text FROM import_jobs WHERE id = $1`, job); !slices.Equal(got, []string{want}) {
			t.Errorf("media_by_path of the job created %s the migration = %v; want %s",
				map[bool]string{true: "before", false: "after"}[job == before], got, want)
		}
	}
}

// After a completed run of a flagged import, the paths in its ledger whose
// path-keyed copy nothing shows any more are released, once. A copy stays
// while an event of an import without a ledger entry for it still resolves
// to it by path, or while a ledger entry uses the path as its key.
func TestReleaseLegacyMedia(t *testing.T) {
	db := emptyDB(t)
	ctx := context.Background()
	env := envOf(db)
	jobs, events := postgres.NewJobs(db), postgres.NewEvents(db)
	u, other := env.NewUser(t), env.NewUser(t)
	acct := env.NewAccount(t, u, "whatsapp")
	a, b, c := env.NewJob(t, u), env.NewJob(t, u), env.NewJob(t, u)
	exec(t, db, `UPDATE import_jobs SET media_by_path = true WHERE id IN ($1, $2)`, a, b)

	show := func(job, id string, names ...string) {
		t.Helper()
		ev := domain.Event{
			SourceID: "whatsapp", ExternalID: id, Type: "whatsapp.message", Category: domain.CategoryMessage,
			OccurredAt: time.Date(2022, 4, 15, 14, 3, 0, 0, time.UTC),
		}
		for _, n := range names {
			ev.Media = append(ev.Media, domain.Media{BlobKey: n})
		}
		if _, err := events.SaveBatch(ctx, u, acct, app.ImportRun{JobID: job, RunID: "run-" + id}, []domain.Event{ev}); err != nil {
			t.Fatal(err)
		}
	}
	record := func(job string, blobs ...app.JobBlob) {
		t.Helper()
		if _, err := jobs.RecordBlobs(ctx, u, job, blobs); err != nil {
			t.Fatal(err)
		}
	}
	release := func(user, job string) []string {
		t.Helper()
		got, err := jobs.ReleaseLegacyMedia(ctx, user, job)
		if err != nil || got == nil {
			t.Fatalf("ReleaseLegacyMedia = %#v, %v", got, err)
		}
		return got
	}

	show(a, "a1", "IMG-1.jpg", "IMG-2.jpg")
	show(b, "b1", "IMG-1.jpg") // b is not reprocessed yet: it has no ledger
	// c, from a development build, recorded IMG-4 under its path.
	record(c, app.JobBlob{Path: "whatsapp/IMG-4.jpg", Key: "whatsapp/IMG-4.jpg", Bytes: 1})
	// a's reprocess stored its files under content keys; no event shows IMG-3.
	record(a,
		app.JobBlob{Path: "whatsapp/IMG-1.jpg", Key: "whatsapp/sha256/1", Bytes: 1},
		app.JobBlob{Path: "whatsapp/IMG-2.jpg", Key: "whatsapp/sha256/2", Bytes: 1},
		app.JobBlob{Path: "whatsapp/IMG-3.jpg", Key: "whatsapp/sha256/3", Bytes: 1},
		app.JobBlob{Path: "whatsapp/IMG-4.jpg", Key: "whatsapp/sha256/4", Bytes: 1})

	if got := release(other, a); len(got) != 0 {
		t.Errorf("another user released %v", got)
	}
	// IMG-1 is still what b's event shows; IMG-4 is c's key.
	if got := release(u, a); !slices.Equal(got, []string{"whatsapp/IMG-2.jpg", "whatsapp/IMG-3.jpg"}) {
		t.Errorf("released %v; want IMG-2 and IMG-3", got)
	}
	if got := release(u, a); len(got) != 0 {
		t.Errorf("released again: %v", got)
	}
	if got := release(u, c); len(got) != 0 {
		t.Errorf("an import that is not flagged released %v", got)
	}
	// b's reprocess: now no event resolves IMG-1 by path.
	record(b, app.JobBlob{Path: "whatsapp/IMG-1.jpg", Key: "whatsapp/sha256/1b", Bytes: 1})
	if got := release(u, b); !slices.Equal(got, []string{"whatsapp/IMG-1.jpg"}) {
		t.Errorf("released %v; want IMG-1", got)
	}
}
