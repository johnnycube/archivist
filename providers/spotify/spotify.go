// Package spotify ingests Spotify's data exports (JSON) and maps plays,
// searches, the saved library and playlists to Events.
//
// Spotify ships two export flavours, both ZIPs, and either can be imported:
//
//   - "Extended streaming history" — the whole listening history, in
//     Spotify Extended Streaming History/Streaming_History_Audio_<years>_<n>.json
//     and Streaming_History_Video_*.json (endsong_<n>.json in older exports).
//   - "Account data" — the last year only, in
//     Spotify Account Data/StreamingHistory_music_<n>.json and
//     StreamingHistory_podcast_<n>.json (StreamingHistory<n>.json in older
//     exports), next to SearchQueries.json, YourLibrary.json and Playlist<n>.json.
//
// Both history flavours become spotify.play. Two things to know about them:
//
//   - The timestamp of a play ("ts" / "endTime") is when playback ENDED, in UTC,
//     and that is what OccurredAt holds; the start is OccurredAt minus the
//     ms_played metadata value.
//   - The flavours overlap for the last year but give the same play different
//     ExternalIDs: the extended history has second precision and track URIs,
//     account data has minute precision and names only. Importing both therefore
//     yields two events per overlapping play; the "export" metadata value
//     (extended|account) tells them apart.
//
// A history runs to hundreds of thousands of records, so members are streamed
// element by element and Normalize decodes each record once, into a fixed
// struct, with a single time layout per flavour.
//
// Follow.json, Userdata.json, Payments.json, Inferences.json, Identity.json and
// the rest are not listed in Extractors and are captured generically by the
// bundle importer, so nothing is dropped.
package spotify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

const sourceID = "spotify"

// Provider implements provider.BundleProvider for Spotify exports.
type Provider struct{}

func New() *Provider { return &Provider{} }

func (p *Provider) Descriptor() domain.Descriptor {
	return domain.Descriptor{
		ID:             sourceID,
		DisplayName:    "Spotify",
		IngestionModes: []domain.Mode{domain.ModeBundle},
		EventTypes: []domain.TypeDef{
			{Type: "spotify.play", Category: domain.CategoryConsumption},
			{Type: "spotify.search", Category: domain.CategoryConsumption, HasBody: true},
			{Type: "spotify.save", Category: domain.CategorySave, NeedsTarget: true},
			{Type: "spotify.playlist", Category: domain.CategorySave},
			{Type: "spotify.playlist_item", Category: domain.CategorySave, NeedsTarget: true},
			{Type: "spotify.unknown", Category: domain.CategoryOther},
		},
	}
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{SupportsBundle: true}
}

// Extractors maps each known member to a provider-local Kind. Unlisted files are
// captured generically by the importer.
func (p *Provider) Extractors() []provider.Extractor {
	return []provider.Extractor{
		// Streaming_History_Audio_*, Streaming_History_Video_* and any sibling
		// Spotify adds (audiobooks ship in the audio files today).
		arrayEx{kind: "play_extended", prefixes: []string{"streaming_history_", "endsong"}},
		arrayEx{kind: "play_account", prefixes: []string{"streaminghistory"}},
		arrayEx{kind: "search", prefixes: []string{"searchqueries"}},
		libraryEx{},
		playlistEx{},
	}
}

// ---- extraction ----

// jsonBase returns the lower-cased base name of a .json member, or "".
func jsonBase(member string) string {
	b := strings.ToLower(path.Base(member))
	if !strings.HasSuffix(b, ".json") {
		return ""
	}
	return b
}

// arrayEx streams a member that is one top-level array of records, passing each
// element through verbatim.
type arrayEx struct {
	kind     string
	prefixes []string // lower-case base-name prefixes
}

func (e arrayEx) Match(member string) bool {
	b := jsonBase(member)
	for _, p := range e.prefixes {
		if b != "" && strings.HasPrefix(b, p) {
			return true
		}
	}
	return false
}

