package whatsapp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/provider"
)

// TestRealWorldHeaderFormats is the header table drawn from public evidence:
// the literal fixture lines of whatsapp-chat-parser (tests/parser.test.ts) and
// the shape families of whatstk's header_format_support.json (96 strftime
// formats: {y-m-d, d-m-y, m-d-y} × {-, /, .} × {24h, 12h} × {with, without
// seconds} × {"date, time - name:", "[date time] name:"}). Each case names its
// source. Cases marked unsupported document what is knowingly not parsed.
func TestRealWorldHeaderFormats(t *testing.T) {
	at := func(y int, mo time.Month, d, h, mi, s int) time.Time {
		return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
	}
	type tc struct {
		src, line string
		ok        bool
		order     string    // settled order, "" when ambiguous
		want      time.Time // in the settled order, else day-first
		sender    string
		text      string
	}
	cases := []tc{
		// ---- whatsapp-chat-parser fixtures (verbatim) ----
		{"wcp", "23/06/2018, 01:55 p.m. - Loris: one", true, "dmy", at(2018, 6, 23, 13, 55, 0), "Loris", "one"},
		{"wcp", "06/03/2017, 00:45 - You created group \"Test\"", true, "", at(2017, 3, 6, 0, 45, 0), "", "You created group \"Test\""},
		{"wcp", "03/02/17, 18:42 - Luke: ", true, "", at(2017, 2, 3, 18, 42, 0), "Luke", ""},
		{"wcp", "23/06/2018, 01:55 a.m. - Luke: Hey!", true, "dmy", at(2018, 6, 23, 1, 55, 0), "Luke", "Hey!"},
		{"wcp", "3/6/18, 1:55 p.m. - a: m", true, "", at(2018, 6, 3, 13, 55, 0), "a", "m"},
		{"wcp", "03-06-2018, 01.55 PM - a: m", true, "", at(2018, 6, 3, 13, 55, 0), "a", "m"},
		{"wcp colon after time", "13.06.18 21.25.15: a: m", true, "dmy", at(2018, 6, 13, 21, 25, 15), "a", "m"},
		{"wcp bracket, no comma, m.d.y", "[06.13.18 21:25:15] a: m", true, "mdy", at(2018, 6, 13, 21, 25, 15), "a", "m"},
		{"wcp finnish 'klo'", "13.6.2018 klo 21.25.15 - a: m", true, "dmy", at(2018, 6, 13, 21, 25, 15), "a", "m"},
		{"wcp croatian spaced dots", "13. 6. 2018. 21:25:15 a: m", true, "dmy", at(2018, 6, 13, 21, 25, 15), "a", "m"},
		{"wcp spanish 'p. m.'", "[3/6/18 1:55:00 p. m.] a: m", true, "", at(2018, 6, 3, 13, 55, 0), "a", "m"},
		{"wcp LRM before bracket", "‎[3/6/18 1:55:00 p. m.] a: m", true, "", at(2018, 6, 3, 13, 55, 0), "a", "m"},
		{"wcp iso in brackets", "[2018/06/13, 21:25:15] a: m", true, "ymd", at(2018, 6, 13, 21, 25, 15), "a", "m"},
		{"wcp year in the middle (unsupported)", "[06/2018/13, 21:25:15] a: m", false, "", time.Time{}, "", ""},
		{"wcp 4-digit year 'p. m.'", "3/6/2018 1:55 p. m. - a: m", true, "", at(2018, 6, 3, 13, 55, 0), "a", "m"},
		{"wcp", "3/6/18, 1:55 PM - a: m", true, "", at(2018, 6, 3, 13, 55, 0), "a", "m"},
		{"wcp ios attachment with inner spaces", "3/6/18, 1:55 p.m. - a: < attached: 00000042-PHOTO-2020-06-07-15-13-20.jpg >", true, "", at(2018, 6, 3, 13, 55, 0), "a", "< attached: 00000042-PHOTO-2020-06-07-15-13-20.jpg >"},
		{"wcp android attachment", "3/6/18, 1:55 p.m. - a: IMG-20210428-WA0001.jpg (file attached)", true, "", at(2018, 6, 3, 13, 55, 0), "a", "IMG-20210428-WA0001.jpg (file attached)"},
		{"wcp old ios attachment", "3/6/18, 1:55 p.m. - a: 2015-08-04-PHOTO-00004762.jpg <‎attached>", true, "", at(2018, 6, 3, 13, 55, 0), "a", "2015-08-04-PHOTO-00004762.jpg <‎attached>"},
		{"wcp german attachment, LRM", "3/6/18, 1:55 p.m. - a: ‎4f2680f1db95a8454775cc2eefc95bfc.jpg (Datei angehängt)", true, "", at(2018, 6, 3, 13, 55, 0), "a", "‎4f2680f1db95a8454775cc2eefc95bfc.jpg (Datei angehängt)"},

		// ---- whatstk shape families (one representative per axis) ----
		{"whatstk %d.%m.%y, %H:%M - (docs example)", "06.08.2016, 13:23 - Ash Ketchum: Hey guys!", true, "", at(2016, 8, 6, 13, 23, 0), "Ash Ketchum", "Hey guys!"},
		{"whatstk %d/%m/%y, %H:%M -", "12/08/2016, 16:20 - username: hi", true, "", at(2016, 8, 12, 16, 20, 0), "username", "hi"},
		{"whatstk %d-%m-%y, %H:%M:%S -", "25-12-24, 14:30:45 - Username: x", true, "dmy", at(2024, 12, 25, 14, 30, 45), "Username", "x"},
		{"whatstk %m/%d/%y, %I:%M %p -", "12/25/24, 2:30 PM - Username: x", true, "mdy", at(2024, 12, 25, 14, 30, 0), "Username", "x"},
		{"whatstk %m-%d-%y, %I:%M:%S %p -", "12-25-24, 2:30:45 PM - Username: x", true, "mdy", at(2024, 12, 25, 14, 30, 45), "Username", "x"},
		{"whatstk [%d/%m/%y %H:%M]", "[25/12/24 14:30] Username: x", true, "dmy", at(2024, 12, 25, 14, 30, 0), "Username", "x"},
		{"whatstk [%d.%m.%y %I:%M:%S %p]", "[25.12.24 2:30:45 PM] Username: x", true, "dmy", at(2024, 12, 25, 14, 30, 45), "Username", "x"},
		{"whatstk [%m-%d-%y %H:%M:%S]", "[12-25-24 14:30:45] Username: x", true, "mdy", at(2024, 12, 25, 14, 30, 45), "Username", "x"},
		{"whatstk [%m/%d/%y %I:%M %p]", "[12/25/24 2:30 PM] Username: x", true, "mdy", at(2024, 12, 25, 14, 30, 0), "Username", "x"},
		// whatstk also lists %y-%m-%d (two-digit year first) and %y-%d-%m. Those
		// are indistinguishable from day-first dates and are read as d-m-y: a
		// documented limitation, not a parse failure.
		{"whatstk %y-%m-%d, %H:%M - (read as d-m-y)", "16-08-06, 13:23 - Ash: hi", true, "dmy", at(2006, 8, 16, 13, 23, 0), "Ash", "hi"},

		// ---- newer exports: narrow no-break space, 4-digit years, en dash ----
		{"ios NNBSP", "[4/15/22, 2:03:21 PM] Jane: Hello", true, "mdy", at(2022, 4, 15, 14, 3, 21), "Jane", "Hello"},
		{"android NNBSP", "4/5/22, 9:02 PM - Max: Hi", true, "", at(2022, 5, 4, 21, 2, 0), "Max", "Hi"},
		{"android 4-digit year, en dash", "15/04/2022, 14:03 – Jane: Hello", true, "dmy", at(2022, 4, 15, 14, 3, 0), "Jane", "Hello"},
		{"android iso 4-digit year", "2022-04-15, 14:03 - Jane: Hello", true, "ymd", at(2022, 4, 15, 14, 3, 0), "Jane", "Hello"},
		{"ios 12h without space before PM", "[4/15/22, 2:03:21PM] Jane: Hello", true, "mdy", at(2022, 4, 15, 14, 3, 21), "Jane", "Hello"},

		// ---- must stay continuation lines ----
		{"continuation: date and time in prose", "15.04.22, 14:03 works for me", false, "", time.Time{}, "", ""},
		{"continuation: date only", "15.04.22 was great", false, "", time.Time{}, "", ""},
		{"continuation: word between but no time", "13.6.2018 klo nothing - a: m", false, "", time.Time{}, "", ""},
		{"continuation: spaced date without the closing dot and no dash", "13. 6. 2018 21:25:15 a: m", false, "", time.Time{}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.src+" "+c.line, func(t *testing.T) {
			h, ok := parseHeader(c.line)
			if ok != c.ok {
				t.Fatalf("parseHeader ok = %v, want %v (%+v)", ok, c.ok, h)
			}
			if !ok {
				return
			}
			if h.order() != c.order {
				t.Errorf("order = %q, want %q", h.order(), c.order)
			}
			order := c.order
			if order == "" {
				order = orderDMY
			}
			if got := h.time(order); !got.Equal(c.want) {
				t.Errorf("time = %v, want %v", got, c.want)
			}
			sender, text := splitSender(h.rest)
			if sender != c.sender || string(text) != c.text {
				t.Errorf("sender/text = %q / %q, want %q / %q", sender, text, c.sender, c.text)
			}
		})
	}
}

