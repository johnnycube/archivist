package whatsapp

import (
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Day/month orders a transcript can be written in.
const (
	orderDMY = "dmy"
	orderMDY = "mdy"
	orderYMD = "ymd"

	basisChronology = "chronology"
	basisSeparator  = "separator"
)

// Header shapes, by platform. The set is the union of what the two widely
// used open-source parsers accept — whatsapp-chat-parser's sharedRegex and the
// 96 formats of whatstk's header_format_support.json:
//
//	iOS      [15.04.22, 14:03:21] Jane Doe: Hello     [4/15/22, 2:03:21 PM] …
//	         [15.04.22 14:03:21] Jane Doe: Hello      (no comma)
//	Android  15.04.22, 14:03 - Jane Doe: Hello        4/15/22, 2:03 PM - …
//	         13.06.18 21.25.15: Jane: Hello            (colon after the time)
//	         13.6.2018 klo 21.25.15 - Jane: Hello      (a word between date and time: fi "klo")
//	         13. 6. 2018. 21:25:15 Jane: Hello         (hr/sr/cs: spaces and a trailing dot)
//
// sp covers the no-break spaces newer exports put before AM/PM; time
// separators may be ":" or "."; AM/PM may be written "PM", "p.m." or "p. m.".
const (
	sp     = `[ \t\x{00A0}\x{202F}\x{2009}]`
	dateRe = `(?P<a>\d{1,4})(?P<s1>[./-])` + sp + `?(?P<b>\d{1,2})(?P<s2>[./-])` + sp + `?(?P<c>\d{1,4})(?P<punct>[,.])?`
	// A short word or two between date and time ("klo", "um", "à", "a las").
	fillerRe = `(?:(?P<filler>\pL{1,3}(?:` + sp + `\pL{1,3})?\.?)` + sp + `+)?`
	timeRe   = `(?P<h>\d{1,2})[:.](?P<mi>\d{2})(?:[:.](?P<sec>\d{2}))?(?:` + sp + `*(?P<ap>[AaPp])\.?` + sp + `?[Mm]\.?)?`
	headerRe = `(?P<hdr>` + dateRe + sp + `+` + fillerRe + timeRe + `)`
)

var (
	bracketRe = regexp.MustCompile(`^\[` + sp + `?` + headerRe + sp + `?\]` + sp + `*(?P<rest>.*)$`)
	dashRe    = regexp.MustCompile(`^` + headerRe + `(?:` + sp + `+[-\x{2013}\x{2014}]|:)(?:` + sp + `(?P<rest>.*))?$`)
	// Neither bracket nor dash: only the "13. 6. 2018. 21:25:15 Name: text" form,
	// recognizable by the dot that closes the date.
	plainRe = regexp.MustCompile(`^` + headerRe + sp + `+(?P<rest>.*)$`)
)

// Layouts for time.Parse. A header is rewritten to "a/b/c h:m[:s][ PM]" first,
// so separators and AM/PM spellings do not multiply the table. time.Parse also
// does the validation: a month 15 or an April 31st simply does not parse.
var (
	dateLayouts = map[string]map[int]string{ // order → digits of the year → layout
		orderDMY: {2: "2/1/06", 4: "2/1/2006"},
		orderMDY: {2: "1/2/06", 4: "1/2/2006"},
		orderYMD: {4: "2006/1/2"},
	}
	timeLayouts = map[[2]bool]string{ // {seconds, AM/PM} → layout
		{false, false}: "15:04",
		{true, false}:  "15:04:05",
		{false, true}:  "3:04 PM",
		{true, true}:   "3:04:05 PM",
	}
)

// header is one parsed timestamp header. Both day/month readings are kept
// because which one applies is decided per file, not per line.
type header struct {
	raw     string // date and time exactly as written
	bracket bool   // iOS style
	sep     string
	ampm    bool
	rest    string // the line after the header

	times map[string]time.Time // order → timestamp, for every order that parses
}

// parseHeader reports whether line opens a new transcript entry.
func parseHeader(line string) (header, bool) {
	line = strings.TrimLeftFunc(line, isMark)
	re, bracket := bracketRe, true
	m := re.FindStringSubmatch(line)
	if m == nil {
		re, bracket = dashRe, false
		m = re.FindStringSubmatch(line)
	}
	if m == nil {
		re = plainRe
		if m = re.FindStringSubmatch(line); m != nil && m[re.SubexpIndex("punct")] != "." {
			m = nil
		}
	}
	if m == nil {
		return header{}, false
	}
	g := func(name string) string { return m[re.SubexpIndex(name)] }
	if g("s1") != g("s2") {
		return header{}, false
	}
	a, b, c, ampm := g("a"), g("b"), g("c"), strings.ToUpper(g("ap"))
	h := header{raw: g("hdr"), bracket: bracket, sep: g("s1"), ampm: ampm != "", rest: g("rest"), times: map[string]time.Time{}}

	clock := g("h") + ":" + g("mi")
	if g("sec") != "" {
		clock += ":" + g("sec")
	}
	if h.ampm {
		clock += " " + ampm + "M"
	}
	clockLayout := timeLayouts[[2]bool{g("sec") != "", h.ampm}]

	orders, yearDigits := []string{orderDMY, orderMDY}, len(c)
	if len(a) > 2 {
		orders, yearDigits = []string{orderYMD}, len(a)
	}
	for _, o := range orders {
		layout, ok := dateLayouts[o][yearDigits]
		if !ok {
			continue
		}
		if t, err := time.Parse(layout+" "+clockLayout, a+"/"+b+"/"+c+" "+clock); err == nil {
			h.times[o] = t // time.Parse without a zone yields UTC
		}
	}
	return h, len(h.times) > 0
}

// order is the header's day/month order when it has only one valid reading
// ("15.04.22", "2022-04-15"), or "" when it is ambiguous ("04.05.22").
func (h header) order() string {
	if len(h.times) != 1 {
		return ""
	}
	for o := range h.times {
		return o
	}
	return ""
}

// time reads the header in the given order, falling back to its only valid
// reading when the line contradicts the file's order.
func (h header) time(order string) time.Time {
	if t, ok := h.times[order]; ok {
		return t
	}
	for _, o := range []string{orderDMY, orderMDY, orderYMD} {
		if t, ok := h.times[o]; ok {
			return t
		}
	}
	return time.Time{}
}

func (h header) format() string {
	if h.bracket {
		return "ios"
	}
	return "android"
}

// guessOrder picks the day/month order of a file in which no header settled
// it. Transcripts are chronological, so the reading whose timestamps go
// backwards less often wins; on a tie the separator decides.
func guessOrder(hs []header) (order, basis string) {
	backwards := func(o string) (n int) {
		for i := 1; i < len(hs); i++ {
			if hs[i].time(o).Before(hs[i-1].time(o)) {
				n++
			}
		}
		return n
	}
	switch d, m := backwards(orderDMY), backwards(orderMDY); {
	case d < m:
		return orderDMY, basisChronology
	case m < d:
		return orderMDY, basisChronology
	}
	return separatorOrder(hs), basisSeparator
}

// separatorOrder is the last resort: "04.05.22" is day-first everywhere,
// "4/5/22" with AM/PM is the US month-first form, anything else day-first.
func separatorOrder(hs []header) string {
	for _, h := range hs {
		if h.sep == "/" && h.ampm {
			return orderMDY
		}
	}
	return orderDMY
}

// splitSender splits "Jane Doe: Hello" into sender and text. Entries without a
// "Sender: " part are system lines and come back with an empty sender.
func splitSender(rest string) (sender string, text []byte) {
	name, body, ok := strings.Cut(rest, ": ")
	if !ok {
		if n, found := strings.CutSuffix(rest, ":"); found { // empty message
			name, body, ok = n, "", true
		}
	}
	name = strings.TrimSpace(stripMarks(name))
	if !ok || name == "" || len(name) > 120 {
		return "", []byte(rest)
	}
	return name, []byte(body)
}

// isMark reports the invisible characters WhatsApp sprinkles into exports: the
// BOM and the bidi controls (LRM before attachments, embeddings around phone
// numbers). Joiners are not marks — emoji sequences need them.
func isMark(r rune) bool {
	return r == '\uFEFF' || unicode.Is(unicode.Bidi_Control, r)
}

func stripMarks(s string) string {
	return strings.Map(func(r rune) rune {
		if isMark(r) {
			return -1
		}
		return r
	}, s)
}