func (e arrayEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	return bundle.StreamJSONArray(r, nil, func(el json.RawMessage) error {
		return emit(rec(e.kind, el))
	}, bundle.ReportSkipped(member, emit))
}

// libraryKinds maps the arrays of YourLibrary.json we understand to the kind of
// thing saved; libraryOther lists the arrays we know of but do not map, whose
// items are kept as spotify.unknown. An array Spotify adds in future is not
// streamed, but leaves one spotify.unknown event saying what was skipped
// (bundle.SkippedArrayRecord); add it here to keep its items.
var (
	libraryKinds = map[string]string{
		"tracks": "track", "albums": "album", "shows": "show", "episodes": "episode", "artists": "artist",
	}
	libraryOther = []string{"bannedTracks", "bannedArtists", "other", "audiobooks", "books", "playlists"}
)

// libraryItem is the payload of a "save" or "library_unknown" record.
type libraryItem struct {
	List string          `json:"list"` // the YourLibrary.json array it came from
	Item json.RawMessage `json:"item"`
}

type libraryEx struct{}

func (libraryEx) Match(member string) bool { return jsonBase(member) == "yourlibrary.json" }

func (libraryEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	keys := append([]string(nil), libraryOther...)
	for k := range libraryKinds {
		keys = append(keys, k)
	}
	return bundle.StreamJSONArraysReport(r, keys, func(key string, el json.RawMessage) error {
		payload, err := json.Marshal(libraryItem{List: key, Item: el})
		if err != nil {
			return err
		}
		if _, ok := libraryKinds[key]; !ok {
			return emit(rec("library_unknown", payload))
		}
		return emit(rec("save", payload))
	}, bundle.ReportSkipped(member, emit))
}

// playlist is one element of Playlist<n>.json's "playlists" array. A file can
// hold many playlists, so they are decoded one at a time; a single playlist is
// bounded (Spotify caps them at 10,000 items).
type playlist struct {
	Name              string            `json:"name"`
	LastModifiedDate  string            `json:"lastModifiedDate"`
	Description       string            `json:"description"`
	NumberOfFollowers int64             `json:"numberOfFollowers"`
	Items             []json.RawMessage `json:"items,omitempty"`
}

// playlistItem is the payload of a "playlist_item" record: the item as exported
// plus the playlist it sits in.
type playlistItem struct {
	Playlist string          `json:"playlist"`
	Item     json.RawMessage `json:"item"`
}

// playlistInfo is the payload of a "playlist" record.
type playlistInfo struct {
	playlist
	ItemCount int `json:"itemCount"`
}

type playlistEx struct{}

func (playlistEx) Match(member string) bool {
	n, ok := strings.CutPrefix(strings.TrimSuffix(jsonBase(member), ".json"), "playlist") // Playlist1.json
	if !ok || n == "" {
		return false
	}
	_, err := strconv.ParseUint(n, 10, 32)
	return err == nil
}

func (playlistEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	return bundle.StreamJSONArraysReport(r, []string{"playlists"}, func(_ string, el json.RawMessage) error {
		var pl playlist
		if err := json.Unmarshal(el, &pl); err != nil {
			return emit(rec("unknown", el)) // not a playlist object: keep it
		}
		items := pl.Items
		pl.Items = nil
		payload, err := json.Marshal(playlistInfo{playlist: pl, ItemCount: len(items)})
		if err != nil {
			return err
		}
		if err := emit(rec("playlist", payload)); err != nil {
			return err
		}
		for _, it := range items {
			payload, err := json.Marshal(playlistItem{Playlist: pl.Name, Item: it})
			if err != nil {
				return err
			}
			if err := emit(rec("playlist_item", payload)); err != nil {
				return err
			}
		}
		return nil
	}, bundle.ReportSkipped(member, emit))
}

