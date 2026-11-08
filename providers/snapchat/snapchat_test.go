package snapchat

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
	zipPath := filepath.Join(t.TempDir(), "snapchat.zip")
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

func checkUnique(t *testing.T, events []domain.Event) {
	t.Helper()
	declared := map[string]domain.Category{}
	for _, d := range New().Descriptor().EventTypes {
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

func TestImportSnapchat(t *testing.T) {
	cov, events := importDir(t, "export")

	checkCounts(t, cov, map[string]int{
		"snapchat.message":        5,
		"snapchat.snap":           2,
		"snapchat.memory":         3,
		"snapchat.friend":         4, // 2 friends + blocked + deleted
		"snapchat.friend_request": 1,
		"snapchat.search":         2,
		"snapchat.login":          3, // account.json + account_history.json
		"snapchat.call":           2,
		"snapchat.story":          1,
		"snapchat.subscription":   1,
		// hidden friend suggestions, account basic information + device history,
		// display name change; user_profile.json + html twin captured generically
		"snapchat.unknown": 6,
	})
	checkUnique(t, events)
	if cov.Duplicates != 0 {
		t.Errorf("Duplicates = %d, want 0", cov.Duplicates)
	}
	if len(cov.GenericMembers) != 2 {
		t.Errorf("GenericMembers = %v, want user_profile.json and the html twin", cov.GenericMembers)
	}

	// Per-conversation shape: thread from the array name, direction from IsSender.
	in := byBody(events, "snapchat.message", "Yes, see you at 8")
	if in == nil || in.Actor != "self" || in.ThreadExternalID != "jane_doe" || in.Title != "Chat with jane_doe" || in.Metadata["direction"] != "sent" {
		t.Errorf("sent message not mapped: %+v", in)
	}
	wantAt := time.Date(2024, 1, 31, 12, 35, 10, 0, time.UTC)
	if in != nil && (!in.OccurredAt.Equal(wantAt) || in.OccurredAt.Location() != time.UTC) {
		t.Errorf("message OccurredAt = %v, want %v", in.OccurredAt, wantAt)
	}
	// The same text appears in a direct chat and, twice in one second, in a group.
	type who struct{ thread, actor string }
	same := map[who]string{}
	for _, ev := range events {
		if ev.Type == "snapchat.message" && ev.Body == "Are you coming tonight?" {
			same[who{ev.ThreadExternalID, ev.Actor}] = ev.Title
		}
	}
	const group = "0a1b2c3d-0000-4000-8000-00000000abcd"
	if len(same) != 3 || same[who{"jane_doe", "jane_doe"}] != "Chat with jane_doe" ||
		same[who{group, "max_mustermann"}] != "Chat with Bike Crew" || same[who{group, "self"}] != "Chat with Bike Crew" {
		t.Errorf("equal texts across conversations not kept apart: %v", same)
	}
	var media *domain.Event
	for i := range events {
		if events[i].Type == "snapchat.message" && events[i].Metadata["media_type"] == "MEDIA" {
			media = &events[i]
		}
	}
	if media == nil || media.Body != "" || media.Metadata["media_ids"] != "b~EXAMPLEMEDIAID0001" {
		t.Errorf("media message not mapped: %+v", media)
	}

	snaps := map[string]domain.Event{}
	for _, ev := range events {
		if ev.Type == "snapchat.snap" {
			snaps[ev.Metadata["direction"].(string)] = ev
		}
	}
	if s := snaps["received"]; s.Actor != "jane_doe" || s.Body != "" || s.Metadata["media_type"] != "IMAGE" || s.Title != "Snap from jane_doe" || s.Category != domain.CategoryMessage {
		t.Errorf("received snap not mapped: %+v", s)
	}
	if s := snaps["sent"]; s.Actor != "self" || s.ThreadExternalID != "jane_doe" || s.Metadata["media_type"] != "VIDEO" {
		t.Errorf("sent snap not mapped: %+v", s)
	}

	var mem *domain.Event
	for i := range events {
		if events[i].Type == "snapchat.memory" && events[i].Metadata["media_type"] == "Video" {
			mem = &events[i]
		}
	}
	if mem == nil || mem.Category != domain.CategoryMedia || len(mem.Media) != 0 ||
		!strings.Contains(mem.Metadata["download_link"].(string), "mid=22222222-ccce") || mem.OccurredAt.Year() != 2023 {
		t.Errorf("memory not mapped: %+v", mem)
	}

	status := map[string]domain.Event{}
	for _, ev := range events {
		if ev.Type == "snapchat.friend" {
			status[ev.Metadata["status"].(string)+":"+ev.Metadata["username"].(string)] = ev
		}
	}
	if f := status["friend:jane_doe"]; len(f.Targets) != 1 || f.Targets[0].Relation != domain.RelationFollowed ||
		f.Targets[0].TargetExternalID != "jane_doe" || f.Title != "Added Jane Doe as a friend" || f.Metadata["added_via"] != "Added by Username" {
		t.Errorf("friend not mapped: %+v", f)
	}
	if b := status["blocked:spam_account_1"]; b.Title != "Blocked Win A Phone" || len(b.Targets) != 0 ||
		!b.OccurredAt.Equal(time.Date(2023, 3, 4, 4, 4, 4, 0, time.UTC)) {
		t.Errorf("blocked user not mapped: %+v", b)
	}
	// Max is listed, with an identical record, as a friend and as a deleted friend.
	if _, ok := status["deleted:max_mustermann"]; !ok || len(status) != 4 {
		t.Errorf("friend variants = %v", keys(status))
	}
	if r := byType(events, "snapchat.friend_request"); r == nil || r.Title != "Sent a friend request to Erika M." || r.Metadata["direction"] != "sent" {
		t.Errorf("friend request not mapped: %+v", r)
	}

	if s := byBody(events, "snapchat.search", "pizza filter"); s == nil || s.OccurredAt.IsZero() {
		t.Errorf("search with the hourly date field not mapped: %+v", s)
	}
	var failed *domain.Event
	for i := range events {
		if events[i].Type == "snapchat.login" && events[i].Metadata["status"] == "failed" {
			failed = &events[i]
		}
	}
	if failed == nil || failed.Metadata["ip"] != "198.51.100.23" || failed.Metadata["country"] != "US" || failed.Title != "Snapchat login: failed" {
		t.Errorf("login not mapped: %+v", failed)
	}

	// talk_history / story_history / subscriptions: keys not publicly documented,
	// read permissively, every field kept.
	calls := map[string]domain.Event{}
	for _, ev := range events {
		if ev.Type == "snapchat.call" {
			calls[ev.Metadata["direction"].(string)] = ev
		}
	}
	if c := calls["received"]; c.Actor != "jane_doe" || c.Title != "Video call with jane_doe" || c.Metadata["duration"] != "125" ||
		c.ThreadExternalID != "jane_doe" || !c.OccurredAt.Equal(time.Date(2024, 1, 29, 20, 0, 0, 0, time.UTC)) {
		t.Errorf("received call not mapped: %+v", c)
	}
	if c := calls["sent"]; c.Actor != "self" || c.Metadata["call_type"] != "AUDIO" {
		t.Errorf("sent call not mapped: %+v", c)
	}
	if st := byType(events, "snapchat.story"); st == nil || st.Title != "Snapchat story (image)" || st.Metadata["views"] != "12" || st.Metadata["list"] != "Your Stories" {
		t.Errorf("story not mapped: %+v", st)
	}
	if sub := byType(events, "snapchat.subscription"); sub == nil || sub.Title != "Subscribed to Fixture News" || sub.Metadata["type"] != "Publisher" ||
		len(sub.Targets) != 1 || sub.Targets[0].TargetExternalID != "fixturenews" {
		t.Errorf("subscription not mapped: %+v", sub)
	}

	// Parts of matched members without a type of their own are kept, not dropped.
	unknown := map[string]domain.Event{}
	for _, ev := range events {
		if ev.Type == "snapchat.unknown" {
			section, _ := ev.Metadata["section"].(string)
			unknown[section] = ev
		}
	}
	for _, section := range []string{
		"friends.json / Hidden Friend Suggestions",
		"account.json / Basic Information",
		"account.json / Device History",
		"account_history.json / Display Name Change",
	} {
		if _, ok := unknown[section]; !ok {
			t.Errorf("unmapped section %q was dropped; have %v", section, keys(unknown))
		}
	}
	if p := unknown["account.json / Basic Information"]; !strings.Contains(p.Metadata["preview"].(string), `"Username":"johnny_example"`) {
		t.Errorf("scalar fields should be previewed: %+v", p)
	}
	if d := unknown["account_history.json / Display Name Change"]; d.OccurredAt.Year() != 2021 {
		t.Errorf("unmapped record with a Date should be dated: %+v", d)
	}
}

// Older exports split chats and snaps into "Received …" and "Sent …" lists and
// name the other party in From / To.
func TestImportSnapchatLegacy(t *testing.T) {
	cov, events := importDir(t, "legacy")
	checkCounts(t, cov, map[string]int{
		"snapchat.message": 3,
		"snapchat.snap":    3,
		"snapchat.search":  1, // top-level array, "Query" field
	})
	checkUnique(t, events)
	if cov.Duplicates != 0 {
		t.Errorf("Duplicates = %d, want 0", cov.Duplicates)
	}

	got := byBody(events, "snapchat.message", "Old style reply")
	if got == nil || got.Actor != "self" || got.ThreadExternalID != "jane_doe" || got.Metadata["to"] != "jane_doe" {
		t.Errorf("sent legacy message not mapped: %+v", got)
	}
	actors := map[string]string{}
	for _, ev := range events {
		if ev.Type == "snapchat.message" && ev.Body == "Old style hello" {
			actors[ev.ThreadExternalID] = ev.Actor
		}
	}
	if actors["jane_doe"] != "jane_doe" || actors["max_mustermann"] != "max_mustermann" {
		t.Errorf("received legacy messages = %v", actors)
	}

	sent, received := 0, 0
	for _, ev := range events {
		if ev.Type != "snapchat.snap" {
			continue
		}
		switch {
		case ev.Actor == "self" && ev.Metadata["direction"] == "sent" && strings.HasPrefix(ev.Title, "Snap to "):
			sent++
		case ev.Actor == "jane_doe" && ev.ThreadExternalID == "jane_doe" && ev.Metadata["direction"] == "received":
			received++
		}
	}
	if sent != 2 || received != 1 {
		t.Errorf("legacy snaps: sent = %d, received = %d, want 2 and 1", sent, received)
	}
	if s := byType(events, "snapchat.search"); s == nil || s.Body != "old search" {
		t.Errorf("legacy search not mapped: %+v", s)
	}
}

// Download links are re-signed for every export; the memory must keep its id.
func TestMemoryIDIgnoresSignature(t *testing.T) {
	norm := func(link string) string {
		raw := rec("memory", []byte(`{"key":"Saved Media","record":{"Date":"2023-08-15 14:20:00 UTC","Media Type":"Image","Download Link":"`+link+`"}}`))
		evs, err := New().Normalize(context.Background(), raw)
		if err != nil || len(evs) != 1 {
			t.Fatalf("normalize: %v %v", evs, err)
		}
		return evs[0].ExternalID
	}
	a := norm("https://app.example-snap.invalid/dmd/memories?uid=u&sid=s&mid=m1&ts=1&sig=AAA")
	b := norm("https://app.example-snap.invalid/dmd/memories?uid=u&sid=s&mid=m1&ts=2&sig=BBB")
	c := norm("https://app.example-snap.invalid/dmd/memories?uid=u&sid=s&mid=m2&ts=1&sig=AAA")
	if a != b || a == c {
		t.Errorf("memory ids: same media %q vs %q, other media %q", a, b, c)
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

func TestMatchIgnoresHTMLTwin(t *testing.T) {
	for member, want := range map[string]bool{
		"json/chat_history.json":              true,
		"mydata~1700000000/json/friends.json": true,
		"json/account.json":                   true,
		"html/chat_history.html":              false,
		"index.html":                          false,
		"json/user_profile.json":              false,
		"json/talk_history.json":              true,
		"json/story_history.json":             true,
		"json/subscriptions.json":             true,
		"chat_media/2024-01-31_b~x.jpg":       false,
	} {
		got := false
		for _, e := range New().Extractors() {
			got = got || e.Match(member)
		}
		if got != want {
			t.Errorf("Match(%q) = %v, want %v", member, got, want)
		}
	}
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
