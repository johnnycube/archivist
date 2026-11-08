package google

import (
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/domain"
)

// enPerType is the exact per-type count of the en fixture (the whole Takeout).
var enPerType = map[string]int{
	// YouTube (unchanged)
	"google.youtube.watch":         5, // video + the same video re-watched + ad + YouTube Music + removed video
	"google.youtube.search":        2,
	"google.youtube.comment":       3,
	"google.youtube.subscribe":     2,
	"google.youtube.playlist_item": 3, // Watch later ×2 + Road trip
	"google.youtube.like":          2, // Liked videos playlist
	// My Activity
	"google.search":            4, // Search ×2, Image Search, Video Search
	"google.visit":             2, // Search "Visited …", YouTube "Visited YouTube Music"
	"google.chrome.search":     1,
	"google.chrome.visit":      2, // My Activity/Chrome + Chrome/History.json
	"google.maps.activity":     3,
	"google.play.activity":     2,
	"google.assistant.query":   1,
	"google.gemini.prompt":     1,
	"google.discover.view":     1,
	"google.ads.view":          1,
	"google.shopping.activity": 1,
	"google.books.activity":    1,
	"google.activity":          2, // "Used Search", Takeout
	// Chrome
	"google.chrome.bookmark": 3,
	// Location
	"google.location.visit": 3, // 2 semantic placeVisits + 1 Timeline.json visit
	"google.location.trip":  2, // 1 activitySegment + 1 Timeline.json activity
	"google.location.point": 5, // 2 Records.json + 2 timelinePath + 1 rawSignals position
	// Maps
	"google.maps.saved":  5, // Saved Places ×2, Labeled places, Want to go ×2
	"google.maps.review": 1,
	// Keep, Chat, Photos
	"google.keep.note":        2,
	"google.chat.message":     2,
	"google.hangouts.message": 1,
	"google.photos.photo":     1,
	// archive_browser.html, playlists.csv, Keep/Idea.html, group_info.json,
	// route.gpx (no blob store in the test), Photos metadata.json, the Hangouts
	// call event, the rawSignals activityRecord, Timeline.json's
	// userLocationProfile.frequentPlaces (skipped array).
	"google.unknown": 9,
}

