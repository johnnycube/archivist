package tiktok

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
)

func importDir(t *testing.T, dir string) (*domain.Coverage, []domain.Event) {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "tiktok.zip")
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
	return cov, events
}

func checkCounts(t *testing.T, cov *domain.Coverage, want map[string]int) {
	t.Helper()
	if len(cov.PerType) != len(want) {
		t.Errorf("PerType has %d types, want %d: %v", len(cov.PerType), len(want), cov.PerType)
	}
	for typ, n := range want {
		if cov.PerType[typ] != n {
			t.Errorf("PerType[%q] = %d, want %d", typ, cov.PerType[typ], n)
		}
	}
}

func checkUnique(t *testing.T, p *Provider, events []domain.Event) {
	t.Helper()
	declared := map[string]domain.Category{}
	for _, d := range p.Descriptor().EventTypes {
		declared[d.Type] = d.Category
	}
	ids := map[string]int{}
	for _, ev := range events {
		ids[ev.ExternalID]++
		if cat, ok := declared[ev.Type]; !ok || cat != ev.Category {
			t.Errorf("event type %q / category %q not declared in the descriptor", ev.Type, ev.Category)
		}
		if ev.SourceID != sourceID || ev.ExternalID == "" {
			t.Errorf("event lacks source or id: %+v", ev)
		}
	}
	for id, n := range ids {
		if n > 1 {
			t.Errorf("ExternalID %q used by %d events", id, n)
		}
	}
}