// rec builds a RawRecord whose ContentHash covers the kind as well as the
// payload: the importer dedupes on that hash across the whole archive, and the
// same bare payload can mean different things in different members.
func rec(kind string, payload []byte) provider.RawRecord {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(payload)
	return provider.RawRecord{Kind: kind, Payload: payload, ContentHash: hex.EncodeToString(h.Sum(nil))}
}

// ---- normalization ----

// extendedPlay is a record of the extended streaming history.
type extendedPlay struct {
	TS           string `json:"ts"` // end of playback, UTC, RFC 3339
	MsPlayed     int64  `json:"ms_played"`
	Track        string `json:"master_metadata_track_name"`
	Artist       string `json:"master_metadata_album_artist_name"`
	Album        string `json:"master_metadata_album_album_name"`
	TrackURI     string `json:"spotify_track_uri"`
	Episode      string `json:"episode_name"`
	Show         string `json:"episode_show_name"`
	EpisodeURI   string `json:"spotify_episode_uri"`
	Audiobook    string `json:"audiobook_title"`
	Chapter      string `json:"audiobook_chapter_title"`
	AudiobookURI string `json:"audiobook_uri"`
	ChapterURI   string `json:"audiobook_chapter_uri"`
	Username     string `json:"username"`             // older exports only
	UserAgent    string `json:"user_agent_decrypted"` // older exports only
	ReasonStart  string `json:"reason_start"`
	ReasonEnd    string `json:"reason_end"`
	Shuffle      *bool  `json:"shuffle"`
	Skipped      *bool  `json:"skipped"`
	Offline      *bool  `json:"offline"`
	Incognito    *bool  `json:"incognito_mode"`
	Platform     string `json:"platform"`
	Country      string `json:"conn_country"`
	IPAddr       string `json:"ip_addr"`
	IPAddrOlder  string `json:"ip_addr_decrypted"`
}

// accountPlay is a record of the account-data streaming history (music or
// podcast file).
type accountPlay struct {
	EndTime  string `json:"endTime"` // end of playback, UTC, minute precision
	Artist   string `json:"artistName"`
	Track    string `json:"trackName"`
	Show     string `json:"podcastName"`
	Episode  string `json:"episodeName"`
	MsPlayed int64  `json:"msPlayed"`
}

const accountTimeLayout = "2006-01-02 15:04"

