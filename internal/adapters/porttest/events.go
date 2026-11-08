package porttest

import (
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// RunEventRepository checks an app.EventRepository implementation. factory
// returns a fresh, empty repository and the Env to create users, accounts and
// import jobs with.
func RunEventRepository(t *testing.T, factory func(t *testing.T) (app.EventRepository, Env)) {
	// tenant is one user with a reddit account.
	type tenant struct{ user, account string }
	newTenant := func(t *testing.T, env Env) tenant {
		t.Helper()
		u := env.NewUser(t)
		return tenant{user: u, account: env.NewAccount(t, u, "reddit")}
	}
	save := func(t *testing.T, repo app.EventRepository, who tenant, ev domain.Event) domain.StoredEvent {
		t.Helper()
		se, _, err := repo.Save(ctx, who.user, who.account, ev)
		if err != nil {
			t.Fatalf("Save %s: %v", ev.ExternalID, err)
		}
		return se
	}
	timeline := func(t *testing.T, repo app.EventRepository, q app.Query) app.Page {
		t.Helper()
		p, err := repo.Timeline(ctx, q)
		if err != nil {
			t.Fatalf("Timeline(%+v): %v", q, err)
		}
		return p
	}

	t.Run("two accounts of one source never share an event", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		second := env.NewLabeledAccount(t, who.user, "reddit", "second")
		jobA, jobB := env.NewJob(t, who.user), env.NewJob(t, who.user)
		vote := domain.Event{
			SourceID: "reddit", ExternalID: "vote:t3_abc", Type: "reddit.vote.up",
			Category: domain.CategoryReaction, OccurredAt: day(1), Title: "voted",
		}
		if n, err := repo.SaveBatch(ctx, who.user, who.account, app.ImportRun{JobID: jobA}, []domain.Event{vote}); err != nil || n != 1 {
			t.Fatalf("first account's import = %d, %v", n, err)
		}
		if n, err := repo.SaveBatch(ctx, who.user, second, app.ImportRun{JobID: jobB}, []domain.Event{vote}); err != nil || n != 1 {
			t.Fatalf("second account's import of the same id = %d new, %v; want a new event", n, err)
		}
		p := timeline(t, repo, app.Query{UserID: who.user})
		if p.Total != 2 || p.Events[0].AccountID == p.Events[1].AccountID {
			t.Fatalf("events = %+v; want one per account", p.Events)
		}
		if n, err := repo.DeleteByImport(ctx, who.user, jobB); err != nil || n != 1 {
			t.Fatalf("DeleteByImport(second) = %d, %v", n, err)
		}
		p = timeline(t, repo, app.Query{UserID: who.user})
		if p.Total != 1 || p.Events[0].AccountID != who.account || p.Events[0].ImportJobID != jobA {
			t.Errorf("after deleting the second account's import: %+v; want the first account's event, still its import's", p.Events)
		}
	})

	t.Run("SweepImport removes the import's events a run did not write", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		job, later := env.NewJob(t, who.user), env.NewJob(t, who.user)
		first := app.ImportRun{JobID: job, RunID: "run-1"}
		second := app.ImportRun{JobID: job, RunID: "run-2"}
		if _, err := repo.SaveBatch(ctx, who.user, who.account, first, []domain.Event{post("a", day(1), "A", ""), post("b", day(2), "B", ""), post("c", day(3), "C", "")}); err != nil {
			t.Fatal(err)
		}
		// A later import takes "c" over: no longer this import's to sweep.
		if _, err := repo.SaveBatch(ctx, who.user, who.account, app.ImportRun{JobID: later, RunID: "x"}, []domain.Event{post("c", day(3), "C", "")}); err != nil {
			t.Fatal(err)
		}
		// The reprocess derives only "a" now.
		if _, err := repo.SaveBatch(ctx, who.user, who.account, second, []domain.Event{post("a", day(1), "A", "")}); err != nil {
			t.Fatal(err)
		}
		for _, noop := range []app.ImportRun{{JobID: job}, {JobID: "", RunID: "run-2"}, {JobID: unknownID, RunID: "r"}, {JobID: malformedID, RunID: "r"}} {
			if n, err := repo.SweepImport(ctx, who.user, noop); err != nil || n != 0 {
				t.Errorf("SweepImport(%+v) = %d, %v; want a no-op", noop, n, err)
			}
		}
		stranger := newTenant(t, env)
		if n, err := repo.SweepImport(ctx, stranger.user, second); err != nil || n != 0 {
			t.Errorf("SweepImport as another user = %d, %v; want nothing", n, err)
		}
		if n, err := repo.SweepImport(ctx, who.user, second); err != nil || n != 1 {
			t.Fatalf("SweepImport = %d, %v; want 1 (b)", n, err)
		}
		got := externalIDs(timeline(t, repo, app.Query{UserID: who.user}).Events)
		if want := []string{"post:c", "post:a"}; !reflect.DeepEqual(got, want) {
			t.Errorf("after the sweep: %v; want %v", got, want)
		}
	})

	t.Run("media and targets keep their order", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		ev := post("ordered", day(1), "T", "")
		for i := range 12 {
			n := strconv.Itoa(11 - i) // keys deliberately not in sorted order
			ev.Media = append(ev.Media, domain.Media{BlobKey: "m/" + n + ".jpg"})
			ev.Targets = append(ev.Targets, domain.Target{Relation: domain.RelationSaved, TargetURL: "https://x/" + n})
		}
		se := save(t, repo, who, ev)
		for range 3 { // a fresh read each time
			got, _, err := repo.Get(ctx, who.user, se.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Media, ev.Media) || !reflect.DeepEqual(got.Targets, ev.Targets) {
				t.Fatalf("children came back reordered:\n media %v\n targets %v", got.Media, got.Targets)
			}
		}
	})

	t.Run("Save upserts on (user, account, source, external id)", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		ev := post("a", day(1), "First title", "first body")
		ev.Actor, ev.ThreadExternalID, ev.ParentExternalID = "me", "thread:1", "post:0"
		ev.Metadata = map[string]any{"subreddit": "golang"}
		ev.Targets = []domain.Target{{Relation: domain.RelationRepliedTo, TargetSource: "reddit", TargetExternalID: "post:0", TargetURL: "https://example.com/0"}}
		ev.Media = []domain.Media{{BlobKey: "media/a.jpg", MIME: "image/jpeg", URL: "https://example.com/a.jpg"}}

		first, created, err := repo.Save(ctx, who.user, who.account, ev)
		if err != nil || !created {
			t.Fatalf("first Save = created %v, err %v; want true, nil", created, err)
		}
		if first.ID == "" || first.UserID != who.user || first.AccountID != who.account || first.IngestedAt.IsZero() {
			t.Errorf("stored event identity = %+v", first)
		}
		got, ok, err := repo.Get(ctx, who.user, first.ID)
		if err != nil || !ok {
			t.Fatalf("Get = %v,%v", ok, err)
		}
		if !got.OccurredAt.Equal(ev.OccurredAt) {
			t.Errorf("OccurredAt = %v, want %v", got.OccurredAt, ev.OccurredAt)
		}
		got.OccurredAt = ev.OccurredAt
		if !reflect.DeepEqual(got.Event, ev) {
			t.Errorf("event round trip:\n got %+v\nwant %+v", got.Event, ev)
		}
		if got.ImportJobID != "" {
			t.Errorf("ImportJobID = %q after a plain Save, want none", got.ImportJobID)
		}

		// Same key: refreshed in place — same id, same ingestion time, new content,
		// children replaced (not appended).
		ev.Title, ev.Targets, ev.Media, ev.Metadata = "Second title", nil, nil, nil
		second, created, err := repo.Save(ctx, who.user, who.account, ev)
		if err != nil || created {
			t.Fatalf("second Save = created %v, err %v; want false, nil", created, err)
		}
		if second.ID != first.ID {
			t.Errorf("re-saved event id = %q, want the original %q", second.ID, first.ID)
		}
		got, _, _ = repo.Get(ctx, who.user, first.ID)
		if got.Title != "Second title" || len(got.Targets) != 0 || len(got.Media) != 0 {
			t.Errorf("refreshed event = %+v; want the new title and no children", got.Event)
		}
		if !got.IngestedAt.Equal(first.IngestedAt) {
			t.Errorf("IngestedAt moved on refresh: %v -> %v", first.IngestedAt, got.IngestedAt)
		}
		if p := timeline(t, repo, app.Query{UserID: who.user}); p.Total != 1 || len(p.Events) != 1 {
			t.Errorf("after re-saving, total = %d (%d events); want 1", p.Total, len(p.Events))
		}

		// A different source or a different user is a different event.
		other := ev
		other.SourceID = "twitter"
		if _, created, err := repo.Save(ctx, who.user, who.account, other); err != nil || !created {
			t.Errorf("same external id, other source = created %v, err %v; want a new event", created, err)
		}
		stranger := newTenant(t, env)
		if se, created, err := repo.Save(ctx, stranger.user, stranger.account, ev); err != nil || !created || se.ID == first.ID {
			t.Errorf("same key, other user = id %q, created %v, err %v; want a new event", se.ID, created, err)
		}
		if p := timeline(t, repo, app.Query{UserID: who.user}); p.Total != 2 {
			t.Errorf("total = %d, want 2", p.Total)
		}
	})

	t.Run("unknown and malformed event ids are not found, not errors", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		for _, id := range []string{unknownID, malformedID, ""} {
			if _, ok, err := repo.Get(ctx, who.user, id); ok || err != nil {
				t.Errorf("Get(%q) = %v,%v; want false,nil", id, ok, err)
			}
		}
	})

	t.Run("SaveBatch upserts, counts new events and records provenance", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		job := env.NewJob(t, who.user)
		save(t, repo, who, post("a", day(1), "A", "old"))

		withChildren := post("b", day(2), "B final", "")
		withChildren.Targets = []domain.Target{{Relation: domain.RelationSaved, TargetURL: "http://x"}}
		withChildren.Media = []domain.Media{{BlobKey: "media/b.jpg", MIME: "image/jpeg"}}
		withChildren.Metadata = map[string]any{"k": "v"}
		batch := []domain.Event{
			post("a", day(1), "A", "new"),      // exists: refreshed
			post("b", day(2), "B draft", ""),   // repeated key in one batch:
			withChildren,                       // … the last one wins
			post("c", time.Time{}, "C", "new"), // undated
		}
		created, err := repo.SaveBatch(ctx, who.user, who.account, app.ImportRun{JobID: job}, batch)
		if err != nil {
			t.Fatal(err)
		}
		if created != 2 {
			t.Errorf("created = %d, want 2 (a existed; b appears twice but is one event)", created)
		}
		if again, err := repo.SaveBatch(ctx, who.user, who.account, app.ImportRun{JobID: job}, batch); err != nil || again != 0 {
			t.Errorf("repeated SaveBatch = %d,%v; want 0 new, nil", again, err)
		}
		if n, err := repo.SaveBatch(ctx, who.user, who.account, app.ImportRun{JobID: job}, nil); err != nil || n != 0 {
			t.Errorf("empty SaveBatch = %d,%v; want 0,nil", n, err)
		}
		p := timeline(t, repo, app.Query{UserID: who.user})
		if p.Total != 3 {
			t.Fatalf("total = %d, want 3", p.Total)
		}
		byExt := map[string]domain.StoredEvent{}
		for _, e := range p.Events {
			byExt[e.ExternalID] = e
			if e.ImportJobID != job {
				t.Errorf("%s: ImportJobID = %q, want the batch's import %q", e.ExternalID, e.ImportJobID, job)
			}
		}
		if byExt["post:a"].Body != "new" {
			t.Errorf("existing event was not refreshed: %+v", byExt["post:a"].Event)
		}
		b := byExt["post:b"]
		if b.Title != "B final" || len(b.Targets) != 1 || len(b.Media) != 1 || b.Metadata["k"] != "v" ||
			b.Targets[0].TargetURL != "http://x" || b.Media[0].BlobKey != "media/b.jpg" {
			t.Errorf("last repeat of a key must win, with its children: %+v", b.Event)
		}
		if got, ok, _ := repo.Get(ctx, who.user, b.ID); !ok || len(got.Targets) != 1 || got.ImportJobID != job {
			t.Errorf("Get of a batched event = %+v", got)
		}
	})

	t.Run("DeleteByImport removes only what the import still owns", func(t *testing.T) {
		repo, env := factory(t)
		who, stranger := newTenant(t, env), newTenant(t, env)
		first, second := env.NewJob(t, who.user), env.NewJob(t, who.user)
		seeded := save(t, repo, who, post("seed", day(1), "seeded, no import", ""))

		mustBatch(t, repo, who.user, who.account, first, post("a", day(2), "a", ""), post("b", day(3), "b", ""), post("x", day(4), "x", ""))
		mustBatch(t, repo, who.user, who.account, second, post("b", day(3), "b v2", ""), post("c", day(5), "c", ""))
		// A plain Save takes an event out of every import's ownership.
		save(t, repo, who, post("x", day(4), "x edited by hand", ""))

		// Nobody else can delete through the import id …
		if n, err := repo.DeleteByImport(ctx, stranger.user, first); err != nil || n != 0 {
			t.Errorf("stranger DeleteByImport = %d,%v; want 0,nil", n, err)
		}
		// … and ids that name no import delete nothing (in particular not the
		// events that belong to no import).
		for _, id := range []string{"", malformedID, unknownID} {
			if n, err := repo.DeleteByImport(ctx, who.user, id); err != nil || n != 0 {
				t.Errorf("DeleteByImport(%q) = %d,%v; want 0,nil", id, n, err)
			}
		}
		if p := timeline(t, repo, app.Query{UserID: who.user}); p.Total != 5 {
			t.Fatalf("total before delete = %d, want 5", p.Total)
		}

		n, err := repo.DeleteByImport(ctx, who.user, first)
		if err != nil || n != 1 {
			t.Errorf("DeleteByImport(first) = %d,%v; want 1 (only a: b was taken over by the second import, x by a Save)", n, err)
		}
		left := map[string]string{}
		for _, e := range timeline(t, repo, app.Query{UserID: who.user}).Events {
			left[e.ExternalID] = e.ImportJobID
		}
		want := map[string]string{"post:seed": "", "post:b": second, "post:c": second, "post:x": ""}
		if !reflect.DeepEqual(left, want) {
			t.Errorf("events left (external id -> import) = %v, want %v", left, want)
		}
		if _, ok, _ := repo.Get(ctx, who.user, seeded.ID); !ok {
			t.Error("an event without an import was deleted")
		}
		if n, _ := repo.DeleteByImport(ctx, who.user, first); n != 0 {
			t.Errorf("second DeleteByImport = %d, want 0", n)
		}
	})

	t.Run("tenant isolation", func(t *testing.T) {
		repo, env := factory(t)
		alice, bob := newTenant(t, env), newTenant(t, env)
		secret := save(t, repo, alice, post("secret", day(1), "alice only", "confidential"))
		save(t, repo, bob, post("mine", day(1), "bob only", ""))

		if _, ok, err := repo.Get(ctx, bob.user, secret.ID); ok || err != nil {
			t.Errorf("bob Get(alice's event) = %v,%v; want false,nil", ok, err)
		}
		for _, tc := range []struct {
			q         app.Query
			wantTotal int
		}{
			{app.Query{UserID: bob.user}, 1},
			{app.Query{UserID: bob.user, Sources: []string{"reddit"}}, 1},
			{app.Query{UserID: bob.user, Search: "confidential"}, 0},
		} {
			p := timeline(t, repo, tc.q)
			for _, e := range p.Events {
				if e.UserID != bob.user || e.ExternalID == "post:secret" {
					t.Errorf("bob's timeline %+v contains %+v", tc.q, e)
				}
			}
			if p.Total != tc.wantTotal {
				t.Errorf("bob's total for %+v = %d, want %d", tc.q, p.Total, tc.wantTotal)
			}
		}
		if p := timeline(t, repo, app.Query{UserID: bob.user}); len(p.Facets.Sources) != 1 || p.Facets.Sources[0].Count != 1 {
			t.Errorf("bob's facets count alice's events: %+v", p.Facets)
		}
		h, err := repo.Histogram(ctx, app.Query{UserID: bob.user})
		if err != nil || len(h) != 1 || h[0].Counts["reddit"] != 1 {
			t.Errorf("bob's histogram = %+v,%v; want his one event", h, err)
		}
		// A cursor minted for alice is only a position; it opens nothing.
		full := timeline(t, repo, app.Query{UserID: alice.user})
		cur := app.CursorAfter(full.Events[0]).Encode()
		for _, e := range timeline(t, repo, app.Query{UserID: bob.user, Cursor: cur}).Events {
			if e.UserID != bob.user {
				t.Errorf("alice's cursor showed bob %+v", e)
			}
		}
	})

	// The corpus for ordering and paging: two events share a timestamp and two
	// are undated, so every tie-break is exercised.
	corpus := func(t *testing.T, repo app.EventRepository, who tenant) {
		t.Helper()
		mustBatch(t, repo, who.user, who.account, "",
			post("old", day(1), "old", ""),
			post("undated-1", time.Time{}, "u1", ""),
			post("tie-1", day(3), "t1", ""),
			post("new", day(5), "new", ""),
			post("tie-2", day(3), "t2", ""),
			post("undated-2", time.Time{}, "u2", ""),
			post("tie-3", day(3), "t3", ""),
		)
	}

	t.Run("timeline order is (occurred_at desc, id desc), undated last", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		corpus(t, repo, who)
		p := timeline(t, repo, app.Query{UserID: who.user})
		if len(p.Events) != 7 || p.Total != 7 || p.NextCursor != "" {
			t.Fatalf("page = %d events, total %d, next %q; want 7, 7, none", len(p.Events), p.Total, p.NextCursor)
		}
		assertTimelineOrder(t, p.Events)
		if p.Events[0].ExternalID != "post:new" || p.Events[4].ExternalID != "post:old" {
			t.Errorf("dated order = %v", externalIDs(p.Events))
		}
		for _, e := range p.Events[5:] {
			if !e.OccurredAt.IsZero() {
				t.Errorf("tail of the timeline must be the undated events, got %v", externalIDs(p.Events))
			}
		}
	})

	t.Run("keyset pagination visits every event once, with stable cursors", func(t *testing.T) {
		for _, limit := range []int{1, 2, 3, 6, 7} {
			t.Run(fmt.Sprintf("limit %d", limit), func(t *testing.T) {
				repo, env := factory(t)
				who := newTenant(t, env)
				corpus(t, repo, who)
				want := externalIDs(timeline(t, repo, app.Query{UserID: who.user}).Events)

				var got []string
				cur, wantTotal := "", len(want)
				for page := 0; ; page++ {
					if page > len(want) {
						t.Fatal("pagination does not terminate")
					}
					p := timeline(t, repo, app.Query{UserID: who.user, Limit: limit, Cursor: cur})
					if p.Total != wantTotal {
						t.Errorf("page %d: total = %d, want %d (the whole set, on every page)", page, p.Total, wantTotal)
					}
					if len(p.Events) > limit {
						t.Errorf("page %d has %d events, limit %d", page, len(p.Events), limit)
					}
					got = append(got, externalIDs(p.Events)...)
					if page == 0 {
						// Keyset, not offset: an event arriving at the head between two
						// requests must not shift the following pages.
						save(t, repo, who, post("arrived-later", day(9), "late", ""))
						want = externalIDs(timeline(t, repo, app.Query{UserID: who.user}).Events)[1:]
						wantTotal++
					}
					if p.NextCursor == "" {
						break
					}
					if len(p.Events) != limit {
						t.Errorf("page %d is short (%d of %d) but has a next cursor", page, len(p.Events), limit)
					}
					// Asking again with the same cursor yields the same page.
					if cur != "" {
						again := timeline(t, repo, app.Query{UserID: who.user, Limit: limit, Cursor: cur})
						if !reflect.DeepEqual(externalIDs(again.Events), externalIDs(p.Events)) {
							t.Errorf("page %d is not repeatable", page)
						}
					}
					cur = p.NextCursor
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("paged   %v\nwant    %v", got, want)
				}
			})
		}
	})

	t.Run("cursors are the application's and compose with filters", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		corpus(t, repo, who)
		all := timeline(t, repo, app.Query{UserID: who.user}).Events
		// Any event is a valid position, in the dated part and in the undated tail.
		for i, e := range all {
			p := timeline(t, repo, app.Query{UserID: who.user, Cursor: app.CursorAfter(e).Encode()})
			if !reflect.DeepEqual(externalIDs(p.Events), externalIDs(all[i+1:])) {
				t.Errorf("after %s: %v, want %v", e.ExternalID, externalIDs(p.Events), externalIDs(all[i+1:]))
			}
		}
		p := timeline(t, repo, app.Query{UserID: who.user, Limit: 2})
		c, err := app.DecodeCursor(p.NextCursor)
		if err != nil || c.ID != p.Events[1].ID {
			t.Errorf("NextCursor = %+v,%v; want the application's encoding of the page's last event", c, err)
		}
		// The store holds microseconds at best; a position between two events
		// works as well as one on an event.
		between := app.Cursor{OccurredAt: day(4), ID: unknownID}.Encode()
		if got := externalIDs(timeline(t, repo, app.Query{UserID: who.user, Cursor: between, Limit: 1}).Events); len(got) != 1 || got[0][:8] != "post:tie" {
			t.Errorf("first event after day 4 = %v, want one of the day-3 ties", got)
		}
	})

	t.Run("a malformed cursor is ErrBadCursor", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		corpus(t, repo, who)
		b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
		for name, cur := range map[string]string{
			"not base64":     "!!!",
			"unknown layout": b64("X|" + unknownID),
			"bad timestamp":  b64("D|yesterday|" + unknownID),
			"bad dated id":   b64("D|2023-01-01T00:00:00Z|" + malformedID),
			"bad undated id": b64("U|'; DROP TABLE events; --"),
			"empty id":       b64("U|"),
		} {
			if _, err := repo.Timeline(ctx, app.Query{UserID: who.user, Cursor: cur}); !errors.Is(err, app.ErrBadCursor) {
				t.Errorf("%s: err = %v, want ErrBadCursor", name, err)
			}
		}
	})

	t.Run("page size follows app.ClampLimit", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		n := app.MaxPageLimit + 20
		evs := make([]domain.Event, n)
		for i := range evs {
			evs[i] = post(fmt.Sprintf("bulk-%04d", i), day(1).Add(time.Duration(i)*time.Minute), "bulk", "")
		}
		mustBatch(t, repo, who.user, who.account, "", evs...)
		for limit, want := range map[int]int{
			0: app.DefaultPageLimit, -5: app.DefaultPageLimit, 7: 7,
			app.MaxPageLimit: app.MaxPageLimit, app.MaxPageLimit + 1: app.MaxPageLimit, 1 << 30: app.MaxPageLimit,
		} {
			p := timeline(t, repo, app.Query{UserID: who.user, Limit: limit})
			if len(p.Events) != want || p.Total != n || p.NextCursor == "" {
				t.Errorf("limit %d: %d events, total %d, next %q; want %d events, total %d and a next cursor",
					limit, len(p.Events), p.Total, p.NextCursor, want, n)
			}
		}
	})

	// The corpus for filters, facets and the histogram.
	filterCorpus := func(t *testing.T, repo app.EventRepository, who tenant) {
		t.Helper()
		mustBatch(t, repo, who.user, who.account, "",
			domain.Event{
				SourceID: "reddit", ExternalID: "r1", Type: "reddit.post", Category: domain.CategoryContent,
				OccurredAt: day(1), Title: "Hello World", Body: "the quick brown fox",
			},
			domain.Event{
				SourceID: "reddit", ExternalID: "r2", Type: "reddit.comment", Category: domain.CategoryContent,
				OccurredAt: day(2), Title: "", Body: "Hello again, brown bear",
			},
			domain.Event{
				SourceID: "reddit", ExternalID: "r3", Type: "reddit.vote.up", Category: domain.CategoryReaction,
				OccurredAt: day(2).Add(time.Hour),
			},
			domain.Event{
				SourceID: "twitter", ExternalID: "t1", Type: "twitter.tweet", Category: domain.CategoryContent,
				OccurredAt: day(3), Title: "Quick thought", Body: "helloworld is one word",
			},
			domain.Event{
				SourceID: "twitter", ExternalID: "t2", Type: "twitter.like", Category: domain.CategoryReaction,
				Body: "undated like of a fox",
			},
		)
	}

	t.Run("filters are ANDed, values within a filter ORed", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		filterCorpus(t, repo, who)
		for name, tc := range map[string]struct {
			q    app.Query
			want []string
		}{
			"no filter":        {app.Query{}, []string{"t1", "r3", "r2", "r1", "t2"}},
			"one source":       {app.Query{Sources: []string{"twitter"}}, []string{"t1", "t2"}},
			"two sources":      {app.Query{Sources: []string{"twitter", "reddit"}}, []string{"t1", "r3", "r2", "r1", "t2"}},
			"unknown source":   {app.Query{Sources: []string{"myspace"}}, nil},
			"category":         {app.Query{Categories: []string{"reaction"}}, []string{"r3", "t2"}},
			"types":            {app.Query{Types: []string{"reddit.post", "twitter.like"}}, []string{"r1", "t2"}},
			"source+category":  {app.Query{Sources: []string{"reddit"}, Categories: []string{"reaction"}}, []string{"r3"}},
			"contradiction":    {app.Query{Sources: []string{"reddit"}, Types: []string{"twitter.tweet"}}, nil},
			"from (inclusive)": {app.Query{From: day(2)}, []string{"t1", "r3", "r2"}},
			"to (inclusive)":   {app.Query{To: day(2)}, []string{"r2", "r1"}},
			"from+to":          {app.Query{From: day(2), To: day(2).Add(time.Hour)}, []string{"r3", "r2"}},
			"empty range":      {app.Query{From: day(10), To: day(11)}, nil},
			"range+source":     {app.Query{From: day(1), To: day(9), Sources: []string{"twitter"}}, []string{"t1"}},
			"search+category":  {app.Query{Search: "fox", Categories: []string{"reaction"}}, []string{"t2"}},
			"search+range":     {app.Query{Search: "fox", From: day(1)}, []string{"r1"}},
		} {
			tc.q.UserID = who.user
			p := timeline(t, repo, tc.q)
			if got := externalIDs(p.Events); !reflect.DeepEqual(got, tc.want) && (len(got) != 0 || len(tc.want) != 0) {
				t.Errorf("%s: events = %v, want %v", name, got, tc.want)
			}
			if p.Total != len(tc.want) {
				t.Errorf("%s: total = %d, want %d", name, p.Total, len(tc.want))
			}
			if p.Events == nil {
				t.Errorf("%s: Events is nil, want an empty slice", name)
			}
		}
	})

	t.Run("search is case-insensitive whole-word AND over title and body", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		filterCorpus(t, repo, who)
		for query, want := range map[string][]string{
			"hello":         {"r2", "r1"},
			"HELLO":         {"r2", "r1"},
			"  hello  ":     {"r2", "r1"},
			"hello brown":   {"r2", "r1"},
			"brown hello":   {"r2", "r1"},
			"hello   fox":   {"r1"},       // terms may be split over title and body
			"quick":         {"t1", "r1"}, // title or body
			"hello, brown!": {"r2", "r1"}, // punctuation separates words, nothing more
			"hell":          nil,          // whole words: no prefix match …
			"world":         {"r1"},       // … and no match inside "helloworld"
			"helloworld":    {"t1"},       //
			"hello penguin": nil,          // every term must match
			"fox":           {"r1", "t2"}, // undated events are searchable
			"!!!":           nil,          // no word at all: matches nothing
			"":              {"t1", "r3", "r2", "r1", "t2"},
			"   ":           {"t1", "r3", "r2", "r1", "t2"}, // blank = no search
		} {
			p := timeline(t, repo, app.Query{UserID: who.user, Search: query})
			if got := externalIDs(p.Events); !reflect.DeepEqual(got, want) && (len(got) != 0 || len(want) != 0) {
				t.Errorf("search %q: %v, want %v", query, got, want)
			}
			if p.Total != len(want) {
				t.Errorf("search %q: total = %d, want %d", query, p.Total, len(want))
			}
		}
	})

	t.Run("total and facets describe the filtered set, not the page", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		filterCorpus(t, repo, who)

		p := timeline(t, repo, app.Query{UserID: who.user, Limit: 1})
		if p.Total != 5 || len(p.Events) != 1 {
			t.Fatalf("total %d, %d events; want 5, 1", p.Total, len(p.Events))
		}
		wantFacets := app.Facets{
			// count desc, then value asc
			Sources:    []app.FacetCount{{Value: "reddit", Count: 3}, {Value: "twitter", Count: 2}},
			Categories: []app.FacetCount{{Value: "content", Count: 3}, {Value: "reaction", Count: 2}},
			Types: []app.FacetCount{
				{Value: "reddit.comment", Count: 1},
				{Value: "reddit.post", Count: 1},
				{Value: "reddit.vote.up", Count: 1},
				{Value: "twitter.like", Count: 1},
				{Value: "twitter.tweet", Count: 1},
			},
		}
		if !reflect.DeepEqual(p.Facets, wantFacets) {
			t.Errorf("facets = %+v\n   want %+v", p.Facets, wantFacets)
		}
		// Later pages report the same facets and total.
		p2 := timeline(t, repo, app.Query{UserID: who.user, Limit: 1, Cursor: p.NextCursor})
		if p2.Total != 5 || !reflect.DeepEqual(p2.Facets, wantFacets) {
			t.Errorf("page 2: total %d, facets %+v; want those of page 1", p2.Total, p2.Facets)
		}
		// SkipSummary spares them: total 0, empty (non-nil) facets, the same page.
		p3 := timeline(t, repo, app.Query{UserID: who.user, Limit: 1, Cursor: p.NextCursor, SkipSummary: true})
		if p3.Total != 0 || !reflect.DeepEqual(p3.Facets, app.EmptyFacets()) {
			t.Errorf("SkipSummary: total %d, facets %#v; want 0 and empty facets", p3.Total, p3.Facets)
		}
		if len(p3.Events) != 1 || p3.Events[0].ID != p2.Events[0].ID || p3.NextCursor != p2.NextCursor {
			t.Errorf("SkipSummary changed the page: %v / %q", p3.Events, p3.NextCursor)
		}

		f := timeline(t, repo, app.Query{UserID: who.user, Categories: []string{"reaction"}})
		if want := []app.FacetCount{{Value: "reddit", Count: 1}, {Value: "twitter", Count: 1}}; !reflect.DeepEqual(f.Facets.Sources, want) {
			t.Errorf("filtered source facet = %+v, want %+v", f.Facets.Sources, want)
		}
		none := timeline(t, repo, app.Query{UserID: who.user, Sources: []string{"myspace"}})
		if none.Facets.Sources == nil || none.Facets.Categories == nil || none.Facets.Types == nil ||
			len(none.Facets.Sources)+len(none.Facets.Categories)+len(none.Facets.Types) != 0 {
			t.Errorf("facets of an empty result = %#v; want empty, non-nil slices", none.Facets)
		}
	})

	t.Run("histogram counts dated events per UTC day and source", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		filterCorpus(t, repo, who)
		// 23:30 on day 3 in UTC-2 is 01:30 on day 4 in UTC.
		west := time.FixedZone("west", -2*3600)
		save(t, repo, who, domain.Event{
			SourceID: "reddit", ExternalID: "tz", Type: "reddit.post", Category: domain.CategoryContent,
			OccurredAt: time.Date(2023, 1, 3, 23, 30, 0, 0, west), Title: "late",
		})

		h, err := repo.Histogram(ctx, app.Query{UserID: who.user})
		if err != nil {
			t.Fatal(err)
		}
		want := []app.HistogramBucket{
			{Day: "2023-01-01", Counts: map[string]int{"reddit": 1}},
			{Day: "2023-01-02", Counts: map[string]int{"reddit": 2}},
			{Day: "2023-01-03", Counts: map[string]int{"twitter": 1}},
			{Day: "2023-01-04", Counts: map[string]int{"reddit": 1}},
		}
		if !reflect.DeepEqual(h, want) {
			t.Errorf("histogram = %+v\n     want %+v (ascending days, the undated like left out)", h, want)
		}
		h, _ = repo.Histogram(ctx, app.Query{UserID: who.user, Search: "hello", Sources: []string{"reddit"}, From: day(2)})
		if want := []app.HistogramBucket{{Day: "2023-01-02", Counts: map[string]int{"reddit": 1}}}; !reflect.DeepEqual(h, want) {
			t.Errorf("filtered histogram = %+v, want %+v", h, want)
		}
		// Pagination fields do not apply to a histogram.
		h, err = repo.Histogram(ctx, app.Query{UserID: who.user, Limit: 1})
		if err != nil || len(h) != 4 {
			t.Errorf("histogram with a limit = %d days, %v; want all 4", len(h), err)
		}
		h, err = repo.Histogram(ctx, app.Query{UserID: who.user, Sources: []string{"myspace"}})
		if err != nil || h == nil || len(h) != 0 {
			t.Errorf("empty histogram = %#v,%v; want an empty, non-nil slice", h, err)
		}
	})

	t.Run("histogram by category counts the same events per category", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		filterCorpus(t, repo, who)
		h, err := repo.HistogramByCategory(ctx, app.Query{UserID: who.user})
		if err != nil {
			t.Fatal(err)
		}
		want := []app.HistogramBucket{
			{Day: "2023-01-01", Counts: map[string]int{"content": 1}},
			{Day: "2023-01-02", Counts: map[string]int{"content": 1, "reaction": 1}},
			{Day: "2023-01-03", Counts: map[string]int{"content": 1}},
		}
		if !reflect.DeepEqual(h, want) {
			t.Errorf("histogram by category = %+v\n                 want %+v", h, want)
		}
		h, _ = repo.HistogramByCategory(ctx, app.Query{UserID: who.user, Sources: []string{"reddit"}, From: day(2), Limit: 1})
		if want := []app.HistogramBucket{{Day: "2023-01-02", Counts: map[string]int{"content": 1, "reaction": 1}}}; !reflect.DeepEqual(h, want) {
			t.Errorf("filtered histogram by category = %+v, want %+v", h, want)
		}
		h, err = repo.HistogramByCategory(ctx, app.Query{UserID: who.user, Sources: []string{"myspace"}})
		if err != nil || h == nil || len(h) != 0 {
			t.Errorf("empty histogram by category = %#v,%v; want an empty, non-nil slice", h, err)
		}
	})

	// --- time zones: the day boundary is the VIEWER'S ---
	//
	// Events are stored as instants; a "day" only exists once someone looks at
	// them from somewhere. Query.Loc says from where, and every day a store
	// draws — a histogram bucket, the collapse rule's "same day" — must be
	// drawn there. The cases below are deliberately the awkward ones: either
	// side of local midnight, a spring-forward day (23 h), a fall-back day
	// (25 h), a southern-hemisphere zone whose DST runs the other way round,
	// and a zone half an hour off the hour. Every adapter runs this same
	// fixture against these same expectations, which is what makes "memory and
	// PostgreSQL agree about days" a checked fact rather than a hope.

	t.Run("histogram buckets by the query's zone", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		mustBatch(t, repo, who.user, who.account, "", tzCorpus()...)

		for _, tc := range []struct {
			zone string
			want []app.HistogramBucket
		}{
			// No zone (nil Loc) is UTC: exactly what every caller got before
			// zones existed.
			{"", []app.HistogramBucket{
				{Day: "2024-01-15", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-03-30", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-03-31", Counts: map[string]int{"reddit": 2}},
				{Day: "2024-05-10", Counts: map[string]int{"reddit": 2}},
				{Day: "2024-07-15", Counts: map[string]int{"reddit": 2}},
				{Day: "2024-10-26", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-10-27", Counts: map[string]int{"reddit": 2}},
			}},
			{"UTC", []app.HistogramBucket{
				{Day: "2024-01-15", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-03-30", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-03-31", Counts: map[string]int{"reddit": 2}},
				{Day: "2024-05-10", Counts: map[string]int{"reddit": 2}},
				{Day: "2024-07-15", Counts: map[string]int{"reddit": 2}},
				{Day: "2024-10-26", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-10-27", Counts: map[string]int{"reddit": 2}},
			}},
			// Berlin: b1 (22:30 UTC the evening before) joins the 31st, b3
			// (22:10 UTC) leaves it for April — the 23-hour day. The 27th of
			// October is 25 hours long and holds b4 and b5.
			{"Europe/Berlin", []app.HistogramBucket{
				{Day: "2024-01-15", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-03-31", Counts: map[string]int{"reddit": 2}},
				{Day: "2024-04-01", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-05-10", Counts: map[string]int{"reddit": 2}},
				{Day: "2024-07-15", Counts: map[string]int{"reddit": 2}},
				{Day: "2024-10-27", Counts: map[string]int{"reddit": 2}},
				{Day: "2024-10-28", Counts: map[string]int{"reddit": 1}},
			}},
			// Auckland: ahead of UTC, and on DST in January while Berlin is not
			// (+13 in January, +12 in July).
			{"Pacific/Auckland", []app.HistogramBucket{
				{Day: "2024-01-16", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-03-31", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-04-01", Counts: map[string]int{"reddit": 2}},
				{Day: "2024-05-11", Counts: map[string]int{"reddit": 2}},
				{Day: "2024-07-15", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-07-16", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-10-27", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-10-28", Counts: map[string]int{"reddit": 2}},
			}},
			// Kolkata: +05:30 all year — the day boundary is at 18:30 UTC, so
			// k1 (18:25) and k2 (18:35) fall on different days. Nothing may
			// assume whole-hour offsets.
			{"Asia/Kolkata", []app.HistogramBucket{
				{Day: "2024-01-15", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-03-31", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-04-01", Counts: map[string]int{"reddit": 2}},
				{Day: "2024-05-10", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-05-11", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-07-15", Counts: map[string]int{"reddit": 2}},
				{Day: "2024-10-27", Counts: map[string]int{"reddit": 1}},
				{Day: "2024-10-28", Counts: map[string]int{"reddit": 2}},
			}},
		} {
			t.Run(orUTC(tc.zone), func(t *testing.T) {
				q := app.Query{UserID: who.user, Loc: zone(t, tc.zone)}
				h, err := repo.Histogram(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(h, tc.want) {
					t.Errorf("histogram in %q =\n %+v\nwant %+v", orUTC(tc.zone), h, tc.want)
				}
				// Every event is a reddit post, so keying by category must
				// produce the very same days with the very same counts.
				byCat, err := repo.HistogramByCategory(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				if len(byCat) != len(h) {
					t.Fatalf("histogram by category has %d days, by source %d", len(byCat), len(h))
				}
				for i := range h {
					if byCat[i].Day != h[i].Day || byCat[i].Counts["content"] != h[i].Counts["reddit"] {
						t.Errorf("day %d: by category %+v, by source %+v", i, byCat[i], h[i])
					}
				}
			})
		}
	})

	t.Run("a date-only range in a zone selects exactly that local day", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		mustBatch(t, repo, who.user, who.account, "", tzCorpus()...)
		// Also: an event at exactly the next local midnight, which the upper
		// bound must NOT reach.
		mustBatch(t, repo, who.user, who.account, "",
			post("midnight", mustTime("2024-03-31T22:00:00Z"), "first instant of April in Berlin", ""))

		for _, tc := range []struct {
			zone, day string
			want      []string // external ids, newest first
		}{
			// 23 hours long in Berlin: starts an hour "early" in UTC terms and
			// ends an hour early too.
			{"Europe/Berlin", "2024-03-31", []string{"post:b2", "post:b1"}},
			{"UTC", "2024-03-31", []string{"post:b3", "post:midnight", "post:b2"}},
			// 25 hours long in Berlin.
			{"Europe/Berlin", "2024-10-27", []string{"post:b5", "post:b4"}},
			{"UTC", "2024-10-27", []string{"post:b6", "post:b5"}},
			// Half-hour offset: the day boundary is 18:30 UTC.
			{"Asia/Kolkata", "2024-05-10", []string{"post:k1"}},
			{"Asia/Kolkata", "2024-05-11", []string{"post:k2"}},
			{"UTC", "2024-05-10", []string{"post:k2", "post:k1"}},
			// Southern hemisphere, a day ahead.
			{"Pacific/Auckland", "2024-01-16", []string{"post:a1"}},
			{"Pacific/Auckland", "2024-01-15", nil},
		} {
			t.Run(tc.zone+" "+tc.day, func(t *testing.T) {
				loc := zone(t, tc.zone)
				from, to := localDayRange(t, tc.day, loc)
				p := timeline(t, repo, app.Query{UserID: who.user, From: from, To: to, Loc: loc})
				if got := externalIDs(p.Events); !reflect.DeepEqual(got, tc.want) {
					t.Errorf("%s in %s = %v, want %v", tc.day, tc.zone, got, tc.want)
				}
				if p.Total != len(tc.want) {
					t.Errorf("total = %d, want %d", p.Total, len(tc.want))
				}
				// The range and the histogram must tell the same story: the
				// day's bucket holds exactly the events the range returns.
				h, err := repo.Histogram(ctx, app.Query{UserID: who.user, Loc: loc})
				if err != nil {
					t.Fatal(err)
				}
				n := 0
				for _, b := range h {
					if b.Day == tc.day {
						n = b.Counts["reddit"]
					}
				}
				if n != len(tc.want) {
					t.Errorf("histogram bucket %s in %s = %d events, the range returns %d", tc.day, tc.zone, n, len(tc.want))
				}
			})
		}
	})

	// --- collapsed timeline (app.CollapseRule) ---

	collapsed := func(t *testing.T, repo app.EventRepository, q app.Query, rule app.CollapseRule) app.CollapsedPage {
		t.Helper()
		p, err := repo.Collapsed(ctx, q, rule)
		if err != nil {
			t.Fatalf("Collapsed(%+v, %+v): %v", q, rule, err)
		}
		if p.Items == nil {
			t.Fatal("Items is nil, want an empty slice")
		}
		return p
	}
	rule := app.DefaultCollapseRule()
	// allEvents pages through the whole plain timeline of q: the reference
	// the collapsed pages are checked against.
	allEvents := func(t *testing.T, repo app.EventRepository, q app.Query) []domain.StoredEvent {
		t.Helper()
		var out []domain.StoredEvent
		q.Limit = app.MaxPageLimit
		for {
			p := timeline(t, repo, q)
			out = append(out, p.Events...)
			if p.NextCursor == "" {
				return out
			}
			q.Cursor = p.NextCursor
		}
	}

	t.Run("collapsed: runs at or above the category's threshold become clusters", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		// Four stretches, hours apart so they never merge (newest first): 3
		// plays, 2 plays, 8 upvotes, 7 upvotes.
		var evs []domain.Event
		evs = append(evs, plays(day(1).Add(10*time.Hour), 3, "a")...)
		evs = append(evs, plays(day(1).Add(9*time.Hour), 2, "b")...)
		evs = append(evs, votes(day(1).Add(8*time.Hour), 8, "c")...)
		evs = append(evs, votes(day(1).Add(7*time.Hour), 7, "d")...)
		mustBatch(t, repo, who.user, who.account, "", evs...)
		all := allEvents(t, repo, app.Query{UserID: who.user})

		p := collapsed(t, repo, app.Query{UserID: who.user}, rule)
		if got := kinds(p.Items); !reflect.DeepEqual(got, []string{"cluster", "event", "event", "cluster", "event", "event", "event", "event", "event", "event", "event"}) {
			t.Fatalf("items = %v", got)
		}
		if p.NextCursor != "" || p.Total != 20 {
			t.Errorf("next %q, total %d; want none, 20", p.NextCursor, p.Total)
		}
		checkCollapsedPages(t, rule, nil, all, [][]app.CollapsedItem{p.Items})
		c := p.Items[0].Cluster
		if c.SourceID != "spotify" || c.Type != "spotify.play" || c.Category != domain.CategoryConsumption || c.Count != 3 ||
			c.FirstID != all[0].ID || c.LastID != all[2].ID || !c.FirstAt.Equal(all[0].OccurredAt) || !c.LastAt.Equal(all[2].OccurredAt) {
			t.Errorf("play cluster = %+v", *c)
		}
		if !c.FirstAt.After(c.LastAt) {
			t.Errorf("FirstAt %v is the newest member, LastAt %v the oldest", c.FirstAt, c.LastAt)
		}
		if v := p.Items[3].Cluster; v.Type != "reddit.vote.up" || v.Count != 8 || v.Category != domain.CategoryReaction {
			t.Errorf("vote cluster = %+v", *v)
		}
		// Facets and total are those of the events, as on a plain page.
		if plain := timeline(t, repo, app.Query{UserID: who.user}); !reflect.DeepEqual(p.Facets, plain.Facets) {
			t.Errorf("facets = %+v, want the timeline's %+v", p.Facets, plain.Facets)
		}
	})

	t.Run("collapsed: a gap above the rule's splits a run; gap 0 never does", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		at := day(1).Add(12 * time.Hour)
		mustBatch(t, repo, who.user, who.account, "",
			play("p1", at, "one"), play("p2", at.Add(10*time.Minute), "two"), play("p3", at.Add(20*time.Minute), "three"),
			play("p4", at.Add(51*time.Minute), "four"), play("p5", at.Add(52*time.Minute), "five"), play("p6", at.Add(53*time.Minute), "six"),
		)
		all := allEvents(t, repo, app.Query{UserID: who.user})
		for gap, want := range map[time.Duration][]string{
			30 * time.Minute: {"cluster", "cluster"},                                 // 31 minutes between p3 and p4
			31 * time.Minute: {"cluster"},                                            // the gap is inclusive
			0:                {"cluster"},                                            // no gap limit
			5 * time.Minute:  {"cluster", "event", "event", "event"},                 // p4..p6 stay, p1..p3 fall apart
			time.Second:      {"event", "event", "event", "event", "event", "event"}, // a run of one is an event
		} {
			r := app.CollapseRule{Gap: gap}
			p := collapsed(t, repo, app.Query{UserID: who.user}, r)
			if got := kinds(p.Items); !reflect.DeepEqual(got, want) {
				t.Errorf("gap %v: items = %v, want %v", gap, got, want)
			}
			checkCollapsedPages(t, r, nil, all, [][]app.CollapsedItem{p.Items})
		}
	})

	t.Run("collapsed: a run never crosses a day, UTC when the query names no zone", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		// 23:58 and 23:59 on day 1, then 00:00, 00:01, 00:02 on day 2 — one
		// minute apart throughout. The last one is written in UTC-2, where it is
		// still day 1: a zoneless query draws the boundary in UTC, whatever
		// zone the event was written in.
		west := time.FixedZone("west", -2*3600)
		mustBatch(t, repo, who.user, who.account, "",
			play("p1", day(2).Add(-2*time.Minute), "a"), play("p2", day(2).Add(-time.Minute), "b"),
			play("p3", day(2), "c"), play("p4", day(2).Add(time.Minute), "d"),
			play("p5", time.Date(2023, 1, 1, 22, 2, 0, 0, west), "e"),
		)
		all := allEvents(t, repo, app.Query{UserID: who.user})
		for _, r := range []app.CollapseRule{rule, {Gap: 0}} {
			p := collapsed(t, repo, app.Query{UserID: who.user}, r)
			if got := kinds(p.Items); !reflect.DeepEqual(got, []string{"cluster", "event", "event"}) {
				t.Errorf("gap %v: items = %v, want the day-2 cluster and the two day-1 events", r.Gap, got)
			}
			checkCollapsedPages(t, r, nil, all, [][]app.CollapsedItem{p.Items})
		}
	})

	t.Run("collapsed: runs split at midnight in the query's zone", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		// Five plays five minutes apart across 22:00 UTC on 2024-03-31, which
		// is midnight in Berlin that night (the clocks went forward that
		// morning, so Berlin is +02:00 by then). In UTC they are one run on one
		// day; in Berlin the newest three belong to April and the older two to
		// March, and the March pair is below the threshold for a cluster.
		mustBatch(t, repo, who.user, who.account, "",
			play("p1", mustTime("2024-03-31T21:50:00Z"), "a"),
			play("p2", mustTime("2024-03-31T21:55:00Z"), "b"),
			play("p3", mustTime("2024-03-31T22:00:00Z"), "c"),
			play("p4", mustTime("2024-03-31T22:05:00Z"), "d"),
			play("p5", mustTime("2024-03-31T22:10:00Z"), "e"),
		)
		all := allEvents(t, repo, app.Query{UserID: who.user})
		for _, tc := range []struct {
			zone string
			want []string
		}{
			{"", []string{"cluster"}},
			{"UTC", []string{"cluster"}},
			{"Europe/Berlin", []string{"cluster", "event", "event"}},
			// +05:30: 22:00 UTC is 03:30 on 1 April, so the whole run is one
			// April day there.
			{"Asia/Kolkata", []string{"cluster"}},
			// +13:00: 1 April all along, and its midnight (11:00 UTC on
			// 31 March) is long past.
			{"Pacific/Auckland", []string{"cluster"}},
		} {
			t.Run(orUTC(tc.zone), func(t *testing.T) {
				loc := zone(t, tc.zone)
				q := app.Query{UserID: who.user, Loc: loc}
				for _, r := range []app.CollapseRule{rule, {Gap: 0}} {
					p := collapsed(t, repo, q, r)
					if got := kinds(p.Items); !reflect.DeepEqual(got, tc.want) {
						t.Errorf("gap %v in %s: items = %v, want %v", r.Gap, orUTC(tc.zone), got, tc.want)
					}
					checkCollapsedPages(t, r, loc, all, [][]app.CollapsedItem{p.Items})
				}
			})
		}
	})

	t.Run("collapsed: filters apply before runs are formed, also to expanded ones", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		at := day(1).Add(12 * time.Hour)
		// play, vote, play, play, play, vote, play, play: without a filter the
		// votes break the plays into 1 + 3 + 2; filtered to plays they are one
		// run of 6.
		mustBatch(t, repo, who.user, who.account, "",
			play("p1", at, "a"), vote("v1", at.Add(time.Minute)),
			play("p2", at.Add(2*time.Minute), "b"), play("p3", at.Add(3*time.Minute), "c"), play("p4", at.Add(4*time.Minute), "d"),
			vote("v2", at.Add(5*time.Minute)),
			play("p5", at.Add(6*time.Minute), "e"), play("p6", at.Add(7*time.Minute), "f"),
		)
		all := allEvents(t, repo, app.Query{UserID: who.user})
		p := collapsed(t, repo, app.Query{UserID: who.user}, rule)
		if got := kinds(p.Items); !reflect.DeepEqual(got, []string{"event", "event", "event", "cluster", "event", "event"}) {
			t.Errorf("unfiltered items = %v", got)
		}
		checkCollapsedPages(t, rule, nil, all, [][]app.CollapsedItem{p.Items})

		q := app.Query{UserID: who.user, Types: []string{"spotify.play"}}
		onlyPlays := allEvents(t, repo, q)
		p = collapsed(t, repo, q, rule)
		if got := kinds(p.Items); !reflect.DeepEqual(got, []string{"cluster"}) || p.Items[0].Cluster.Count != 6 || p.Total != 6 {
			t.Errorf("filtered to plays: items = %v, total %d; want one cluster of 6", got, p.Total)
		}
		checkCollapsedPages(t, rule, nil, onlyPlays, [][]app.CollapsedItem{p.Items})

		// A short run whose members straddle a filtered-out event: the event in
		// between must not come back with them.
		q = app.Query{UserID: who.user, Types: []string{"spotify.play"}, To: at.Add(2 * time.Minute)}
		p = collapsed(t, repo, q, rule)
		if got := externalIDs(eventsOf(p.Items)); !reflect.DeepEqual(got, []string{"p2", "p1"}) || p.Total != 2 {
			t.Errorf("plays up to +2m = %v (total %d), want [p2 p1] — v1 lies between them but is filtered out", got, p.Total)
		}
		checkCollapsedPages(t, rule, nil, allEvents(t, repo, q), [][]app.CollapsedItem{p.Items})
	})

	t.Run("collapsed: sample titles are the first three distinct ones, body when there is no title", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		at := day(1).Add(12 * time.Hour)
		untitled := play("p3", at.Add(-2*time.Minute), "")
		untitled.Body = "Bee"
		blank := play("p2", at.Add(-time.Minute), "")
		mustBatch(t, repo, who.user, who.account, "",
			play("p1", at, "Alpha"), blank, untitled, play("p4", at.Add(-3*time.Minute), "Alpha"),
			play("p5", at.Add(-4*time.Minute), "Gamma"), play("p6", at.Add(-5*time.Minute), "Delta"),
			play("p7", at.Add(-6*time.Minute), strings.Repeat("x", app.SampleTitleLen+50)),
		)
		p := collapsed(t, repo, app.Query{UserID: who.user}, rule)
		if len(p.Items) != 1 || p.Items[0].Cluster == nil {
			t.Fatalf("items = %v, want one cluster", kinds(p.Items))
		}
		if got := p.Items[0].Cluster.SampleTitles; !reflect.DeepEqual(got, []string{"Alpha", "Bee", "Gamma"}) {
			t.Errorf("sample titles = %q, want [Alpha Bee Gamma]: newest first, distinct, blanks skipped, body when untitled", got)
		}
		// A run of untitled events has an empty, non-nil sample list.
		two, env2 := factory(t)
		other := newTenant(t, env2)
		mustBatch(t, two, other.user, other.account, "", play("q1", at, ""), play("q2", at.Add(time.Minute), ""), play("q3", at.Add(2*time.Minute), ""))
		p = collapsed(t, two, app.Query{UserID: other.user}, rule)
		if len(p.Items) != 1 || p.Items[0].Cluster == nil || p.Items[0].Cluster.SampleTitles == nil || len(p.Items[0].Cluster.SampleTitles) != 0 {
			t.Errorf("untitled cluster = %+v, want empty non-nil samples", p.Items)
		}
		// Long titles are cut.
		three, env3 := factory(t)
		third := newTenant(t, env3)
		long := strings.Repeat("y", app.SampleTitleLen+50)
		mustBatch(t, three, third.user, third.account, "", play("r1", at, long), play("r2", at.Add(time.Minute), long), play("r3", at.Add(2*time.Minute), long))
		p = collapsed(t, three, app.Query{UserID: third.user}, rule)
		if got := p.Items[0].Cluster.SampleTitles; len(got) != 1 || got[0] != long[:app.SampleTitleLen] {
			t.Errorf("long sample title = %d chars, want %d", len(got[0]), app.SampleTitleLen)
		}
	})

	// pagingCorpus mixes clusters, singles, short runs and an undated tail:
	// (newest first) 4 plays · 1 post · 2 plays · 9 votes · 1 play · 3 undated
	// plays. (Undated events order by store-issued id, so the tail holds one
	// run only.)
	pagingCorpus := func(t *testing.T, repo app.EventRepository, who tenant) {
		t.Helper()
		var evs []domain.Event
		evs = append(evs, plays(day(3).Add(10*time.Hour), 4, "a")...)
		evs = append(evs, post("mid", day(3).Add(9*time.Hour), "a post", ""))
		evs = append(evs, plays(day(3).Add(8*time.Hour), 2, "b")...)
		evs = append(evs, votes(day(2).Add(8*time.Hour), 9, "c")...)
		evs = append(evs, play("lone", day(1), "lone"))
		evs = append(evs, play("u1", time.Time{}, "u1"), play("u2", time.Time{}, "u2"), play("u3", time.Time{}, "u3"))
		mustBatch(t, repo, who.user, who.account, "", evs...)
	}

	t.Run("collapsed: pages of mixed items visit every event once, in order", func(t *testing.T) {
		for _, limit := range []int{1, 2, 3, 5, 50} {
			t.Run(fmt.Sprintf("limit %d", limit), func(t *testing.T) {
				repo, env := factory(t)
				who := newTenant(t, env)
				pagingCorpus(t, repo, who)
				all := allEvents(t, repo, app.Query{UserID: who.user})
				plain := timeline(t, repo, app.Query{UserID: who.user})

				var pages [][]app.CollapsedItem
				cur := ""
				for n := 0; ; n++ {
					if n > len(all) {
						t.Fatal("pagination does not terminate")
					}
					p := collapsed(t, repo, app.Query{UserID: who.user, Limit: limit, Cursor: cur}, rule)
					if len(p.Items) > limit {
						t.Errorf("page %d has %d items, limit %d", n, len(p.Items), limit)
					}
					if p.Total != plain.Total || !reflect.DeepEqual(p.Facets, plain.Facets) {
						t.Errorf("page %d: total %d / facets differ from the timeline's (%d)", n, p.Total, plain.Total)
					}
					pages = append(pages, p.Items)
					if p.NextCursor == "" {
						break
					}
					if len(p.Items) != limit {
						t.Errorf("page %d is short (%d of %d) but has a next cursor", n, len(p.Items), limit)
					}
					if again := collapsed(t, repo, app.Query{UserID: who.user, Limit: limit, Cursor: cur}, rule); !reflect.DeepEqual(kinds(again.Items), kinds(p.Items)) {
						t.Errorf("page %d is not repeatable", n)
					}
					cur = p.NextCursor
				}
				checkCollapsedPages(t, rule, nil, all, pages)
				if limit == 50 {
					// Everything on one page: 4-play cluster, post, 2 plays, 9-vote
					// cluster, lone play, undated cluster.
					want := []string{"cluster", "event", "event", "event", "cluster", "event", "cluster"}
					if got := kinds(pages[0]); !reflect.DeepEqual(got, want) {
						t.Errorf("items = %v, want %v", got, want)
					}
					if u := pages[0][6].Cluster; u == nil || !u.FirstAt.IsZero() || !u.LastAt.IsZero() || u.Count != 3 {
						t.Errorf("undated cluster = %+v, want 3 members with zero times", pages[0][6].Cluster)
					}
				}
			})
		}
	})

	t.Run("collapsed: a run longer than the window is cut at the page boundary and continues", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		// 20 seconds apart: CollapseWindow+500 plays fit in one UTC day.
		n := app.CollapseWindow + 500
		evs := make([]domain.Event, 0, n+2)
		evs = append(evs, post("after", day(2), "later", ""))
		for i := 0; i < n; i++ {
			evs = append(evs, play(fmt.Sprintf("big-%05d", i), day(1).Add(time.Duration(i)*20*time.Second), fmt.Sprintf("track %d", i%7)))
		}
		evs = append(evs, post("before", day(1).Add(-time.Hour), "earlier", ""))
		mustBatch(t, repo, who.user, who.account, "", evs...)
		all := allEvents(t, repo, app.Query{UserID: who.user})

		var pages [][]app.CollapsedItem
		cur := ""
		for i := 0; i < 10 && (i == 0 || cur != ""); i++ {
			p := collapsed(t, repo, app.Query{UserID: who.user, Limit: 25, Cursor: cur}, rule)
			pages = append(pages, p.Items)
			cur = p.NextCursor
		}
		if cur != "" {
			t.Fatal("pagination does not terminate")
		}
		checkCollapsedPages(t, rule, nil, all, pages)
		// Page 1: the post alone — the window was full, so its last run (the
		// big one, possibly cut) waits for the next page. Page 2: the window is
		// the big run and nothing else, so it is emitted cut at the window.
		// Page 3: the rest of the run and the older post.
		want := [][]string{{"event"}, {"cluster"}, {"cluster", "event"}}
		if got := pagesKinds(pages); !reflect.DeepEqual(got, want) {
			t.Fatalf("pages = %v, want %v", got, want)
		}
		first, second := pages[1][0].Cluster, pages[2][0].Cluster
		if first.Count != app.CollapseWindow || second.Count != n-app.CollapseWindow {
			t.Errorf("cut run = %d + %d members, want %d + %d", first.Count, second.Count, app.CollapseWindow, n-app.CollapseWindow)
		}
		if first.Type != second.Type || first.SourceID != second.SourceID || len(first.SampleTitles) != 3 {
			t.Errorf("continuation = %+v after %+v", *second, *first)
		}
	})

	t.Run("collapsed: tenant isolation", func(t *testing.T) {
		repo, env := factory(t)
		alice, bob := newTenant(t, env), newTenant(t, env)
		at := day(1).Add(12 * time.Hour)
		// Interleaved in time: alice's plays must not join bob's run, and bob's
		// two plays stay two events.
		mustBatch(t, repo, alice.user, alice.account, "", play("a1", at, "alice"), play("a2", at.Add(2*time.Minute), "alice"), play("a3", at.Add(4*time.Minute), "alice"))
		mustBatch(t, repo, bob.user, bob.account, "", play("b1", at.Add(time.Minute), "bob"), play("b2", at.Add(3*time.Minute), "bob"))
		p := collapsed(t, repo, app.Query{UserID: bob.user}, rule)
		if got := externalIDs(eventsOf(p.Items)); !reflect.DeepEqual(got, []string{"b2", "b1"}) || p.Total != 2 {
			t.Errorf("bob's collapsed page = %v (total %d), want his two plays as events", got, p.Total)
		}
		for _, it := range p.Items {
			if it.Event != nil && it.Event.UserID != bob.user {
				t.Errorf("bob's page shows %+v", it.Event)
			}
		}
		p = collapsed(t, repo, app.Query{UserID: alice.user}, rule)
		if len(p.Items) != 1 || p.Items[0].Cluster == nil || p.Items[0].Cluster.Count != 3 {
			t.Errorf("alice's collapsed page = %v, want one cluster of 3", kinds(p.Items))
		}
		if len(p.Items) == 1 && p.Items[0].Cluster != nil && !reflect.DeepEqual(p.Items[0].Cluster.SampleTitles, []string{"alice"}) {
			t.Errorf("alice's cluster samples %q, want only hers", p.Items[0].Cluster.SampleTitles)
		}
		// A cursor minted for alice opens nothing of hers to bob.
		cur := app.CursorAt(at.Add(4*time.Minute), "ffffffff-ffff-4fff-8fff-ffffffffffff").Encode()
		for _, it := range collapsed(t, repo, app.Query{UserID: bob.user, Cursor: cur}, rule).Items {
			if it.Event != nil && it.Event.UserID != bob.user {
				t.Errorf("alice's cursor showed bob %+v", it.Event)
			}
		}
	})

	t.Run("collapsed: a malformed cursor is ErrBadCursor and the limit is clamped", func(t *testing.T) {
		repo, env := factory(t)
		who := newTenant(t, env)
		pagingCorpus(t, repo, who)
		if _, err := repo.Collapsed(ctx, app.Query{UserID: who.user, Cursor: "!!!"}, rule); !errors.Is(err, app.ErrBadCursor) {
			t.Errorf("err = %v, want ErrBadCursor", err)
		}
		if p := collapsed(t, repo, app.Query{UserID: who.user, Limit: 0}, rule); len(p.Items) != 7 || p.NextCursor != "" {
			t.Errorf("limit 0 = %d items, next %q; want all 7 (the default page size), none", len(p.Items), p.NextCursor)
		}
		if p := collapsed(t, repo, app.Query{UserID: who.user, Sources: []string{"myspace"}}, rule); len(p.Items) != 0 || p.NextCursor != "" || p.Total != 0 {
			t.Errorf("empty result = %+v", p)
		}
	})
}

