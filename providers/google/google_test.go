package google

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
)

func importFixture(t *testing.T, dir string) ([]domain.Event, *domain.Coverage) {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "takeout.zip")
	if err := archivetest.ZipDir(filepath.Join("testdata", dir), zipPath); err != nil {
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

func checkCoverage(t *testing.T, events []domain.Event, cov *domain.Coverage, want map[string]int) {
	t.Helper()
	if len(cov.PerType) != len(want) {
		t.Errorf("PerType has %d types, want %d: %v", len(cov.PerType), len(want), cov.PerType)
	}
	for typ, n := range want {
		if cov.PerType[typ] != n {
			t.Errorf("PerType[%q] = %d, want %d", typ, cov.PerType[typ], n)
		}
	}
	declared := map[string]domain.TypeDef{}
	for _, d := range New().Descriptor().EventTypes {
		declared[d.Type] = d
	}
	ids := map[string]int{}
	for _, ev := range events {
		ids[ev.ExternalID]++
		def, ok := declared[ev.Type]
		if !ok || def.Category != ev.Category {
			t.Errorf("event type %q / category %q is not declared in the Descriptor", ev.Type, ev.Category)
		}
		if def.NeedsTarget && len(ev.Targets) != 1 {
			t.Errorf("%s %q needs a target: %+v", ev.Type, ev.ExternalID, ev)
		}
		if ev.SourceID != sourceID || ev.ExternalID == "" {
			t.Errorf("event without source or ExternalID: %+v", ev)
		}
		switch ev.Type {
		case "google.unknown":
		case "google.chat.message", "google.hangouts.message":
			if ev.Actor == "" || ev.ThreadExternalID == "" {
				t.Errorf("message without actor or thread: %+v", ev)
			}
		default:
			if ev.Actor != "self" {
				t.Errorf("Actor = %q, want self: %+v", ev.Actor, ev)
			}
		}
		if !ev.OccurredAt.IsZero() && ev.OccurredAt.Location() != time.UTC {
			t.Errorf("OccurredAt is not UTC: %+v", ev)
		}
	}
	for id, n := range ids {
		if n > 1 {
			t.Errorf("ExternalID %q used by %d events", id, n)
		}
	}
}

func TestImportTakeoutYouTube(t *testing.T) {
	events, cov := importFixture(t, "en")

	checkCoverage(t, events, cov, enPerType)
	// search-history.json and My Activity/YouTube/MyActivity.json each repeat a
	// watch record verbatim.
	if cov.Duplicates != 2 {
		t.Errorf("Duplicates = %d, want 2", cov.Duplicates)
	}
	if cov.Mapped != 35 || cov.Generic != 6 {
		t.Errorf("Mapped/Generic members = %d/%d, want 35/6 (%v)", cov.Mapped, cov.Generic, cov.GenericMembers)
	}

	// Watch history: prefix stripped, channel kept, re-watch is its own event.
	var wheel []domain.Event
	for _, ev := range events {
		if ev.Type == "google.youtube.watch" && ev.Title == "How to true a bicycle wheel" {
			wheel = append(wheel, ev)
		}
	}
	if len(wheel) != 2 {
		t.Fatalf("got %d watches of the re-watched video, want 2", len(wheel))
	}
	w := wheel[0]
	if w.Metadata["url"] != "https://www.youtube.com/watch?v=aaaaaaaaa01" || w.Metadata["video_id"] != "aaaaaaaaa01" ||
		w.Metadata["channel"] != "Example Bike Shop" || w.Metadata["channel_url"] != "https://www.youtube.com/channel/UCaaaaaaaaaaaaaaaaaaaaaa" ||
		w.Metadata["product"] != "YouTube" || w.Metadata["ad"] != nil || w.Metadata["raw_title"] != nil || len(w.Targets) != 0 {
		t.Errorf("watch not mapped: %+v", w)
	}
	if want := time.Date(2024, 3, 1, 18, 22, 11, 123000000, time.UTC); !w.OccurredAt.Equal(want) {
		t.Errorf("watch OccurredAt = %v, want %v", w.OccurredAt, want)
	}
	if w.ExternalID != "yt.watch:2024-03-01T18:22:11.123Z:"+short("https://www.youtube.com/watch?v=aaaaaaaaa01") {
		t.Errorf("watch ExternalID = %q", w.ExternalID)
	}

	if ad := byTitle(events, "google.youtube.watch", "Buy the new Example Phone"); ad == nil || ad.Metadata["ad"] != true || ad.Metadata["channel"] != nil {
		t.Errorf("ad not flagged: %+v", ad)
	}
	music := byTitle(events, "google.youtube.watch", "Synthetic Song")
	if music == nil || music.Metadata["product"] != "YouTube Music" || music.Metadata["video_id"] != "aaaaaaaaa03" {
		t.Errorf("YouTube Music watch not mapped: %+v", music)
	} else if want := time.Date(2024, 2, 27, 20, 0, 0, 0, time.UTC); !music.OccurredAt.Equal(want) {
		t.Errorf("offset time not converted to UTC: %v", music.OccurredAt)
	}
	if rm := byTitle(events, "google.youtube.watch", "Watched a video that has been removed"); rm == nil || rm.Metadata["url"] != nil {
		t.Errorf("removed video should keep its sentence: %+v", rm)
	}
	if v := byTitle(events, "google.visit", "YouTube Music"); v == nil || v.Metadata["url"] != "https://music.youtube.com/" || v.Metadata["product"] != "YouTube Music" {
		t.Errorf("non-video YouTube activity should be a google.visit: %+v", v)
	}

	// Search history: the query is the body, decoded from the URL.
	for _, q := range []string{"wheel truing stand", "café racer & co"} {
		if s := byBody(events, "google.youtube.search", q); s == nil || s.Title != "Searched YouTube" {
			t.Errorf("search %q not mapped", q)
		}
	}

	// Comments: the JSON blob of the Comment Text column is flattened.
	c := byBody(events, "google.youtube.comment", "Great tutorial, thanks!")
	if c == nil || c.ExternalID != "yt.comment:UgxSYNTHETIC0000000001" || c.Metadata["video_id"] != "aaaaaaaaa01" ||
		c.Targets[0].Relation != domain.RelationRepliedTo || c.Targets[0].TargetExternalID != "aaaaaaaaa01" ||
		c.Metadata["price"] != nil {
		t.Errorf("comment not mapped: %+v", c)
	} else if want := time.Date(2024, 3, 1, 18, 30, 0, 123456000, time.UTC); !c.OccurredAt.Equal(want) {
		t.Errorf("comment OccurredAt = %v, want %v", c.OccurredAt, want)
	}
	if seg := byBody(events, "google.youtube.comment", "See 2:31 for the trick"); seg == nil {
		t.Errorf("takeoutSegments comment not flattened")
	}
	reply := byBody(events, "google.youtube.comment", "Replying to @someone")
	if reply == nil || reply.ParentExternalID != "yt.comment:UgxSYNTHETIC0000000001" || reply.Targets[0].TargetExternalID != "UgxSYNTHETIC0000000001" {
		t.Errorf("reply not mapped: %+v", reply)
	}

	// Subscriptions are undated follows.
	sub := byTitle(events, "google.youtube.subscribe", "Subscribed to Cooking, Slowly")
	if sub == nil || !sub.OccurredAt.IsZero() || sub.ExternalID != "yt.subscribe:UCdddddddddddddddddddddd" ||
		sub.Targets[0].Relation != domain.RelationFollowed || sub.Targets[0].TargetURL != "http://www.youtube.com/channel/UCdddddddddddddddddddddd" {
		t.Errorf("subscription not mapped: %+v", sub)
	}

	// Playlists: name from the file name; the same video and timestamp in
	// "Watch later" and "Liked videos" must both survive dedupe.
	playlists := map[string]int{}
	for _, ev := range events {
		if ev.Type == "google.youtube.playlist_item" {
			playlists[ev.Metadata["playlist"].(string)]++
			if ev.Targets[0].Relation != domain.RelationSaved || ev.OccurredAt.IsZero() {
				t.Errorf("playlist item not mapped: %+v", ev)
			}
		}
	}
	if len(playlists) != 2 || playlists["Watch later"] != 2 || playlists["Road trip"] != 1 {
		t.Errorf("playlist items = %v, want Watch later ×2 and Road trip ×1", playlists)
	}
	likes := map[string]bool{}
	for _, ev := range events {
		if ev.Type == "google.youtube.like" {
			likes[ev.Targets[0].TargetExternalID] = ev.Targets[0].Relation == domain.RelationLiked && ev.Metadata["playlist"] == "Liked videos"
		}
	}
	if !likes["aaaaaaaaa01"] || !likes["aaaaaaaaa06"] {
		t.Errorf("likes = %v, want aaaaaaaaa01 and aaaaaaaaa06", likes)
	}
}

// A German export: localized folders, file names, CSV headers and sentences, and
// the search history in Takeout's default HTML format.
func TestImportLocalizedTakeout(t *testing.T) {
	events, cov := importFixture(t, "de")

	checkCoverage(t, events, cov, map[string]int{
		"google.youtube.watch":         2,
		"google.youtube.comment":       1,
		"google.youtube.subscribe":     1,
		"google.youtube.playlist_item": 1,
		"google.search":                1, // Meine Aktivitäten/Google Suche
		"google.visit":                 1,
		"google.location.point":        1, // Standortverlauf (Zeitachse)/Records.json
		"google.keep.note":             1,
		"google.maps.saved":            1, // Maps (Meine Orte)/Gespeicherte Orte.json
		"google.unknown":               1, // Suchverlauf.html is not parsed
	})
	if len(cov.GenericMembers) != 1 || filepath.Base(cov.GenericMembers[0]) != "Suchverlauf.html" {
		t.Errorf("GenericMembers = %v, want only the HTML history", cov.GenericMembers)
	}

	w := byTitle(events, "google.youtube.watch", "Fahrrad richtig putzen")
	if w == nil || w.Metadata["raw_title"] != "Fahrrad richtig putzen angesehen" || w.Metadata["channel"] != "Beispielkanal" {
		t.Errorf("German watch not mapped: %+v", w)
	}
	if ad := byTitle(events, "google.youtube.watch", "Beispielwerbung"); ad == nil || ad.Metadata["ad"] != true {
		t.Errorf("German ad not flagged: %+v", ad)
	}
	if c := byBody(events, "google.youtube.comment", "Danke für die Tipps!"); c == nil || c.OccurredAt.IsZero() || c.Metadata["video_id"] != "bbbbbbbbb01" {
		t.Errorf("comment with German headers not mapped: %+v", c)
	}
	if s := byTitle(events, "google.youtube.subscribe", "Subscribed to Beispielkanal"); s == nil || s.ExternalID != "yt.subscribe:UCeeeeeeeeeeeeeeeeeeeeee" {
		t.Errorf("subscription with German headers not mapped: %+v", s)
	}
	if p := byTitle(events, "google.youtube.playlist_item", "Saved a video to Später ansehen"); p == nil || p.OccurredAt.IsZero() || p.Metadata["video_id"] != "bbbbbbbbb01" {
		t.Errorf("playlist with German headers not mapped: %+v", p)
	}

	// My Activity in German: the product is recognized from "Google Suche", the
	// query still comes from the URL, and the German frames are stripped.
	if s := byBody(events, "google.search", "fahrrad zentrieren"); s == nil || s.Metadata["product"] != "Google Suche" || s.Metadata["raw_title"] != nil {
		t.Errorf("German search not mapped: %+v", s)
	}
	if v := byTitle(events, "google.visit", "Beispielseite"); v == nil || v.Metadata["raw_title"] != "Beispielseite aufgerufen" || v.Metadata["url"] != "https://beispiel.example.de/" {
		t.Errorf("German visit not mapped: %+v", v)
	}
	if p := byTitle(events, "google.location.point", "Location 48.1371000, 11.5754000"); p == nil || p.Metadata["source"] != "GPS" {
		t.Errorf("German Records.json not mapped: %+v", p)
	}
	if n := byTitle(events, "google.keep.note", "Einkauf"); n == nil || n.Body != "Brot, Käse" {
		t.Errorf("German Keep note not mapped: %+v", n)
	}
	if m := byTitle(events, "google.maps.saved", "Saved Beispiel Radladen in Google Maps"); m == nil || m.Metadata["lat"] != 48.1371 {
		t.Errorf("German saved place not mapped: %+v", m)
	}
}

func TestMatch(t *testing.T) {
	cases := map[string]bool{
		"Takeout/YouTube and YouTube Music/history/watch-history.json":                         true,
		"Takeout/YouTube/history/search-history.json":                                          true,
		"Takeout/YouTube and YouTube Music/history/watch-history.html":                         false,
		"Takeout/YouTube and YouTube Music/playlists/playlists.csv":                            false,
		"Takeout/YouTube and YouTube Music/playlists/Favs-videos.csv":                          true,
		"Takeout/YouTube and YouTube Music/comments/comments(1).csv":                           true,
		"Takeout/YouTube and YouTube Music/channels/channel.csv":                               false,
		"Takeout/My Activity/YouTube/MyActivity.json":                                          true,
		"Takeout/My Activity/Search/MyActivity.json":                                           true,
		"Takeout/My Activity/Search/MyActivity.html":                                           false,
		"Takeout/Meine Aktivitäten/Google Suche/MeineAktivitäten.json":                         true,
		"Takeout/My Activity/Assistant/2024-01-01_recording.mp3":                               false,
		"Takeout/Chrome/History.json":                                                          true,
		"Takeout/Chrome/BrowserHistory.json":                                                   true,
		"Takeout/Chrome/Bookmarks.html":                                                        true,
		"Takeout/Chrome/SearchEngines.json":                                                    false,
		"Takeout/Chrome/Extensions.json":                                                       false,
		"Takeout/Location History (Timeline)/Records.json":                                     true,
		"Takeout/Standortverlauf (Zeitachse)/Records.json":                                     true,
		"Takeout/Location History (Timeline)/Semantic Location History/2021/2021_JANUARY.json": true,
		"Takeout/Location History (Timeline)/Settings.json":                                    false,
		"Takeout/Timeline/Timeline.json":                                                       true,
		"Takeout/Timeline/location-history.json":                                               true,
		"Takeout/Maps (your places)/Saved Places.json":                                         true,
		"Takeout/Maps (your places)/Reviews.json":                                              true,
		"Takeout/Maps (Meine Orte)/Gespeicherte Orte.json":                                     true,
		"Takeout/Maps/My labeled places/Labeled places.json":                                   true,
		"Takeout/Maps/Commute settings/Commute settings.json":                                  false,
		"Takeout/Saved/Want to go.csv":                                                         true,
		"Takeout/Keep/Groceries.json":                                                          true,
		"Takeout/Keep/Groceries.html":                                                          false,
		"Takeout/Google Chat/Groups/Space AAAA/messages.json":                                  true,
		"Takeout/Google Chat/Groups/Space AAAA/group_info.json":                                false,
		"Takeout/Google Chat/Users/User 1/user_info.json":                                      false,
		"Takeout/Hangouts/Hangouts.json":                                                       true,
		"Takeout/Google Photos/Photos from 2024/IMG_0001.jpg.json":                             true,
		"Takeout/Google Photos/Photos from 2024/IMG_0001.jpg.supplemental-metad.json":          true,
		"Takeout/Google Photos/Photos from 2024/metadata.json":                                 false,
		"Takeout/Google Photos/Photos from 2024/IMG_0001.jpg":                                  false,
		"Takeout/Mail/All mail Including Spam and Trash.mbox":                                  false,
		"Takeout/Calendar/Personal.ics":                                                        false,
		"Takeout/Google Play Store/subscriptions/subscriptions.csv":                            false,
		"Takeout/YouTube und YouTube Music/Verlauf/Irgendwas.json":                             true,
		"Takeout/YouTube and YouTube Music/video metadata/comments.json":                       false,
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

func byTitle(evs []domain.Event, typ, title string) *domain.Event {
	for i := range evs {
		if evs[i].Type == typ && evs[i].Title == title {
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