func TestImportTikTok(t *testing.T) {
	cov, events := importDir(t, "export")

	checkCounts(t, cov, map[string]int{
		"tiktok.watch":    3,
		"tiktok.like":     2,
		"tiktok.save":     2, // favorite video + favorite effect
		"tiktok.search":   2,
		"tiktok.follow":   2,
		"tiktok.follower": 2,
		"tiktok.share":    1,
		"tiktok.login":    1,
		"tiktok.comment":  2,
		"tiktok.message":  4,
		"tiktok.post":     2,
		"tiktok.block":    1,
		"tiktok.unknown":  2, // settings map + profile map
	})
	checkUnique(t, New(), events)
	if cov.Duplicates != 0 {
		// The same {"Date","Link"} is watched, liked, favorited and posted.
		t.Errorf("Duplicates = %d: identical records of different sections must not dedupe", cov.Duplicates)
	}

	// "VideoList" is the watch history under Activity and the own videos under Video.
	const shared = "https://www.tiktokv.com/share/video/7300000000000000001/"
	byLink := map[string]domain.Event{}
	for _, ev := range events {
		if ev.Metadata["link"] == shared {
			byLink[ev.Type] = ev
		}
	}
	for _, typ := range []string{"tiktok.watch", "tiktok.like", "tiktok.save", "tiktok.post"} {
		if _, ok := byLink[typ]; !ok {
			t.Errorf("no %s event for the video listed in every section", typ)
		}
	}
	wantAt := time.Date(2024, 1, 31, 12, 34, 56, 0, time.UTC)
	if w := byLink["tiktok.watch"]; !w.OccurredAt.Equal(wantAt) || w.OccurredAt.Location() != time.UTC || w.Category != domain.CategoryConsumption {
		t.Errorf("watch not mapped: %+v", w)
	}
	if l := byLink["tiktok.like"]; len(l.Targets) != 1 || l.Targets[0].Relation != domain.RelationLiked ||
		l.Targets[0].TargetURL != shared || l.Targets[0].TargetExternalID != "7300000000000000001" {
		t.Errorf("like target not mapped: %+v", l)
	}
	if s := byLink["tiktok.save"]; len(s.Targets) != 1 || s.Targets[0].Relation != domain.RelationSaved || s.Category != domain.CategorySave {
		t.Errorf("save not mapped: %+v", s)
	}

	var titled *domain.Event
	for i := range events {
		if events[i].Type == "tiktok.post" && events[i].Title == "First ride of the year" {
			titled = &events[i]
		}
	}
	if titled == nil || titled.Metadata["likes"] != "12" || titled.OccurredAt.IsZero() {
		t.Errorf("own video not mapped: %+v", titled)
	}
	if byLink["tiktok.post"].Metadata["likes"] != "3" {
		t.Errorf("numeric Likes not kept: %+v", byLink["tiktok.post"])
	}

	// Direct messages: the thread comes from the dynamic array name.
	msg := byBody(events, "tiktok.message", "See you tomorrow")
	if msg == nil || msg.Actor != "johnny_example" || msg.ThreadExternalID != "jane_doe" {
		t.Errorf("message not mapped: %+v", msg)
	}
	threads := map[string]string{}
	for _, ev := range events {
		if ev.Type == "tiktok.message" && ev.Body == "Haha, love it" {
			threads[ev.ThreadExternalID] = ev.Actor
		}
	}
	if threads["jane_doe"] != "jane_doe" || threads["max_mustermann"] != "max_mustermann" {
		t.Errorf("same text in two chats should yield one message per thread: %v", threads)
	}

	if c := byBody(events, "tiktok.comment", "Great tip, thanks!"); c == nil || c.Category != domain.CategoryContent {
		t.Errorf("comment not mapped: %+v", c)
	}
	if s := byBody(events, "tiktok.search", "café latte art"); s == nil {
		t.Errorf("search not mapped")
	}
	follows := map[string]bool{}
	for _, ev := range events {
		if ev.Type == "tiktok.follow" && len(ev.Targets) == 1 && ev.Targets[0].Relation == domain.RelationFollowed {
			follows[ev.Targets[0].TargetExternalID] = true
		}
	}
	if !follows["jane_doe"] || !follows["bike.channel"] {
		t.Errorf("follow targets = %v", follows)
	}
	if f := byType(events, "tiktok.follower"); f == nil || f.Actor != "max_mustermann" {
		t.Errorf("follower not mapped: %+v", f)
	}
	if sh := byType(events, "tiktok.share"); sh == nil || len(sh.Targets) != 1 || sh.Targets[0].Relation != domain.RelationShared ||
		sh.Metadata["method"] != "WhatsApp" || sh.Title != "Shared a TikTok video via WhatsApp" {
		t.Errorf("share not mapped: %+v", sh)
	}
	if l := byType(events, "tiktok.login"); l == nil || l.Metadata["ip"] != "203.0.113.7" || l.Metadata["device_model"] != "Pixel 8" || l.Metadata["carrier"] != "Example Mobile" {
		t.Errorf("login not mapped: %+v", l)
	}

	// Sections without a type of their own are kept, not dropped.
	unknown := map[string]domain.Event{}
	for _, ev := range events {
		if ev.Type == "tiktok.unknown" {
			unknown[ev.Metadata["section"].(string)] = ev
		}
	}
	for _, section := range []string{
		"App Settings / Settings / SettingsMap",
		"Profile / Profile Information / ProfileMap",
	} {
		if _, ok := unknown[section]; !ok {
			t.Errorf("unmapped section %q was dropped; have %v", section, keys(unknown))
		}
	}
	if p := unknown["Profile / Profile Information / ProfileMap"]; !strings.Contains(p.Metadata["preview"].(string), `"likesReceived":42`) {
		t.Errorf("scalar fields should be previewed: %+v", p)
	}
	if b := byType(events, "tiktok.block"); b == nil || b.OccurredAt.IsZero() || len(b.Targets) != 1 || b.Targets[0].TargetExternalID != "spam_account_1" {
		t.Errorf("block list not mapped: %+v", b)
	}
	var effect *domain.Event
	for i := range events {
		if events[i].Type == "tiktok.save" && events[i].Metadata["kind"] == "effect" {
			effect = &events[i]
		}
	}
	if effect == nil || effect.Targets[0].TargetURL != "https://www.tiktok.com/sticker/Green-Screen-100001" {
		t.Errorf("favorite effect not mapped: %+v", effect)
	}
}

