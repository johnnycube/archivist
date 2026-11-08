// Package instagram ingests a Meta/Instagram data export (JSON). It is a sibling
// of the facebook provider and shares its Meta conventions (providers/internal/meta):
// posts, stories, comments, direct messages, likes, saves, followers/followings
// and the Threads posts shipped in the same export become Events, with Meta's
// UTF-8/latin-1 mojibake repaired via bundle.FixMetaMojibake.
//
// Members are matched by base name, so both the flat legacy layout and the
// current your_instagram_activity/… and connections/… layouts work. List-style
// records are read in their string_list_data / string_map_data shape and in the
// generic label_values shape newer exports use.
package instagram

import (
	"context"
	"encoding/json"
	"path"
	"strconv"
	"strings"

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
	"github.com/johnnycube/archivist/providers/internal/meta"
)

const sourceID = "instagram"

type Provider struct{}

func New() *Provider { return &Provider{} }

var (
	_ provider.BundleProvider    = (*Provider)(nil)
	_ provider.AttachmentMatcher = (*Provider)(nil)
)

func (p *Provider) Descriptor() domain.Descriptor {
	return domain.Descriptor{
		ID:             sourceID,
		DisplayName:    "Instagram",
		IngestionModes: []domain.Mode{domain.ModeBundle},
		EventTypes: []domain.TypeDef{
			{Type: "instagram.post", Category: domain.CategoryContent, HasBody: true},
			{Type: "instagram.thread_post", Category: domain.CategoryContent, HasBody: true},
			{Type: "instagram.comment", Category: domain.CategoryContent, HasBody: true},
			{Type: "instagram.story", Category: domain.CategoryMedia},
			{Type: "instagram.message", Category: domain.CategoryMessage, HasBody: true},
			{Type: "instagram.like", Category: domain.CategoryReaction, NeedsTarget: true},
			{Type: "instagram.save", Category: domain.CategorySave, NeedsTarget: true},
			{Type: "instagram.follow", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "instagram.follower", Category: domain.CategoryConnection},
			{Type: "instagram.follow_request", Category: domain.CategoryConnection},
			{Type: "instagram.unfollow", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "instagram.search", Category: domain.CategoryConsumption, HasBody: true},
			{Type: "instagram.login", Category: domain.CategoryOther},
			{Type: "instagram.unknown", Category: domain.CategoryOther},
		},
	}
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{SupportsBundle: true}
}

// IsAttachment implements provider.AttachmentMatcher (see meta.IsAttachment).
func (p *Provider) IsAttachment(member string) bool { return meta.IsAttachment(member) }