// TestLocalizedMarkers covers the placeholder strings in the languages the
// parser knows, end to end through Normalize.
func TestLocalizedMarkers(t *testing.T) {
	type tc struct {
		line       string
		attachment string // BlobKey expected (member dir is ""), "" when none
		body       string
		flag       string // metadata flag expected: media_omitted / deleted / edited / attachment / ""
	}
	cases := []tc{
		{"[15.04.22, 14:03:21] Jane: ‎<attached: 00000012-PHOTO-2022-04-15-14-03-21.jpg>", "00000012-PHOTO-2022-04-15-14-03-21.jpg", "", "attachment"},
		{"[15.04.22, 14:03:21] Jane: ‎<Anhang: 00000013-AUDIO-2022-04-15-14-03-21.opus>", "00000013-AUDIO-2022-04-15-14-03-21.opus", "", "attachment"},
		{"[15.04.22, 14:03:21] Jane: ‎<archivo adjunto: 00000014-VIDEO-2022-04-15-14-03-21.mp4>", "00000014-VIDEO-2022-04-15-14-03-21.mp4", "", "attachment"},
		{"[15.04.22, 14:03:21] Jane: ‎<pièce jointe : 00000015-PHOTO-2022-04-15-14-03-21.jpg>", "00000015-PHOTO-2022-04-15-14-03-21.jpg", "", "attachment"},
		{"[15.04.22, 14:03:21] Jane: < attached: 00000042-PHOTO-2020-06-07-15-13-20.jpg >", "00000042-PHOTO-2020-06-07-15-13-20.jpg", "", "attachment"},
		{"15.04.22, 14:03 - Jane: IMG-20220415-WA0001.jpg (file attached)\nLook", "IMG-20220415-WA0001.jpg", "Look", "attachment"},
		{"15.04.22, 14:03 - Jane: ‎4f2680f1db95a8454775cc2eefc95bfc.jpg (Datei angehängt)\nDir auch frohe Ostern.", "4f2680f1db95a8454775cc2eefc95bfc.jpg", "Dir auch frohe Ostern.", "attachment"},
		{"15.04.22, 14:03 - Jane: 2015-08-04-PHOTO-00004762.jpg <‎attached>", "2015-08-04-PHOTO-00004762.jpg", "", "attachment"},
		{"15.04.22, 14:03 - Jane: PTT-20220415-WA0003.opus (archivo adjunto)", "PTT-20220415-WA0003.opus", "", "attachment"},
		{"15.04.22, 14:03 - Jane: rapport.pdf (fichier joint)", "rapport.pdf", "", "attachment"},
		{"15.04.22, 14:03 - Jane: relatório.pdf (arquivo anexado)", "relatório.pdf", "", "attachment"},
		{"15.04.22, 14:03 - Jane: verslag.pdf (bestand bijgevoegd)", "verslag.pdf", "", "attachment"},
		{"15.04.22, 14:03 - Jane: rapor.pdf (dosya ekli)", "rapor.pdf", "", "attachment"},
		{"15.04.22, 14:03 - Jane: relazione.pdf (file allegato)", "relazione.pdf", "", "attachment"},

		{"15.04.22, 14:03 - Jane: <Media omitted>", "", "<Media omitted>", "media_omitted"},
		{"15.04.22, 14:03 - Jane: <Medien ausgeschlossen>", "", "<Medien ausgeschlossen>", "media_omitted"},
		{"15.04.22, 14:03 - Jane: <Multimedia omitido>", "", "<Multimedia omitido>", "media_omitted"},
		{"15.04.22, 14:03 - Jane: <Médias omis>", "", "<Médias omis>", "media_omitted"},
		{"15.04.22, 14:03 - Jane: <Media omessi>", "", "<Media omessi>", "media_omitted"},
		{"15.04.22, 14:03 - Jane: <Mídia omitida>", "", "<Mídia omitida>", "media_omitted"},
		{"15.04.22, 14:03 - Jane: <Media weggelaten>", "", "<Media weggelaten>", "media_omitted"},
		{"15.04.22, 14:03 - Jane: <Medya dahil edilmedi>", "", "<Medya dahil edilmedi>", "media_omitted"},
		{"[15.04.22, 14:03:21] Jane: ‎image omitted", "", "image omitted", "media_omitted"},
		{"[15.04.22, 14:03:21] Jane: ‎Contact card omitted", "", "Contact card omitted", "media_omitted"},
		{"[15.04.22, 14:03:21] Jane: ‎Bild weggelassen", "", "Bild weggelassen", "media_omitted"},
		{"[15.04.22, 14:03:21] Jane: ‎imagen omitida", "", "imagen omitida", "media_omitted"},
		{"[15.04.22, 14:03:21] Jane: ‎vidéo absente", "", "vidéo absente", "media_omitted"},
		{"[15.04.22, 14:03:21] Jane: ‎audio omis", "", "audio omis", "media_omitted"},
		{"[15.04.22, 14:03:21] Jane: ‎immagine omessa", "", "immagine omessa", "media_omitted"},
		{"[15.04.22, 14:03:21] Jane: ‎figurinha omitida", "", "figurinha omitida", "media_omitted"},
		{"[15.04.22, 14:03:21] Jane: ‎afbeelding weggelaten", "", "afbeelding weggelaten", "media_omitted"},
		{"[15.04.22, 14:03:21] Jane: ‎Report.pdf • 3 pages document omitted", "", "Report.pdf • 3 pages document omitted", "media_omitted"},

		{"15.04.22, 14:03 - Jane: This message was deleted", "", "This message was deleted", "deleted"},
		{"[15.04.22, 14:03:21] Jane: ‎This message was deleted.", "", "This message was deleted.", "deleted"},
		{"15.04.22, 14:03 - Jane: You deleted this message", "", "You deleted this message", "deleted"},
		{"15.04.22, 14:03 - Jane: Diese Nachricht wurde gelöscht", "", "Diese Nachricht wurde gelöscht", "deleted"},
		{"15.04.22, 14:03 - Jane: Du hast diese Nachricht gelöscht", "", "Du hast diese Nachricht gelöscht", "deleted"},
		{"15.04.22, 14:03 - Jane: Se eliminó este mensaje", "", "Se eliminó este mensaje", "deleted"},
		{"15.04.22, 14:03 - Jane: Ce message a été supprimé", "", "Ce message a été supprimé", "deleted"},
		{"15.04.22, 14:03 - Jane: Questo messaggio è stato eliminato", "", "Questo messaggio è stato eliminato", "deleted"},
		{"15.04.22, 14:03 - Jane: Mensagem apagada", "", "Mensagem apagada", "deleted"},
		{"15.04.22, 14:03 - Jane: Dit bericht is verwijderd", "", "Dit bericht is verwijderd", "deleted"},
		{"15.04.22, 14:03 - Jane: Bu mesaj silindi", "", "Bu mesaj silindi", "deleted"},

		{"15.04.22, 14:03 - Jane: See you at 7 <This message was edited>", "", "See you at 7", "edited"},
		{"15.04.22, 14:03 - Jane: Bis 7 <Diese Nachricht wurde bearbeitet.>", "", "Bis 7", "edited"},
		{"15.04.22, 14:03 - Jane: A las 7 <Se editó este mensaje.>", "", "A las 7", "edited"},
		{"15.04.22, 14:03 - Jane: À 7h <Ce message a été modifié.>", "", "À 7h", "edited"},
		{"15.04.22, 14:03 - Jane: Alle 7 <Questo messaggio è stato modificato.>", "", "Alle 7", "edited"},
		{"15.04.22, 14:03 - Jane: Às 7 <Mensagem editada>", "", "Às 7", "edited"},
		{"15.04.22, 14:03 - Jane: Om 7 <Dit bericht is bewerkt.>", "", "Om 7", "edited"},
		{"15.04.22, 14:03 - Jane: 7'de <Bu mesaj düzenlendi>", "", "7'de", "edited"},

		{"15.04.22, 14:03 - Jane: the scene was omitted", "", "the scene was omitted", ""},
		{"15.04.22, 14:03 - Jane: see report.pdf (final version)", "", "see report.pdf (final version)", ""},
		{"15.04.22, 14:03 - Jane: <3>", "", "<3>", ""},
	}
	for _, c := range cases {
		t.Run(c.line, func(t *testing.T) {
			events := extract(t, "_chat.txt", c.line+"\n")
			if len(events) != 1 || events[0].Type != "whatsapp.message" {
				t.Fatalf("got %+v, want one whatsapp.message", events)
			}
			ev := events[0]
			if ev.Body != c.body {
				t.Errorf("body = %q, want %q", ev.Body, c.body)
			}
			for _, flag := range []string{"media_omitted", "deleted", "edited", "attachment"} {
				if _, has := ev.Metadata[flag]; has != (flag == c.flag) {
					t.Errorf("flag %q present = %v, want %v (%v)", flag, has, flag == c.flag, ev.Metadata)
				}
			}
			var key string
			if len(ev.Media) == 1 {
				key = ev.Media[0].BlobKey
			}
			if key != c.attachment {
				t.Errorf("BlobKey = %q, want %q", key, c.attachment)
			}
		})
	}
}