func (p *Provider) Normalize(_ context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	ev := domain.Event{SourceID: sourceID, Actor: "self"}

	switch raw.Kind {
	case bundle.SkippedArrayKind:
		ev, err := bundle.SkippedArrayEvent(sourceID, raw)
		if err != nil {
			return unknownEvent(raw, "malformed skipped-array record"), nil
		}
		return []domain.Event{ev}, nil
	case "play_extended":
		var e extendedPlay
		if err := json.Unmarshal(raw.Payload, &e); err != nil {
			return unknownEvent(raw, "malformed extended play: "+err.Error()), nil
		}
		kind, by, what, uri := "track", e.Artist, e.Track, e.TrackURI
		switch {
		case e.TrackURI != "" || e.Track != "":
		case e.EpisodeURI != "" || e.Episode != "":
			kind, by, what, uri = "episode", e.Show, e.Episode, e.EpisodeURI
		case e.AudiobookURI != "" || e.Audiobook != "" || e.ChapterURI != "":
			kind, by, what, uri = "audiobook", e.Audiobook, e.Chapter, firstNonEmpty(e.ChapterURI, e.AudiobookURI)
		}
		ev.Metadata = make(map[string]any, 14)
		ev.Metadata["export"] = "extended"
		play(&ev, parseTime(e.TS, time.RFC3339, accountTimeLayout), e.TS, kind, by, what, uri, e.MsPlayed)
		set(ev, "album", e.Album)
		set(ev, "platform", e.Platform)
		set(ev, "country", e.Country)
		set(ev, "reason_start", e.ReasonStart)
		set(ev, "reason_end", e.ReasonEnd)
		set(ev, "ip", firstNonEmpty(e.IPAddr, e.IPAddrOlder))
		setBool(ev, "skipped", e.Skipped)
		setBool(ev, "shuffle", e.Shuffle)
		setBool(ev, "offline", e.Offline)
		setBool(ev, "incognito", e.Incognito)
		set(ev, "username", e.Username)
		set(ev, "user_agent", e.UserAgent)
		set(ev, "audiobook", e.Audiobook)

	case "play_account":
		var e accountPlay
		if err := json.Unmarshal(raw.Payload, &e); err != nil {
			return unknownEvent(raw, "malformed account play: "+err.Error()), nil
		}
		kind, by, what := "track", e.Artist, e.Track
		switch {
		case e.Track == "" && e.Artist == "" && (e.Episode != "" || e.Show != ""):
			kind, by, what = "episode", e.Show, e.Episode
		case strings.Contains(strings.ToLower(raw.Member), "audiobook"):
			// StreamingHistory_audiobook_<n>.json: the record keys are not
			// publicly documented; whatever names it carries are used.
			kind = "audiobook"
			if by == "" && what == "" {
				by, what = e.Show, e.Episode
			}
		}
		ev.Metadata = make(map[string]any, 4)
		ev.Metadata["export"] = "account"
		play(&ev, parseTime(e.EndTime, accountTimeLayout, time.RFC3339), e.EndTime, kind, by, what, "", e.MsPlayed)

	case "search":
		var e struct {
			Platform string   `json:"platform"`
			Time     string   `json:"searchTime"`
			Query    string   `json:"searchQuery"`
			URIs     []string `json:"searchInteractionURIs"`
		}
		if err := json.Unmarshal(raw.Payload, &e); err != nil {
			return unknownEvent(raw, "malformed search: "+err.Error()), nil
		}
		ts := e.Time
		if i := strings.IndexByte(ts, '['); i >= 0 { // "2021-01-31T12:34:56.789Z[UTC]"
			ts = ts[:i]
		}
		ev.Type, ev.Category = "spotify.search", domain.CategoryConsumption
		ev.OccurredAt = parseTime(ts, time.RFC3339, accountTimeLayout)
		ev.Title = "Searched Spotify"
		ev.Body = e.Query
		ev.ExternalID = "search:" + stamp(ev.OccurredAt, e.Time, true) + ":" + digest(e.Query)
		ev.Metadata = map[string]any{}
		set(ev, "platform", e.Platform)
		if len(e.URIs) > 0 {
			ev.Metadata["interaction_uris"] = e.URIs
		}

	case "save":
		var li libraryItem
		if err := json.Unmarshal(raw.Payload, &li); err != nil {
			return unknownEvent(raw, "malformed library item: "+err.Error()), nil
		}
		var it struct {
			Artist    string `json:"artist"`
			Album     string `json:"album"`
			Track     string `json:"track"`
			Name      string `json:"name"`
			Show      string `json:"show"`
			Publisher string `json:"publisher"`
			URI       string `json:"uri"`
		}
		if err := json.Unmarshal(li.Item, &it); err != nil {
			return unknownEvent(raw, "malformed library item: "+err.Error()), nil
		}
		kind := libraryKinds[li.List]
		var label string
		switch kind {
		case "track":
			label = title(it.Artist, it.Track)
		case "album":
			label = title(it.Artist, it.Album)
		case "episode":
			label = title(it.Show, it.Name)
		case "show":
			label = title(it.Publisher, it.Name)
		default:
			label = it.Name
		}
		ev.Type, ev.Category = "spotify.save", domain.CategorySave
		ev.Title = "Saved " + kind + ": " + firstNonEmpty(label, it.URI, "unknown")
		if kind == "artist" {
			ev.Title = "Followed artist: " + firstNonEmpty(label, it.URI, "unknown")
		}
		ev.ExternalID = "save:" + kind + ":" + firstNonEmpty(it.URI, digest(label))
		ev.Targets = target(it.URI, label)
		ev.Metadata = map[string]any{"kind": kind}
		set(ev, "uri", it.URI)
		set(ev, "artist", it.Artist)
		set(ev, "album", it.Album)
		set(ev, "show", it.Show)
		set(ev, "publisher", it.Publisher)

	case "playlist":
		var pl playlistInfo
		if err := json.Unmarshal(raw.Payload, &pl); err != nil {
			return unknownEvent(raw, "malformed playlist: "+err.Error()), nil
		}
		ev.Type, ev.Category = "spotify.playlist", domain.CategorySave
		ev.OccurredAt = parseTime(pl.LastModifiedDate, "2006-01-02", time.RFC3339)
		ev.Title = "Playlist: " + firstNonEmpty(pl.Name, "untitled")
		ev.Body = pl.Description
		ev.ExternalID = "playlist:" + digest(pl.Name)
		ev.Metadata = map[string]any{"playlist": pl.Name, "items": pl.ItemCount, "followers": pl.NumberOfFollowers}
		set(ev, "last_modified", pl.LastModifiedDate)

	case "playlist_item":
		var pi playlistItem
		if err := json.Unmarshal(raw.Payload, &pi); err != nil {
			return unknownEvent(raw, "malformed playlist item: "+err.Error()), nil
		}
		var it struct {
			Track *struct {
				Name   string `json:"trackName"`
				Artist string `json:"artistName"`
				Album  string `json:"albumName"`
				URI    string `json:"trackUri"`
			} `json:"track"`
			Episode *struct {
				Name string `json:"episodeName"`
				Show string `json:"showName"`
				URI  string `json:"episodeUri"`
			} `json:"episode"`
			LocalTrack json.RawMessage `json:"localTrack"`
			AddedDate  string          `json:"addedDate"`
		}
		if err := json.Unmarshal(pi.Item, &it); err != nil {
			return unknownEvent(raw, "malformed playlist item: "+err.Error()), nil
		}
		kind, label, uri := "unknown", "", ""
		ev.Metadata = map[string]any{"playlist": pi.Playlist}
		switch {
		case it.Track != nil:
			kind, label, uri = "track", title(it.Track.Artist, it.Track.Name), it.Track.URI
			set(ev, "album", it.Track.Album)
		case it.Episode != nil:
			kind, label, uri = "episode", title(it.Episode.Show, it.Episode.Name), it.Episode.URI
		case len(it.LocalTrack) > 0 && string(it.LocalTrack) != "null":
			kind, label = "local_track", "local file"
			ev.Metadata["local_track"] = it.LocalTrack
		}
		ev.Type, ev.Category = "spotify.playlist_item", domain.CategorySave
		ev.OccurredAt = parseTime(it.AddedDate, "2006-01-02", time.RFC3339)
		ev.Title = "Added " + firstNonEmpty(label, uri, "an item") + " to " + firstNonEmpty(pi.Playlist, "a playlist")
		// A playlist has no item ids and positions shift, so an item is its
		// playlist, what it is and when it was added.
		ev.ExternalID = "playlist_item:" + digest(pi.Playlist) + ":" + digest(firstNonEmpty(uri, label, string(it.LocalTrack))) + ":" + it.AddedDate
		ev.ThreadExternalID = "playlist:" + digest(pi.Playlist)
		ev.Targets = target(uri, label)
		ev.Metadata["kind"] = kind
		set(ev, "uri", uri)

	default: // library_unknown and anything unforeseen
		ev.Type, ev.Category = "spotify.unknown", domain.CategoryOther
		ev.Title = "Unmapped Spotify record"
		ev.ExternalID = "unknown:" + digest(raw.Kind, string(raw.Payload))
		ev.Metadata = map[string]any{}
		var li libraryItem
		if raw.Kind == "library_unknown" && json.Unmarshal(raw.Payload, &li) == nil {
			ev.Title = "Unmapped Spotify library entry (" + li.List + ")"
			ev.Metadata["list"] = li.List
			ev.Metadata["record"] = li.Item
		} else if json.Valid(raw.Payload) {
			ev.Metadata["record"] = json.RawMessage(raw.Payload)
		} else {
			ev.Metadata["record"] = strings.ToValidUTF8(string(raw.Payload), "")
		}
	}
	if raw.Member != "" {
		ev.Metadata["member"] = raw.Member
	}
	return []domain.Event{ev}, nil
}