// Extractors maps each known member to a provider-local Kind. Unlisted files are
// captured generically by the importer.
func (p *Provider) Extractors() []provider.Extractor {
	return []provider.Extractor{
		meta.MessageExtractor{},

		meta.Arr("post", numbered("posts")),
		meta.Arr("reel", meta.BaseIs("reels.json"), "ig_reels_media"),
		meta.Arr("archived_post", meta.BaseIs("archived_posts.json"), "ig_archived_post_media"),
		meta.Arr("igtv", meta.BaseIs("igtv_videos.json"), "ig_igtv_media"),
		meta.Arr("story", meta.BaseIs("stories.json"), "ig_stories"),
		meta.Arr("thread_post", meta.BaseIs("threads_and_replies.json"), "text_post_app_text_posts"),
		meta.Arr("comment", numbered("post_comments"), "comments_media_comments"),
		meta.Arr("reel_comment", meta.BaseIs("reels_comments.json"), "comments_reels_comments"),

		meta.Arr("like", meta.BaseIs("liked_posts.json"), "likes_media_likes"),
		meta.Arr("comment_like", meta.BaseIs("liked_comments.json"), "likes_comment_likes"),
		meta.Arr("story_like", meta.BaseIs("story_likes.json"), "story_activities_story_likes"),
		meta.Arr("thread_like", meta.BaseIs("liked_threads.json"), "text_post_app_media_likes"),
		meta.Arr("save", meta.BaseIs("saved_posts.json"), "saved_saved_media"),

		meta.Arr("follow", meta.BaseIs("following.json"), "relationships_following"),
		meta.Arr("follower", numbered("followers"), "relationships_followers"),
		meta.Arr("close_friend", meta.BaseIs("close_friends.json"), "relationships_close_friends"),
		meta.Arr("follow_request_sent", meta.BaseIs("pending_follow_requests.json"), "relationships_follow_requests_sent"),
		meta.Arr("follow_request_received", meta.BaseIs("follow_requests_you've_received.json", "follow_requests_received.json"), "relationships_follow_requests_received"),
		meta.Arr("unfollow", meta.BaseIs("recently_unfollowed_profiles.json", "recently_unfollowed_accounts.json"), "relationships_unfollowed_users"),
		meta.Arr("block", meta.BaseIs("blocked_profiles.json", "blocked_accounts.json"), "relationships_blocked_users"),

		// logged_information/recent_searches/…: {"Search": {value}, "Time": {timestamp}}
		meta.Arr("search", meta.BaseIs("profile_searches.json", "word_or_phrase_searches.json", "account_searches.json"), "searches_user", "searches_keyword"),
		// security_and_login_information/login_and_profile_creation/…
		meta.Arr("login", meta.BaseIs("login_activity.json"), "account_history_login_history"),
		meta.Arr("logout", meta.BaseIs("logout_activity.json"), "account_history_logout_history"),
	}
}

// numbered matches <stem>_<N>.json, the way Meta splits long lists. A plain
// prefix would also claim unrelated members (posts_viewed.json), whose records
// sit under keys the extractor does not stream.
func numbered(stem string) func(string) bool {
	return func(member string) bool {
		b := strings.ToLower(path.Base(member))
		n, ok := strings.CutPrefix(b, stem+"_")
		if !ok {
			return false
		}
		n, ok = strings.CutSuffix(n, ".json")
		if !ok || n == "" {
			return false
		}
		_, err := strconv.ParseUint(n, 10, 32)
		return err == nil
	}
}

// ---- on-disk shapes ----

type strListItem struct {
	Href      string `json:"href"`
	Value     string `json:"value"`
	Timestamp int64  `json:"timestamp"`
}

// post is a feed post, and equally a Threads post: text in title, files in media.
type post struct {
	Media             []meta.Media `json:"media"`
	Title             string       `json:"title"`
	CreationTimestamp int64        `json:"creation_timestamp"`
}

// entry is a list-style record (like, save, follow, follower, comment) in any of
// the three shapes Meta writes it in.
type entry struct {
	Title          string                 `json:"title"`
	StringListData []strListItem          `json:"string_list_data"`
	StringMapData  map[string]strListItem `json:"string_map_data"`
	Timestamp      int64                  `json:"timestamp"`
	LabelValues    []meta.LabelValue      `json:"label_values"`
}

// ref is what a list-style record points at.
type ref struct {
	name  string // the record's title: usually the account concerned
	value string
	href  string
	ts    int64
}

func (e entry) ref() (ref, bool) {
	switch {
	case len(e.StringListData) > 0:
		it := e.StringListData[0]
		return ref{name: e.Title, value: it.Value, href: it.Href, ts: it.Timestamp}, true
	case len(e.StringMapData) > 0:
		it, ok := e.StringMapData["Saved on"]
		if !ok {
			it = timed(e.StringMapData)
		}
		return ref{name: e.Title, value: it.Value, href: it.Href, ts: it.Timestamp}, true
	case len(e.LabelValues) > 0:
		r := ref{ts: e.Timestamp}
		r.name = meta.FirstNonEmpty(e.Title, meta.LabelFind(e.LabelValues, "Username"), meta.LabelFind(e.LabelValues, "Name"))
		for _, lv := range e.LabelValues {
			if lv.Label == "URL" {
				r.href = meta.FirstNonEmpty(lv.Href, lv.Value)
				break
			}
		}
		return r, true
	}
	return ref{}, false
}

