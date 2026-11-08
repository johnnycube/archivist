// Package tiktok ingests TikTok's "Download your data" export in JSON format: a
// ZIP holding one large user_data_tiktok.json (older: user_data.json) whose
// nested sections carry watch history, likes, favorites, searches, follows,
// shares, logins, comments, direct messages and the account's own videos.
//
// Everything lives in that single member, record arrays repeat their names
// across sections ("VideoList" is both the watch history and the own videos)
// and direct-message arrays are named after the chat partner, so the file is
// streamed with a path-tracking walker and records are classified by where they
// sit. Section names have changed between export versions; the known aliases are
// accepted. Whatever is not recognized becomes tiktok.unknown — a matched member
// is never partially dropped.
package tiktok

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
	"github.com/johnnycube/archivist/providers/internal/jsonwalk"
)

const sourceID = "tiktok"

type Provider struct{}

func New() *Provider { return &Provider{} }

func (p *Provider) Descriptor() domain.Descriptor {
	return domain.Descriptor{
		ID:             sourceID,
		DisplayName:    "TikTok",
		IngestionModes: []domain.Mode{domain.ModeBundle},
		EventTypes: []domain.TypeDef{
			{Type: "tiktok.post", Category: domain.CategoryContent},
			{Type: "tiktok.comment", Category: domain.CategoryContent, HasBody: true},
			{Type: "tiktok.message", Category: domain.CategoryMessage, HasBody: true},
			{Type: "tiktok.like", Category: domain.CategoryReaction, NeedsTarget: true},
			{Type: "tiktok.save", Category: domain.CategorySave, NeedsTarget: true},
			{Type: "tiktok.share", Category: domain.CategoryContent, NeedsTarget: true},
			{Type: "tiktok.follow", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "tiktok.follower", Category: domain.CategoryConnection},
			{Type: "tiktok.watch", Category: domain.CategoryConsumption, NeedsTarget: true},
			{Type: "tiktok.search", Category: domain.CategoryConsumption, HasBody: true},
			{Type: "tiktok.block", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "tiktok.login", Category: domain.CategoryOther},
			{Type: "tiktok.unknown", Category: domain.CategoryOther},
		},
	}
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{SupportsBundle: true}
}

// Extractors: the JSON export is one member. The TXT flavour is one file per
// section (Activity/Like List.txt, …), read by textEx. Anything else in the ZIP
// is captured generically by the importer.
func (p *Provider) Extractors() []provider.Extractor {
	return []provider.Extractor{userDataEx{}, textEx{}}
}

// ---- extraction ----

type userDataEx struct{}

func (userDataEx) Match(member string) bool {
	b := strings.ToLower(path.Base(member))
	return strings.HasPrefix(b, "user_data") && strings.HasSuffix(b, ".json")
}

func (userDataEx) Extract(_ string, r io.Reader, emit func(provider.RawRecord) error) error {
	return jsonwalk.Walk(r, jsonwalk.Handler{
		Element: func(p []string, el json.RawMessage) error {
			switch kind := classify(p); kind {
			case "message":
				payload, err := json.Marshal(dmRecord{Thread: chatPartner(p[len(p)-1]), Message: el})
				if err != nil {
					return err
				}
				return emit(rec(kind, payload))
			case "unknown":
				return emitUnknown(emit, p, el)
			default:
				return emit(rec(kind, el))
			}
		},
		// Scalar settings and profile fields have no event type of their own.
		Fields: func(p []string, fields json.RawMessage) error {
			return emitUnknown(emit, p, fields)
		},
	})
}

// A route assigns a record kind to an array by its own member name and,
// where that name is reused, by the names of the objects it is nested in.
type route struct {
	kind  string
	array string
	under []string // any of these among the ancestors; empty = anywhere
}

var routes = []route{
	{"watch", "VideoList", []string{"Video Browsing History", "Watch History", "Browsing History"}},
	{"post", "VideoList", []string{"Video", "Videos", "Post", "Posts"}},
	{"like", "ItemFavoriteList", nil},
	{"save", "FavoriteVideoList", nil},
	{"save", "FavoriteEffectsList", nil},
	{"save", "FavoriteHashtagList", nil},
	{"save", "FavoriteSoundList", nil},
	{"search", "SearchList", nil},
	{"follow", "Following", []string{"Following List", "Following"}},
	{"follower", "FansList", nil},
	{"share", "ShareHistoryList", nil},
	{"login", "LoginHistoryList", nil},
	{"comment", "CommentsList", nil},
	{"block", "BlockList", nil},
}