// checkCollapsedPages checks the invariant of collapsed paging against the
// plain timeline: walking the pages' items in order, every event appears exactly
// once — as an event item or as a member of a cluster whose bounds, times and
// samples describe exactly the events at that position — and clusters inside a
// page are maximal runs (a page's first and last item may continue a run cut
// by a page boundary).
// checkCollapsedPages replays the run rule over the plain timeline `all` and
// checks the collapsed pages against it. loc is the zone the query was made in
// (nil = UTC): the run rule's day boundary is drawn there.
func checkCollapsedPages(t *testing.T, rule app.CollapseRule, loc *time.Location, all []domain.StoredEvent, pages [][]app.CollapsedItem) {
	t.Helper()
	pos := 0
	for pn, items := range pages {
		for i, it := range items {
			switch {
			case it.Event != nil && it.Cluster != nil, it.Event == nil && it.Cluster == nil:
				t.Fatalf("page %d item %d is neither/both an event and a cluster: %+v", pn, i, it)
			case it.Event != nil:
				if pos >= len(all) {
					t.Fatalf("page %d item %d: more items than events", pn, i)
				}
				if it.Event.ID != all[pos].ID || !reflect.DeepEqual(it.Event.Event, all[pos].Event) {
					t.Fatalf("page %d item %d: event %s, want %s (position %d)", pn, i, it.Event.ExternalID, all[pos].ExternalID, pos)
				}
				pos++
			default:
				c := it.Cluster
				if c.Count < 1 || pos+c.Count > len(all) {
					t.Fatalf("page %d item %d: cluster of %d at position %d of %d", pn, i, c.Count, pos, len(all))
				}
				members := all[pos : pos+c.Count]
				first, last := members[0], members[len(members)-1]
				if c.FirstID != first.ID || c.LastID != last.ID || !c.FirstAt.Equal(first.OccurredAt) || !c.LastAt.Equal(last.OccurredAt) {
					t.Errorf("page %d item %d: cluster bounds %s@%v..%s@%v, want %s@%v..%s@%v", pn, i,
						c.FirstID, c.FirstAt, c.LastID, c.LastAt, first.ID, first.OccurredAt, last.ID, last.OccurredAt)
				}
				if c.Count < rule.MinRun(c.Category) {
					t.Errorf("page %d item %d: a cluster of %d %s events is below the threshold %d", pn, i, c.Count, c.Category, rule.MinRun(c.Category))
				}
				var samples []string
				for k, m := range members {
					if m.SourceID != c.SourceID || m.Type != c.Type || m.Category != c.Category {
						t.Errorf("page %d item %d: member %s is a %s/%s/%s in a %s/%s/%s cluster", pn, i, m.ExternalID, m.SourceID, m.Type, m.Category, c.SourceID, c.Type, c.Category)
					}
					if k > 0 && !rule.Continues(members[k-1], m, loc) {
						t.Errorf("page %d item %d: %s does not continue %s, yet both are in one cluster", pn, i, m.ExternalID, members[k-1].ExternalID)
					}
					if s := app.SampleTitle(m.Event); s != "" && len(samples) < app.SampleTitles && !containsStr(samples, s) {
						samples = append(samples, s)
					}
				}
				if c.SampleTitles == nil || (len(samples) > 0 || len(c.SampleTitles) > 0) && !reflect.DeepEqual(c.SampleTitles, samples) {
					t.Errorf("page %d item %d: sample titles %q, want %q", pn, i, c.SampleTitles, samples)
				}
				// Maximal within the page.
				if i > 0 && rule.Continues(all[pos-1], first, loc) {
					t.Errorf("page %d item %d: cluster could have started one event earlier (%s)", pn, i, all[pos-1].ExternalID)
				}
				if i < len(items)-1 && pos+c.Count < len(all) && rule.Continues(last, all[pos+c.Count], loc) {
					t.Errorf("page %d item %d: cluster could have continued with %s", pn, i, all[pos+c.Count].ExternalID)
				}
				pos += c.Count
			}
		}
	}
	if pos != len(all) {
		t.Errorf("pages cover %d events, the timeline has %d", pos, len(all))
	}
}

