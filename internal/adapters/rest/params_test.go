package rest_test

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	// ?tz= resolves against the IANA database; the server embeds it
	// (cmd/server imports time/tzdata) and so must this test binary, or the
	// zone cases would depend on the host having /usr/share/zoneinfo.
	_ "time/tzdata"

	"github.com/johnnycube/archivist/internal/adapters/rest"
	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// A filter the server cannot understand is a 400 that says which one — never a
// filter silently dropped (which would answer with MORE than was asked for).
func TestTimelineParameterValidation(t *testing.T) {
	h := rest.New(newApp(t), false).Handler()
	c := signup(t, h)

	for _, tc := range []struct {
		name, query string
		want        int
		mentions    string // in the error message
		histogram   bool   // the parameter applies to the histogram as well
	}{
		{"no filters", "", 200, "", true},
		{"what the SPA sends", "source=reddit,chatgpt&category=content,message&type=reddit.post&q=hello&from=2023-01-01&to=2023-12-31&limit=25", 200, "", false},
		{"date-only from", "from=2023-01-01", 200, "", true},
		{"date-only to", "to=2023-01-01", 200, "", true},
		{"RFC 3339 from", "from=" + url.QueryEscape("2023-01-01T10:00:00Z"), 200, "", true},
		{"RFC 3339 with offset", "to=" + url.QueryEscape("2023-01-01T10:00:00+02:00"), 200, "", true},
		{"RFC 3339 with fraction", "from=" + url.QueryEscape("2023-01-01T10:00:00.5Z"), 200, "", true},
		{"padded date", "from=%202023-01-01%20", 200, "", true},
		{"empty values", "from=&to=&category=&limit=", 200, "", true},
		{"every category", "category=content,message,reaction,save,connection,media,profile,consumption,other", 200, "", true},
		{"limit 0 = default", "limit=0", 200, "", false},
		{"limit above the maximum is clamped", "limit=100000", 200, "", false},

		{"from: not a date", "from=yesterday", 400, "from", true},
		{"from: impossible date", "from=2023-02-30", 400, "from", true},
		{"from: US order", "from=01/02/2023", 400, "from", true},
		{"from: unix time", "from=1672531200", 400, "from", true},
		{"from: local time without zone", "from=2023-01-01T10:00:00", 400, "from", true},
		{"to: not a date", "to=2023-13-01", 400, "to", true},
		{"category: unknown", "category=contents", 400, "contents", true},
		{"category: one bad among good", "category=content,nope", 400, "nope", true},
		{"category: wrong case", "category=Content", 400, "Content", true},
		{"limit: not a number", "limit=ten", 400, "limit", false},
		{"limit: negative", "limit=-1", 400, "limit", false},
		{"limit: fraction", "limit=2.5", 400, "limit", false},
		{"limit: overflow", "limit=99999999999999999999", 400, "limit", false},
		{"cursor: malformed", "cursor=%21%21", 400, "cursor", false},

		{"collapse=1", "collapse=1", 200, "", false},
		{"collapse=true", "collapse=true", 200, "", false},
		{"collapse with a gap", "collapse=1&collapse_gap=2h", 200, "", false},
		{"collapse with gap 0", "collapse=1&collapse_gap=0", 200, "", false},
		{"collapse with the largest gap", "collapse=1&collapse_gap=24h", 200, "", false},
		{"collapse with filters and a cursorless page", "collapse=1&source=reddit&category=consumption&limit=5", 200, "", false},
		{"collapse: empty is off", "collapse=", 200, "", false},
		{"collapse: yes", "collapse=yes", 400, "collapse", false},
		{"collapse: 0", "collapse=0", 400, "collapse", false},
		{"collapse: TRUE", "collapse=TRUE", 400, "collapse", false},
		{"collapse_gap: not a duration", "collapse=1&collapse_gap=30", 400, "collapse_gap", false},
		{"collapse_gap: negative", "collapse=1&collapse_gap=-5m", 400, "collapse_gap", false},
		{"collapse_gap: above 24h", "collapse=1&collapse_gap=25h", 400, "collapse_gap", false},
		{"collapse_gap: checked without collapse", "collapse_gap=never", 400, "collapse_gap", false},

		{"tz: an IANA zone", "tz=Europe/Berlin", 200, "", true},
		{"tz: the other hemisphere", "tz=Pacific/Auckland", 200, "", true},
		{"tz: a half-hour offset", "tz=Asia/Kolkata", 200, "", true},
		{"tz: UTC", "tz=UTC", 200, "", true},
		{"tz: empty is UTC", "tz=", 200, "", true},
		{"tz: padded", "tz=%20Europe/Berlin%20", 200, "", true},
		{"tz: with a date-only range", "from=2024-03-01&to=2024-03-31&tz=Europe/Berlin", 200, "", true},
		{"tz: unknown zone", "tz=Europe/Berlim", 400, "tz", true},
		{"tz: an offset, not a name", "tz=%2B02:00", 400, "tz", true},
		{"tz: junk", "tz=%C2%A1%21%3F", 400, "tz", true},
		{"tz: path traversal", "tz=../../etc/passwd", 400, "tz", true},
		{"tz: absolute path", "tz=/etc/localtime", 400, "tz", true},
		{"tz: the server's own zone is not the viewer's", "tz=Local", 400, "tz", true},
		{"tz: a control character", "tz=Europe/Berlin%00", 400, "tz", true},
		{"tz: a newline", "tz=Europe%0ABerlin", 400, "tz", true},
		{"tz: overlong", "tz=" + strings.Repeat("A", 4096), 400, "tz", true},
		{"tz: two zones", "tz=Europe/Berlin,UTC", 400, "tz", true},

		{"q: a NUL byte", "q=a%00b", 400, "q", true},
		{"q: invalid UTF-8", "q=%ff%fe", 400, "q", true},
		{"q: overlong", "q=" + strings.Repeat("word+", 200), 400, "q", true},
		{"q: unicode words", "q=" + url.QueryEscape("Müller 東京 🙂"), 200, "", true},
		{"source: invalid UTF-8", "source=%ff", 400, "source", true},
		{"type: a NUL byte", "type=reddit.post%00", 400, "type", true},
		{"cursor: a NUL byte", "cursor=%00", 400, "cursor", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := []string{"/api/events"}
			if tc.histogram {
				paths = append(paths, "/api/events/histogram")
			}
			for _, path := range paths {
				rec := request(h, "GET", path+"?"+tc.query, "", nil, c)
				if rec.Code != tc.want {
					t.Errorf("GET %s?%s = %d, want %d: %s", path, tc.query, rec.Code, tc.want, rec.Body.String())
				}
				if tc.want == http.StatusBadRequest && !strings.Contains(rec.Body.String(), tc.mentions) {
					t.Errorf("GET %s?%s: error %s does not mention %q", path, tc.query, rec.Body.String(), tc.mentions)
				}
			}
		})
	}
}

