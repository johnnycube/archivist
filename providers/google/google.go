// Package google ingests a Google Takeout export: one archive that can hold
// dozens of products. The parts mapped to events are
//
//   - YouTube ("YouTube and YouTube Music/", older "YouTube/"): watch and
//     search history, comments, subscriptions, playlist items and likes
//     (google.youtube.*);
//   - My Activity ("My Activity/<Product>/MyActivity.json") for EVERY product:
//     one record schema shared by all of them, mapped by product — Search,
//     Chrome, Maps, Play Store, Assistant, Gemini, Discover/News, Ads,
//     Shopping, Books, Image/Video Search — and google.activity for products
//     without a dedicated type (activity.go);
//   - Chrome: History.json (google.chrome.visit) and Bookmarks.html
//     (google.chrome.bookmark), see chrome.go;
//   - Location: Semantic Location History (google.location.visit / .trip),
//     Records.json (google.location.point) and the newer on-device
//     Timeline.json / location-history.json, see location.go;
//   - Maps: Saved Places.json, Labeled places.json, Saved/*.csv lists
//     (google.maps.saved) and Reviews.json (google.maps.review), see maps.go;
//   - Keep notes (google.keep.note), see keep.go;
//   - Google Chat (google.chat.message) and classic Hangouts
//     (google.hangouts.message), see chat.go;
//   - Google Photos sidecar JSON (google.photos.photo), see photos.go.
//
// Everything else — Gmail (mbox), Calendar (ics), Contacts (vcf), Drive files,
// Fit, Pay, Tasks, … — is left to the importer's generic capture and surfaces
// as google.unknown, one event per member, never dropped.
//
// # Choose JSON in Takeout
//
// Takeout writes My Activity and the YouTube history as HTML BY DEFAULT
// (MyActivity.html, history/watch-history.html). This provider does not parse
// HTML: such members become a single google.unknown event each. When creating
// the export choose "My Activity → Multiple formats → Activity records: JSON"
// and "YouTube and YouTube Music → Multiple formats → History: JSON".
//
// # Volume
//
// Location History/Records.json holds every raw location fix (millions of
// records over the years) and each becomes a google.location.point event; the
// same goes for the rawSignals in Timeline.json. Both are streamed record by
// record, so memory stays flat, but the resulting event count is very large.
// Chrome's History.json and My Activity/Chrome describe the same visits; both
// are mapped (their timestamps differ in precision, so they do not dedupe).
//
// # Localization
//
// Takeout localizes folder names, file names, CSV headers and the My Activity
// sentences to the account language ("YouTube und YouTube Music",
// "Meine Aktivitäten/Google Suche/MeineAktivitäten.json", "Dune angesehen").
// Matching is therefore defensive, but necessarily incomplete:
//
//   - members are matched by base name or a directory on their path against
//     small alias lists (English, German, some French/Spanish/Italian; only
//     English and German were checked against real exports). A language not
//     listed stays google.unknown until its names are added;
//   - a member belongs to YouTube when any directory on its path contains
//     "youtube"; My Activity records name their product in header/products[],
//     which is matched against productAliases together with the folder name
//     and the URL host;
//   - records are classified by content where possible: the titleUrl says
//     whether an activity is a search (q=/search_query=) or a video (watch?v=),
//     and the search query is read from that URL. The sentence frames
//     ("Searched for X", "Visited X", "X aufgerufen") are only stripped for the
//     languages in verbFrames; otherwise the Title keeps the export's sentence
//     and metadata.raw_title marks it;
//   - CSV columns are found by a case-insensitive "header contains" lookup on the
//     English names and fall back to recognizing values (a channel id, a video
//     id, a timestamp, a URL, a JSON text blob) when the headers are localized;
//   - Google Chat's created_date is an English sentence ("Thursday, July 14,
//     2016 at 5:04:34 PM UTC"); in other languages the message stays undated
//     with the raw string in metadata.
//
// Rows and records that cannot be understood are emitted as google.unknown
// rather than dropped, and arrays a JSON member holds under names this
// provider does not stream are reported as google.unknown too
// (bundle.ReportSkipped).
package google

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