// TestEncryptionNoticeLocalized: the notice iOS attributes to the chat is a
// system line in every known language, a message that merely mentions
// end-to-end encryption is not.
func TestEncryptionNoticeLocalized(t *testing.T) {
	for _, notice := range []string{
		"Messages and calls are end-to-end encrypted. No one outside of this chat, not even WhatsApp, can read or listen to them.",
		"Nachrichten und Anrufe sind Ende-zu-Ende-verschlüsselt. Niemand außerhalb dieses Chats kann sie lesen oder anhören, nicht einmal WhatsApp.",
		"Los mensajes y las llamadas están cifrados de extremo a extremo.",
		"Les messages et les appels sont chiffrés de bout en bout.",
		"I messaggi e le chiamate sono crittografati end-to-end.",
		"As mensagens e as chamadas são protegidas com a criptografia de ponta a ponta.",
		"Berichten en gesprekken worden end-to-end versleuteld.",
		"Mesajlar ve aramalar uçtan uca şifrelidir.",
	} {
		events := extract(t, "_chat.txt", "[15.04.22, 14:03:21] Book Club: ‎"+notice+"\n")
		if len(events) != 1 || events[0].Type != "whatsapp.system" {
			t.Errorf("%q: got %+v, want whatsapp.system", notice, events)
		}
	}
	events := extract(t, "_chat.txt", "[15.04.22, 14:03:21] Jane: our end-to-end tests are broken again\n")
	if len(events) != 1 || events[0].Type != "whatsapp.message" {
		t.Errorf("a message about end-to-end tests must stay a message: %+v", events)
	}
}