// The TXT flavour of the export: one "Key: Value" block file per section.
func TestImportTikTokText(t *testing.T) {
	cov, events := importDir(t, "text")
	checkCounts(t, cov, map[string]int{
		"tiktok.watch":   2,
		"tiktok.like":    1,
		"tiktok.post":    1,
		"tiktok.message": 3,
		"tiktok.login":   1,
		"tiktok.unknown": 1, // Profile Information.txt has no section mapping
	})
	checkUnique(t, New(), events)
	if cov.Mapped != 5 || cov.Generic != 1 {
		t.Errorf("mapped/generic = %d/%d, want 5/1", cov.Mapped, cov.Generic)
	}
	w := byType(events, "tiktok.watch")
	if w == nil || w.OccurredAt.IsZero() || w.Targets[0].TargetExternalID != "7400000000000000001" {
		t.Errorf("txt watch not mapped: %+v", w)
	}
	if p := byType(events, "tiktok.post"); p == nil || p.Metadata["link"] != "https://www.tiktokv.com/share/video/7399999999999999999/" || p.Metadata["likes"] != "5" {
		t.Errorf("txt post (Video Link:) not mapped: %+v", p)
	}
	if m := byBody(events, "tiktok.message", "See you tomorrow"); m == nil || m.ThreadExternalID != "jane_doe" || m.Actor != "johnny_example" {
		t.Errorf("txt message not mapped: %+v", m)
	}
	if m := byBody(events, "tiktok.message", "Hi from Max\nsecond line"); m == nil || m.ThreadExternalID != "max_mustermann" {
		t.Errorf("multi-line txt message not mapped: %+v", m)
	}
	if l := byType(events, "tiktok.login"); l == nil || l.Metadata["device_model"] != "Pixel 8" || l.OccurredAt.IsZero() {
		t.Errorf("txt login (zone-suffixed date) not mapped: %+v", l)
	}
}

// Export versions differ in section names (and in the file name); arrays are
// still found, and a "VideoList" under an unrelated section is not mistaken for the watch history.
func TestImportTikTokAliases(t *testing.T) {
	cov, events := importDir(t, "aliases")
	checkCounts(t, cov, map[string]int{
		"tiktok.watch":   2, // Your Activity / Watch History
		"tiktok.search":  1,
		"tiktok.like":    1, // lower-case member names
		"tiktok.post":    1, // Post / Posts
		"tiktok.unknown": 1, // Tiktok Live / Watch Live History / VideoList
	})
	checkUnique(t, New(), events)

	if l := byType(events, "tiktok.like"); l == nil || l.OccurredAt.IsZero() || l.Targets[0].TargetExternalID != "7400000000000000001" {
		t.Errorf("like with lower-case keys not mapped: %+v", l)
	}
	if u := byType(events, "tiktok.unknown"); u == nil || u.Metadata["section"] != "Tiktok Live / Watch Live History / VideoList" || !u.OccurredAt.IsZero() {
		t.Errorf("live history should stay unknown and undated: %+v", u)
	}
}

func TestStableExternalIDs(t *testing.T) {
	_, first := importDir(t, "export")
	_, second := importDir(t, "export")
	if len(first) != len(second) {
		t.Fatalf("event counts differ: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ExternalID != second[i].ExternalID {
			t.Errorf("ExternalID changed between imports: %q vs %q", first[i].ExternalID, second[i].ExternalID)
		}
	}
}

func TestMatch(t *testing.T) {
	ex := userDataEx{}
	for member, want := range map[string]bool{
		"user_data_tiktok.json":               true,
		"user_data.json":                      true,
		"TikTok_Data/user_data.json":          true,
		"Activity/Like List.txt":              true,
		"Your Activity/Watch History.txt":     true,
		"Direct Messages/Direct Messages.txt": true,
		"Profile/Profile Information.txt":     false,
		"Video/7300000000000000001.mp4":       false,
	} {
		got := false
		for _, e := range New().Extractors() {
			got = got || e.Match(member)
		}
		if got != want {
			t.Errorf("Match(%q) = %v, want %v", member, got, want)
		}
	}
	_ = ex
}

func byType(evs []domain.Event, typ string) *domain.Event {
	for i := range evs {
		if evs[i].Type == typ {
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
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
