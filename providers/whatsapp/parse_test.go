package whatsapp

import (
	"testing"
	"time"
)

func TestParseHeader(t *testing.T) {
	at := func(y int, mo time.Month, d, h, mi, s int) time.Time {
		return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
	}
	cases := []struct {
		name   string
		line   string
		ok     bool
		order  string // "" = ambiguous
		format string
		want   time.Time // in the settled order, or day-first when ambiguous
		rest   string
	}{
		{"ios 24h dotted", "[15.04.22, 14:03:21] Jane Doe: Hello", true, "dmy", "ios", at(2022, 4, 15, 14, 3, 21), "Jane Doe: Hello"},
		{"ios 12h us", "[4/15/22, 2:03:21 PM] Jane Doe: Hello", true, "mdy", "ios", at(2022, 4, 15, 14, 3, 21), "Jane Doe: Hello"},
		{"ios iso", "[2022-04-15, 14:03:21] Jane Doe: Hello", true, "ymd", "ios", at(2022, 4, 15, 14, 3, 21), "Jane Doe: Hello"},
		{"ios leading LRM", "\u200e[15.04.22, 14:03:21] Jane Doe: \u200e<attached: a.jpg>", true, "dmy", "ios", at(2022, 4, 15, 14, 3, 21), "Jane Doe: \u200e<attached: a.jpg>"},
		{"ios BOM on first line", "\ufeff[15.04.22, 14:03:21] Jane Doe: Hello", true, "dmy", "ios", at(2022, 4, 15, 14, 3, 21), "Jane Doe: Hello"},
		{"ios NNBSP before PM", "[4/15/22, 2:03:21\u202fPM] Jane Doe: Hello", true, "mdy", "ios", at(2022, 4, 15, 14, 3, 21), "Jane Doe: Hello"},
		{"ios NBSP before AM", "[4/15/22, 12:00:00\u00a0AM] Jane Doe: Hello", true, "mdy", "ios", at(2022, 4, 15, 0, 0, 0), "Jane Doe: Hello"},
		{"ios 4-digit year no comma", "[15/04/2022 14:03:21] Jane Doe: Hello", true, "dmy", "ios", at(2022, 4, 15, 14, 3, 21), "Jane Doe: Hello"},
		{"ios dotted time", "[15.04.2022 14.03.21] Jane Doe: Hello", true, "dmy", "ios", at(2022, 4, 15, 14, 3, 21), "Jane Doe: Hello"},
		{"android 24h", "15.04.22, 14:03 - Jane Doe: Hello", true, "dmy", "android", at(2022, 4, 15, 14, 3, 0), "Jane Doe: Hello"},
		{"android 12h", "4/15/22, 2:03 PM - Jane Doe: Hello", true, "mdy", "android", at(2022, 4, 15, 14, 3, 0), "Jane Doe: Hello"},
		{"android 12 PM is noon", "4/15/22, 12:30 PM - Jane Doe: Hello", true, "mdy", "android", at(2022, 4, 15, 12, 30, 0), "Jane Doe: Hello"},
		{"android lowercase pm", "15/04/2022, 2:03 pm - Jane Doe: Hello", true, "dmy", "android", at(2022, 4, 15, 14, 3, 0), "Jane Doe: Hello"},
		{"android spanish p. m.", "15/4/22, 2:03 p.\u00a0m. - Jane Doe: Hola", true, "dmy", "android", at(2022, 4, 15, 14, 3, 0), "Jane Doe: Hola"},
		{"android a.m.", "15/4/22, 9:03 a.m. - Jane Doe: Hola", true, "dmy", "android", at(2022, 4, 15, 9, 3, 0), "Jane Doe: Hola"},
		{"android dashes", "15-04-2022 14:03 - Jane Doe: Hallo", true, "dmy", "android", at(2022, 4, 15, 14, 3, 0), "Jane Doe: Hallo"},
		{"android iso slashes", "2022/04/15, 14:03 - Jane Doe: Hello", true, "ymd", "android", at(2022, 4, 15, 14, 3, 0), "Jane Doe: Hello"},
		{"android system line", "15.04.22, 14:03 - Jane added Bob", true, "dmy", "android", at(2022, 4, 15, 14, 3, 0), "Jane added Bob"},
		{"ambiguous day/month", "04.05.22, 14:03 - Jane Doe: Hello", true, "", "android", at(2022, 5, 4, 14, 3, 0), "Jane Doe: Hello"},
		{"continuation: April 31st does not exist", "31.04.22, 14:03 - x", false, "", "", time.Time{}, ""},
		{"day valid one way only", "12/31/22, 14:03 - x", true, "mdy", "android", at(2022, 12, 31, 14, 3, 0), "x"},
		{"leap day", "29.02.24, 08:00 - x", true, "dmy", "android", at(2024, 2, 29, 8, 0, 0), "x"},

		{"continuation: plain text", "how are you?", false, "", "", time.Time{}, ""},
		{"continuation: date without time", "15.04.22 was great", false, "", "", time.Time{}, ""},
		{"continuation: date and time but no dash", "15.04.22, 14:03 works for me", false, "", "", time.Time{}, ""},
		{"continuation: header not at line start", "see 15.04.22, 14:03 - Jane Doe: Hello", false, "", "", time.Time{}, ""},
		{"continuation: both fields above 12", "15.14.22, 14:03 - x", false, "", "", time.Time{}, ""},
		{"continuation: mixed separators", "15.04/22, 14:03 - x", false, "", "", time.Time{}, ""},
		{"continuation: hour out of range", "15.04.22, 25:03 - x", false, "", "", time.Time{}, ""},
		{"continuation: 24h hour with PM", "[15.04.22, 14:03:21 PM] x", false, "", "", time.Time{}, ""},
		{"continuation: 3-digit year", "15.04.202, 14:03 - x", false, "", "", time.Time{}, ""},
		{"continuation: unclosed bracket", "[15.04.22, 14:03:21 Jane Doe: Hello", false, "", "", time.Time{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, ok := parseHeader(c.line)
			if ok != c.ok {
				t.Fatalf("parseHeader ok = %v, want %v (%+v)", ok, c.ok, h)
			}
			if !ok {
				return
			}
			if h.order() != c.order || h.format() != c.format || h.rest != c.rest {
				t.Errorf("order %q format %q rest %q, want %q %q %q", h.order(), h.format(), h.rest, c.order, c.format, c.rest)
			}
			order := c.order
			if order == "" {
				order = orderDMY
			}
			if got := h.time(order); !got.Equal(c.want) || got.Location() != time.UTC {
				t.Errorf("time = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAmbiguousHeaderKeepsBothReadings(t *testing.T) {
	h, ok := parseHeader("[04/05/22, 14:03:21] Jane Doe: Hello")
	if !ok || h.order() != "" {
		t.Fatalf("04/05/22 must parse as ambiguous: %+v", h)
	}
	if d, m := h.time(orderDMY), h.time(orderMDY); d.Month() != time.May || d.Day() != 4 || m.Month() != time.April || m.Day() != 5 {
		t.Errorf("dmy = %v, mdy = %v", d, m)
	}
	if h.raw != "04/05/22, 14:03:21" {
		t.Errorf("raw = %q", h.raw)
	}
}

func TestGuessOrder(t *testing.T) {
	cases := []struct {
		name         string
		lines        []string
		order, basis string
	}{
		// Day-first runs 1 Feb, 2 Feb, 3 Feb, 1 Mar; month-first would jump back
		// from 2 March to 3 January.
		{"chronology says day-first", []string{"1/2/22, 10:00 - x", "2/2/22, 10:00 - x", "3/2/22, 10:00 - x", "1/3/22, 10:00 - x"}, "dmy", "chronology"},
		{"chronology says month-first", []string{"2/1/22, 10:00 - x", "2/2/22, 10:00 - x", "2/3/22, 10:00 - x", "3/1/22, 10:00 - x"}, "mdy", "chronology"},
		{"chronology beats the separator", []string{"2.1.22, 10:00 - x", "2.3.22, 10:00 - x", "3.1.22, 10:00 - x"}, "mdy", "chronology"},
		{"tie: dots are day-first", []string{"01.04.22, 09:00 - x", "02.04.22, 09:00 - x"}, "dmy", "separator"},
		{"tie: slashes with AM/PM are month-first", []string{"4/5/22, 9:00 AM - x"}, "mdy", "separator"},
		{"tie: slashes in 24h are day-first", []string{"04/05/22, 09:00 - x"}, "dmy", "separator"},
		{"tie: dashes are day-first", []string{"04-05-22 09:00 - x"}, "dmy", "separator"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var hs []header
			for _, l := range c.lines {
				h, ok := parseHeader(l)
				if !ok || h.order() != "" {
					t.Fatalf("%q should be an ambiguous header", l)
				}
				hs = append(hs, h)
			}
			if order, basis := guessOrder(hs); order != c.order || basis != c.basis {
				t.Errorf("guessOrder = %q (%s), want %q (%s)", order, basis, c.order, c.basis)
			}
		})
	}
}

func TestSplitSender(t *testing.T) {
	cases := []struct{ rest, sender, text string }{
		{"Jane Doe: Hello", "Jane Doe", "Hello"},
		{"Jane Doe: note: keep the second colon", "Jane Doe", "note: keep the second colon"},
		{"\u202a+49 170 0000000\u202c: Hi", "+49 170 0000000", "Hi"},
		{"Jane Doe: ", "Jane Doe", ""},
		{"Jane Doe:", "Jane Doe", ""},
		{"Jane added Bob", "", "Jane added Bob"},
		{"Meeting at 10:30 was moved", "", "Meeting at 10:30 was moved"},
	}
	for _, c := range cases {
		if sender, text := splitSender(c.rest); sender != c.sender || string(text) != c.text {
			t.Errorf("splitSender(%q) = %q, %q; want %q, %q", c.rest, sender, text, c.sender, c.text)
		}
	}
}

func TestChatName(t *testing.T) {
	cases := map[string]string{
		"WhatsApp Chat with Jane Doe.txt":                       "Jane Doe",
		"export/WhatsApp Chat with Book Club.txt":               "Book Club",
		"WhatsApp Chat mit Erika Musterfrau.txt":                "Erika Musterfrau",
		"Chat de WhatsApp con Juan Pérez.txt":                   "Juan Pérez",
		"Discussion WhatsApp avec Marie.txt":                    "Marie",
		"Conversa do WhatsApp com Ana.txt":                      "Ana",
		"WhatsApp-chat met Piet.txt":                            "Piet",
		"WhatsApp Chat with Friends with Benefits.txt":          "Friends with Benefits",
		"Чат WhatsApp с Иван.txt":                               "Чат WhatsApp с Иван",
		"_chat.txt":                                             "WhatsApp chat",
		"WhatsApp Chat - Book Club/_chat.txt":                   "Book Club",
		"Family/_chat.txt":                                      "Family",
		"some/nested/Family/_chat.txt":                          "Family",
		"notes.txt":                                             "WhatsApp chat",
		"WhatsApp Chat - Book Club/WhatsApp Chat with Jane.txt": "Jane",
	}
	for member, want := range cases {
		if got := chatName(member); got != want {
			t.Errorf("chatName(%q) = %q, want %q", member, got, want)
		}
	}
}

func TestAttachmentAndPlaceholders(t *testing.T) {
	attach := []struct{ text, file, rest string }{
		{"<attached: 00000012-PHOTO-2022-04-15-14-03-21.jpg>", "00000012-PHOTO-2022-04-15-14-03-21.jpg", ""},
		{"<Anhang: 00000013-AUDIO-2022-04-15-14-03-21.opus>", "00000013-AUDIO-2022-04-15-14-03-21.opus", ""},
		{"IMG-20220415-WA0001.jpg (file attached)", "IMG-20220415-WA0001.jpg", ""},
		{"IMG-20220415-WA0001.jpg (file attached)\nLook at this", "IMG-20220415-WA0001.jpg", "Look at this"},
		{"My Report.pdf (Datei angehängt)", "My Report.pdf", ""},
		{"VID-20220415-WA0007.mp4 (some unlisted language)", "VID-20220415-WA0007.mp4", ""},
		{"see report.pdf (final version)", "", "see report.pdf (final version)"},
		{"I wrote <note: nothing.here> in the middle of it", "", "I wrote <note: nothing.here> in the middle of it"},
		{"plain text", "", "plain text"},
	}
	for _, c := range attach {
		if file, rest := attachment(c.text); file != c.file || rest != c.rest {
			t.Errorf("attachment(%q) = %q, %q; want %q, %q", c.text, file, rest, c.file, c.rest)
		}
	}

	omitted := []struct {
		text   string
		marked bool
		want   bool
	}{
		{"<Media omitted>", false, true},
		{"<Medien ausgeschlossen>", false, true},
		{"<Multimedia omitido>", false, true},
		{"image omitted", true, true},
		{"GIF omitted", false, true},
		{"Bild weggelassen", true, true},
		{"Report.pdf • 3 pages document omitted", true, true},
		{"the scene was omitted", false, false},
		{"<3>", false, false},
		{"<attached: a.jpg>", false, false},
		{"Hello", true, false},
	}
	for _, c := range omitted {
		if got := isMediaOmitted(c.text, c.marked); got != c.want {
			t.Errorf("isMediaOmitted(%q, %v) = %v, want %v", c.text, c.marked, got, c.want)
		}
	}
}
