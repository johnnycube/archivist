package whatsapp

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

// importDir zips one fixture directory (one exported chat) and imports it,
// collecting the keys of the media members the importer stores as blobs.
func importDir(t *testing.T, dir string) ([]domain.Event, *domain.Coverage, map[string]bool) {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "chat.zip")
	if err := archivetest.ZipDir(filepath.Join("testdata", dir), zipPath); err != nil {
		t.Fatal(err)
	}
	blobs := map[string]bool{}
	im := bundle.NewImporter(New())
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
	if cov.Duplicates != 0 {
		t.Errorf("Duplicates = %d, want 0 (same-minute repeats are real messages)", cov.Duplicates)
	}
}

func checkUniqueIDs(t *testing.T, events []domain.Event) {
	t.Helper()
	ids := map[string]bool{}
	for _, ev := range events {
		if ev.ExternalID == "" || ids[ev.ExternalID] {
			t.Errorf("ExternalID %q empty or reused", ev.ExternalID)
		}
		ids[ev.ExternalID] = true
		if ev.Metadata["timezone"] != unknownTZ && ev.Type != "whatsapp.unknown" {
			t.Errorf("event %q lacks the timezone note: %v", ev.ExternalID, ev.Metadata)
		}
	}
}

func TestImportIOS(t *testing.T) {
	events, cov, blobs := importDir(t, "ios")
	checkCounts(t, cov, map[string]int{"whatsapp.message": 9, "whatsapp.system": 2})
	checkUniqueIDs(t, events)
	if cov.MediaStored != 1 {
		t.Errorf("MediaStored = %d, want 1", cov.MediaStored)
	}

	// The first three dates (01.04, 02.04, 03.04) are ambiguous and only
	// 15.04 settles day-first; the early messages must come out as April.
	welcome := byBody(events, "Welcome everyone!\nThis month we read:\n\n- Dune\n- Solaris")
	if welcome == nil {
		t.Fatalf("multi-line message not found")
	}
	if want := time.Date(2022, 4, 2, 10, 15, 30, 0, time.UTC); !welcome.OccurredAt.Equal(want) {
		t.Errorf("OccurredAt = %v, want %v", welcome.OccurredAt, want)
	}
	if welcome.Actor != "Jane Doe" || welcome.ThreadExternalID != "chat:Book Club" || welcome.Title != "Message in Book Club" {
		t.Errorf("message not mapped: %+v", welcome)
	}
	if welcome.Metadata["raw_date"] != "02.04.22, 10:15:30" || welcome.Metadata["export_format"] != "ios" {
		t.Errorf("metadata = %v", welcome.Metadata)
	}
	if _, guessed := welcome.Metadata["date_order_guessed"]; guessed {
		t.Errorf("a settled date order must not be flagged as a guess: %v", welcome.Metadata)
	}

	photo := byMeta(events, "attachment")
	const key = "WhatsApp Chat - Book Club/00000012-PHOTO-2022-04-15-14-03-21.jpg"
	if photo == nil || photo.Body != "" || len(photo.Media) != 1 || photo.Media[0].BlobKey != key || !blobs[key] {
		t.Errorf("attachment not mapped to the stored blob %q (blobs %v): %+v", key, blobs, photo)
	}
	if ev := byMeta(events, "media_omitted"); ev == nil || ev.Body != "image omitted" || ev.Actor != "Bob Builder" {
		t.Errorf("media omitted not flagged: %+v", ev)
	}
	if ev := byMeta(events, "deleted"); ev == nil || ev.Actor != "Bob Builder" {
		t.Errorf("deleted message not flagged: %+v", ev)
	}
	if ev := byMeta(events, "edited"); ev == nil || ev.Body != "See you at 7" {
		t.Errorf("edited message not flagged / tag not stripped: %+v", ev)
	}
	if ev := byBody(events, "Hi, I am new here"); ev == nil || ev.Actor != "+49 170 0000000" {
		t.Errorf("bidi marks around a phone-number sender not removed: %+v", ev)
	}

	var notices []string
	for _, ev := range events {
		if ev.Type == "whatsapp.system" {
			notices = append(notices, ev.Body)
			if ev.Category != domain.CategoryOther || ev.Actor != "" {
				t.Errorf("system line not mapped: %+v", ev)
			}
		}
	}
	if len(notices) != 2 || !strings.HasPrefix(notices[0], "Messages and calls are end-to-end encrypted") || notices[1] != "Jane Doe added Bob Builder" {
		t.Errorf("system lines = %q", notices)
	}

	if oks := bodies(events, "ok"); len(oks) != 2 || !strings.HasSuffix(oks[0].ExternalID, ":1") || !strings.HasSuffix(oks[1].ExternalID, ":2") {
		t.Errorf("identical same-second messages must both survive with occurrence ids: %+v", oks)
	}
}