const chatPrefix = "chat history with"

// classify maps the path of an array to a record kind; "unknown" if the array
// is not one this provider understands.
func classify(p []string) string {
	if len(p) == 0 {
		return "unknown"
	}
	name, ancestors := p[len(p)-1], p[:len(p)-1]
	if strings.HasPrefix(strings.ToLower(name), chatPrefix) || (len(ancestors) > 0 && ancestors[len(ancestors)-1] == "ChatHistory") {
		return "message"
	}
	for _, rt := range routes {
		if rt.array != name {
			continue
		}
		if len(rt.under) == 0 {
			return rt.kind
		}
		for _, a := range ancestors {
			for _, u := range rt.under {
				if strings.EqualFold(a, u) {
					return rt.kind
				}
			}
		}
	}
	return "unknown"
}

// chatPartner turns the array name "Chat History with jane_doe:" into jane_doe.
func chatPartner(name string) string {
	s := strings.TrimSpace(name)
	if len(s) >= len(chatPrefix) && strings.EqualFold(s[:len(chatPrefix)], chatPrefix) {
		s = s[len(chatPrefix):]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), ":"))
}

type dmRecord struct {
	Thread  string          `json:"thread"`
	Message json.RawMessage `json:"message"`
}

type unknownRecord struct {
	Path  []string        `json:"path"`
	Value json.RawMessage `json:"value"`
}

func emitUnknown(emit func(provider.RawRecord) error, p []string, value json.RawMessage) error {
	payload, err := json.Marshal(unknownRecord{Path: p, Value: value})
	if err != nil {
		return err
	}
	return emit(rec("unknown", payload))
}

// rec hashes the kind into the dedupe key: a bare {"Date","Link"} is identical
// whether the video was watched, liked or favorited in that second.
func rec(kind string, payload []byte) provider.RawRecord {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(payload)
	return provider.RawRecord{Kind: kind, Payload: payload, ContentHash: hex.EncodeToString(h.Sum(nil))}
}

// ---- normalization ----

// entry is the permissive union of the fields TikTok's list records carry.
// (encoding/json matches member names case-insensitively.)
type entry struct {
	Date string `json:"Date"`

	Link        string `json:"Link"`
	VideoLink   string `json:"VideoLink"`
	EffectLink  string `json:"EffectLink"`
	HashtagLink string `json:"HashtagLink"`
	HashtagName string `json:"HashtagName"`
	SoundLink   string `json:"SoundLink"`
	Title       string `json:"Title"`
	Likes       any    `json:"Likes"`
	Sound       string `json:"Sound"`

	SearchTerm string `json:"SearchTerm"`
	UserName   string `json:"UserName"`
	Comment    string `json:"Comment"`

	SharedContent string `json:"SharedContent"`
	Method        string `json:"Method"`

	IP           string `json:"IP"`
	DeviceModel  string `json:"DeviceModel"`
	DeviceSystem string `json:"DeviceSystem"`
	NetworkType  string `json:"NetworkType"`
	Carrier      string `json:"Carrier"`

	From    string `json:"From"`
	Content string `json:"Content"`
}