// timed returns the entry of a string_map_data record that carries its date.
func timed(m map[string]strListItem) strListItem {
	if it, ok := m["Time"]; ok && it.Timestamp > 0 {
		return it
	}
	var out strListItem
	for _, it := range m {
		if it.Timestamp > out.Timestamp {
			out = it
		}
	}
	return out
}

// ---- normalization ----

func (p *Provider) Normalize(_ context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	base := domain.Event{SourceID: sourceID, Actor: "self", Metadata: map[string]any{}}

	switch raw.Kind {
	case bundle.SkippedArrayKind:
		ev, err := bundle.SkippedArrayEvent(sourceID, raw)
		return meta.One(ev), err
	case meta.MessageKind:
		return meta.Messages{EventType: "instagram.message", Extras: true}.Normalize(base, raw)
	case "post", "thread_post", "reel", "archived_post", "igtv":
		return normalizePost(base, raw)
	case "story":
		return normalizeStory(base, raw)
	case "comment", "reel_comment":
		return normalizeComment(base, raw)
	case "like", "comment_like", "story_like", "thread_like", "save", "follow", "follower",
		"close_friend", "follow_request_sent", "follow_request_received", "unfollow", "block":
		return normalizeEntry(base, raw)
	case "search":
		return normalizeSearch(base, raw)
	case "login", "logout":
		return normalizeLogin(base, raw)
	}
	return unknown(raw), nil
}

func normalizePost(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var po post
	if err := json.Unmarshal(raw.Payload, &po); err != nil {
		return unknown(raw), nil
	}
	ev := base
	ev.Type, ev.Category = "instagram.post", domain.CategoryContent
	ev.Title = "Instagram post"
	ev.Body = bundle.FixMetaMojibake(meta.FirstNonEmpty(po.Title, mediaTitle(po.Media)))
	ts, uri := po.CreationTimestamp, ""
	if len(po.Media) > 0 {
		uri = po.Media[0].URI
		if ts == 0 {
			ts = po.Media[0].Created
		}
	}
	for _, m := range po.Media {
		if m.URI != "" {
			ev.Media = append(ev.Media, domain.Media{BlobKey: m.URI})
		}
	}
	ev.OccurredAt = meta.UnixSec(ts)
	ev.ExternalID = "post:" + meta.FirstNonEmpty(uri, strconv.FormatInt(ts, 10))
	switch raw.Kind {
	case "reel":
		ev.Title = "Instagram reel"
		meta.Set(ev, "via", "reels")
	case "igtv":
		ev.Title = "IGTV video"
		meta.Set(ev, "via", "igtv")
	case "archived_post":
		ev.Metadata["archived"] = true
	}
	if raw.Kind == "thread_post" {
		// Threads posts are mostly text, so the file cannot identify them.
		ev.Type, ev.Title = "instagram.thread_post", "Threads post"
		ev.ExternalID = meta.ID("thread_post", ts, ev.Body+uri)
	}
	return meta.One(ev), nil
}

func normalizeStory(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var st meta.Media
	if err := json.Unmarshal(raw.Payload, &st); err != nil {
		return unknown(raw), nil
	}
	ev := base
	ev.Type, ev.Category = "instagram.story", domain.CategoryMedia
	ev.OccurredAt = meta.UnixSec(st.Created)
	ev.Title = "Instagram story"
	ev.Body = bundle.FixMetaMojibake(st.Title)
	if st.URI != "" {
		ev.Media = []domain.Media{{BlobKey: st.URI}}
	}
	ev.ExternalID = meta.ID("story", st.Created, st.URI+st.Title)
	return meta.One(ev), nil
}