const sourceID = "google"

type Provider struct{}

func New() *Provider { return &Provider{} }

func (p *Provider) Descriptor() domain.Descriptor {
	return domain.Descriptor{
		ID:             sourceID,
		DisplayName:    "Google Takeout",
		IngestionModes: []domain.Mode{domain.ModeBundle},
		EventTypes: []domain.TypeDef{
			// YouTube
			{Type: "google.youtube.watch", Category: domain.CategoryConsumption},
			{Type: "google.youtube.search", Category: domain.CategoryConsumption, HasBody: true},
			{Type: "google.youtube.comment", Category: domain.CategoryContent, HasBody: true},
			{Type: "google.youtube.subscribe", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "google.youtube.playlist_item", Category: domain.CategorySave, NeedsTarget: true},
			{Type: "google.youtube.like", Category: domain.CategoryReaction, NeedsTarget: true},
			// My Activity, by product
			{Type: "google.search", Category: domain.CategoryConsumption, HasBody: true},
			{Type: "google.visit", Category: domain.CategoryConsumption},
			{Type: "google.chrome.search", Category: domain.CategoryConsumption, HasBody: true},
			{Type: "google.chrome.visit", Category: domain.CategoryConsumption},
			{Type: "google.maps.activity", Category: domain.CategoryConsumption, HasBody: true},
			{Type: "google.play.activity", Category: domain.CategoryConsumption},
			{Type: "google.assistant.query", Category: domain.CategoryMessage, HasBody: true},
			{Type: "google.gemini.prompt", Category: domain.CategoryMessage, HasBody: true},
			{Type: "google.discover.view", Category: domain.CategoryConsumption},
			{Type: "google.ads.view", Category: domain.CategoryOther},
			{Type: "google.shopping.activity", Category: domain.CategoryConsumption, HasBody: true},
			{Type: "google.books.activity", Category: domain.CategoryConsumption},
			{Type: "google.activity", Category: domain.CategoryOther},
			// Chrome
			{Type: "google.chrome.bookmark", Category: domain.CategorySave},
			// Location History / Timeline
			{Type: "google.location.visit", Category: domain.CategoryOther},
			{Type: "google.location.trip", Category: domain.CategoryOther},
			{Type: "google.location.point", Category: domain.CategoryOther},
			// Maps
			{Type: "google.maps.saved", Category: domain.CategorySave},
			{Type: "google.maps.review", Category: domain.CategoryContent, HasBody: true},
			// Keep
			{Type: "google.keep.note", Category: domain.CategoryContent, HasBody: true},
			// Chat / Hangouts
			{Type: "google.chat.message", Category: domain.CategoryMessage, HasBody: true},
			{Type: "google.hangouts.message", Category: domain.CategoryMessage, HasBody: true},
			// Photos
			{Type: "google.photos.photo", Category: domain.CategoryMedia, HasBody: true},
			{Type: "google.unknown", Category: domain.CategoryOther},
		},
	}
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{SupportsBundle: true}
}

// IsAttachment implements provider.AttachmentMatcher: files sent in Google Chat
// spaces (PDFs, documents) sit next to messages.json and are pointed at by
// attached_files, so they are stored as blobs rather than captured as
// google.unknown. Data files never are.
func (p *Provider) IsAttachment(member string) bool {
	switch strings.ToLower(path.Ext(member)) {
	case ".json", ".html", ".htm", ".csv", ".txt":
		return false
	}
	return inChatGroup(member)
}

// ---- member matching ----

// Localized names, lower case. Files are listed by stem (no extension).
var (
	historyDirs      = []string{"history", "verlauf", "historique", "historial"}
	watchHistoryStem = []string{"watch-history", "wiedergabeverlauf"}
	searchHistory    = []string{"search-history", "suchverlauf"}
	commentStems     = []string{"comments", "kommentare", "commentaires", "comentarios"}
	subscriptionStem = []string{"subscriptions", "abos", "abonnements", "suscripciones"}
	playlistDirs     = []string{"playlists", "playlist"}
	playlistIndex    = []string{"playlists"} // playlists.csv lists the playlists, not their items
	likedPlaylists   = []string{"liked videos", "liked music", "likes", "positiv bewertete videos", "videos, die ich mag"}
)