// A date-only `to` still means "through the end of that day".
func TestDateOnlyUpperBoundIsInclusive(t *testing.T) {
	a := newApp(t)
	h := rest.New(a, false).Handler()
	c := signup(t, h)
	if rec := upload(t, h, c, "reddit", zipBytes(t)); rec.Code != http.StatusAccepted {
		t.Fatalf("import = %d", rec.Code)
	}
	drain(t, a)
	// The fixture has events on 2023-01-01 and 2023-01-02 (midnight UTC), plus
	// an undated vote that any date filter excludes.
	for query, want := range map[string]float64{
		"from=2023-01-01&to=2023-01-02":                 2,
		"from=2023-01-02&to=2023-01-02":                 1,
		"to=2023-01-01":                                 1,
		"from=2023-01-03":                               0,
		"to=" + url.QueryEscape("2023-01-01T23:59:59Z"): 1,
	} {
		if got := getJSON(t, h, c, "/api/events?"+query)["total"]; got != want {
			t.Errorf("?%s: total = %v, want %v", query, got, want)
		}
	}
}

// ?tz= is the viewer's zone: it decides what a date-only from/to MEANS and
// which day an event is bucketed under. The events below straddle midnight in
// Berlin on the night the clocks went forward (2024-03-31T22:00Z is
// 2024-04-01T00:00+02:00), which is the case that used to print an "01 APR"
// card inside a March filter.
func TestTimeZoneShiftsDaysAndRanges(t *testing.T) {
	deps := newDeps(t)
	a := app.New(deps)
	h := rest.New(a, false).Handler()
	c := signup(t, h)

	at := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	ev := func(ext, when string) domain.Event {
		return domain.Event{
			SourceID: "reddit", ExternalID: ext, Type: "reddit.post",
			Category: domain.CategoryContent, OccurredAt: at(when), Title: ext,
		}
	}
	seedEvents(t, h, c, deps.Events,
		ev("feb29", "2024-02-29T12:00:00Z"),           // Berlin: 29 Feb 13:00
		ev("mar01-early", "2024-02-29T23:30:00Z"),     // Berlin: 1 Mar 00:30 — in March only there
		ev("mar31-late", "2024-03-31T21:30:00Z"),      // Berlin: 31 Mar 23:30 — still March
		ev("apr01-utc-march", "2024-03-31T22:10:00Z"), // Berlin: 1 Apr 00:10 — April there, March in UTC
		ev("apr02", "2024-04-02T09:00:00Z"),
	)

	// A date-only range is a range of LOCAL days. March in Berlin starts and
	// ends an hour (or two) before March in UTC, so the two zones disagree by
	// exactly the boundary events — and by nothing else.
	for _, tc := range []struct {
		tz   string
		want []string // newest first
	}{
		{"", []string{"apr01-utc-march", "mar31-late"}},
		{"UTC", []string{"apr01-utc-march", "mar31-late"}},
		{"Europe/Berlin", []string{"mar31-late", "mar01-early"}},
	} {
		name := tc.tz
		if name == "" {
			name = "no tz"
		}
		t.Run("march range, "+name, func(t *testing.T) {
			qs := "from=2024-03-01&to=2024-03-31"
			if tc.tz != "" {
				qs += "&tz=" + url.QueryEscape(tc.tz)
			}
			page := getJSON(t, h, c, "/api/events?"+qs)
			var got []string
			for _, e := range page["events"].([]any) {
				got = append(got, e.(map[string]any)["external_id"].(string))
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("?%s = %v, want %v", qs, got, tc.want)
			}
			if page["total"] != float64(len(tc.want)) {
				t.Errorf("?%s: total = %v, want %d", qs, page["total"], len(tc.want))
			}
		})
	}

	// The histogram's day keys are local days too, so the navigator's chips
	// describe the same days the list shows.
	for _, tc := range []struct {
		tz   string
		want map[string]float64
	}{
		{"", map[string]float64{"2024-02-29": 2, "2024-03-31": 2, "2024-04-02": 1}},
		{"Europe/Berlin", map[string]float64{"2024-02-29": 1, "2024-03-01": 1, "2024-03-31": 1, "2024-04-01": 1, "2024-04-02": 1}},
		{"Pacific/Auckland", map[string]float64{"2024-03-01": 2, "2024-04-01": 2, "2024-04-02": 1}},
	} {
		name := tc.tz
		if name == "" {
			name = "no tz"
		}
		t.Run("histogram, "+name, func(t *testing.T) {
			qs := ""
			if tc.tz != "" {
				qs = "?tz=" + url.QueryEscape(tc.tz)
			}
			got := map[string]float64{}
			for _, b := range getJSON(t, h, c, "/api/events/histogram"+qs)["buckets"].([]any) {
				m := b.(map[string]any)
				n := 0.0
				for _, v := range m["by_source"].(map[string]any) {
					n += v.(float64)
				}
				got[m["day"].(string)] = n
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("histogram%s = %v, want %v", qs, got, tc.want)
			}
		})
	}

	// A full RFC 3339 bound carries its own offset and is NOT re-read in tz.
	for _, qs := range []string{
		"from=" + url.QueryEscape("2024-03-31T22:00:00Z") + "&tz=Europe/Berlin",
		"from=" + url.QueryEscape("2024-04-01T00:00:00+02:00") + "&tz=UTC",
	} {
		if got := getJSON(t, h, c, "/api/events?"+qs)["total"]; got != float64(2) {
			t.Errorf("?%s: total = %v, want 2 (the same instant, whatever tz says)", qs, got)
		}
	}
}

// ?by= selects the histogram's key; anything but source/category is a 400.
func TestHistogramByValidation(t *testing.T) {
	h := rest.New(newApp(t), false).Handler()
	c := signup(t, h)
	for query, want := range map[string]int{
		"": 200, "by=source": 200, "by=category": 200, "by=": 200,
		"by=type": 400, "by=Category": 400, "by=source,category": 400,
	} {
		rec := request(h, "GET", "/api/events/histogram?"+query, "", nil, c)
		if rec.Code != want {
			t.Errorf("GET /api/events/histogram?%s = %d, want %d: %s", query, rec.Code, want, rec.Body.String())
		}
		if want == 400 && !strings.Contains(rec.Body.String(), "by") {
			t.Errorf("?%s: error %s does not mention by", query, rec.Body.String())
		}
	}
}
