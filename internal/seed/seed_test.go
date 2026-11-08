package seed_test

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/blob"
	"github.com/johnnycube/archivist/internal/adapters/catalog"
	"github.com/johnnycube/archivist/internal/adapters/crypto"
	"github.com/johnnycube/archivist/internal/adapters/memory"
	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/seed"
)

// The demo account is created behind the use cases' back (no SignUp, so the
// signup rules do not apply to it); nothing but this test keeps it in step with
// them: the seeded user must be able to log in and see the seeded data, and a
// second boot must refresh the same events rather than add to them.
func TestDemoAccountWorksWithTheApplication(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore(memory.Options{})
	blobs := blob.NewFS(t.TempDir())
	deps := seed.Deps{Users: store.Users, Hasher: crypto.Hasher{}, Accounts: store.Accounts, Events: store.Events, Blobs: blobs}

	start := time.Now()
	n, err := seed.Demo(ctx, deps)
	took := time.Since(start)
	if err != nil || n == 0 {
		t.Fatalf("Demo = %d, %v", n, err)
	}
	t.Logf("seeded %d events in %s", n, took)
	if n < 12000 || n > 16000 {
		t.Errorf("seeded %d events; the demo is meant to hold 12–16k", n)
	}
	if took > 3*time.Second && !raceDetector {
		t.Errorf("seeding took %s; must stay under 3s on the in-memory store", took)
	}
	// Idempotent: a second boot refreshes the same events.
	if again, err := seed.Demo(ctx, deps); err != nil || again != n {
		t.Fatalf("second Demo = %d, %v; want %d", again, err, n)
	}

	a := app.New(app.Deps{
		Users: store.Users, Accounts: store.Accounts, Events: store.Events, Jobs: store.Jobs,
		Sessions: store.Sessions, Attempts: store.Attempts, Hasher: crypto.Hasher{},
		Catalog: catalog.New(), Blobs: blobs,
	})
	u, token, err := a.Auth.LogInGuarded(ctx, seed.DemoEmail, seed.DemoPassword, "", "test")
	if err != nil || token == "" {
		t.Fatalf("demo login: %v", err)
	}
	page, err := a.Timeline.Query(ctx, app.Query{UserID: u.ID, Limit: 500})
	if err != nil || page.Total != n {
		t.Errorf("timeline total = %d, %v; want the %d seeded events (a duplicate external id collapses two events into one)", page.Total, err, n)
	}
	for _, ev := range page.Events {
		if !ev.Category.Valid() {
			t.Errorf("seeded event %s has category %q, which the timeline cannot be filtered by", ev.ExternalID, ev.Category)
		}
	}

	// Attachments: every blob-backed media entry streams through the use case;
	// PNGs are what the media endpoint serves inline, the PDFs as downloads.
	var pngs, pdfs int
	for _, se := range allEvents(t, ctx, a, u.ID) {
		for i, m := range se.Media {
			if m.BlobKey == "" || !(m.MIME == "image/png" || m.MIME == "application/pdf") {
				continue
			}
			rc, key, err := a.Timeline.Media(ctx, u.ID, se.ID, i)
			if err != nil {
				if m.MIME == "application/pdf" || strings.HasSuffix(m.BlobKey, ".png") && seededBytes(se, i) {
					t.Errorf("media %s/%s of %s: %v", se.SourceID, m.BlobKey, se.Type, err)
				}
				continue
			}
			data, _ := io.ReadAll(rc)
			rc.Close()
			switch {
			case strings.HasSuffix(key, ".png"):
				if !strings.HasPrefix(string(data), "\x89PNG") {
					t.Errorf("%s is not a PNG", key)
				}
				pngs++
			case strings.HasSuffix(key, ".pdf"):
				if !strings.HasPrefix(string(data), "%PDF-1.4") || !strings.HasSuffix(strings.TrimSpace(string(data)), "%%EOF") {
					t.Errorf("%s is not a well-formed PDF", key)
				}
				pdfs++
			default:
				t.Errorf("unexpected media key %s", key)
			}
		}
	}
	t.Logf("%d PNG and %d PDF attachments served", pngs, pdfs)
	if pngs < 15 || pdfs < 2 {
		t.Errorf("want ≥ 15 PNG and ≥ 2 PDF attachments backed by blobs, got %d and %d", pngs, pdfs)
	}
}

// seededBytes reports whether the seed rendered bytes for media i of se (the
// seed attaches more media entries than it renders, as real imports do when
// the export omits files).
func seededBytes(se domain.StoredEvent, i int) bool {
	return se.Media[i].MIME != ""
}

// allEvents pages through the whole timeline.
func allEvents(t *testing.T, ctx context.Context, a *app.App, userID string) []domain.StoredEvent {
	t.Helper()
	var out []domain.StoredEvent
	q := app.Query{UserID: userID, Limit: 500}
	for {
		page, err := a.Timeline.Query(ctx, q)
		if err != nil {
			t.Fatalf("timeline: %v", err)
		}
		out = append(out, page.Events...)
		if page.NextCursor == "" {
			return out
		}
		q.Cursor = page.NextCursor
	}
}