// normalizeComment reads the string_map_data shape: "Comment", "Media Owner" and
// "Time". Anything else in the map is kept as metadata.
func normalizeComment(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var e entry
	if err := json.Unmarshal(raw.Payload, &e); err != nil || len(e.StringMapData) == 0 {
		return unknown(raw), nil
	}
	ts := timed(e.StringMapData).Timestamp
	owner := bundle.FixMetaMojibake(e.StringMapData["Media Owner"].Value)

	ev := base
	ev.Type, ev.Category = "instagram.comment", domain.CategoryContent
	ev.OccurredAt = meta.UnixSec(ts)
	ev.Body = bundle.FixMetaMojibake(e.StringMapData["Comment"].Value)
	on := "post"
	if raw.Kind == "reel_comment" {
		on = "reel"
		meta.Set(ev, "via", "reels")
	}
	ev.Title = "Commented on a " + on
	if owner != "" {
		ev.Title = "Commented on " + owner + "'s " + on
	}
	meta.Set(ev, "media_owner", owner)
	for k, it := range e.StringMapData {
		if k != "Comment" && k != "Media Owner" && it.Value != "" {
			meta.Set(ev, bundle.FixMetaMojibake(k), bundle.FixMetaMojibake(it.Value))
		}
	}
	ev.ExternalID = meta.ID("comment", ts, ev.Body+"|"+owner)
	return meta.One(ev), nil
}

// normalizeEntry maps the list-style records. Likes of comments, stories and
// threads get their own id space: a liked comment links to the post it is on,
// so its href may equal that of a like of the post itself.
func normalizeEntry(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var e entry
	if err := json.Unmarshal(raw.Payload, &e); err != nil {
		return unknown(raw), nil
	}
	r, ok := e.ref()
	if !ok {
		return unknown(raw), nil
	}
	name := bundle.FixMetaMojibake(r.name)

	ev := base
	ev.OccurredAt = meta.UnixSec(r.ts)
	target := domain.Target{TargetSource: sourceID, TargetExternalID: r.name, TargetURL: r.href}

	switch raw.Kind {
	case "like":
		ev.Type, ev.Category = "instagram.like", domain.CategoryReaction
		ev.Title = "Liked " + name
		ev.ExternalID = "like:" + meta.FirstNonEmpty(r.href, r.name)
		target.Relation = domain.RelationLiked

	case "comment_like", "story_like", "thread_like":
		via := strings.TrimSuffix(raw.Kind, "_like")
		ev.Type, ev.Category = "instagram.like", domain.CategoryReaction
		ev.Title = map[string]string{
			"comment": "Liked a comment by ",
			"story":   "Liked a story by ",
			"thread":  "Liked a thread by ",
		}[via] + name
		if name == "" {
			ev.Title = "Liked a " + via
		}
		if via == "thread" {
			via = "threads"
		}
		meta.Set(ev, "via", via)
		ev.ExternalID = meta.ID(raw.Kind, r.ts, r.href+"|"+r.name)
		target.Relation = domain.RelationLiked

	case "save":
		ev.Type, ev.Category = "instagram.save", domain.CategorySave
		ev.Title = "Saved " + name
		ev.ExternalID = "save:" + meta.FirstNonEmpty(r.href, r.name)
		target.Relation = domain.RelationSaved

	case "follow", "follower", "close_friend", "follow_request_sent", "follow_request_received", "unfollow", "block":
		// Older exports name the account in value, newer ones in title.
		name = bundle.FixMetaMojibake(meta.FirstNonEmpty(r.value, r.name))
		account := meta.FirstNonEmpty(r.href, name)
		ev.Type, ev.Category = "instagram.follow", domain.CategoryConnection
		ev.Title = "Followed " + name
		ev.ExternalID = "follow:" + account
		target.Relation, target.TargetExternalID = domain.RelationFollowed, name
		meta.Set(ev, "profile", r.href)
		switch raw.Kind {
		case "follower", "follow_request_received":
			ev.Actor = meta.FirstNonEmpty(name, "self")
			ev.Type, ev.Title = "instagram.follower", name+" followed you"
			if raw.Kind == "follow_request_received" {
				ev.Type, ev.Title = "instagram.follow_request", name+" requested to follow you"
				meta.Set(ev, "direction", "received")
			}
			ev.ExternalID = meta.ID(raw.Kind, r.ts, account)
			return meta.One(ev), nil // the target is the account owner
		case "close_friend":
			ev.Title = "Added " + name + " to close friends"
			ev.ExternalID = "close_friend:" + account
			meta.Set(ev, "list", "close_friends")
		case "follow_request_sent":
			ev.Type, ev.Title = "instagram.follow_request", "Requested to follow "+name
			ev.ExternalID = "follow_request:" + account
			meta.Set(ev, "direction", "sent")
		case "unfollow", "block":
			ev.Type, ev.Title = "instagram.unfollow", "Unfollowed "+name
			ev.ExternalID = meta.ID(raw.Kind, r.ts, account)
			meta.Set(ev, "status", "unfollowed")
			if raw.Kind == "block" {
				ev.Title = "Blocked " + name
				meta.Set(ev, "status", "blocked")
			}
		}
	}
	ev.Targets = []domain.Target{target}
	return meta.One(ev), nil
}