// FuzzTranscript feeds arbitrary bytes through the extractor and Normalize:
// nothing may panic, and a malformed transcript must still come out as events
// (messages, system lines or one whatsapp.unknown), never as an error.
func FuzzTranscript(f *testing.F) {
	seeds := []string{
		"[15.04.22, 14:03:21] Jane Doe: Hello\n[15.04.22, 14:03:22] Jane Doe: ‎<attached: a.jpg>\n",
		"15.04.22, 14:03 - Jane Doe: IMG-20220415-WA0001.jpg (file attached)\nLook at this\n",
		"4/5/22, 9:02 PM - Max Example: Hi Jane\nhow are you?\n\n4/20/22 works for me.\n",
		"13. 6. 2018. 21:25:15 a: m\n13.6.2018 klo 21.25.15 - a: m\n13.06.18 21.25.15: a: m\n",
		"\ufeffexported by a tool\n[15.04.22, 14:03:21] Jane Doe: Hello\n",
		"shopping list\n- milk\n",
		"",
		"[15.04.22, 14:03:21] Jane: <Media omitted>\n15.04.22, 14:03 - Jane: This message was deleted\n",
		"[15.04.22, 14:03:21] ‪+49 170 0000000‬: Hi <This message was edited>\n",
		"31.04.22, 14:03 - x\n[06/2018/13, 21:25:15] a: m\n[15.04.22, 14:03:21 Jane Doe: Hello\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	p := New()
	f.Fuzz(func(t *testing.T, transcript string) {
		err := chatEx{}.Extract("WhatsApp Chat with Fuzz.txt", strings.NewReader(transcript), func(raw provider.RawRecord) error {
			evs, err := p.Normalize(context.Background(), raw)
			if err != nil {
				return err
			}
			for _, ev := range evs {
				if ev.ExternalID == "" || ev.Type == "" || ev.SourceID != sourceID {
					t.Errorf("event without id/type/source: %+v", ev)
				}
			}
			return nil
		})
		if err != nil {
			t.Errorf("Extract failed on %q: %v", transcript, err)
		}
	})
}
