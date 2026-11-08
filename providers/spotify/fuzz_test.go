package spotify

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

// recorder wraps the provider so the importer hands us every RawRecord the
// extractors produce from the fixtures: those are the fuzz seeds.
type recorder struct {
	*Provider
	raws []provider.RawRecord
}

func (r *recorder) Normalize(ctx context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	r.raws = append(r.raws, raw)
	return r.Provider.Normalize(ctx, raw)
}

func seedRecords(f *testing.F) []provider.RawRecord {
	f.Helper()
	zipPath := filepath.Join(f.TempDir(), "seed.zip")
	if err := archivetest.ZipDir("testdata", zipPath); err != nil {
		f.Fatal(err)
	}
	rec := &recorder{Provider: New()}
	if _, err := bundle.NewImporter(rec).ImportZip(context.Background(), zipPath, func(domain.Event) error { return nil }); err != nil {
		f.Fatal(err)
	}
	return rec.raws
}

// checkEvents enforces the never-drop contract: a record of any shape yields at
// least one event with a stable id and a declared type, and nothing panics.
func checkEvents(t *testing.T, p *Provider, evs []domain.Event, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("Normalize returned an error (import would abort): %v", err)
	}
	if len(evs) == 0 {
		t.Fatal("Normalize dropped the record")
	}
	declared := map[string]domain.Category{}
	for _, d := range p.Descriptor().EventTypes {
		declared[d.Type] = d.Category
	}
	for _, ev := range evs {
		if ev.ExternalID == "" || ev.SourceID != sourceID {
			t.Errorf("event without id/source: %+v", ev)
		}
		if cat, ok := declared[ev.Type]; !ok || cat != ev.Category {
			t.Errorf("undeclared type/category %q/%q", ev.Type, ev.Category)
		}
	}
}

func FuzzNormalize(f *testing.F) {
	for _, raw := range seedRecords(f) {
		f.Add(raw.Kind, raw.Payload)
	}
	for _, kind := range []string{"play_extended", "play_account", "search", "save", "playlist", "playlist_item", "library_unknown", bundle.SkippedArrayKind, ""} {
		f.Add(kind, []byte(`not json`))
		f.Add(kind, []byte(`[1,2]`))
		f.Add(kind, []byte(`{"item":5,"list":"tracks","playlist":1}`))
	}
	f.Add("play_extended", []byte(`{"ts":"2021-01-31T12:34:56Z","ms_played":1,"skipped":null,"offline_timestamp":1612096496.5,"audiobook_title":"B","audiobook_uri":"spotify:show:1"}`))

	p := New()
	f.Fuzz(func(t *testing.T, kind string, payload []byte) {
		evs, err := p.Normalize(context.Background(), provider.RawRecord{Kind: kind, Payload: payload, Member: "x.json"})
		checkEvents(t, p, evs, err)
	})
}

func FuzzExtract(f *testing.F) {
	f.Add("Spotify Extended Streaming History/Streaming_History_Audio_2019_0.json", `[{"ts":"2021-01-31T12:34:56Z","ms_played":215000,"master_metadata_track_name":"T","spotify_track_uri":"spotify:track:1","skipped":null}]`)
	f.Add("Spotify Account Data/StreamingHistory_music_0.json", `[{"endTime":"2021-01-31 12:34","artistName":"A","trackName":"T","msPlayed":1}]`)
	f.Add("Spotify Account Data/SearchQueries.json", `[{"platform":"ANDROID","searchTime":"2021-01-31T12:34:56.789Z[UTC]","searchQuery":"q","searchInteractionURIs":[]}]`)
	f.Add("Spotify Account Data/YourLibrary.json", `{"tracks":[{"artist":"A","album":"B","track":"T","uri":"spotify:track:1"}],"other":["x"],"courses":[1]}`)
	f.Add("Spotify Account Data/Playlist1.json", `{"playlists":[{"name":"P","lastModifiedDate":"2021-01-31","items":[{"track":{"trackName":"T","artistName":"A","albumName":"B","trackUri":"spotify:track:1"},"episode":null,"localTrack":null,"addedDate":"2021-01-30"}],"description":null,"numberOfFollowers":0}]}`)
	f.Add("Spotify Account Data/Playlist1.json", `{"playlists":[1,"two",{"items":"x"}]}`)
	f.Add("Spotify Account Data/YourLibrary.json", `{"tracks":[1,"two"]}`)
	f.Add("Spotify Account Data/StreamingHistory_music_0.json", `[1,"two",null]`)
	f.Add("Spotify Account Data/StreamingHistory_music_0.json", ``)
	f.Add("Spotify Account Data/StreamingHistory_music_0.json", `[`)

	p := New()
	f.Fuzz(func(t *testing.T, member, data string) {
		start := time.Now()
		for _, ex := range p.Extractors() {
			if !ex.Match(member) {
				continue
			}
			_ = ex.Extract(member, strings.NewReader(data), func(raw provider.RawRecord) error {
				raw.Member = member
				evs, err := p.Normalize(context.Background(), raw)
				checkEvents(t, p, evs, err)
				return nil
			})
		}
		// A member of a few KB must never take seconds: that is a hang.
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("Extract took %v on %d bytes", d, len(data))
		}
	})
}
