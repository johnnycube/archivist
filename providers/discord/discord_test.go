package discord

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
)

func TestImportDiscord(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "discord.zip")
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

	want := map[string]int{
		"discord.message": 6, // 3 from messages.json + 3 from messages.csv
		"discord.channel": 6, // 3 channel.json + 3 messages/index.json labels
		"discord.server":  3, // 2 index entries + servers/<id>/guild.json
		"discord.friend":  2,
		// user.json: settings, connections, payments, notes + its scalar fields;
		// generic capture: README.txt, the activity log.
		"discord.unknown": 7,
	}
	if len(cov.PerType) != len(want) {
		t.Errorf("PerType has %d types, want %d: %v", len(cov.PerType), len(want), cov.PerType)
	}
	for typ, n := range want {
		if cov.PerType[typ] != n {
			t.Errorf("PerType[%q] = %d, want %d", typ, cov.PerType[typ], n)
		}
	}
	// The server index repeats the bare record {id, name} of a channel label
	// (old guilds share their id with their first channel); the kind keeps the
	// two apart in the dedupe set.
	if cov.Duplicates != 0 {
		t.Errorf("Duplicates = %d, want 0", cov.Duplicates)
	}
	sort.Strings(cov.GenericMembers)
	if wantGeneric := []string{"README.txt", "activity/analytics/events-2024-00000-of-00001.json"}; !reflect.DeepEqual(cov.GenericMembers, wantGeneric) {
		t.Errorf("GenericMembers = %v, want %v", cov.GenericMembers, wantGeneric)
	}

	ids := map[string]int{}
	for _, ev := range events {
		ids[ev.ExternalID]++
		if !ev.OccurredAt.IsZero() && ev.OccurredAt.Location() != time.UTC {
			t.Errorf("OccurredAt not UTC: %+v", ev)
		}
	}
	for id, n := range ids {
		if n > 1 {
			t.Errorf("ExternalID %q used by %d events", id, n)
		}
	}

	// messages.json: numeric snowflake kept exact, plain UTC timestamp.
	msg := byID(events, "msg:1202220000000000001")
	if msg == nil {
		t.Fatal("msg:1202220000000000001 not found (snowflake precision lost?)")
	}
	if msg.Type != "discord.message" || msg.Category != domain.CategoryMessage || msg.Actor != "self" ||
		msg.ThreadExternalID != "100000000000000001" || msg.Title != "Message in channel 100000000000000001" ||
		msg.Body != "See you at the café?" || len(msg.Media) != 0 || msg.Metadata["attachments"] != nil {
		t.Errorf("message not mapped: %+v", msg)
	}
	if want := time.Date(2024, 1, 31, 12, 34, 56, 0, time.UTC); !msg.OccurredAt.Equal(want) {
		t.Errorf("OccurredAt = %v, want %v", msg.OccurredAt, want)
	}
	// Fractional seconds with an explicit offset; attachments stay remote URLs.
	att := byID(events, "msg:1202220000000000002")
	if att == nil || !att.OccurredAt.Equal(time.Date(2024, 1, 31, 12, 35, 10, 123000000, time.UTC)) {
		t.Errorf("fractional +00:00 timestamp not parsed: %+v", att)
	}
	if att != nil {
		wantURLs := []string{"https://cdn.discordapp.example/attachments/1/2/route.png", "https://cdn.discordapp.example/attachments/1/3/map.png"}
		if !reflect.DeepEqual(att.Metadata["attachments"], wantURLs) || len(att.Media) != 0 {
			t.Errorf("attachments = %v (media %v), want %v in metadata only", att.Metadata["attachments"], att.Media, wantURLs)
		}
	}
	if iso := byID(events, "msg:1202220000000000003"); iso == nil || !iso.OccurredAt.Equal(time.Date(2024, 1, 31, 11, 36, 0, 0, time.UTC)) {
		t.Errorf("RFC 3339 timestamp not converted to UTC: %+v", iso)
	}

	// messages.csv: same mapping, thread id from the member path.
	csv := byID(events, "msg:1100000000000000001")
	if csv == nil || csv.ThreadExternalID != "100000000000000002" || csv.Body != "Hello, server\nsecond line" ||
		!csv.OccurredAt.Equal(time.Date(2023, 6, 1, 8, 0, 0, 500000000, time.UTC)) {
		t.Errorf("csv message not mapped: %+v", csv)
	}
	if pic := byID(events, "msg:1100000000000000002"); pic == nil ||
		!reflect.DeepEqual(pic.Metadata["attachments"], []string{"https://cdn.discordapp.example/attachments/9/9/bike.jpg"}) {
		t.Errorf("csv attachment not mapped: %+v", pic)
	}
	if undated := byID(events, "msg:1100000000000000003"); undated == nil || undated.OccurredAt.UnixMilli() != 1682330837011 {
		t.Errorf("unparseable timestamp should fall back to the snowflake's time: %+v", undated)
	}

	// Channels: channel.json and the index label join on ThreadExternalID.
	dm := byID(events, "channel:100000000000000001")
	if dm == nil || dm.Type != "discord.channel" || dm.ThreadExternalID != "100000000000000001" || dm.Metadata["channel_type"] != "1" || dm.Metadata["channel_type_name"] != "DM" ||
		!reflect.DeepEqual(dm.Metadata["recipients"], []string{"300000000000000001", "300000000000000002"}) {
		t.Errorf("DM channel not mapped: %+v", dm)
	}
	if label := byID(events, "channel-label:100000000000000001"); label == nil || label.ThreadExternalID != "100000000000000001" ||
		label.Title != "Direct Message with jane#0" {
		t.Errorf("channel label not mapped: %+v", label)
	}
	if label := byID(events, "channel-label:100000000000000003"); label == nil || label.Title != "Channel 100000000000000003" {
		t.Errorf("null channel label not mapped: %+v", label)
	}
	guild := byID(events, "channel:100000000000000002")
	if guild == nil || guild.Title != "Channel general in Fixture Server" || guild.Metadata["guild_id"] != "200000000000000001" ||
		guild.Metadata["channel_type"] != "GUILD_TEXT" {
		t.Errorf("guild channel not mapped: %+v", guild)
	}

	srv := byID(events, "server:200000000000000001")
	if srv == nil || srv.Type != "discord.server" || srv.Category != domain.CategoryConnection || srv.Title != "Server Fixture Server" || !srv.OccurredAt.IsZero() {
		t.Errorf("server not mapped: %+v", srv)
	}
	if g := byID(events, "guild:200000000000000001"); g == nil || g.Type != "discord.server" || g.Title != "Server Fixture Server" || g.Metadata["guild_id"] != "200000000000000001" {
		t.Errorf("guild.json not mapped: %+v", g)
	}

	friend := byID(events, "relationship:300000000000000002")
	if friend == nil || friend.Type != "discord.friend" || friend.Title != "Friend Jane Fixture" ||
		friend.Metadata["relationship"] != "friend" || friend.Metadata["username"] != "jane" || friend.Metadata["discriminator"] != nil {
		t.Errorf("friend not mapped: %+v", friend)
	}
	if blocked := byID(events, "relationship:300000000000000003"); blocked == nil || blocked.Title != "Blocked spammer" ||
		blocked.Metadata["relationship"] != "blocked" || blocked.Metadata["discriminator"] != "1234" {
		t.Errorf("blocked relationship not mapped: %+v", blocked)
	}

	// Nothing of user.json is lost by matching it.
	sections := map[string]domain.Event{}
	for _, ev := range events {
		if ev.Type == "discord.unknown" && ev.Metadata["member"] == "account/user.json" {
			sections[ev.Metadata["section"].(string)] = ev
		}
	}
	for _, s := range []string{"settings", "connections", "payments", "notes", "account fields"} {
		if _, ok := sections[s]; !ok {
			t.Errorf("user.json section %q has no unknown event (have %v)", s, sections)
		}
	}
	if got := sections["notes"].Metadata["preview"]; got != `{"300000000000000002":"met at the bike market"}` {
		t.Errorf("notes preview = %q", got)
	}
	if p, _ := sections["settings"].Metadata["preview"].(string); !strings.HasPrefix(p, `{"locale":"en-US","theme":"dark","frecency":"a long`) || !strings.HasSuffix(p, "…") {
		t.Errorf("settings preview = %q", p)
	}
	fields, _ := sections["account fields"].Metadata["fields"].(map[string]any)
	if fields["username"] != "johnny_fixture" || fields["verified"] != true || fields["email"] != "johnny@example.com" {
		t.Errorf("account fields = %v", fields)
	}
	if _, ok := fields["phone"]; !ok || fields["phone"] != nil {
		t.Errorf("null account field should be kept: %v", fields)
	}
}

func TestMatch(t *testing.T) {
	match := func(member string) bool {
		for _, ex := range New().Extractors() {
			if ex.Match(member) {
				return true
			}
		}
		return false
	}
	for member, want := range map[string]bool{
		"messages/c1/messages.json":         true,
		"Messages/c1/messages.csv":          true, // newer packages capitalize folders
		"package/messages/1/messages.csv":   true,
		"messages/c1/channel.json":          true,
		"messages/index.json":               true,
		"servers/index.json":                true,
		"Account/user.json":                 true,
		"servers/2/guild.json":              true,
		"servers/2/audit-log.json":          false,
		"activity/analytics/events-1.json":  false,
		"activity/reporting/events-1.json":  false,
		"activity/tns/messages.json":        false,
		"account/applications/1/index.json": false,
	} {
		if got := match(member); got != want {
			t.Errorf("Match(%q) = %v, want %v", member, got, want)
		}
	}
	if got := channelOf("package/messages/987/messages.csv"); got != "987" {
		t.Errorf("channelOf bare id = %q", got)
	}
}

func byID(evs []domain.Event, id string) *domain.Event {
	for i := range evs {
		if evs[i].ExternalID == id {
			return &evs[i]
		}
	}
	return nil
}