func TestImportAndroid(t *testing.T) {
	events, cov, blobs := importDir(t, "android")
	checkCounts(t, cov, map[string]int{"whatsapp.message": 7, "whatsapp.system": 1})
	checkUniqueIDs(t, events)

	// 4/5/22 is ambiguous until 4/15/22 proves month-first: April 5th, 9:02 PM.
	multi := byBody(events, "Hi Jane\nhow are you?\n\n4/20/22 works for me.")
	if multi == nil {
		t.Fatalf("multi-line message (with a date-like continuation line) not found")
	}
	if want := time.Date(2022, 4, 5, 21, 2, 0, 0, time.UTC); !multi.OccurredAt.Equal(want) {
		t.Errorf("OccurredAt = %v, want %v", multi.OccurredAt, want)
	}
	if multi.Actor != "Max Example" || multi.ThreadExternalID != "chat:Jane Doe" || multi.Metadata["export_format"] != "android" {
		t.Errorf("message not mapped: %+v", multi)
	}
	if multi.Metadata["raw_date"] != "4/5/22, 9:02\u202fPM" {
		t.Errorf("raw_date = %q, want the header verbatim", multi.Metadata["raw_date"])
	}

	omitted := byMeta(events, "media_omitted")
	if omitted == nil || !omitted.OccurredAt.Equal(time.Date(2022, 4, 6, 0, 5, 0, 0, time.UTC)) {
		t.Errorf("<Media omitted> at 12:05 AM not mapped: %+v", omitted)
	}
	photo := byMeta(events, "attachment")
	if photo == nil || photo.Body != "Look at this view" || len(photo.Media) != 1 ||
		photo.Media[0].BlobKey != "IMG-20220415-WA0001.jpg" || !blobs["IMG-20220415-WA0001.jpg"] {
		t.Errorf("attachment with caption not mapped: %+v", photo)
	}
	if ev := byMeta(events, "deleted"); ev == nil || ev.Body != "You deleted this message" {
		t.Errorf("deleted message not flagged: %+v", ev)
	}
	if oks := bodies(events, "ok"); len(oks) != 2 || oks[0].ExternalID == oks[1].ExternalID {
		t.Errorf("identical same-minute messages must both survive: %+v", oks)
	}
	if sys := byType(events, "whatsapp.system"); sys == nil || sys.Title != "System message in Jane Doe" {
		t.Errorf("system line not mapped: %+v", sys)
	}
}

func TestImportAndroidGermanNeverDisambiguated(t *testing.T) {
	events, cov, blobs := importDir(t, "android_de")
	// The shared PDF is no media type, but the provider declares every file of
	// a chat export an attachment (provider.AttachmentMatcher): it is stored,
	// not captured as whatsapp.unknown.
	checkCounts(t, cov, map[string]int{"whatsapp.message": 4, "whatsapp.system": 1})
	checkUniqueIDs(t, events)
	if cov.MediaStored != 1 || cov.Generic != 0 {
		t.Errorf("MediaStored/Generic = %d/%d, want 1/0", cov.MediaStored, cov.Generic)
	}

	hallo := byBody(events, "Hallo")
	if hallo == nil || hallo.ThreadExternalID != "chat:Erika Musterfrau" {
		t.Fatalf("message not mapped: %+v", hallo)
	}
	if want := time.Date(2022, 4, 1, 9, 1, 0, 0, time.UTC); !hallo.OccurredAt.Equal(want) {
		t.Errorf("OccurredAt = %v, want day-first %v", hallo.OccurredAt, want)
	}
	if hallo.Metadata["date_order"] != "dmy" || hallo.Metadata["date_order_guessed"] != true || hallo.Metadata["date_order_basis"] != "separator" {
		t.Errorf("the fallback guess must be recorded: %v", hallo.Metadata)
	}
	if ev := byMeta(events, "media_omitted"); ev == nil || ev.Actor != "Max Mustermann" {
		t.Errorf("<Medien ausgeschlossen> not flagged: %+v", ev)
	}
	if doc := byMeta(events, "attachment"); doc == nil || doc.Media[0].BlobKey != "DOC-20220403-WA0002.pdf" {
		t.Errorf("(Datei angehängt) not mapped: %+v", doc)
	} else if !blobs[doc.Media[0].BlobKey] {
		t.Errorf("the message's BlobKey %q is not a stored blob: %v", doc.Media[0].BlobKey, blobs)
	}
}