// Extractors maps each known member to a provider-local Kind. Unlisted files
// (HTML history, Gmail, Drive, …) are captured generically by the importer.
// Order matters where matchers overlap: the first match wins.
func (p *Provider) Extractors() []provider.Extractor {
	return []provider.Extractor{
		activityEx{},
		chromeHistoryEx{},
		bookmarksEx{},
		semanticLocationEx{},
		recordsEx{},
		timelineEx{},
		mapsFeaturesEx{},
		savedListEx{},
		keepEx{},
		chatEx{},
		hangoutsEx{},
		photoMetaEx{},
		csvEx{kind: "comment", match: func(m string) bool { return inYouTube(m) && stemHasPrefix(m, ".csv", commentStems) }},
		csvEx{kind: "subscription", match: func(m string) bool { return inYouTube(m) && stemHasPrefix(m, ".csv", subscriptionStem) }},
		playlistEx{},
	}
}

// inYouTube reports whether member lies below a directory whose name mentions
// YouTube ("YouTube and YouTube Music", "YouTube und YouTube Music", "YouTube").
func inYouTube(member string) bool {
	return dirContains(member, "youtube")
}

// dirContains reports whether some directory on member's path contains needle
// (lower case).
func dirContains(member, needle string) bool {
	return strings.Contains(strings.ToLower(path.Dir(member)), needle)
}

// anyDirIs reports whether some directory on member's path is (lower-cased)
// one of names.
func anyDirIs(member string, names []string) bool {
	for _, d := range strings.Split(strings.ToLower(path.Dir(member)), "/") {
		if contains(names, d) {
			return true
		}
	}
	return false
}

func parentIs(member string, dirs []string) bool {
	return contains(dirs, strings.ToLower(path.Base(path.Dir(member))))
}

// parentContains reports whether the member's parent directory name contains
// needle (lower case).
func parentContains(member, needle string) bool {
	return strings.Contains(strings.ToLower(path.Base(path.Dir(member))), needle)
}

// stem returns the lower-cased base name without ext, or "" if member does not
// end in ext.
func stem(member, ext string) string {
	b := strings.ToLower(path.Base(member))
	if !strings.HasSuffix(b, ext) {
		return ""
	}
	return strings.TrimSuffix(b, ext)
}