// play fills in a spotify.play. at is the END of playback; rawTS is the
// timestamp as exported, which keeps the ExternalID stable if it ever fails to
// parse.
func play(ev *domain.Event, at time.Time, rawTS, kind, by, what, uri string, ms int64) {
	ev.Type, ev.Category = "spotify.play", domain.CategoryConsumption
	ev.OccurredAt = at
	ev.Title = firstNonEmpty(title(by, what), uri, "Unknown "+kind)
	played := strconv.FormatInt(ms, 10)
	disc := uri
	if disc == "" {
		disc = by + "\x00" + what
	}
	ev.ExternalID = "play:" + stamp(at, rawTS, false) + ":" + digest(disc) + ":" + played
	ev.Metadata["kind"] = kind
	ev.Metadata["ms_played"] = ms
	set(*ev, "uri", uri)
}

// ---- helpers ----

// unknownEvent is the never-drop fallback for a record Normalize cannot decode:
// a spotify.unknown event carrying the payload, instead of an error that would
// abort the whole import.
func unknownEvent(raw provider.RawRecord, reason string) []domain.Event {
	ev := domain.Event{
		SourceID: sourceID, Type: "spotify.unknown", Category: domain.CategoryOther,
		Title:      "Unmapped Spotify record",
		ExternalID: "unknown:" + digest(raw.Kind, string(raw.Payload)),
		Metadata:   map[string]any{"reason": reason, "kind": raw.Kind},
	}
	if json.Valid(raw.Payload) {
		ev.Metadata["record"] = json.RawMessage(raw.Payload)
	} else {
		ev.Metadata["record"] = strings.ToValidUTF8(string(raw.Payload), "")
	}
	if raw.Member != "" {
		ev.Metadata["member"] = raw.Member
	}
	return []domain.Event{ev}
}

