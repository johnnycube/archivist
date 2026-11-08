package spotify

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
)

func importTestdata(t *testing.T) ([]domain.Event, *domain.Coverage) {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "spotify.zip")
	if err := archivetest.ZipDir("testdata", zipPath); err != nil {
		t.Fatal(err)
	}
	var events []domain.Event
	cov, err := bundle.NewImporter(New()).ImportZip(context.Background(), zipPath, func(ev domain.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return events, cov
}

func TestImportSpotify(t *testing.T) {
	events, cov := importTestdata(t)

	want := map[string]int{
		"spotify.play":          9, // extended: audio 4 + video 1 + endsong 1; account: music 2 + podcast 1
		"spotify.search":        2,
		"spotify.save":          6, // tracks 2 + album + show + episode + artist
		"spotify.playlist":      3,
		"spotify.playlist_item": 3,
		"spotify.unknown":       4, // YourLibrary bannedTracks + other; Userdata.json + Follow.json captured generically
	}
	if len(cov.PerType) != len(want) {
		t.Errorf("PerType has %d types, want %d: %v", len(cov.PerType), len(want), cov.PerType)
	}
	for typ, n := range want {
		if cov.PerType[typ] != n {
			t.Errorf("PerType[%q] = %d, want %d", typ, cov.PerType[typ], n)
		}
	}
	if cov.Mapped != 8 || cov.Generic != 2 || cov.Duplicates != 0 {
		t.Errorf("mapped/generic/duplicates = %d/%d/%d, want 8/2/0", cov.Mapped, cov.Generic, cov.Duplicates)
	}

	declared := map[string]bool{}
	for _, d := range New().Descriptor().EventTypes {
		declared[d.Type] = true
	}
	ids := map[string]bool{}
	for _, ev := range events {
		if !declared[ev.Type] {
			t.Errorf("type %q is not declared in the descriptor", ev.Type)
		}
		if ev.ExternalID == "" || ids[ev.ExternalID] {
			t.Errorf("ExternalID %q empty or reused (%s %q)", ev.ExternalID, ev.Type, ev.Title)
		}
		ids[ev.ExternalID] = true
		if ev.OccurredAt.Location() != time.UTC {
			t.Errorf("%s OccurredAt is not UTC: %v", ev.ExternalID, ev.OccurredAt)
		}
		if ev.Actor != "self" && ev.Type != "spotify.unknown" { // generic captures carry no actor
			t.Errorf("%s actor = %q, want self", ev.ExternalID, ev.Actor)
		}
	}

	// Extended history: a track. OccurredAt is "ts", the END of playback.
	track := findPlay(events, "extended", "The Placeholders — Night Train")
	if track == nil {
		t.Fatalf("extended track play not found")
	}
	if !track.OccurredAt.Equal(time.Date(2021, 1, 31, 12, 34, 56, 0, time.UTC)) || track.Category != domain.CategoryConsumption ||
		track.Metadata["kind"] != "track" || track.Metadata["ms_played"] != int64(215000) ||
		track.Metadata["uri"] != "spotify:track:1111111111111111111111" || track.Metadata["album"] != "Rails" ||
		track.Metadata["platform"] != "linux" || track.Metadata["country"] != "DE" || track.Metadata["ip"] != "203.0.113.7" ||
		track.Metadata["skipped"] != false || track.Metadata["reason_end"] != "trackdone" {
		t.Errorf("extended track play not mapped: %+v", track)
	}
	if !strings.HasPrefix(track.ExternalID, "play:1612096496:") || !strings.HasSuffix(track.ExternalID, ":215000") {
		t.Errorf("play ExternalID = %q, want play:<ts>:<hash>:<ms_played>", track.ExternalID)
	}

	// Same track, same second, different ms_played: two plays.
	var skips []domain.Event
	for _, ev := range events {
		if ev.Type == "spotify.play" && ev.Title == "Fixture Quartet — Slow Morning" && ev.Metadata["export"] == "extended" {
			skips = append(skips, ev)
		}
	}
	if len(skips) != 2 || skips[0].ExternalID == skips[1].ExternalID || skips[0].Metadata["skipped"] != true || skips[0].Metadata["reason_end"] != "fwdbtn" {
		t.Errorf("plays within one second not kept apart: %+v", skips)
	}

	// Extended history: episodes, in the audio and the video member.
	ep := findPlay(events, "extended", "The Sample Show — Episode 12: Test Data")
	if ep == nil || ep.Metadata["kind"] != "episode" || ep.Metadata["uri"] != "spotify:episode:3333333333333333333333" ||
		ep.Metadata["album"] != nil || !strings.Contains(ep.Metadata["member"].(string), "Streaming_History_Audio") {
		t.Errorf("extended episode play not mapped: %+v", ep)
	}
	if v := findPlay(events, "extended", "Watchable Podcast — Video Episode 1"); v == nil || v.Metadata["kind"] != "episode" ||
		!strings.Contains(v.Metadata["member"].(string), "Streaming_History_Video") {
		t.Errorf("video play not mapped: %+v", v)
	}

	// endsong_<n>.json: the older name, ip_addr_decrypted, "skipped": null.
	old := findPlay(events, "extended", "Vintage Band — Old Song")
	if old == nil || old.Metadata["ip"] != "198.51.100.23" || old.OccurredAt.Year() != 2018 {
		t.Errorf("endsong play not mapped: %+v", old)
	} else if _, ok := old.Metadata["skipped"]; ok {
		t.Errorf("null skipped must stay unset: %+v", old.Metadata)
	}

	// Account data: minute precision, names only; overlaps the extended history
	// under a different ExternalID (documented in the package comment).
	acc := findPlay(events, "account", "The Placeholders — Night Train")
	if acc == nil || !acc.OccurredAt.Equal(time.Date(2021, 1, 31, 12, 34, 0, 0, time.UTC)) ||
		acc.Metadata["kind"] != "track" || acc.Metadata["ms_played"] != int64(215000) || acc.Metadata["uri"] != nil {
		t.Errorf("account track play not mapped: %+v", acc)
	} else if acc.ExternalID == track.ExternalID {
		t.Errorf("account and extended plays are expected to differ, both %q", acc.ExternalID)
	}
	if pod := findPlay(events, "account", "The Sample Show — Episode 12: Test Data"); pod == nil || pod.Metadata["kind"] != "episode" ||
		!pod.OccurredAt.Equal(time.Date(2021, 2, 1, 7, 0, 0, 0, time.UTC)) {
		t.Errorf("account podcast play not mapped: %+v", pod)
	}

	// SearchQueries.json: the "[UTC]" suffix is stripped, milliseconds kept.
	s := byBody(events, "spotify.search", "placeholders night")
	if s == nil || !s.OccurredAt.Equal(time.Date(2021, 1, 31, 12, 34, 56, 789_000_000, time.UTC)) ||
		s.Metadata["platform"] != "ANDROID" || len(s.Metadata["interaction_uris"].([]string)) != 1 {
		t.Errorf("search not mapped: %+v", s)
	}

	// YourLibrary.json
	saves := map[string]domain.Event{}
	for _, ev := range events {
		if ev.Type == "spotify.save" {
			saves[ev.Metadata["kind"].(string)+"|"+ev.Title] = ev
			if !ev.OccurredAt.IsZero() || len(ev.Targets) != 1 || ev.Targets[0].Relation != domain.RelationSaved ||
				ev.Targets[0].TargetExternalID != ev.Metadata["uri"] || ev.Category != domain.CategorySave {
				t.Errorf("save not mapped: %+v", ev)
			}
		}
	}
	for _, k := range []string{
		"track|Saved track: The Placeholders — Night Train",
		"track|Saved track: Fixture Quartet — Slow Morning",
		"album|Saved album: The Placeholders — Rails",
		"show|Saved show: Sample Media — The Sample Show",
		"episode|Saved episode: The Sample Show — Episode 12: Test Data",
		"artist|Followed artist: The Placeholders",
	} {
		if _, ok := saves[k]; !ok {
			t.Errorf("save %q not found in %v", k, keys(saves))
		}
	}
	if tr := saves["track|Saved track: The Placeholders — Night Train"]; tr.ExternalID != "save:track:spotify:track:1111111111111111111111" ||
		tr.Targets[0].TargetURL != "https://open.spotify.com/track/1111111111111111111111" {
		t.Errorf("saved track not mapped: %+v", tr)
	}

	// Library arrays we do not map are kept, not dropped.
	lists := map[string]bool{}
	for _, ev := range events {
		if ev.Type == "spotify.unknown" && ev.Metadata["list"] != nil {
			lists[ev.Metadata["list"].(string)] = true
			if ev.Metadata["record"] == nil {
				t.Errorf("unmapped library entry lost its record: %+v", ev)
			}
		}
	}
	if len(lists) != 2 || !lists["bannedTracks"] || !lists["other"] {
		t.Errorf("unmapped library arrays = %v, want bannedTracks and other", lists)
	}

	// Playlist1.json
	if pl := byTitle(events, "Playlist: Road Trip"); pl == nil || pl.Body != "For the drive" || pl.Metadata["items"] != 2 ||
		pl.Metadata["followers"] != int64(2) || !pl.OccurredAt.Equal(time.Date(2021, 1, 31, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("playlist not mapped: %+v", pl)
	}
	if empty := byTitle(events, "Playlist: Empty One"); empty == nil || empty.Metadata["items"] != 0 {
		t.Errorf("empty playlist not kept: %+v", empty)
	}
	item := byTitle(events, "Added The Placeholders — Night Train to Road Trip")
	if item == nil || item.Metadata["playlist"] != "Road Trip" || item.Metadata["kind"] != "track" || item.Metadata["album"] != "Rails" ||
		!item.OccurredAt.Equal(time.Date(2021, 1, 30, 0, 0, 0, 0, time.UTC)) || item.Category != domain.CategorySave ||
		item.Targets[0].TargetExternalID != "spotify:track:1111111111111111111111" {
		t.Errorf("playlist item not mapped: %+v", item)
	}
	// The same track added to another playlist on the same day is its own item.
	if other := byTitle(events, "Added The Placeholders — Night Train to Focus"); other == nil || item == nil || other.ExternalID == item.ExternalID {
		t.Errorf("same track in a second playlist not kept apart: %+v", other)
	}
	if epItem := byTitle(events, "Added The Sample Show — Episode 12: Test Data to Road Trip"); epItem == nil || epItem.Metadata["kind"] != "episode" ||
		epItem.Targets[0].TargetURL != "https://open.spotify.com/episode/3333333333333333333333" {
		t.Errorf("playlist episode not mapped: %+v", epItem)
	}
}

// Re-importing the same archive must produce the same ExternalIDs.
func TestExternalIDsStable(t *testing.T) {
	a, _ := importTestdata(t)
	b, _ := importTestdata(t)
	if len(a) != len(b) {
		t.Fatalf("event counts differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ExternalID != b[i].ExternalID {
			t.Errorf("event %d: %q vs %q", i, a[i].ExternalID, b[i].ExternalID)
		}
	}
}

func TestMatch(t *testing.T) {
	cases := map[string]bool{
		"Spotify Extended Streaming History/Streaming_History_Audio_2019-2021_0.json": true,
		"Spotify Extended Streaming History/Streaming_History_Video_2019-2024.json":   true,
		"MyData/endsong_12.json":                                                   true,
		"Spotify Account Data/StreamingHistory_music_0.json":                       true,
		"Spotify Account Data/StreamingHistory_podcast_0.json":                     true,
		"Spotify Account Data/StreamingHistory_audiobook_0.json":                   true,
		"Spotify Extended Streaming History/Streaming_History_Audiobook_2024.json": true,
		"MyData/StreamingHistory0.json":                                            true,
		"MyData/SearchQueries.json":                                                true,
		"MyData/YourLibrary.json":                                                  true,
		"MyData/Playlist1.json":                                                    true,
		"MyData/Playlist12.json":                                                   true,
		"MyData/PlaylistInsights.json":                                             false,
		"MyData/Follow.json":                                                       false,
		"MyData/Userdata.json":                                                     false,
		"MyData/Inferences.json":                                                   false,
		"Spotify Extended Streaming History/ReadMeFirst_ExtendedStreamingHistory.pdf": false,
	}
	for member, want := range cases {
		got := false
		for _, ex := range New().Extractors() {
			got = got || ex.Match(member)
		}
		if got != want {
			t.Errorf("Match(%q) = %v, want %v", member, got, want)
		}
	}
}

func BenchmarkNormalizePlay(b *testing.B) {
	raw := rec("play_extended", []byte(`{"ts":"2021-01-31T12:34:56Z","platform":"linux","ms_played":215000,"conn_country":"DE","ip_addr":"203.0.113.7","master_metadata_track_name":"Night Train","master_metadata_album_artist_name":"The Placeholders","master_metadata_album_album_name":"Rails","spotify_track_uri":"spotify:track:1111111111111111111111","episode_name":null,"episode_show_name":null,"spotify_episode_uri":null,"reason_start":"trackdone","reason_end":"trackdone","shuffle":false,"skipped":false,"offline":false,"offline_timestamp":null,"incognito_mode":false}`))
	p := New()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := p.Normalize(context.Background(), raw); err != nil {
			b.Fatal(err)
		}
	}
}

func findPlay(evs []domain.Event, export, title string) *domain.Event {
	for i := range evs {
		if evs[i].Type == "spotify.play" && evs[i].Title == title && evs[i].Metadata["export"] == export {
			return &evs[i]
		}
	}
	return nil
}

func byTitle(evs []domain.Event, title string) *domain.Event {
	for i := range evs {
		if evs[i].Title == title {
			return &evs[i]
		}
	}
	return nil
}

func byBody(evs []domain.Event, typ, body string) *domain.Event {
	for i := range evs {
		if evs[i].Type == typ && evs[i].Body == body {
			return &evs[i]
		}
	}
	return nil
}

func keys(m map[string]domain.Event) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// An array YourLibrary.json gains in future is not mapped, but is not dropped
// silently either: it shows up as one spotify.unknown event with its size.
func TestYourLibraryUnknownArrayIsReported(t *testing.T) {
	data, err := archivetest.Zip(map[string]string{
		"Spotify Account Data/YourLibrary.json": `{"tracks": [{"artist": "A", "track": "T", "uri": "spotify:track:1"}],
			"courses": [{"name": "c1"}, {"name": "c2"}, {"name": "c3"}], "albums": []}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	var unknown []domain.Event
	part := bundle.Part{ReaderAt: bytes.NewReader(data), Size: int64(len(data))}
	cov, err := bundle.NewImporter(New()).ImportParts(context.Background(), []bundle.Part{part}, func(ev domain.Event) error {
		if ev.Type == "spotify.unknown" {
			unknown = append(unknown, ev)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cov.PerType["spotify.save"] != 1 || len(unknown) != 1 {
		t.Fatalf("PerType = %v, unknown = %d; want 1 save and 1 unknown", cov.PerType, len(unknown))
	}
	ev := unknown[0]
	if ev.Metadata["key"] != "courses" || ev.Metadata["count"] != 3 || ev.Metadata["member"] != "Spotify Account Data/YourLibrary.json" {
		t.Errorf("unknown event = %+v", ev)
	}
}