func kinds(items []app.CollapsedItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = "event"
		if it.Cluster != nil {
			out[i] = "cluster"
		}
	}
	return out
}

func pagesKinds(pages [][]app.CollapsedItem) [][]string {
	out := make([][]string, len(pages))
	for i, p := range pages {
		out[i] = kinds(p)
	}
	return out
}

// eventsOf returns the event items of a page (clusters left out).
func eventsOf(items []app.CollapsedItem) []domain.StoredEvent {
	var out []domain.StoredEvent
	for _, it := range items {
		if it.Event != nil {
			out = append(out, *it.Event)
		}
	}
	return out
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// play is a Spotify play (consumption); plays is n of them a minute apart,
// newest at `at`, external ids prefixed with tag.
func play(ext string, at time.Time, title string) domain.Event {
	return domain.Event{
		SourceID: "spotify", ExternalID: ext, Type: "spotify.play",
		Category: domain.CategoryConsumption, OccurredAt: at, Title: title,
	}
}

func plays(at time.Time, n int, tag string) []domain.Event {
	out := make([]domain.Event, n)
	for i := range out {
		out[i] = play(fmt.Sprintf("%s-play-%d", tag, i), at.Add(-time.Duration(i)*time.Minute), fmt.Sprintf("%s track %d", tag, i))
	}
	return out
}

// vote is a Reddit upvote (reaction); votes is n of them a minute apart.
func vote(ext string, at time.Time) domain.Event {
	return domain.Event{
		SourceID: "reddit", ExternalID: ext, Type: "reddit.vote.up",
		Category: domain.CategoryReaction, OccurredAt: at,
	}
}

func votes(at time.Time, n int, tag string) []domain.Event {
	out := make([]domain.Event, n)
	for i := range out {
		out[i] = vote(fmt.Sprintf("%s-vote-%d", tag, i), at.Add(-time.Duration(i)*time.Minute))
	}
	return out
}

// day returns 00:00 UTC on the n-th of January 2023.
func day(n int) time.Time { return time.Date(2023, 1, n, 0, 0, 0, 0, time.UTC) }

// mustTime parses an RFC 3339 instant of a fixture.
func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("porttest: bad fixture time " + s + ": " + err.Error())
	}
	return t
}