func TestImportTakeoutProducts(t *testing.T) {
	events, _ := importFixture(t, "en")
	utc := func(y int, m time.Month, d, h, min, s int) time.Time {
		return time.Date(y, m, d, h, min, s, 0, time.UTC)
	}

	// Search: query from the URL, location info kept, ad flagged, offset → UTC.
	s := byBody(events, "google.search", "bicycle wheel truing stand")
	if s == nil || s.Title != "Searched Google" || s.Metadata["action"] != "searched" || s.Metadata["location"] != "At this general area - From your places (Home)" || s.Metadata["kind"] != nil {
		t.Errorf("search not mapped: %+v", s)
	}
	if s := byBody(events, "google.search", "café racer & co"); s == nil || s.Metadata["ad"] != true || !s.OccurredAt.Equal(utc(2024, 3, 4, 18, 0, 0)) {
		t.Errorf("ad search not mapped: %+v", s)
	}
	if s := byBody(events, "google.search", "vintage bicycle poster"); s == nil || s.Metadata["kind"] != "image" || s.Title != "Searched Google Images" {
		t.Errorf("image search not mapped: %+v", s)
	}
	if s := byBody(events, "google.search", "spoke wrench review"); s == nil || s.Metadata["kind"] != "video" {
		t.Errorf("video search not mapped: %+v", s)
	}
	if v := byTitle(events, "google.visit", "Wheel Truing 101 - Example Bike Wiki"); v == nil || v.Metadata["url"] != "https://bikes.example.org/wiki/wheel-truing" || v.Metadata["raw_title"] != nil {
		t.Errorf("visit not mapped: %+v", v)
	}
	if g := byTitle(events, "google.activity", "Used Search"); g == nil || g.Metadata["product"] != "Search" || g.Metadata["action"] != "used" {
		t.Errorf("unframed Search activity should be google.activity: %+v", g)
	}
	if g := byTitle(events, "google.activity", "Created an export"); g == nil || g.Metadata["product"] != "Takeout" || g.OccurredAt.IsZero() {
		t.Errorf("Takeout activity should be a dated google.activity: %+v", g)
	}

	// Chrome: My Activity and History.json both map; the bookmark tree keeps folders.
	if c := byTitle(events, "google.chrome.visit", "Example Domain"); c == nil || c.Metadata["url"] != "https://example.com/" || c.Metadata["origin"] != nil {
		t.Errorf("Chrome activity visit not mapped: %+v", c)
	}
	h := byTitle(events, "google.chrome.visit", "Example")
	if h == nil || h.Metadata["origin"] != "chrome_history" || !h.OccurredAt.Equal(utc(2023, 11, 14, 22, 13, 20)) {
		t.Errorf("Chrome History.json not mapped: %+v", h)
	}
	if c := byBody(events, "google.chrome.search", "chrome flags"); c == nil {
		t.Errorf("Chrome search not mapped")
	}
	b := byTitle(events, "google.chrome.bookmark", "Bookmarked Wheel Truing 101 & more")
	if b == nil || b.Metadata["folder"] != "Bookmarks bar/Bikes & Gear" || !b.OccurredAt.Equal(utc(2023, 11, 14, 22, 14, 30)) || b.Targets[0].Relation != domain.RelationSaved {
		t.Errorf("nested bookmark not mapped: %+v", b)
	}
	if b := byTitle(events, "google.chrome.bookmark", "Example Domain (again)"); b != nil {
		t.Errorf("bookmark title should carry the verb: %+v", b)
	}
	if b := byTitle(events, "google.chrome.bookmark", "Bookmarked Example Domain (again)"); b == nil || b.Metadata["folder"] != nil {
		t.Errorf("root-level bookmark not mapped: %+v", b)
	}

	// Maps, Play, Assistant, Gemini, Discover, Ads, Shopping, Books.
	m := byBody(events, "google.maps.activity", "bike shop near me")
	if m == nil || m.Metadata["location"] != "At this general area - From your device" || m.Metadata["action"] != "searched" {
		t.Errorf("Maps search not mapped: %+v", m)
	}
	if d := byTitle(events, "google.maps.activity", "Directions to Example Bike Shop"); d == nil || d.Metadata["destination"] != "Example Bike Shop" {
		t.Errorf("Maps directions not mapped: %+v", d)
	}
	if p := byTitle(events, "google.play.activity", "Installed Example Fitness Tracker"); p == nil || p.Metadata["app"] != "Example Fitness Tracker" || p.Metadata["action"] != "installed" {
		t.Errorf("Play install not mapped: %+v", p)
	}
	if a := byBody(events, "google.assistant.query", "set a timer for ten minutes"); a == nil || a.Metadata["response"] != "Sure, ten minutes, starting now." || a.Category != domain.CategoryMessage {
		t.Errorf("Assistant query not mapped: %+v", a)
	}
	if g := byBody(events, "google.gemini.prompt", "Explain how to true a bicycle wheel in three steps"); g == nil || g.Title != "Prompted Gemini" {
		t.Errorf("Gemini prompt not mapped: %+v", g)
	}
	if d := byTitle(events, "google.discover.view", "Synthetic Gazette: Bike lanes expand downtown"); d == nil || d.Metadata["url"] != "https://news.example.com/bike-lanes" {
		t.Errorf("Discover view not mapped: %+v", d)
	}
	if a := byTitle(events, "google.ads.view", "Viewed ad for Example Phone"); a == nil || a.Metadata["ad"] != true || a.Category != domain.CategoryOther {
		t.Errorf("ad not mapped: %+v", a)
	}
	if s := byBody(events, "google.shopping.activity", "spoke wrench"); s == nil {
		t.Errorf("Shopping search not mapped")
	}
	if b := byTitle(events, "google.books.activity", "Used Google Play Books"); b == nil || b.Metadata["product"] != "Google Play Books" {
		t.Errorf("Books activity not mapped (must not be Play Store): %+v", b)
	}

	// Location: E7 coordinates, RFC3339 and *Ms timestamps, on-device strings.
	v := byTitle(events, "google.location.visit", "Visited Example Square")
	if v == nil || v.Metadata["lat"] != 52.52 || v.Metadata["lon"] != 13.405 || v.Metadata["place_id"] != "ChIJSYNTHETIC0000000000001" ||
		v.Metadata["duration_min"] != int64(90) || v.Metadata["end"] != "2024-03-06T09:40:00Z" || !v.OccurredAt.Equal(utc(2024, 3, 6, 8, 10, 0)) {
		t.Errorf("placeVisit not mapped: %+v", v)
	}
	if v := byTitle(events, "google.location.visit", "Visited Home"); v == nil || !v.OccurredAt.Equal(time.UnixMilli(1709719500000).UTC()) || v.Metadata["confidence"] != "USER_CONFIRMED" {
		t.Errorf("placeVisit with startTimestampMs not mapped: %+v", v)
	}
	if tr := byTitle(events, "google.location.trip", "Traveled cycling (3.9 km)"); tr == nil || tr.Metadata["distance_m"] != int64(3850) || tr.Metadata["end_lat"] != 52.5 || tr.Metadata["activity_type"] != "CYCLING" {
		t.Errorf("activitySegment not mapped: %+v", tr)
	}
	if p := byTitle(events, "google.location.point", "Location 52.5200123, 13.4050456"); p == nil || p.Metadata["accuracy_m"] != float64(12) || p.Metadata["source"] != "WIFI" || !p.OccurredAt.Equal(utc(2024, 3, 6, 8, 0, 0)) {
		t.Errorf("Records.json point not mapped: %+v", p)
	}
	if p := byTitle(events, "google.location.point", "Location 52.5200200, 13.4050500"); p == nil || !p.OccurredAt.Equal(time.UnixMilli(1709712300000).UTC()) {
		t.Errorf("Records.json timestampMs not mapped: %+v", p)
	}
	if w := byTitle(events, "google.location.visit", "Visited Work"); w == nil || w.Metadata["lat"] != 50.0506312 || w.Metadata["place_id"] != "ChIJSYNTHETIC0000000000003" || !w.OccurredAt.Equal(utc(2024, 4, 3, 6, 13, 57)) || w.Metadata["probability"] != 0.87 {
		t.Errorf("Timeline.json visit not mapped: %+v", w)
	}
	if tr := byTitle(events, "google.location.trip", "Traveled in passenger vehicle (6.1 km)"); tr == nil || tr.Metadata["distance_m"] != int64(6100) || tr.Metadata["end_lon"] != 14.42 {
		t.Errorf("Timeline.json activity not mapped: %+v", tr)
	}
	if p := byTitle(events, "google.location.point", "Location 50.0700000, 14.4000000"); p == nil || !p.OccurredAt.Equal(utc(2024, 4, 3, 8, 15, 0)) || p.Metadata["lat"] != 50.07 {
		t.Errorf("iOS geo: point with minute offset not mapped: %+v", p)
	}
	if p := byTitle(events, "google.location.point", "Location 48.8336570, 2.2562230"); p == nil || p.Metadata["speed_mps"] != 0.07 || !p.OccurredAt.Equal(utc(2024, 6, 6, 10, 44, 37)) {
		t.Errorf("rawSignals position not mapped: %+v", p)
	}
	if u := byTitle(events, "google.unknown", `Unmapped export data: Takeout/Timeline/Timeline.json ("userLocationProfile.frequentPlaces", 1 records)`); u == nil {
		t.Errorf("userLocationProfile should be reported as a skipped array")
	}

	// Maps places and reviews.
	sp := byTitle(events, "google.maps.saved", "Saved Example Bike Shop in Google Maps")
	if sp == nil || sp.Metadata["lat"] != 52.52 || sp.Metadata["address"] != "Example Square 1, 10178 Berlin, Germany" || !sp.OccurredAt.Equal(utc(2023, 5, 1, 10, 0, 0)) || sp.Targets[0].TargetURL != "http://maps.google.com/?cid=1234567890123456789" {
		t.Errorf("saved place not mapped: %+v", sp)
	}
	if lp := byTitle(events, "google.maps.saved", "Labeled Home in Google Maps"); lp == nil || lp.Metadata["list"] != "labeled" || lp.Metadata["lat"] != 52.5 {
		t.Errorf("labeled place not mapped: %+v", lp)
	}
	if l := byTitle(events, "google.maps.saved", "Saved Synthetic Bakery to Want to go"); l == nil || l.Metadata["note"] != "Try the rye" || l.Metadata["list"] != "Want to go" {
		t.Errorf("saved list row not mapped: %+v", l)
	}
	r := byBody(events, "google.maps.review", "Trued my wheel while I waited. Friendly staff.")
	if r == nil || r.Metadata["rating"] != "5" || r.Metadata["place"] != "Example Bike Shop" || !r.OccurredAt.Equal(utc(2023, 6, 2, 12, 0, 0)) {
		t.Errorf("review not mapped: %+v", r)
	}

	// Keep: list rendered, flags, attachment next to the note.
	if n := byTitle(events, "google.keep.note", "Groceries"); n == nil || n.Body != "[x] Milk\n[ ] Spokes (36)" || n.Metadata["pinned"] != true || n.Metadata["labels"] != "Shopping" || n.ExternalID != "g.keep.note:1709700000000000" || !n.OccurredAt.Equal(time.UnixMicro(1709700000000000).UTC()) {
		t.Errorf("Keep list not mapped: %+v", n)
	}
	if n := byTitle(events, "google.keep.note", "Idea"); n == nil || n.Metadata["archived"] != true || n.Metadata["color"] != "BLUE" || len(n.Media) != 1 || n.Media[0].BlobKey != "Takeout/Keep/Idea.png" || n.Metadata["links"] != "https://bikes.example.org/wiki/wheel-truing" {
		t.Errorf("Keep note not mapped: %+v", n)
	}

	// Chat: sentence dates, actor, thread, attachment blob key, reactions.
	c := byBody(events, "google.chat.message", "Sure, here is the route.")
	if c == nil || c.Actor != "Bob Example" || c.ExternalID != "g.chat.message:msg-0002" || c.ThreadExternalID != "g.chat:Space AAAAsynthetic1" ||
		!c.OccurredAt.Equal(utc(2016, 7, 14, 17, 6, 0)) || len(c.Media) != 1 || c.Media[0].BlobKey != "Takeout/Google Chat/Groups/Space AAAAsynthetic1/route.gpx" ||
		c.Metadata["links"] != "https://routes.example.com/1" || c.Metadata["reactions"] != "👍" {
		t.Errorf("chat message not mapped: %+v", c)
	}
	if !New().IsAttachment("Takeout/Google Chat/Groups/Space AAAAsynthetic1/route.gpx") || New().IsAttachment("Takeout/Google Chat/Groups/Space AAAAsynthetic1/group_info.json") {
		t.Errorf("IsAttachment should cover chat files but not data files")
	}
	hm := byBody(events, "google.hangouts.message", "Look at this: bikes.example.org\nsecond line")
	if hm == nil || hm.Actor != "Alice Example" || hm.ThreadExternalID != "g.hangouts:UgxSYNTHETICconv0001" || !hm.OccurredAt.Equal(utc(2016, 7, 14, 17, 4, 34)) || hm.Metadata["links"] != "https://bikes.example.org/" {
		t.Errorf("Hangouts message not mapped: %+v", hm)
	}
	if he := byTitle(events, "google.unknown", "Hangouts event HANGOUT_EVENT"); he == nil || he.Metadata["links"] != nil || he.ExternalID != "g.hangouts.event:evt-0002" {
		t.Errorf("Hangouts call event should be a distinct unknown record: %+v", he)
	}

	// Photos: sidecar → Media event pointing at the file next to it.
	ph := byTitle(events, "google.photos.photo", "IMG_0001.jpg")
	if ph == nil || ph.Body != "Freshly trued wheel" || !ph.OccurredAt.Equal(utc(2024, 3, 6, 8, 0, 0)) || len(ph.Media) != 1 ||
		ph.Media[0].BlobKey != "Takeout/Google Photos/Photos from 2024/IMG_0001.jpg" || ph.Metadata["lat"] != 52.52 || ph.Metadata["people"] != "Alice Example" || ph.Metadata["favorited"] != true || ph.Metadata["views"] != int64(3) {
		t.Errorf("photo sidecar not mapped: %+v", ph)
	}
}

