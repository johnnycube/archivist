package facebook

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
)

func TestImportFacebook(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "facebook.zip")
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
		"facebook.post":           4, // your_posts_1 + group post + two photo-only posts in the same second
		"facebook.comment":        2, // comments.json + your_comments_in_groups.json
		"facebook.app_post":       1,
		"facebook.message":        3,
		"facebook.reaction":       3, // posts_and_comments + likes_and_reactions_1 + label_values likes_and_reactions
		"facebook.friend":         2, // friends.json + connections/friends/your_friends.json
		"facebook.friend_request": 2,
		"facebook.follow":         3, // followed + pages followed + page like
		"facebook.unfollow":       2,
		"facebook.group_join":     1,
		"facebook.event":          3, // invited + joined (same bare record) + declined
		"facebook.app":            2,
		"facebook.search":         1,
		"facebook.photo":          1,
		"facebook.profile_update": 2, // update history + record_details entry with extra_info
		"facebook.login":          2, // account_activity + record_details checkpoint
		"facebook.unknown":        2, // profile_information.json + privacy_settings.json captured generically
	}
	if len(cov.PerType) != len(want) {
		t.Errorf("PerType has %d types, want %d: %v", len(cov.PerType), len(want), cov.PerType)
	}
	for typ, n := range want {
		if cov.PerType[typ] != n {
			t.Errorf("PerType[%q] = %d, want %d", typ, cov.PerType[typ], n)
		}
	}

	// The export lists one follow in both who_you've_followed.json and
	// pages_and_profiles_you_follow.json; sharing an ExternalID is what lets the
	// store collapse them. Nothing else may collide.
	ids := map[string]int{}
	for _, ev := range events {
		ids[ev.ExternalID]++
	}
	for id, n := range ids {
		if n > 1 && !(n == 2 && strings.HasPrefix(id, "follow:1650031000:")) {
			t.Errorf("ExternalID %q used by %d events", id, n)
		}
	}

	post := byTitleBody(events, "facebook.post", "Weekend at the café")
	if post == nil {
		t.Errorf("post with repaired mojibake %q not found", "Weekend at the café")
	}
	react := byType(events, "facebook.reaction")
	if react == nil || len(react.Targets) != 1 || react.Metadata["reaction"] != "LIKE" {
		t.Errorf("reaction not mapped: %+v", react)
	}
	friends := map[string]bool{}
	for _, ev := range events {
		if ev.Type == "facebook.friend" && len(ev.Targets) == 1 {
			friends[ev.Targets[0].TargetExternalID] = true
		}
	}
	if !friends["Jane Doe"] || !friends["Max Mustermann"] {
		t.Errorf("friend targets = %v, want Jane Doe (friends.json) and Max Mustermann (your_friends.json)", friends)
	}

	msg := byTitleBody(events, "facebook.message", "See you at the café?")
	if msg == nil {
		t.Fatalf("message with repaired mojibake not found")
	}
	if msg.Actor != "Jane Doe" || msg.ThreadExternalID != "inbox/janedoe_1234" || msg.Title != "Message in Jane Doe, Johnny" {
		t.Errorf("message not mapped: %+v", msg)
	}
	if got := msg.OccurredAt.UnixMilli(); got != 1650010002000 {
		t.Errorf("message OccurredAt = %d ms, want timestamp_ms 1650010002000", got)
	}
	if photo := byTitleBody(events, "facebook.message", ""); photo == nil || len(photo.Media) != 1 {
		t.Errorf("photo message should carry its attachment: %+v", photo)
	}
	if share := byTitleBody(events, "facebook.message", "Look at this"); share == nil || share.Metadata["link"] != "https://example.com/article" {
		t.Errorf("shared link not kept: %+v", share)
	}

	var lv *domain.Event
	for i := range events {
		if events[i].Type == "facebook.reaction" && events[i].Title == "Reacted to Jane Doe" {
			lv = &events[i]
		}
	}
	if lv == nil || lv.Metadata["reaction"] != "LIKE" || lv.Targets[0].TargetURL != "https://www.facebook.com/photo/?fbid=1" {
		t.Errorf("label_values reaction not mapped: %+v", lv)
	}

	rsvps := map[string]string{}
	for _, ev := range events {
		if ev.Type == "facebook.event" {
			rsvps[ev.Metadata["rsvp"].(string)] = ev.Title
		}
	}
	wantRSVPs := map[string]string{"invited": "Invited to Summer party", "joined": "Going to Summer party", "declined": "Declined Winter party"}
	for k, v := range wantRSVPs {
		if rsvps[k] != v {
			t.Errorf("event rsvp %q = %q, want %q", k, rsvps[k], v)
		}
	}

	unf := byType(events, "facebook.unfollow")
	if unf == nil || unf.Title != "Unfollowed Loud Uncle" || unf.OccurredAt.Unix() != 1650060000 {
		t.Errorf("unfollow pairs not mapped: %+v", unf)
	}
	if s := byType(events, "facebook.search"); s == nil || s.Body != "bike market" {
		t.Errorf("search not mapped: %+v", s)
	}
	if ph := byType(events, "facebook.photo"); ph == nil || len(ph.Media) != 1 || ph.Body != "Beach day" {
		t.Errorf("album photo not mapped: %+v", ph)
	}
	if gc := byTitleBody(events, "facebook.comment", "Still available?"); gc == nil || gc.Metadata["group"] != "Bike Market" {
		t.Errorf("group comment not mapped: %+v", gc)
	}
	if login := byType(events, "facebook.login"); login == nil || login.Metadata["location"] != "Karlsruhe, Baden-Württemberg, DE" {
		t.Errorf("login not mapped: %+v", login)
	}
	for _, ev := range events {
		if ev.Type == "facebook.app" && ev.Metadata["app"] == "Old Quiz" && (ev.Title != "Removed the app Old Quiz" || ev.OccurredAt.Unix() != 1640094000) {
			t.Errorf("app without an added date should be dated by its removal: %+v", ev)
		}
	}
	var email *domain.Event
	for i := range events {
		if events[i].Type == "facebook.profile_update" && events[i].Title == "Email change" {
			email = &events[i]
		}
	}
	if email == nil || email.Metadata["new_email"] != "new@example.com" {
		t.Errorf("record_details change not mapped as a profile update: %+v", email)
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

func byTitleBody(evs []domain.Event, typ, body string) *domain.Event {
	for i := range evs {
		if evs[i].Type == typ && evs[i].Body == body {
			return &evs[i]
		}
	}
	return nil
}

// A matched member whose records sit under an array name the extractor does not
// know must not vanish: each such non-empty array leaves one facebook.unknown
// event naming the member, the key path and the element count.
func TestUnknownArraysAreReported(t *testing.T) {
	data, err := archivetest.Zip(map[string]string{
		"your_facebook_activity/events/your_event_responses.json": `{"event_responses_v2": {
			"events_joined": [{"name": "Picnic", "start_timestamp": 1700000000}],
			"events_hidden_v9": [{"name": "A"}, {"name": "B"}],
			"events_empty": []}}`,
		"your_facebook_activity/messages/inbox/ann_1/message_1.json": `{
			"participants": [{"name": "Ann"}, {"name": "Me"}],
			"messages": [{"sender_name": "Ann", "timestamp_ms": 1700000000000, "content": "hi"}],
			"magic_words": [{"magic_word": "yay"}], "title": "Ann"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	var unknown []domain.Event
	part := bundle.Part{ReaderAt: bytes.NewReader(data), Size: int64(len(data))}
	cov, err := bundle.NewImporter(New()).ImportParts(context.Background(), []bundle.Part{part}, func(ev domain.Event) error {
		if ev.Type == "facebook.unknown" {
			unknown = append(unknown, ev)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cov.PerType["facebook.event"] != 1 || cov.PerType["facebook.message"] != 1 || cov.Generic != 0 {
		t.Errorf("PerType = %v, Generic = %d", cov.PerType, cov.Generic)
	}
	got := map[string]any{}
	for _, ev := range unknown {
		got[ev.Metadata["key"].(string)] = ev.Metadata["count"]
		if ev.Metadata["member"] == "" || ev.ExternalID == "" {
			t.Errorf("unknown event lacks member/id: %+v", ev)
		}
	}
	want := map[string]any{"event_responses_v2.events_hidden_v9": 2, "magic_words": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reported arrays = %v, want %v", got, want)
	}
}

// A PDF sent in a chat is no media by extension, but lies in the thread's
// files/ directory: it is stored under the key the message carries. Data files
// are never swallowed as attachments.
func TestMessageAttachmentsAreStored(t *testing.T) {
	const pdf = "your_facebook_activity/messages/inbox/ann_1/files/contract_123.pdf"
	data, err := archivetest.Zip(map[string]string{
		"your_facebook_activity/messages/inbox/ann_1/message_1.json": `{
			"participants": [{"name": "Ann"}, {"name": "Me"}],
			"messages": [{"sender_name": "Ann", "timestamp_ms": 1700000000000, "files": [{"uri": "` + pdf + `"}]}]}`,
		pdf: "%PDF-1.7",
		"your_facebook_activity/messages/inbox/ann_1/files/index.json": `{"surprise": true}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]bool{}
	im := bundle.NewImporter(New())
	im.PutBlob = func(key, _ string, r io.Reader) error {
		stored[key] = true
		_, err := io.Copy(io.Discard, r)
		return err
	}
	var msg *domain.Event
	part := bundle.Part{ReaderAt: bytes.NewReader(data), Size: int64(len(data))}
	cov, err := im.ImportParts(context.Background(), []bundle.Part{part}, func(ev domain.Event) error {
		if ev.Type == "facebook.message" {
			msg = &ev
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg == nil || len(msg.Media) != 1 || msg.Media[0].BlobKey != pdf || !stored[pdf] {
		t.Fatalf("message = %+v, stored = %v; want its BlobKey %q stored", msg, stored, pdf)
	}
	if cov.MediaStored != 1 || cov.Generic != 1 || len(stored) != 1 {
		t.Errorf("MediaStored/Generic = %d/%d, stored %v; want 1/1 (index.json stays unknown)", cov.MediaStored, cov.Generic, stored)
	}
}