// zone loads an IANA zone by name; "" is the zoneless query (nil = UTC).
func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	if name == "" {
		return nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load zone %q: %v", name, err)
	}
	return loc
}

// orUTC names the zoneless query in a subtest title.
func orUTC(name string) string {
	if name == "" {
		return "no zone"
	}
	return name
}

// localDayRange is the inclusive instant range of the calendar day `ymd` in
// loc, built exactly as the REST adapter builds it from ?from=&to=&tz= — a
// calendar step to the next local midnight, not 24 hours, so a 23- or 25-hour
// day still comes out whole.
func localDayRange(t *testing.T, ymd string, loc *time.Location) (from, to time.Time) {
	t.Helper()
	start, err := time.ParseInLocation(time.DateOnly, ymd, app.Zone(loc))
	if err != nil {
		t.Fatalf("bad day %q: %v", ymd, err)
	}
	return start, start.AddDate(0, 0, 1).Add(-time.Microsecond)
}

// tzCorpus is the fixture of the time-zone cases: eleven reddit posts whose
// instants sit on either side of local midnight in Berlin (across both DST
// switches of 2024), in Auckland (+13/+12, DST the other way round) and in
// Kolkata (+05:30, no DST). Every one of them lands on a different calendar
// day depending on where the viewer stands.
func tzCorpus() []domain.Event {
	return []domain.Event{
		// Berlin, spring forward: the local day 2024-03-31 runs from
		// 2024-03-30T23:00Z to 2024-03-31T22:00Z — 23 hours.
		post("b1", mustTime("2024-03-30T23:30:00Z"), "Berlin: just after midnight on the 31st", ""),
		post("b2", mustTime("2024-03-31T21:59:00Z"), "Berlin: the last minute of the 31st", ""),
		post("b3", mustTime("2024-03-31T22:10:00Z"), "Berlin: already the 1st of April", ""),
		// Berlin, fall back: the local day 2024-10-27 runs from
		// 2024-10-26T22:00Z to 2024-10-27T23:00Z — 25 hours.
		post("b4", mustTime("2024-10-26T22:30:00Z"), "Berlin: just after midnight on the 27th", ""),
		post("b5", mustTime("2024-10-27T22:30:00Z"), "Berlin: still the 27th, after the clocks went back", ""),
		post("b6", mustTime("2024-10-27T23:30:00Z"), "Berlin: the 28th", ""),
		// Auckland is +13 in January (its summer) and +12 in July.
		post("a1", mustTime("2024-01-15T11:30:00Z"), "Auckland: past midnight into the 16th", ""),
		post("a2", mustTime("2024-07-15T11:30:00Z"), "Auckland: the last half hour of the 15th", ""),
		post("a3", mustTime("2024-07-15T12:30:00Z"), "Auckland: the 16th", ""),
		// Kolkata's day boundary is 18:30 UTC.
		post("k1", mustTime("2024-05-10T18:25:00Z"), "Kolkata: five minutes before midnight", ""),
		post("k2", mustTime("2024-05-10T18:35:00Z"), "Kolkata: five minutes after midnight", ""),
	}
}