func (p *Provider) Normalize(_ context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	base := domain.Event{SourceID: sourceID, Actor: "self", Metadata: map[string]any{}}

	switch raw.Kind {
	case "message":
		return normalizeMessage(base, raw)
	case "unknown":
		return normalizeUnknown(base, raw)
	}

	var e entry
	if err := json.Unmarshal(raw.Payload, &e); err != nil {
		// A list holding something other than objects: keep it, untyped.
		return normalizeUnknown(base, provider.RawRecord{Kind: "unknown", Payload: mustUnknown(raw.Kind, raw.Payload)})
	}
	at, ts := parseDate(e.Date)
	base.OccurredAt = at
	link := firstNonEmpty(e.Link, e.VideoLink, e.EffectLink, e.HashtagLink, e.SoundLink)

	switch raw.Kind {
	case "watch", "like", "save":
		ev := base
		rel := domain.Relation("")
		switch raw.Kind {
		case "watch":
			ev.Type, ev.Category, ev.Title = "tiktok.watch", domain.CategoryConsumption, "Watched a TikTok video"
		case "like":
			ev.Type, ev.Category, ev.Title, rel = "tiktok.like", domain.CategoryReaction, "Liked a TikTok video", domain.RelationLiked
		case "save":
			ev.Type, ev.Category, ev.Title, rel = "tiktok.save", domain.CategorySave, "Added a TikTok video to favorites", domain.RelationSaved
			switch {
			case e.EffectLink != "":
				ev.Title = "Added a TikTok effect to favorites"
				ev.Metadata["kind"] = "effect"
			case e.HashtagLink != "" || e.HashtagName != "":
				ev.Title = "Added #" + strings.TrimPrefix(e.HashtagName, "#") + " to favorite hashtags"
				ev.Metadata["kind"] = "hashtag"
				meta(ev, "hashtag", e.HashtagName)
			case e.SoundLink != "":
				ev.Title = "Added a TikTok sound to favorites"
				ev.Metadata["kind"] = "sound"
			default:
				ev.Metadata["kind"] = "video"
			}
		}
		ev.ExternalID = id(raw.Kind, ts, link+"|"+e.HashtagName)
		ev.Targets = []domain.Target{{Relation: rel, TargetSource: sourceID, TargetExternalID: firstNonEmpty(videoID(link), e.HashtagName), TargetURL: link}}
		meta(ev, "link", link)
		return one(ev), nil

	case "block":
		ev := base
		ev.Type, ev.Category = "tiktok.block", domain.CategoryConnection
		ev.Title = "Blocked " + e.UserName
		ev.ExternalID = id("block", ts, e.UserName)
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: sourceID, TargetExternalID: e.UserName, TargetURL: profileURL(e.UserName)}}
		meta(ev, "username", e.UserName)
		return one(ev), nil

	case "post":
		ev := base
		ev.Type, ev.Category = "tiktok.post", domain.CategoryContent
		ev.Title = firstNonEmpty(e.Title, "TikTok video")
		ev.ExternalID = id("post", ts, link)
		if link != "" {
			ev.Media = []domain.Media{{URL: link}}
		}
		meta(ev, "link", link)
		meta(ev, "video_id", videoID(link))
		meta(ev, "likes", scalar(e.Likes))
		meta(ev, "sound", e.Sound)
		return one(ev), nil

	case "comment":
		ev := base
		ev.Type, ev.Category = "tiktok.comment", domain.CategoryContent
		ev.Title, ev.Body = "Comment", e.Comment
		ev.ExternalID = id("comment", ts, e.Comment)
		return one(ev), nil

	case "search":
		ev := base
		ev.Type, ev.Category = "tiktok.search", domain.CategoryConsumption
		ev.Title, ev.Body = "Searched TikTok", e.SearchTerm
		ev.ExternalID = id("search", ts, e.SearchTerm)
		return one(ev), nil

	case "follow":
		ev := base
		ev.Type, ev.Category = "tiktok.follow", domain.CategoryConnection
		ev.Title = "Followed " + e.UserName
		ev.ExternalID = id("follow", ts, e.UserName)
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: sourceID, TargetExternalID: e.UserName, TargetURL: profileURL(e.UserName)}}
		return one(ev), nil

	case "follower":
		ev := base
		ev.Type, ev.Category = "tiktok.follower", domain.CategoryConnection
		ev.Actor = firstNonEmpty(e.UserName, "self")
		ev.Title = "Followed by " + e.UserName
		ev.ExternalID = id("follower", ts, e.UserName)
		meta(ev, "username", e.UserName)
		return one(ev), nil

	case "share":
		ev := base
		ev.Type, ev.Category = "tiktok.share", domain.CategoryContent
		ev.Title = "Shared a TikTok " + firstNonEmpty(strings.ToLower(e.SharedContent), "item")
		if e.Method != "" {
			ev.Title += " via " + e.Method
		}
		ev.ExternalID = id("share", ts, link+"|"+e.SharedContent+"|"+e.Method)
		ev.Targets = []domain.Target{{Relation: domain.RelationShared, TargetSource: sourceID, TargetExternalID: videoID(link), TargetURL: link}}
		meta(ev, "shared_content", e.SharedContent)
		meta(ev, "method", e.Method)
		meta(ev, "link", link)
		return one(ev), nil

	case "login":
		ev := base
		ev.Type, ev.Category = "tiktok.login", domain.CategoryOther
		ev.Title = "Logged in to TikTok"
		ev.ExternalID = id("login", ts, e.IP+"|"+e.DeviceModel+"|"+e.DeviceSystem+"|"+e.NetworkType)
		meta(ev, "ip", e.IP)
		meta(ev, "device_model", e.DeviceModel)
		meta(ev, "device_system", e.DeviceSystem)
		meta(ev, "network_type", e.NetworkType)
		meta(ev, "carrier", e.Carrier)
		return one(ev), nil
	}

	return normalizeUnknown(base, provider.RawRecord{Kind: "unknown", Payload: mustUnknown(raw.Kind, raw.Payload)})
}