// Every product folder of the fixture yields a dated event with the product
// recorded, whatever its type.
func TestMyActivityProductsAreDated(t *testing.T) {
	events, _ := importFixture(t, "en")
	for _, ev := range events {
		if _, ok := ev.Metadata["product"]; !ok {
			continue
		}
		if ev.OccurredAt.IsZero() {
			t.Errorf("My Activity event without time: %+v", ev)
		}
	}
}

func TestVerbOf(t *testing.T) {
	cases := []struct {
		title, action, object string
		english               bool
	}{
		{"Searched for wheel", "searched", "wheel", true},
		{"Nach wheel gesucht", "searched", "wheel", false},
		{"Gesucht nach wheel", "searched", "wheel", false},
		{"Visited example.com", "visited", "example.com", true},
		{"example.com aufgerufen", "visited", "example.com", false},
		{"Installed App", "installed", "App", true},
		{"Prompted hello", "prompted", "hello", true},
		{"Something else entirely", "", "Something else entirely", false},
		{"Visited ", "", "Visited ", false}, // no object
	}
	for _, c := range cases {
		action, object, english, _ := verbOf(c.title)
		if action != c.action || object != c.object || english != c.english {
			t.Errorf("verbOf(%q) = %q %q %v, want %q %q %v", c.title, action, object, english, c.action, c.object, c.english)
		}
	}
}

func TestParseLatLng(t *testing.T) {
	for s, want := range map[string][2]float64{
		"50.0506312°, 14.3439906°": {50.0506312, 14.3439906},
		"geo:-33.8688,151.2093":    {-33.8688, 151.2093},
		"52.52°, 13.405°":          {52.52, 13.405},
	} {
		lat, lon, ok := parseLatLng(s)
		if !ok || lat != want[0] || lon != want[1] {
			t.Errorf("parseLatLng(%q) = %v %v %v", s, lat, lon, ok)
		}
	}
	for _, s := range []string{"", "nope", "1.0", "1,2,3"} {
		if _, _, ok := parseLatLng(s); ok {
			t.Errorf("parseLatLng(%q) should fail", s)
		}
	}
}