// stemHasPrefix also accepts split or renamed copies ("comments(1).csv").
func stemHasPrefix(member, ext string, stems []string) bool {
	s := stem(member, ext)
	for _, want := range stems {
		if s != "" && strings.HasPrefix(s, want) {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ---- extraction ----

// rec hashes the kind into the dedupe key, so equal payloads from members with
// different meanings never collapse into one record.
func rec(kind string, payload []byte) provider.RawRecord {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(payload)
	return provider.RawRecord{Kind: kind, Payload: payload, ContentHash: hex.EncodeToString(h.Sum(nil))}
}

// Localized names of the "My Activity" folder and of the MyActivity.json file
// (stem, lower case). Only English and German are verified against real
// exports; the rest are best-effort.
var (
	myActivityDirs  = []string{"my activity", "meine aktivitäten", "mon activité", "mi actividad", "le mie attività", "mijn activiteit", "minha atividade", "a minha atividade"}
	myActivityStems = []string{"myactivity", "meineaktivitäten", "monactivité", "miactividad", "lemieattività", "mijnactiviteit", "minhaatividade"}
)

// activityEx streams a My Activity JSON history (a top-level array, easily
// 100+ MB): the YouTube history files and every "My Activity/<Product>/
// MyActivity.json". Records are told apart during normalization.
type activityEx struct{}

func (activityEx) Match(member string) bool {
	if stem(member, ".json") == "" {
		return false
	}
	if inYouTube(member) && (parentIs(member, historyDirs) || historyHint(member) != "") {
		return true
	}
	return anyDirIs(member, myActivityDirs) || stemHasPrefix(member, ".json", myActivityStems)
}

func (activityEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	return bundle.StreamJSONArray(r, nil, func(el json.RawMessage) error {
		return emit(rec("activity", el))
	}, bundle.ReportSkipped(member, emit))
}

// historyHint tells from the file name what its records are, for records that
// carry no URL to classify them by.
func historyHint(member string) string {
	switch {
	case stemHasPrefix(member, ".json", watchHistoryStem):
		return "watch"
	case stemHasPrefix(member, ".json", searchHistory):
		return "search"
	}
	return ""
}

// csvEx emits every CSV row as a JSON object of header → value.
type csvEx struct {
	kind  string
	match func(member string) bool
}

func (e csvEx) Match(member string) bool { return e.match(member) }

func (e csvEx) Extract(_ string, r io.Reader, emit func(provider.RawRecord) error) error {
	return bundle.ReadCSV(r, func(row map[string]string) error {
		payload, err := json.Marshal(row)
		if err != nil {
			return err
		}
		return emit(rec(e.kind, payload))
	})
}

// playlistEx reads one playlist's items (playlists/<name>-videos.csv). A row
// does not say which playlist it belongs to, so each is emitted together with
// the playlist name taken from the file name; that also keeps the same video in
// two playlists from deduplicating into one record.
type playlistEx struct{}

func (playlistEx) Match(member string) bool {
	s := stem(member, ".csv")
	return inYouTube(member) && parentIs(member, playlistDirs) && s != "" && !contains(playlistIndex, s)
}

func (playlistEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	name := playlistName(member)
	return bundle.ReadCSV(r, func(row map[string]string) error {
		payload, err := json.Marshal(playlistRow{Playlist: name, Row: row})
		if err != nil {
			return err
		}
		return emit(rec("playlist_item", payload))
	})
}

type playlistRow struct {
	Playlist string            `json:"playlist"`
	Row      map[string]string `json:"row"`
}

// playlistName turns …/playlists/Watch later-videos.csv into "Watch later".
func playlistName(member string) string {
	name := path.Base(member)
	name = name[:len(name)-len(path.Ext(name))]
	if i := len(name) - len("-videos"); i > 0 && strings.EqualFold(name[i:], "-videos") {
		name = name[:i]
	}
	return strings.TrimSpace(name)
}

// ---- normalization ----

// Normalize never fails the import over one record: a payload that does not
// decode as its kind expects becomes a google.unknown event carrying the error,
// so the rest of the archive still imports and the record stays visible.
func (p *Provider) Normalize(_ context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	base := domain.Event{SourceID: sourceID, Actor: "self", Metadata: map[string]any{}}
	evs, err := normalize(base, raw)
	if err != nil {
		ev := unknown(base, raw, "", nil)
		meta(ev, "error", err.Error())
		return one(ev), nil
	}
	return evs, nil
}

func normalize(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	switch raw.Kind {
	case "activity":
		return normalizeActivity(base, raw)
	case "comment":
		return normalizeComment(base, raw)
	case "subscription":
		return normalizeSubscription(base, raw)
	case "playlist_item":
		return normalizePlaylistItem(base, raw)
	case "chrome_history":
		return normalizeChromeHistory(base, raw)
	case "chrome_bookmark":
		return normalizeBookmark(base, raw)
	case "timeline_object":
		return normalizeTimelineObject(base, raw)
	case "location":
		return normalizeLocationRecord(base, raw)
	case "semantic_segment":
		return normalizeSemanticSegment(base, raw)
	case "raw_signal":
		return normalizeRawSignal(base, raw)
	case "maps_feature":
		return normalizeMapsFeature(base, raw)
	case "saved_list":
		return normalizeSavedList(base, raw)
	case "keep_note":
		return normalizeKeepNote(base, raw)
	case "chat_message":
		return normalizeChatMessage(base, raw)
	case "hangouts_conversation":
		return normalizeHangoutsConversation(base, raw)
	case "photo_meta":
		return normalizePhotoMeta(base, raw)
	case bundle.SkippedArrayKind:
		ev, err := bundle.SkippedArrayEvent(sourceID, raw)
		return one(ev), err
	}
	return one(unknown(base, raw, "", nil)), nil
}

// watchAffixes are the localized "Watched <title>" sentences that are stripped
// from the Title.
var watchAffixes = []struct{ prefix, suffix string }{
	{prefix: "Watched "},
	{suffix: " angesehen"}, // de
}

var searchAffixes = []struct{ prefix, suffix string }{
	{prefix: "Searched for "},
}

// adMarkers are the (lower-cased) details names of a video shown as an ad.
var adMarkers = []string{"from google ads", "von google anzeigen"}

// normalizeYouTubeActivity maps a My Activity record of the YouTube product: a
// watch, a search, or (anything else, "Visited YouTube Music") a visit.
func normalizeYouTubeActivity(base domain.Event, raw provider.RawRecord, a activity) ([]domain.Event, error) {
	u, _ := url.Parse(a.TitleURL)
	kind := historyHint(raw.Member)
	var query, videoID string
	if u != nil && a.TitleURL != "" {
		q := u.Query()
		switch {
		case q.Has("search_query"):
			kind, query = "search", q.Get("search_query")
		case q.Has("v"):
			kind, videoID = "watch", q.Get("v")
		case strings.EqualFold(u.Host, "youtu.be"):
			kind, videoID = "watch", strings.Trim(u.Path, "/")
		default:
			kind = "" // a visited page, a viewed post, …
		}
	}

	ev := base
	ev.OccurredAt = parseTime(a.Time)
	meta(ev, "product", a.Header)
	when := a.Time
	if !ev.OccurredAt.IsZero() {
		when = ev.OccurredAt.Format(time.RFC3339Nano)
	}

	switch kind {
	case "watch":
		ev.Type, ev.Category = "google.youtube.watch", domain.CategoryConsumption
		ev.Title = a.Title
		if a.TitleURL != "" { // "Watched a video that has been removed" stays whole
			title, english, ok := stripAffix(a.Title, watchAffixes)
			ev.Title = title
			if !ok || !english {
				meta(ev, "raw_title", a.Title)
			}
		}
		meta(ev, "url", a.TitleURL)
		meta(ev, "video_id", videoID)
		if len(a.Subtitles) > 0 {
			meta(ev, "channel", a.Subtitles[0].Name)
			meta(ev, "channel_url", a.Subtitles[0].URL)
		}
		var details []string
		for _, d := range a.Details {
			if contains(adMarkers, strings.ToLower(d.Name)) {
				ev.Metadata["ad"] = true
			} else if d.Name != "" {
				details = append(details, d.Name)
			}
		}
		meta(ev, "details", strings.Join(details, ", "))
		ev.ExternalID = "yt.watch:" + when + ":" + short(firstNonEmpty(a.TitleURL, a.Title))
		return one(ev), nil

	case "search":
		if query == "" {
			query, _, _ = stripAffix(a.Title, searchAffixes)
		}
		ev.Type, ev.Category = "google.youtube.search", domain.CategoryConsumption
		ev.Title = "Searched YouTube"
		ev.Body = query
		meta(ev, "url", a.TitleURL)
		ev.ExternalID = "yt.search:" + when + ":" + short(query)
		return one(ev), nil
	}

	// "Visited YouTube Music", "Viewed a post", …: a plain visit of a YouTube page.
	return one(visitOrGeneric(base, raw, a, "youtube")), nil
}

// stripAffix removes the first matching sentence frame. english reports whether
// that was the English one.
func stripAffix(title string, affixes []struct{ prefix, suffix string }) (stripped string, english, ok bool) {
	for i, a := range affixes {
		if strings.HasPrefix(title, a.prefix) && strings.HasSuffix(title, a.suffix) && len(title) > len(a.prefix)+len(a.suffix) {
			return title[len(a.prefix) : len(title)-len(a.suffix)], i == 0, true
		}
	}
	return title, false, false
}

func normalizeComment(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var row map[string]string
	if err := json.Unmarshal(raw.Payload, &row); err != nil {
		return nil, err
	}
	commentID := field(row, "comment id", "parent", "top")
	parent := field(row, "parent comment id")
	videoID := field(row, "video id")
	postID := field(row, "post id")
	created := field(row, "timestamp")
	text := field(row, "comment text")
	if text == "" {
		text = sniff(row, func(v string) bool { return strings.HasPrefix(strings.TrimSpace(v), "{") })
	}
	if created == "" {
		created = sniff(row, func(v string) bool { return !parseTime(v).IsZero() })
	}
	body := commentText(text)
	if commentID == "" && body == "" {
		return one(unknown(base, raw, "", row)), nil
	}

	ev := base
	ev.Type, ev.Category = "google.youtube.comment", domain.CategoryContent
	ev.OccurredAt = parseTime(created)
	ev.Body = body
	ev.Title = "Commented on a YouTube video"
	var target string
	switch {
	case videoID != "":
		target = videoURL(videoID)
		if commentID != "" {
			target += "&lc=" + commentID
		}
	case postID != "":
		ev.Title = "Commented on a YouTube post"
		target = "https://www.youtube.com/post/" + postID
	}
	ev.ExternalID = "yt.comment:" + firstNonEmpty(commentID, created+":"+short(body))
	if parent != "" {
		ev.Title = "Replied to a YouTube comment"
		ev.ParentExternalID = "yt.comment:" + parent
		ev.Targets = tgt(domain.RelationRepliedTo, parent, target)
	} else if id := firstNonEmpty(videoID, postID); id != "" {
		ev.Targets = tgt(domain.RelationRepliedTo, id, target)
	}
	meta(ev, "url", target)
	meta(ev, "video_id", videoID)
	meta(ev, "post_id", postID)
	meta(ev, "channel_id", field(row, "channel id"))
	if price := field(row, "price"); price != "" && price != "0" {
		meta(ev, "price", price) // Super Thanks / paid comment
	}
	return one(ev), nil
}

// commentText flattens the JSON blob of the "Comment Text" column:
// {"text":"…"}, {"takeoutSegments":[{"text":"…"},…]}, or several such objects
// separated by commas. Anything else is taken literally.
func commentText(cell string) string {
	type segment struct {
		Text     string    `json:"text"`
		Segments []segment `json:"takeoutSegments"`
	}
	cell = strings.TrimSpace(cell)
	if !strings.HasPrefix(cell, "{") && !strings.HasPrefix(cell, "[") {
		return cell
	}
	var segs []segment
	if err := json.Unmarshal([]byte(cell), &segs); err != nil {
		if err := json.Unmarshal([]byte("["+cell+"]"), &segs); err != nil {
			return cell
		}
	}
	var b strings.Builder
	var walk func([]segment)
	walk = func(ss []segment) {
		for _, s := range ss {
			b.WriteString(s.Text)
			walk(s.Segments)
		}
	}
	walk(segs)
	return b.String()
}

var (
	channelIDRe = regexp.MustCompile(`^UC[\w-]{22}$`)
	videoIDRe   = regexp.MustCompile(`^[\w-]{11}$`)
)

func normalizeSubscription(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var row map[string]string
	if err := json.Unmarshal(raw.Payload, &row); err != nil {
		return nil, err
	}
	isURL := func(v string) bool { return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") }
	channelID := firstNonEmpty(field(row, "channel id"), sniff(row, channelIDRe.MatchString))
	channelURL := firstNonEmpty(field(row, "channel url"), sniff(row, isURL))
	title := field(row, "channel title")
	if title == "" {
		title = sniff(row, func(v string) bool { return v != "" && v != channelID && v != channelURL })
	}
	if channelID == "" && channelURL == "" {
		return one(unknown(base, raw, "", row)), nil
	}
	if channelURL == "" {
		channelURL = "https://www.youtube.com/channel/" + channelID
	}

	ev := base
	ev.Type, ev.Category = "google.youtube.subscribe", domain.CategoryConnection
	ev.Title = "Subscribed to " + firstNonEmpty(title, channelID, channelURL)
	ev.ExternalID = "yt.subscribe:" + firstNonEmpty(channelID, short(channelURL))
	ev.Targets = tgt(domain.RelationFollowed, firstNonEmpty(channelID, channelURL), channelURL)
	meta(ev, "channel", title)
	meta(ev, "channel_id", channelID)
	meta(ev, "channel_url", channelURL)
	return one(ev), nil // the export does not say when the subscription started
}

func normalizePlaylistItem(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var pr playlistRow
	if err := json.Unmarshal(raw.Payload, &pr); err != nil {
		return nil, err
	}
	videoID := strings.TrimSpace(firstNonEmpty(field(pr.Row, "video id"), sniff(pr.Row, func(v string) bool {
		return videoIDRe.MatchString(strings.TrimSpace(v))
	})))
	if !videoIDRe.MatchString(videoID) {
		// The header row of the legacy two-table layout, a localized index, …
		return one(unknown(base, raw, "", pr.Row)), nil
	}
	added := field(pr.Row, "timestamp")
	if parseTime(added).IsZero() {
		added = sniff(pr.Row, func(v string) bool { return !parseTime(v).IsZero() })
	}

	ev := base
	ev.OccurredAt = parseTime(added)
	meta(ev, "playlist", pr.Playlist)
	meta(ev, "video_id", videoID)
	meta(ev, "url", videoURL(videoID))
	if contains(likedPlaylists, strings.ToLower(pr.Playlist)) {
		ev.Type, ev.Category = "google.youtube.like", domain.CategoryReaction
		ev.Title = "Liked a YouTube video"
		ev.ExternalID = "yt.like:" + videoID
		ev.Targets = tgt(domain.RelationLiked, videoID, videoURL(videoID))
		return one(ev), nil
	}
	ev.Type, ev.Category = "google.youtube.playlist_item", domain.CategorySave
	ev.Title = "Saved a video to " + firstNonEmpty(pr.Playlist, "a playlist")
	ev.ExternalID = "yt.playlist_item:" + short(pr.Playlist) + ":" + videoID
	ev.Targets = tgt(domain.RelationSaved, videoID, videoURL(videoID))
	return one(ev), nil
}

// unknown keeps a record this provider matched but could not interpret.
func unknown(base domain.Event, raw provider.RawRecord, title string, fields map[string]string) domain.Event {
	ev := base
	ev.Type, ev.Category = "google.unknown", domain.CategoryOther
	ev.Title = firstNonEmpty(title, "Unrecognized Google record")
	ev.ExternalID = "g.unknown:" + short(raw.Kind+"\x00"+string(raw.Payload))
	meta(ev, "member", raw.Member)
	meta(ev, "kind", raw.Kind)
	for k, v := range fields {
		meta(ev, headerKey(k), v)
	}
	return ev
}

// ---- helpers ----

// headerKey normalizes a CSV header: no BOM, lower case, "Video-ID" and
// "video_id" read as "video id".
func headerKey(h string) string {
	h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
	return strings.NewReplacer("-", " ", "_", " ").Replace(h)
}

// field finds a CSV column tolerantly: a header equal to want wins, else the
// first header (in sorted order, so the choice is stable) that contains want
// and none of the not words. Matching ignores case and a leading BOM.
func field(row map[string]string, want string, not ...string) string {
	headers := make([]string, 0, len(row))
	for h := range row {
		headers = append(headers, h)
	}
	sort.Strings(headers)
	for _, h := range headers {
		if headerKey(h) == want {
			return strings.TrimSpace(row[h])
		}
	}
next:
	for _, h := range headers {
		k := headerKey(h)
		if !strings.Contains(k, want) {
			continue
		}
		for _, n := range not {
			if strings.Contains(k, n) {
				continue next
			}
		}
		return strings.TrimSpace(row[h])
	}
	return ""
}

// sniff returns the first value (in header order) that ok accepts; it stands in
// for field when the CSV headers are localized.
func sniff(row map[string]string, ok func(string) bool) string {
	headers := make([]string, 0, len(row))
	for h := range row {
		headers = append(headers, h)
	}
	sort.Strings(headers)
	for _, h := range headers {
		if v := strings.TrimSpace(row[h]); ok(v) {
			return v
		}
	}
	return ""
}

var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05 MST", // legacy playlist CSVs: 2021-03-04 10:00:00 UTC
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
}

// parseTime returns the zero time for anything it does not understand.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func videoURL(id string) string { return "https://www.youtube.com/watch?v=" + id }

func tgt(rel domain.Relation, id, url string) []domain.Target {
	return []domain.Target{{Relation: rel, TargetSource: sourceID, TargetExternalID: id, TargetURL: url}}
}

// short is a stable 8-byte digest for ExternalIDs.
func short(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func one(ev domain.Event) []domain.Event { return []domain.Event{ev} }

// fresh copies base with its own Metadata map, for normalizers that emit
// several events from one record (they must not share the map).
func fresh(base domain.Event) domain.Event {
	base.Metadata = map[string]any{}
	return base
}

func meta(ev domain.Event, k, v string) {
	if v != "" {
		ev.Metadata[k] = v
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// metaAny sets a non-string metadata value (numbers, booleans).
func metaAny(ev domain.Event, k string, v any) {
	if v != nil {
		ev.Metadata[k] = v
	}
}

// flex is a JSON scalar the export writes sometimes as a number and sometimes
// as a string ("time_usec": 1700000000000000, "distanceMeters": "3850",
// "timestampMs": "1520000000000"). It decodes both.
type flex string

func (f *flex) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flex(s)
		return nil
	}
	*f = flex(strings.Trim(string(b), " \t\r\n"))
	return nil
}

func (f flex) String() string { return string(f) }

func (f flex) Float() (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(string(f)), 64)
	return v, err == nil
}

func (f flex) Int() (int64, bool) {
	s := strings.TrimSpace(string(f))
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v, true
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(v), true
	}
	return 0, false
}

// epoch converts an integer count of units since the Unix epoch — seconds,
// milliseconds or microseconds, per unit — to UTC; zero when it is not a number.
func epoch(f flex, unit time.Duration) time.Time {
	n, ok := f.Int()
	if !ok || n == 0 {
		return time.Time{}
	}
	return time.Unix(0, 0).Add(time.Duration(n) * unit).UTC()
}

// e7 converts a latitude/longitude stored as an integer × 10^7.
func e7(f flex) (float64, bool) {
	v, ok := f.Float()
	if !ok {
		return 0, false
	}
	return v / 1e7, true
}

// latLngRe finds the two decimals of a coordinate string as the on-device
// Timeline export writes them: "50.0506312°, 14.3439906°" (Android) or
// "geo:50.0506312,14.3439906" (iOS).
var latLngRe = regexp.MustCompile(`-?\d+(?:\.\d+)?`)

func parseLatLng(s string) (lat, lon float64, ok bool) {
	m := latLngRe.FindAllString(s, 3)
	if len(m) != 2 {
		return 0, 0, false
	}
	lat, err1 := strconv.ParseFloat(m[0], 64)
	lon, err2 := strconv.ParseFloat(m[1], 64)
	return lat, lon, err1 == nil && err2 == nil
}

// coord renders a coordinate for ExternalIDs (stable, locale-free).
func coord(v float64) string { return strconv.FormatFloat(v, 'f', 7, 64) }

// when renders a time for ExternalIDs, falling back to the raw string of an
// unparsed one so two records that differ only in time still differ.
func when(t time.Time, raw string) string {
	if !t.IsZero() {
		return t.UTC().Format(time.RFC3339Nano)
	}
	return raw
}

// rawTime returns the first time-typed value the record carries, for
// records whose time key varies between export generations.
func rawTime(vals ...flex) (time.Time, string) {
	for _, v := range vals {
		if v == "" {
			continue
		}
		if t := parseTime(v.String()); !t.IsZero() {
			return t, v.String()
		}
	}
	for _, v := range vals {
		if v != "" {
			return time.Time{}, v.String()
		}
	}
	return time.Time{}, ""
}