func post(ext string, at time.Time, title, body string) domain.Event {
	return domain.Event{
		SourceID: "reddit", ExternalID: "post:" + ext, Type: "reddit.post",
		Category: domain.CategoryContent, OccurredAt: at, Title: title, Body: body,
	}
}

func mustBatch(t *testing.T, repo app.EventRepository, userID, accountID, importJobID string, evs ...domain.Event) {
	t.Helper()
	if _, err := repo.SaveBatch(ctx, userID, accountID, app.ImportRun{JobID: importJobID}, evs); err != nil {
		t.Fatalf("SaveBatch: %v", err)
	}
}

func externalIDs(evs []domain.StoredEvent) []string {
	var ids []string
	for _, e := range evs {
		ids = append(ids, e.ExternalID)
	}
	return ids
}

// assertTimelineOrder checks the documented order pairwise.
func assertTimelineOrder(t *testing.T, evs []domain.StoredEvent) {
	t.Helper()
	for i := 1; i < len(evs); i++ {
		a, b := evs[i-1], evs[i]
		ok := false
		switch {
		case a.OccurredAt.IsZero() != b.OccurredAt.IsZero():
			ok = b.OccurredAt.IsZero() // dated before undated
		case a.OccurredAt.Equal(b.OccurredAt):
			ok = a.ID > b.ID
		default:
			ok = a.OccurredAt.After(b.OccurredAt)
		}
		if !ok {
			t.Errorf("order violated at %d: (%v, %s) before (%v, %s)", i, a.OccurredAt, a.ID, b.OccurredAt, b.ID)
		}
	}
}