func TestImportIOSTwelveHour(t *testing.T) {
	events, cov, _ := importDir(t, "ios_us")
	checkCounts(t, cov, map[string]int{"whatsapp.message": 2})
	hello := byBody(events, "Hello")
	if hello == nil || hello.ThreadExternalID != "chat:WhatsApp chat" || !hello.OccurredAt.Equal(time.Date(2022, 4, 15, 14, 3, 21, 0, time.UTC)) {
		t.Errorf("12h iOS message not mapped: %+v", hello)
	}
	if snack := byBody(events, "Midnight snack?"); snack == nil || !snack.OccurredAt.Equal(time.Date(2022, 4, 16, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("12:00:00 AM (no-break space) not mapped: %+v", snack)
	}
}

// An iOS export names the chat only in the ZIP's own file name. When the upload
// name is known the chat gets its name (and ids that cover it); an upload name
// that says nothing, or a transcript that names the chat itself, changes nothing.
func TestChatNameFromArchiveName(t *testing.T) {
	imp := func(dir, zipName string) []domain.Event {
		t.Helper()
		zipPath := filepath.Join(t.TempDir(), zipName)
		if err := archivetest.ZipDir(filepath.Join("testdata", dir), zipPath); err != nil {
			t.Fatal(err)
		}
		var events []domain.Event
		_, err := bundle.NewImporter(New()).ImportZip(context.Background(), zipPath, func(ev domain.Event) error {
			events = append(events, ev)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return events
	}

	named := imp("ios_us", "WhatsApp Chat - Book Club (1).zip")
	hello := byBody(named, "Hello")
	if hello == nil || hello.ThreadExternalID != "chat:Book Club" || hello.Metadata["chat"] != "Book Club" || hello.Title != "Message in Book Club" {
		t.Fatalf("chat not named after the archive: %+v", hello)
	}
	checkUniqueIDs(t, named)
	if again := byBody(imp("ios_us", "WhatsApp Chat - Book Club.zip"), "Hello"); again.ExternalID != hello.ExternalID {
		t.Errorf("a browser's copy suffix changed the id: %s vs %s", again.ExternalID, hello.ExternalID)
	}
	if other := byBody(imp("ios_us", "WhatsApp Chat - Family.zip"), "Hello"); other.ExternalID == hello.ExternalID {
		t.Error("the same message in two differently named chats shares an id")
	}
	unnamed := byBody(imp("ios_us", "export.zip"), "Hello")
	if unnamed.ThreadExternalID != "chat:WhatsApp chat" || unnamed.ExternalID == hello.ExternalID {
		t.Errorf("an upload name that says nothing must not name the chat: %+v", unnamed)
	}

	// A name from inside the archive wins, ids untouched.
	inside, renamed := byBody(imp("android", "chat.zip"), "Hello!"), byBody(imp("android", "WhatsApp Chat - Someone Else.zip"), "Hello!")
	if inside == nil || renamed == nil {
		t.Fatal("android fixture message not found")
	}
	if renamed.ThreadExternalID != inside.ThreadExternalID || renamed.ExternalID != inside.ExternalID {
		t.Errorf("archive name overrode the transcript's: %+v", renamed)
	}
}

func TestArchiveChatName(t *testing.T) {
	for archive, want := range map[string]string{
		"WhatsApp Chat - Book Club.zip":     "Book Club",
		"whatsapp chat - Émile & Zoë.ZIP":   "Émile & Zoë",
		"WhatsApp Chat - Book Club (2).zip": "Book Club",
		"WhatsApp Chat with Jane Doe.zip":   "Jane Doe",
		"WhatsApp Chat mit Erika.zip":       "Erika",
		"export.zip":                        "",
		"4f2a9c.zip":                        "",
		"":                                  "",
	} {
		if got := archiveChatName(archive); got != want {
			t.Errorf("archiveChatName(%q) = %q, want %q", archive, got, want)
		}
	}
}

func TestDescriptorDeclaresEveryType(t *testing.T) {
	declared := map[string]bool{}
	for _, d := range New().Descriptor().EventTypes {
		declared[d.Type] = true
	}
	for _, dir := range []string{"ios", "android", "android_de", "ios_us"} {
		events, _, _ := importDir(t, dir)
		for _, ev := range events {
			if !declared[ev.Type] || ev.SourceID != sourceID {
				t.Errorf("%s: undeclared type or wrong source: %+v", dir, ev)
			}
		}
	}
}

// ---- extractor behaviour ----

func extract(t *testing.T, member, transcript string) []domain.Event {
	t.Helper()
	p := New()
	var events []domain.Event
	err := chatEx{}.Extract(member, strings.NewReader(transcript), func(raw provider.RawRecord) error {
		evs, err := p.Normalize(context.Background(), raw)
		events = append(events, evs...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// A later, longer export of the same chat must give the old messages the same
// ids, even though every line number after an insertion point differs.
func TestExternalIDsStableAcrossLongerExport(t *testing.T) {
	const member = "WhatsApp Chat with Jane Doe.txt"
	short := "13.04.22, 10:00 - Jane Doe: ok\n13.04.22, 10:00 - Jane Doe: ok\n13.04.22, 10:01 - Max Example: fine\n"
	long := "12.04.22, 08:00 - Max Example: earlier history\nnow included\n" + short + "14.04.22, 09:00 - Jane Doe: ok\n"

	ids := func(evs []domain.Event) map[string]bool {
		out := map[string]bool{}
		for _, ev := range evs {
			out[ev.ExternalID] = true
		}
		return out
	}
	first, second := extract(t, member, short), extract(t, member, long)
	if len(first) != 3 || len(second) != 5 || len(ids(second)) != 5 {
		t.Fatalf("got %d and %d events (%d distinct ids), want 3 and 5 (5)", len(first), len(second), len(ids(second)))
	}
	for id := range ids(first) {
		if !ids(second)[id] {
			t.Errorf("id %q of the short export is missing from the longer one", id)
		}
	}
	if again := extract(t, member, short); fmt.Sprint(ids(again)) != fmt.Sprint(ids(first)) {
		t.Errorf("re-import changed ids: %v vs %v", ids(again), ids(first))
	}
}

func TestPendingBufferIsBounded(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxPendingMsgs+10; i++ {
		fmt.Fprintf(&b, "4/5/22, 9:%02d AM - Jane Doe: message %d\n", i%60, i)
	}
	b.WriteString("4/15/22, 9:00 AM - Jane Doe: settles month-first\n4/16/22, 9:00 AM - Jane Doe: after\n")

	emitted := 0
	var atBound int
	err := chatEx{}.Extract("WhatsApp Chat with Jane Doe.txt", strings.NewReader(b.String()), func(raw provider.RawRecord) error {
		emitted++
		if strings.Contains(string(raw.Payload), fmt.Sprintf(`"text":"message %d"`, maxPendingMsgs-1)) {
			atBound = emitted
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if emitted != maxPendingMsgs+12 || atBound != maxPendingMsgs {
		t.Errorf("emitted %d records, bound reached at %d", emitted, atBound)
	}

	events := extract(t, "WhatsApp Chat with Jane Doe.txt", b.String())
	early, late := events[0], events[len(events)-1]
	if early.Metadata["date_order_guessed"] != true || early.Metadata["date_order"] != "mdy" {
		t.Errorf("messages flushed at the bound must be flagged as guessed: %v", early.Metadata)
	}
	if _, guessed := late.Metadata["date_order_guessed"]; guessed || !late.OccurredAt.Equal(time.Date(2022, 4, 16, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("messages after the settling header must not be flagged: %+v", late)
	}
}

func TestTextWithoutHeadersStaysVisible(t *testing.T) {
	events := extract(t, "notes.txt", "shopping list\n- milk\n")
	if len(events) != 1 || events[0].Type != "whatsapp.unknown" || events[0].Metadata["preview"] != "shopping list\n- milk" {
		t.Errorf("a .txt that is no transcript must become whatsapp.unknown: %+v", events)
	}
	events = extract(t, "_chat.txt", "\ufeffexported by a tool\n[15.04.22, 14:03:21] Jane Doe: Hello\n")
	if len(events) != 2 || events[0].Type != "whatsapp.message" || events[1].Type != "whatsapp.unknown" {
		t.Errorf("lines before the first header must not be dropped: %+v", events)
	}
}

func TestOverlongLineIsTruncatedNotFatal(t *testing.T) {
	events := extract(t, "_chat.txt", "[15.04.22, 14:03:21] Jane Doe: "+strings.Repeat("x", maxLineBytes+100)+"\n[15.04.22, 14:04:00] Jane Doe: next\n")
	if len(events) != 2 || events[0].Metadata["truncated"] != true || len(events[0].Body) > maxLineBytes || events[1].Body != "next" {
		t.Errorf("overlong line not handled: %d events", len(events))
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

func byBody(evs []domain.Event, body string) *domain.Event {
	if m := bodies(evs, body); len(m) > 0 {
		return &m[0]
	}
	return nil
}

func bodies(evs []domain.Event, body string) (out []domain.Event) {
	for _, ev := range evs {
		if ev.Type == "whatsapp.message" && ev.Body == body {
			out = append(out, ev)
		}
	}
	return out
}

func byMeta(evs []domain.Event, key string) *domain.Event {
	for i := range evs {
		if evs[i].Metadata[key] != nil {
			return &evs[i]
		}
	}
	return nil
}
