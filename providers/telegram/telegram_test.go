package telegram

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
	"github.com/johnnycube/archivist/providers/internal/jsonpreview"
)

// importDir zips testdata/<dir> the way a user zips the export folder and
// imports it, returning the events, coverage and the stored media keys.
func importDir(t *testing.T, dir string) ([]domain.Event, *domain.Coverage, map[string]bool) {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), dir+".zip")
	if err := archivetest.ZipDir(filepath.Join("testdata", dir), zipPath); err != nil {
		t.Fatal(err)
	}
	im := bundle.NewImporter(New())
	blobs := map[string]bool{}
	im.PutBlob = func(key, _ string, r io.Reader) error {
		blobs[key] = true
		_, err := io.Copy(io.Discard, r)
		return err
	}
	var events []domain.Event
	cov, err := im.ImportZip(context.Background(), zipPath, func(ev domain.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return events, cov, blobs
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

func checkUniqueIDs(t *testing.T, events []domain.Event) {
	t.Helper()
	ids := map[string]int{}
	for _, ev := range events {
		ids[ev.ExternalID]++
		if ev.SourceID != "telegram" || ev.ExternalID == "" {
			t.Errorf("event without source/id: %+v", ev)
		}
		if !ev.OccurredAt.IsZero() && ev.OccurredAt.Location() != time.UTC {
			t.Errorf("OccurredAt not UTC: %+v", ev)
		}
	}
	for id, n := range ids {
		if n > 1 {
			t.Errorf("ExternalID %q used by %d events", id, n)
		}
	}
}

func TestImportFullExport(t *testing.T) {
	events, cov, blobs := importDir(t, "full")

	checkCounts(t, cov, map[string]int{
		"telegram.message": 13, // 6 personal + 5 group + 1 saved + 1 left channel
		"telegram.service": 2,
		"telegram.contact": 2,
		"telegram.unknown": 5, // about, personal_information, profile_pictures, frequent_contacts, sessions
	})
	if cov.Duplicates != 0 {
		t.Errorf("Duplicates = %d: the identical message in two chats must not be deduped", cov.Duplicates)
	}
	if cov.MediaStored != 1 || cov.Generic != 0 {
		t.Errorf("MediaStored = %d, Generic = %d, want 1 and 0", cov.MediaStored, cov.Generic)
	}
	checkUniqueIDs(t, events)

	hello := byID(events, "msg:1111:1")
	if hello == nil {
		t.Fatal("msg:1111:1 not found")
	}
	if hello.Type != "telegram.message" || hello.Category != domain.CategoryMessage || hello.Actor != "Jane Example" ||
		hello.ThreadExternalID != "1111" || hello.Title != "Message in Jane Example" || hello.Body != "Hello from the café" {
		t.Errorf("message not mapped: %+v", hello)
	}
	// date says 13:34:56 (local, UTC+1); date_unixtime is the truth.
	if want := time.Date(2024, 1, 31, 12, 34, 56, 0, time.UTC); !hello.OccurredAt.Equal(want) {
		t.Errorf("OccurredAt = %v, want date_unixtime %v", hello.OccurredAt, want)
	}
	if hello.Metadata["date_is_local"] != nil || hello.Metadata["chat_type"] != "personal_chat" {
		t.Errorf("message metadata: %v", hello.Metadata)
	}

	reply := byID(events, "msg:1111:2")
	if reply == nil || reply.Body != "Look at https://example.com/route and this map — great ride!" {
		t.Fatalf("text array not flattened: %+v", reply)
	}
	if reply.ParentExternalID != "msg:1111:1" {
		t.Errorf("ParentExternalID = %q, want msg:1111:1", reply.ParentExternalID)
	}
	if want := []string{"https://example.com/route", "https://example.org/map"}; !reflect.DeepEqual(reply.Metadata["links"], want) {
		t.Errorf("links = %v, want %v", reply.Metadata["links"], want)
	}
	if reply.Metadata["edited_at"] != "2024-01-31T12:37:00Z" {
		t.Errorf("edited_at = %v", reply.Metadata["edited_at"])
	}

	photo := byID(events, "msg:1111:3")
	if photo == nil || len(photo.Media) != 1 || !blobs[photo.Media[0].BlobKey] {
		t.Errorf("photo BlobKey should be the stored member path (blobs %v): %+v", blobs, photo)
	}

	voice := byID(events, "msg:1111:4")
	if voice == nil || len(voice.Media) != 0 || voice.Metadata["media_not_included"] != true ||
		voice.Metadata["media_type"] != "voice_message" || voice.Metadata["mime_type"] != "audio/ogg" {
		t.Errorf("media-not-included sentinel not detected: %+v", voice)
	}

	// Pre-2021 shape: numeric from_id and no date_unixtime.
	old := byID(events, "msg:1111:5")
	if old == nil || old.Metadata["from_id"] != "1000001" || old.Metadata["date_is_local"] != true ||
		!old.OccurredAt.Equal(time.Date(2019, 3, 1, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("old-format message not mapped: %+v", old)
	}
	// The byte-identical message in Saved Messages is its own event.
	saved := byID(events, "msg:1000001:5")
	if saved == nil || saved.Title != "Message in Saved Messages" || saved.ThreadExternalID != "1000001" {
		t.Errorf("saved message not mapped: %+v", saved)
	}

	fwd := byID(events, "msg:1111:6")
	if fwd == nil || fwd.Actor != "user4444" || fwd.Metadata["forwarded"] != true {
		t.Errorf("message of a deleted account not mapped: %+v", fwd)
	}

	svc := byID(events, "msg:2222:1")
	if svc == nil || svc.Type != "telegram.service" || svc.Category != domain.CategoryOther || svc.Actor != "Johnny Example" ||
		svc.Metadata["action"] != "invite_members" || svc.Title != "invite_members in Bike Crew" ||
		!reflect.DeepEqual(svc.Metadata["members"], []string{"Jane Example", "unknown"}) {
		t.Errorf("service message not mapped: %+v", svc)
	}
	sticker := byID(events, "msg:2222:2")
	if sticker == nil || len(sticker.Media) != 1 || sticker.Metadata["sticker_emoji"] != "🚲" ||
		sticker.Media[0].BlobKey != "DataExport_2024-02-01/chats/chat_002/stickers/sticker.webp" {
		t.Errorf("sticker not mapped: %+v", sticker)
	}

	if sticker != nil && sticker.Metadata["thumbnail"] != "DataExport_2024-02-01/chats/chat_002/stickers/sticker.webp_thumb.jpg" {
		t.Errorf("thumbnail not resolved to a member path: %v", sticker.Metadata["thumbnail"])
	}
	// Structured attachments (keys as tdesktop's export_output_json.cpp writes them).
	if poll := byID(events, "msg:2222:3"); poll == nil || poll.Body != "Poll: Ride on Sunday?\n- Yes\n- No" || poll.Metadata["poll"] == nil {
		t.Errorf("poll not mapped: %+v", poll)
	}
	if loc := byID(events, "msg:2222:4"); loc == nil || loc.Body != "Location: Schlossplatz Karlsruhe" || loc.Metadata["location"] == nil || loc.Metadata["reactions"] == nil {
		t.Errorf("location / reactions not mapped: %+v", loc)
	}
	if c := byID(events, "msg:2222:5"); c == nil || c.Body != "@johnny_example here is his number" || c.Metadata["contact"] == nil ||
		!reflect.DeepEqual(c.Metadata["mentions"], []string{"@johnny_example"}) {
		t.Errorf("contact / mention not mapped: %+v", c)
	}
	if call := byID(events, "msg:2222:6"); call == nil || call.Type != "telegram.service" || call.Metadata["action"] != "phone_call" ||
		call.Metadata["duration_seconds"] != float64(125) || call.Metadata["discard_reason"] != "hangup" {
		t.Errorf("phone_call details not kept: %+v", call)
	}
	if x := byID(events, "msg:2222:7"); x == nil || x.ParentExternalID != "msg:channel3333:10" {
		t.Errorf("reply_to_peer_id not used for the parent: %+v", x)
	}

	left := byID(events, "msg:3333:10")
	if left == nil || left.Metadata["left_chat"] != true || left.Metadata["chat"] != "Fixture News" {
		t.Errorf("left chat message not mapped: %+v", left)
	}

	contacts := map[string]domain.Event{}
	for _, ev := range events {
		if ev.Type == "telegram.contact" {
			contacts[ev.Title] = ev
		}
	}
	if jane, ok := contacts["Contact Jane Example"]; !ok || jane.Category != domain.CategoryConnection ||
		jane.OccurredAt.Unix() != 1685602800 || jane.Metadata["phone_number"] != "+00 000 0000002" {
		t.Errorf("contact not mapped: %v", contacts)
	}
	if max, ok := contacts["Contact Max"]; !ok || !max.OccurredAt.IsZero() {
		t.Errorf("contact with the epoch placeholder date should be undated: %v", contacts)
	}

	sections := map[string]domain.Event{}
	for _, ev := range events {
		if ev.Type == "telegram.unknown" {
			sections[ev.Metadata["section"].(string)] = ev
		}
	}
	for _, s := range []string{"about", "personal_information", "profile_pictures", "frequent_contacts", "sessions"} {
		if _, ok := sections[s]; !ok {
			t.Errorf("unmapped section %q has no unknown event", s)
		}
	}
	pi := sections["personal_information"]
	preview, _ := pi.Metadata["preview"].(string)
	if !strings.HasPrefix(preview, `{"about":"Your personal information.","user_id":1000001,"first_name":"Johnny"`) ||
		!strings.HasSuffix(preview, "…") || len(preview) > jsonpreview.Bytes+len("…") {
		t.Errorf("personal_information preview = %q", preview)
	}
	if got := sections["profile_pictures"].Metadata["preview"]; got !=
		`[{"date":"2023-05-01T10:00:00","date_unixtime":"1682928000","photo":"(File not included. Change data exporting settings to download.)"}]` {
		t.Errorf("profile_pictures preview = %q", got)
	}
}

func TestImportSingleChatExport(t *testing.T) {
	events, cov, blobs := importDir(t, "single")

	checkCounts(t, cov, map[string]int{
		"telegram.message": 2,
		"telegram.service": 1,
	})
	checkUniqueIDs(t, events)

	photo := byID(events, "msg:5555:1")
	if photo == nil || photo.ThreadExternalID != "5555" || photo.Title != "Message in Max Example" || photo.Body != "Single chat photo" {
		t.Fatalf("single-chat message not mapped: %+v", photo)
	}
	if len(photo.Media) != 1 || photo.Media[0].BlobKey != "ChatExport_2024-02-02/photos/photo_2@02-02-2024_10-00-00.jpg" || !blobs[photo.Media[0].BlobKey] {
		t.Errorf("photo BlobKey should be the stored member path (blobs %v): %+v", blobs, photo.Media)
	}
	if reply := byID(events, "msg:5555:2"); reply == nil || reply.ParentExternalID != "msg:5555:1" {
		t.Errorf("reply not mapped: %+v", reply)
	}
	if pin := byID(events, "msg:5555:3"); pin == nil || pin.Type != "telegram.service" ||
		pin.Metadata["action"] != "pin_message" || pin.Metadata["message_external_id"] != "msg:5555:1" {
		t.Errorf("pin service message not mapped: %+v", pin)
	}
}

// Chat metadata after the messages is not what Telegram writes; the walker must
// not buffer for it, so the name stays empty and the id falls back to the
// chat's position.
func TestChatMetadataAfterMessages(t *testing.T) {
	const doc = `{"chats":{"list":[
		{"messages":[{"id":1,"type":"message","date_unixtime":"1700000000","from":"A","text":"x"}],"name":"Late","id":1},
		{"messages":[{"id":1,"type":"message","date_unixtime":"1700000000","from":"A","text":"x"}],"name":"Later","id":2}
	]}}`
	p := New()
	var events []domain.Event
	err := resultEx{}.Extract("result.json", strings.NewReader(doc), func(raw provider.RawRecord) error {
		raw.Member = "result.json"
		evs, err := p.Normalize(context.Background(), raw)
		events = append(events, evs...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ExternalID != "msg:noid-chats-1:1" || events[1].ExternalID != "msg:noid-chats-2:1" ||
		events[0].Metadata["chat"] != nil {
		t.Errorf("fallback chat context: %+v", events)
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

// Documents and animated stickers are no media by extension; the provider
// declares Telegram's attachment directories, so they are stored under exactly
// the key the message carries — and a stray file elsewhere stays visible.
func TestNonMediaAttachmentsAreStored(t *testing.T) {
	data, err := archivetest.Zip(map[string]string{
		"ChatExport/result.json": `{"name": "Max", "type": "personal_chat", "id": 5555, "messages": [
			{"id": 1, "type": "message", "date_unixtime": "1706868000", "from": "Max", "from_id": "user5555",
			 "file": "files/contract.pdf", "mime_type": "application/pdf", "text": "the contract"},
			{"id": 2, "type": "message", "date_unixtime": "1706868060", "from": "Max", "from_id": "user5555",
			 "file": "stickers/AnimatedSticker.tgs", "media_type": "sticker", "text": ""}]}`,
		"ChatExport/files/contract.pdf":           "%PDF-1.7",
		"ChatExport/stickers/AnimatedSticker.tgs": "TGS",
		"ChatExport/css/style.css":                "body{}",
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
	var events []domain.Event
	part := bundle.Part{ReaderAt: bytes.NewReader(data), Size: int64(len(data))}
	cov, err := im.ImportParts(context.Background(), []bundle.Part{part}, func(ev domain.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cov.MediaStored != 2 || cov.Generic != 1 || cov.PerType["telegram.unknown"] != 1 {
		t.Errorf("MediaStored/Generic/unknown = %d/%d/%d, want 2/1/1 (the stylesheet)", cov.MediaStored, cov.Generic, cov.PerType["telegram.unknown"])
	}
	var keys []string
	for _, ev := range events {
		for _, m := range ev.Media {
			keys = append(keys, m.BlobKey)
			if !stored[m.BlobKey] {
				t.Errorf("BlobKey %q of %q is not a stored blob (stored: %v)", m.BlobKey, ev.ExternalID, stored)
			}
		}
	}
	if want := []string{"ChatExport/files/contract.pdf", "ChatExport/stickers/AnimatedSticker.tgs"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("BlobKeys = %v, want %v", keys, want)
	}
}