// The demo must show every provider the catalog registers and every event
// type each of them declares — adding a provider or a type without sample
// content fails here — and every seeded event must be one its provider could
// have produced: a declared type with the declared category, a body where the
// type has one, a target where the type needs one, and an external id unique
// within its source (the upsert key).
func TestDemoCoversEveryCatalogType(t *testing.T) {
	bySource := seed.Events(true)
	descriptors := catalog.New().Descriptors()

	declared := map[string]map[string]domain.TypeDef{}
	for _, d := range descriptors {
		declared[d.ID] = map[string]domain.TypeDef{}
		for _, td := range d.EventTypes {
			declared[d.ID][td.Type] = td
		}
	}
	for src := range bySource {
		if declared[src] == nil {
			t.Errorf("seeded source %q is not in the catalog", src)
		}
	}

	seen := map[string]map[string]int{}
	total := 0
	for src, evs := range bySource {
		seen[src] = map[string]int{}
		ids := map[string]string{}
		for _, ev := range evs {
			total++
			seen[src][ev.Type]++
			if ev.SourceID != src {
				t.Errorf("%s: event %s has SourceID %q", src, ev.ExternalID, ev.SourceID)
			}
			if ev.ExternalID == "" {
				t.Errorf("%s: %s event without external id (%q)", src, ev.Type, ev.Title)
			}
			if other, dup := ids[ev.ExternalID]; dup {
				t.Errorf("%s: external id %q used twice (%s and %s)", src, ev.ExternalID, other, ev.Type)
			}
			ids[ev.ExternalID] = ev.Type
			td, ok := declared[src][ev.Type]
			if !ok {
				t.Errorf("%s: type %q is not declared by the provider", src, ev.Type)
				continue
			}
			if ev.Category != td.Category {
				t.Errorf("%s: %s has category %q, provider declares %q", src, ev.Type, ev.Category, td.Category)
			}
			if td.HasBody && strings.TrimSpace(ev.Body) == "" {
				t.Errorf("%s: %s %q has no body", src, ev.Type, ev.ExternalID)
			}
			if td.NeedsTarget && len(ev.Targets) == 0 {
				t.Errorf("%s: %s %q has no target", src, ev.Type, ev.ExternalID)
			}
			if !ev.Category.Valid() {
				t.Errorf("%s: %s has invalid category %q", src, ev.Type, ev.Category)
			}
			if !ev.OccurredAt.IsZero() && ev.OccurredAt.After(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) && ev.Type != "reddit.scheduled_post" {
				t.Errorf("%s: %s %q is dated in the future: %s", src, ev.Type, ev.ExternalID, ev.OccurredAt)
			}
		}
	}

	var missing []string
	for _, d := range descriptors {
		if len(bySource[d.ID]) == 0 {
			missing = append(missing, d.ID+" (whole source)")
			continue
		}
		for _, td := range d.EventTypes {
			if seen[d.ID][td.Type] == 0 {
				missing = append(missing, td.Type)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("catalog types without sample content:\n  %s", strings.Join(missing, "\n  "))
	}
	t.Logf("%d events cover %d sources", total, len(bySource))
}

// The data set is a pure function of the code: two generations are identical
// (same events, same order), which is what makes re-seeding an upsert of the
// same rows rather than a slowly growing pile.
func TestDemoIsDeterministic(t *testing.T) {
	a, b := seed.Events(true), seed.Events(true)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two generations of the demo data differ")
	}
	// Without a blob store the events are the same, only the bytes are skipped.
	if c := seed.Events(false); !reflect.DeepEqual(a, c) {
		t.Fatal("generating with and without media yields different events")
	}
}

// The shape the UI is meant to show: three consumption sources dominating
// (clusters, log-scale histogram), the rest moderate, a few undated events.
func TestDemoShape(t *testing.T) {
	bySource := seed.Events(false)
	count := func(src, typ string) int {
		n := 0
		for _, ev := range bySource[src] {
			if ev.Type == typ {
				n++
			}
		}
		return n
	}
	for _, want := range []struct {
		src, typ string
		min      int
	}{
		{"spotify", "spotify.play", 3500},
		{"google", "google.youtube.watch", 1200},
		{"google", "google.chrome.visit", 1200},
	} {
		if n := count(want.src, want.typ); n < want.min {
			t.Errorf("%s: %d events, want ≥ %d for the clustering/log-scale demo", want.typ, n, want.min)
		}
	}
	undated := 0
	for _, evs := range bySource {
		for _, ev := range evs {
			if ev.OccurredAt.IsZero() {
				undated++
			}
		}
	}
	if undated < 10 || undated > 200 {
		t.Errorf("%d undated events; want a few (10–200) so the undated tail shows without dominating", undated)
	}
	// Every provider gets a few "<source>.unknown" events shaped like the
	// importer's generic capture.
	for src, evs := range bySource {
		n := 0
		for _, ev := range evs {
			if ev.Type == src+".unknown" {
				n++
				if ev.Metadata["member"] == nil {
					t.Errorf("%s: unknown event %q lacks metadata.member", src, ev.ExternalID)
				}
			}
		}
		if n == 0 {
			t.Errorf("%s: no %s.unknown sample", src, src)
		}
	}
	var lines []string
	for src, evs := range bySource {
		lines = append(lines, fmt.Sprintf("%-10s %6d", src, len(evs)))
	}
	sort.Strings(lines)
	t.Logf("events per source:\n%s", strings.Join(lines, "\n"))
}