// title renders "<artist> — <track>", or whichever half is present.
func title(by, what string) string {
	if by == "" || what == "" {
		return by + what
	}
	return by + " — " + what
}

// target points a save at the saved thing: spotify:track:ID and its
// open.spotify.com URL.
func target(uri, label string) []domain.Target {
	t := domain.Target{Relation: domain.RelationSaved, TargetSource: sourceID, TargetExternalID: firstNonEmpty(uri, label)}
	if parts := strings.Split(uri, ":"); len(parts) == 3 && parts[0] == "spotify" {
		t.TargetURL = "https://open.spotify.com/" + parts[1] + "/" + parts[2]
	}
	return []domain.Target{t}
}

func set(ev domain.Event, k, v string) {
	if v != "" {
		ev.Metadata[k] = v
	}
}

func setBool(ev domain.Event, k string, v *bool) {
	if v != nil {
		ev.Metadata[k] = *v
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

// digest is a short stable hash of the fields that tell two records apart.
func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:6])
}

// stamp is the timestamp part of an ExternalID, or the raw text when it could
// not be parsed (still stable across re-imports).
func stamp(t time.Time, raw string, millis bool) string {
	switch {
	case t.IsZero():
		return raw
	case millis:
		return strconv.FormatInt(t.UnixMilli(), 10)
	}
	return strconv.FormatInt(t.Unix(), 10)
}

// parseTime tries the layout the member is known to use, then one fallback; all
// Spotify export timestamps are UTC.
func parseTime(s, layout, fallback string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		if t, err = time.Parse(fallback, s); err != nil {
			return time.Time{}
		}
	}
	return t.UTC()
}
