package google

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

// ---- Chrome/History.json ----

// chromeHistoryEx streams Chrome's synced browsing history:
// {"Browser History": [{title, url, time_usec, client_id, page_transition}]}.
// Older exports name the file BrowserHistory.json.
type chromeHistoryEx struct{}

var chromeHistoryStems = []string{"history", "browserhistory", "verlauf", "historique", "historial"}

func (chromeHistoryEx) Match(member string) bool {
	return parentContains(member, "chrome") && contains(chromeHistoryStems, stem(member, ".json"))
}

func (chromeHistoryEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	return bundle.StreamJSONArray(r, []string{"Browser History"}, func(el json.RawMessage) error {
		return emit(rec("chrome_history", el))
	}, bundle.ReportSkipped(member, emit))
}

type chromeVisit struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	TimeUsec   flex   `json:"time_usec"`
	Transition string `json:"page_transition"`
	ClientID   string `json:"client_id"`
}

func normalizeChromeHistory(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var v chromeVisit
	if err := json.Unmarshal(raw.Payload, &v); err != nil {
		return nil, err
	}
	if v.URL == "" {
		return one(unknown(base, raw, v.Title, nil)), nil
	}
	ev := base
	ev.Type, ev.Category = "google.chrome.visit", domain.CategoryConsumption
	ev.OccurredAt = epoch(v.TimeUsec, time.Microsecond)
	ev.Title = firstNonEmpty(v.Title, v.URL)
	meta(ev, "url", v.URL)
	meta(ev, "transition", v.Transition)
	meta(ev, "client_id", v.ClientID)
	ev.Metadata["origin"] = "chrome_history"
	ev.ExternalID = "g.chrome.visit:" + when(ev.OccurredAt, v.TimeUsec.String()) + ":" + short(v.URL)
	return one(ev), nil
}

// ---- Chrome/Bookmarks.html ----

// bookmarksEx reads the Netscape bookmark file Chrome exports. The stdlib has
// no HTML parser (golang.org/x/net/html is not a dependency), and the file is
// mechanical — one <DT><A HREF="…" ADD_DATE="…">title</A> per line, folders as
// <DT><H3 …>name</H3> followed by a <DL>…</DL> block — so a tolerant regexp over
// each line is used instead. Lines longer than maxBookmarkLine fail the member.
type bookmarksEx struct{}

var bookmarkStems = []string{"bookmarks", "lesezeichen", "favoris", "marcadores", "segnalibri"}

func (bookmarksEx) Match(member string) bool {
	s := firstNonEmpty(stem(member, ".html"), stem(member, ".htm"))
	return parentContains(member, "chrome") && hasAnyPrefix(s, bookmarkStems)
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

const maxBookmarkLine = 1 << 20

var (
	bookmarkLinkRe   = regexp.MustCompile(`(?i)<A\s+([^>]*)>(.*?)</A>`)
	bookmarkFolderRe = regexp.MustCompile(`(?i)<H3\b[^>]*>(.*?)</H3>`)
	bookmarkAttrRe   = regexp.MustCompile(`(?i)([A-Z_]+)="([^"]*)"`)
	bookmarkDLOpen   = regexp.MustCompile(`(?i)<DL\b`)
	bookmarkDLClose  = regexp.MustCompile(`(?i)</DL>`)
)

type bookmark struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	AddDate  string `json:"add_date"`
	LastMod  string `json:"last_modified"`
	Icon     bool   `json:"icon,omitempty"`
	Folder   string `json:"folder"`
	Position int    `json:"position"`
}

func (bookmarksEx) Extract(_ string, r io.Reader, emit func(provider.RawRecord) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxBookmarkLine)
	var folders []string
	pending := "" // folder named by the last <H3>, pushed when its <DL> opens
	n := 0
	for sc.Scan() {
		line := sc.Text()
		if m := bookmarkFolderRe.FindStringSubmatch(line); m != nil {
			pending = html.UnescapeString(strings.TrimSpace(m[1]))
		}
		if bookmarkDLOpen.MatchString(line) {
			folders = append(folders, pending)
			pending = ""
		}
		for _, m := range bookmarkLinkRe.FindAllStringSubmatch(line, -1) {
			b := bookmark{Title: html.UnescapeString(strings.TrimSpace(m[2])), Folder: folderPath(folders), Position: n}
			for _, a := range bookmarkAttrRe.FindAllStringSubmatch(m[1], -1) {
				switch strings.ToUpper(a[1]) {
				case "HREF":
					b.URL = html.UnescapeString(a[2])
				case "ADD_DATE":
					b.AddDate = a[2]
				case "LAST_MODIFIED":
					b.LastMod = a[2]
				case "ICON":
					b.Icon = true
				}
			}
			n++
			payload, err := json.Marshal(b)
			if err != nil {
				return err
			}
			if err := emit(rec("chrome_bookmark", payload)); err != nil {
				return err
			}
		}
		for range bookmarkDLClose.FindAllStringIndex(line, -1) {
			if len(folders) > 0 {
				folders = folders[:len(folders)-1]
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read bookmarks: %w", err)
	}
	return nil
}

// folderPath joins the folder stack, dropping the root <DL> (which has no <H3>)
// and any other unnamed level.
func folderPath(folders []string) string {
	var named []string
	for _, f := range folders {
		if f != "" {
			named = append(named, f)
		}
	}
	return strings.Join(named, "/")
}

func normalizeBookmark(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var b bookmark
	if err := json.Unmarshal(raw.Payload, &b); err != nil {
		return nil, err
	}
	if b.URL == "" {
		return one(unknown(base, raw, b.Title, nil)), nil
	}
	ev := base
	ev.Type, ev.Category = "google.chrome.bookmark", domain.CategorySave
	ev.OccurredAt = epoch(flex(b.AddDate), time.Second)
	ev.Title = "Bookmarked " + firstNonEmpty(b.Title, b.URL)
	meta(ev, "url", b.URL)
	meta(ev, "name", b.Title)
	meta(ev, "folder", b.Folder)
	if t := epoch(flex(b.LastMod), time.Second); !t.IsZero() {
		meta(ev, "modified", t.Format(time.RFC3339))
	}
	ev.Targets = tgt(domain.RelationSaved, short(b.URL), b.URL)
	// A URL bookmarked twice in different folders is two bookmarks.
	ev.ExternalID = "g.chrome.bookmark:" + short(b.Folder+"\x00"+b.URL)
	return one(ev), nil
}
