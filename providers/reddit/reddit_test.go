package reddit

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
)

// zipTestdata packs the testdata directory into a ZIP and returns its path,
// standing in for a real Reddit export archive.
func zipTestdata(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "reddit_export.zip")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join("testdata", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		// Nest under a folder to prove path.Base matching works.
		w, err := zw.Create("reddit/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestImportReddit(t *testing.T) {
	zipPath := zipTestdata(t)
	im := bundle.NewImporter(New())

	var events []domain.Event
	cov, err := im.ImportZip(context.Background(), zipPath, func(ev domain.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	// Coverage: 9 members, 7 mapped CSVs, 2 generic (statistics.csv, user_preferences.csv).
	if cov.Members != 9 {
		t.Errorf("Members = %d, want 9", cov.Members)
	}
	if cov.Mapped != 7 {
		t.Errorf("Mapped = %d, want 7", cov.Mapped)
	}
	if cov.Generic != 2 {
		t.Errorf("Generic = %d, want 2", cov.Generic)
	}

	// Per-type counts — every data type mapped to events.
	want := map[string]int{
		"reddit.comment":   2,
		"reddit.post":      2,
		"reddit.vote.up":   1,
		"reddit.vote.down": 2, // 1 post downvote + 1 comment downvote
		"reddit.save":      1,
		"reddit.subscribe": 2,
		"reddit.gild":      1, // gilded_content.csv
		"reddit.unknown":   2, // statistics.csv + user_preferences.csv captured generically
	}
	for typ, n := range want {
		if cov.PerType[typ] != n {
			t.Errorf("PerType[%q] = %d, want %d", typ, cov.PerType[typ], n)
		}
	}
	if cov.Events != 13 {
		t.Errorf("Events = %d, want 13", cov.Events)
	}

	// comments.csv: "link" is the parent post's URL and "parent" a fullname or a
	// comment URL; both resolve to the ids the post/comment events carry.
	c1 := byExternalID(events, "comment:c1")
	if c1 == nil || c1.ThreadExternalID != "post:aaa" || c1.ParentExternalID != "post:aaa" || c1.Metadata["ip"] != "203.0.113.7" {
		t.Errorf("comment c1 = %+v, want thread/parent post:aaa", c1)
	}
	if c2 := byExternalID(events, "comment:c2"); c2 == nil || c2.ThreadExternalID != "post:bbb" || c2.ParentExternalID != "comment:c9" ||
		len(c2.Targets) != 1 || c2.Targets[0].TargetURL != "https://old.reddit.com/r/rust/comments/bbb/y/c9/" {
		t.Errorf("comment c2 = %+v, want thread post:bbb, parent comment:c9", c2)
	}
	if p := byExternalID(events, "post:aaa"); p == nil || p.Metadata["gildings"] != "0" || p.Metadata["url"] != "https://example.com/archiver" {
		t.Errorf("post aaa = %+v", p)
	}

	// A downvote must carry a voted_down target (powers "all my reactions").
	if !hasTypeWithRelation(events, "reddit.vote.down", domain.RelationVotedDown) {
		t.Error("expected a reddit.vote.down event with a voted_down target")
	}
	// The unknown file must be respected as a generic 'other' domain.
	if !hasType(events, "reddit.unknown", domain.CategoryOther) {
		t.Error("expected statistics.csv to be captured as reddit.unknown / other")
	}
	// A save must carry a saved target.
	if !hasTypeWithRelation(events, "reddit.save", domain.RelationSaved) {
		t.Error("expected a reddit.save event with a saved target")
	}
}

func TestImportRedditIsIdempotent(t *testing.T) {
	// Importing the same archive twice through one importer dedupes by content
	// hash, so no duplicate events are produced on the second pass.
	zipPath := zipTestdata(t)
	im := bundle.NewImporter(New())

	count := func() int {
		n := 0
		_, err := im.ImportZip(context.Background(), zipPath, func(domain.Event) error { n++; return nil })
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	first := count()
	second := count()
	if first != 13 {
		t.Errorf("first import events = %d, want 13", first)
	}
	if second != 0 {
		t.Errorf("second import events = %d, want 0 (all deduped)", second)
	}
}

func byExternalID(evs []domain.Event, id string) *domain.Event {
	for i := range evs {
		if evs[i].ExternalID == id {
			return &evs[i]
		}
	}
	return nil
}

func hasType(evs []domain.Event, typ string, cat domain.Category) bool {
	for _, e := range evs {
		if e.Type == typ && e.Category == cat {
			return true
		}
	}
	return false
}

func hasTypeWithRelation(evs []domain.Event, typ string, rel domain.Relation) bool {
	for _, e := range evs {
		if e.Type != typ {
			continue
		}
		for _, tg := range e.Targets {
			if tg.Relation == rel {
				return true
			}
		}
	}
	return false
}