// normalizeSearch reads logged_information/recent_searches/*: string_map_data
// with "Search" (the text or account searched for) and "Time".
func normalizeSearch(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var e entry
	if err := json.Unmarshal(raw.Payload, &e); err != nil || len(e.StringMapData) == 0 {
		return unknown(raw), nil
	}
	ts := timed(e.StringMapData).Timestamp
	term := bundle.FixMetaMojibake(meta.FirstNonEmpty(e.StringMapData["Search"].Value, e.StringMapData["Search"].Href))
	ev := base
	ev.Type, ev.Category = "instagram.search", domain.CategoryConsumption
	ev.OccurredAt = meta.UnixSec(ts)
	ev.Title = "Searched Instagram"
	ev.Body = term
	meta.Set(ev, "kind", strings.TrimSuffix(strings.ToLower(path.Base(raw.Member)), ".json"))
	for k, it := range e.StringMapData {
		if k != "Search" && k != "Time" && it.Value != "" {
			meta.Set(ev, bundle.FixMetaMojibake(k), bundle.FixMetaMojibake(it.Value))
		}
	}
	ev.ExternalID = meta.ID("search", ts, term)
	return meta.One(ev), nil
}

// normalizeLogin reads security_and_login_information/login_and_profile_creation/
// login_activity.json and logout_activity.json: string_map_data with "IP
// Address", "Time", "User Agent", "Language Code", "Cookie Name", "Port".
func normalizeLogin(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var e entry
	if err := json.Unmarshal(raw.Payload, &e); err != nil || len(e.StringMapData) == 0 {
		return unknown(raw), nil
	}
	ts := timed(e.StringMapData).Timestamp
	ip := e.StringMapData["IP Address"].Value
	ua := e.StringMapData["User Agent"].Value
	ev := base
	ev.Type, ev.Category = "instagram.login", domain.CategoryOther
	ev.OccurredAt = meta.UnixSec(ts)
	ev.Title = "Logged in to Instagram"
	meta.Set(ev, "action", "login")
	if raw.Kind == "logout" {
		ev.Title = "Logged out of Instagram"
		meta.Set(ev, "action", "logout")
	}
	meta.Set(ev, "ip", ip)
	meta.Set(ev, "user_agent", ua)
	meta.Set(ev, "language", e.StringMapData["Language Code"].Value)
	meta.Set(ev, "port", e.StringMapData["Port"].Value)
	ev.ExternalID = meta.ID(raw.Kind, ts, ip+"|"+ua)
	return meta.One(ev), nil
}

// unknown keeps a record whose shape was not recognized; the id is derived from
// its content so that several of them stay distinct.
func unknown(raw provider.RawRecord) []domain.Event {
	ev := meta.Unknown(sourceID, raw)
	ev.Title = "Unrecognized Instagram record"
	return meta.One(ev)
}

// ---- helpers ----

func mediaTitle(m []meta.Media) string {
	if len(m) > 0 {
		return m[0].Title
	}
	return ""
}