func normalizeMessage(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var w dmRecord
	if err := json.Unmarshal(raw.Payload, &w); err != nil {
		return normalizeUnknown(base, provider.RawRecord{Kind: "unknown", Payload: mustUnknown("message", raw.Payload)})
	}
	var m entry
	if err := json.Unmarshal(w.Message, &m); err != nil {
		return normalizeUnknown(base, provider.RawRecord{Kind: "unknown", Payload: mustUnknown("message", raw.Payload)})
	}
	at, ts := parseDate(m.Date)

	ev := base
	ev.Type, ev.Category = "tiktok.message", domain.CategoryMessage
	ev.OccurredAt = at
	ev.Actor = firstNonEmpty(m.From, "self")
	ev.ThreadExternalID = w.Thread
	ev.Title = "Message in chat with " + firstNonEmpty(w.Thread, "unknown")
	ev.Body = m.Content
	ev.ExternalID = id("message", ts, w.Thread+"|"+m.From+"|"+m.Content)
	if strings.HasPrefix(m.Content, "https://") || strings.HasPrefix(m.Content, "http://") {
		meta(ev, "link", m.Content) // shared videos arrive as a bare link
	}
	return one(ev), nil
}

const previewRunes = 240

func normalizeUnknown(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var u unknownRecord
	if err := json.Unmarshal(raw.Payload, &u); err != nil {
		// Not even our own wrapper: keep the bytes as they are.
		u = unknownRecord{Path: []string{"unrecognized record"}}
		if json.Valid(raw.Payload) {
			u.Value = raw.Payload
		} else {
			u.Value, _ = json.Marshal(string(raw.Payload))
		}
	}
	section := strings.Join(u.Path, " / ")
	sum := sha256.Sum256(raw.Payload)

	ev := base
	ev.Type, ev.Category = "tiktok.unknown", domain.CategoryOther
	ev.Title = "Unmapped TikTok data: " + firstNonEmpty(section, "(top level)")
	ev.ExternalID = "unknown:" + hex.EncodeToString(sum[:8])
	var dated struct {
		Date string `json:"Date"`
	}
	if json.Unmarshal(u.Value, &dated) == nil {
		ev.OccurredAt, _ = parseDate(dated.Date)
	}
	meta(ev, "section", section)
	meta(ev, "preview", preview(u.Value))
	ev.Metadata["bytes"] = len(u.Value)
	return one(ev), nil
}

// mustUnknown rewraps a record that turned out not to have the expected shape.
func mustUnknown(kind string, payload []byte) []byte {
	if !json.Valid(payload) {
		payload, _ = json.Marshal(string(payload))
	}
	out, _ := json.Marshal(unknownRecord{Path: []string{"unrecognized " + kind + " record"}, Value: payload})
	return out
}

// ---- helpers ----

// parseDate reads TikTok's "2024-01-31 12:34:56" (UTC; login history appends a
// " UTC" zone token). Unparseable or missing dates ("N/A") leave the event
// undated.
func parseDate(s string) (time.Time, int64) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "UTC"))
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			t = t.UTC()
			return t, t.Unix()
		}
	}
	return time.Time{}, 0
}

// videoID picks the numeric id out of https://www.tiktokv.com/share/video/<id>/,
// falling back to the link itself so the target stays identifiable.
func videoID(link string) string {
	s := link
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	seg := path.Base(strings.TrimRight(s, "/"))
	if seg != "" {
		if _, err := strconv.ParseUint(seg, 10, 64); err == nil {
			return seg
		}
	}
	return link
}

func profileURL(user string) string {
	if user == "" || strings.ContainsAny(user, " /") {
		return ""
	}
	return "https://www.tiktok.com/@" + user
}

// scalar renders a number-or-string member ("Likes": 12 or "12").
func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func preview(b []byte) string {
	s := string(b)
	if utf8.RuneCountInString(s) <= previewRunes {
		return s
	}
	return string([]rune(s)[:previewRunes]) + "…"
}

func id(kind string, ts int64, disc string) string {
	sum := sha256.Sum256([]byte(disc))
	return kind + ":" + strconv.FormatInt(ts, 10) + ":" + hex.EncodeToString(sum[:4])
}

func one(ev domain.Event) []domain.Event { return []domain.Event{ev} }

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
