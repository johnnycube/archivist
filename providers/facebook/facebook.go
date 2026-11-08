// Package facebook ingests a Meta/Facebook data export (JSON). It is a sibling of
// the instagram provider: same Meta JSON conventions and the same UTF-8/latin-1
// mojibake repair, mapping posts, comments, reactions, messages, connections,
// events, searches, photos and account activity to Events.
//
// Facebook's export file names and top-level shapes vary (arrays vs objects keyed
// by *_v2, flat vs your_facebook_activity/… layouts), so extraction is defensive:
// members are matched by base name, the record array is located by trying
// several keys, and normalization uses permissive structs. Settings and ad
// profiling in Meta's generic label_values format stay facebook.unknown.
package facebook

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
	"github.com/johnnycube/archivist/providers/internal/meta"
)

const sourceID = "facebook"

type Provider struct{}

func New() *Provider { return &Provider{} }

var (
	_ provider.BundleProvider    = (*Provider)(nil)
	_ provider.AttachmentMatcher = (*Provider)(nil)
)

func (p *Provider) Descriptor() domain.Descriptor {
	return domain.Descriptor{
		ID:             sourceID,
		DisplayName:    "Facebook",
		IngestionModes: []domain.Mode{domain.ModeBundle},
		Maturity:       domain.MaturityVerified, // developed against a real export
		EventTypes: []domain.TypeDef{
			{Type: "facebook.post", Category: domain.CategoryContent, HasBody: true},
			{Type: "facebook.comment", Category: domain.CategoryContent, HasBody: true},
			{Type: "facebook.app_post", Category: domain.CategoryContent},
			{Type: "facebook.message", Category: domain.CategoryMessage, HasBody: true},
			{Type: "facebook.reaction", Category: domain.CategoryReaction, NeedsTarget: true},
			{Type: "facebook.friend", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "facebook.friend_request", Category: domain.CategoryConnection},
			{Type: "facebook.follow", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "facebook.unfollow", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "facebook.group_join", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "facebook.event", Category: domain.CategoryConnection},
			{Type: "facebook.app", Category: domain.CategoryConnection},
			{Type: "facebook.search", Category: domain.CategoryConsumption, HasBody: true},
			{Type: "facebook.photo", Category: domain.CategoryMedia},
			{Type: "facebook.profile_update", Category: domain.CategoryProfile},
			{Type: "facebook.login", Category: domain.CategoryOther},
			{Type: "facebook.unknown", Category: domain.CategoryOther},
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
		unfollowEx{},

		meta.Arr("post", meta.BasePrefix("your_posts"), "status_updates", "posts"),
		meta.Arr("group_post", meta.BaseIs("group_posts_and_comments.json"), "group_posts_v2"),
		meta.Arr("comment", meta.BaseIs("comments.json", "your_comments_in_groups.json"), "comments_v2", "comments", "group_comments_v2"),
		meta.Arr("app_post", meta.BaseIs("posts_from_apps_and_websites.json"), "app_posts_v2"),

		meta.Arr("reaction", func(m string) bool {
			return meta.BaseIs("posts_and_comments.json")(m) || meta.BasePrefix("likes_and_reactions_")(m)
		}, "reactions_v2", "reactions"),
		meta.Arr("reaction_lv", meta.BaseIs("likes_and_reactions.json")),

		meta.Arr("friend", meta.BaseIs("friends.json", "your_friends.json"), "friends_v2", "friends"),
		meta.Arr("friend_request_sent", meta.BaseIs("sent_friend_requests.json"), "sent_requests_v2"),
		meta.Arr("friend_request_received", meta.BaseIs("received_friend_requests.json"), "received_requests_v2"),
		meta.Arr("friend_request_rejected", meta.BaseIs("rejected_friend_requests.json"), "rejected_requests_v2"),
		meta.Arr("follow", meta.BaseIs("who_you've_followed.json", "pages_and_profiles_you_follow.json"), "following_v3", "following_v2", "pages_followed_v2"),
		meta.Arr("page_like", meta.BaseIs("pages_you've_liked.json"), "page_likes_v2"),
		meta.Arr("group_join", meta.BaseIs("your_group_membership_activity.json"), "groups_joined_v2"),
		meta.Arr("app", meta.BaseIs("connected_apps_and_websites.json"), "installed_apps_v2"),

		meta.Arr("event_invited", meta.BaseIs("event_invitations.json"), "events_invited_v2"),
		meta.Arrays{Matches: meta.BaseIs("your_event_responses.json"), Kinds: map[string]string{
			"events_joined": "event_joined", "events_declined": "event_declined", "events_interested": "event_interested",
		}},

		meta.Arr("search", meta.BaseIs("your_search_history.json"), "searches_v2"),
		meta.Arr("photo", func(m string) bool {
			return meta.BaseIs("your_uncategorized_photos.json")(m) || (meta.InDir(m, "album") && strings.HasSuffix(m, ".json"))
		}, "photos", "other_photos_v2"),

		meta.Arr("profile_update", meta.BaseIs("profile_update_history.json"), "profile_updates_v2"),
		meta.Arr("admin_record", meta.BaseIs("record_details.json"), "admin_records_v2"),
		meta.Arr("login", meta.BaseIs("account_activity.json", "logins_and_logouts.json"), "account_activity_v2", "account_accesses_v2"),
	}
}

// ---- extraction ----

// unfollowEx reads unfollowed_profiles.json, which is one flat label_values
// list alternating a "Name" entry and its "Time".
type unfollowEx struct{}

func (unfollowEx) Match(member string) bool { return meta.BaseIs("unfollowed_profiles.json")(member) }

func (unfollowEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	var name string
	return bundle.StreamJSONArraysReport(r, []string{"label_values"}, func(_ string, el json.RawMessage) error {
		var lv labelValue
		if err := json.Unmarshal(el, &lv); err != nil {
			return err
		}
		if lv.TimestampValue == 0 {
			name = meta.FirstNonEmpty(lv.Value, name)
			return nil
		}
		if name == "" {
			return nil
		}
		payload, err := json.Marshal(fbEntry{Name: name, Timestamp: lv.TimestampValue})
		if err != nil {
			return err
		}
		name = ""
		return emit(meta.Rec("unfollow", payload))
	}, bundle.ReportSkipped(member, emit))
}

// ---- normalization ----

type fbMedia = meta.Media

type fbData struct {
	Post    string `json:"post,omitempty"`
	Text    string `json:"text,omitempty"`
	Name    string `json:"name,omitempty"`
	Comment struct {
		Comment string `json:"comment"`
		Author  string `json:"author"`
		Group   string `json:"group"`
	} `json:"comment"`
	Reaction struct {
		Reaction string `json:"reaction"`
		Actor    string `json:"actor"`
	} `json:"reaction"`
	Media           *fbMedia `json:"media,omitempty"`
	ExternalContext struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"external_context"`
}

// fbEntry is the permissive shape shared by most timestamped Facebook records.
type fbEntry struct {
	Timestamp   int64    `json:"timestamp"`
	Title       string   `json:"title,omitempty"`
	Name        string   `json:"name,omitempty"`
	URL         string   `json:"url,omitempty"`
	Data        []fbData `json:"data,omitempty"`
	Attachments []struct {
		Data []fbData `json:"data"`
	} `json:"attachments,omitempty"`
}

type labelValue = meta.LabelValue

func (p *Provider) Normalize(_ context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	base := domain.Event{SourceID: sourceID, Actor: "self", Metadata: map[string]any{}}

	switch raw.Kind {
	case bundle.SkippedArrayKind:
		ev, err := bundle.SkippedArrayEvent(sourceID, raw)
		return meta.One(ev), err
	case meta.MessageKind:
		return meta.Messages{EventType: "facebook.message"}.Normalize(base, raw)
	case "reaction_lv":
		return normalizeLabelReaction(base, raw)
	case "event_invited", "event_joined", "event_declined", "event_interested":
		return normalizeEvent(base, raw)
	case "photo":
		return normalizePhoto(base, raw)
	case "app":
		return normalizeApp(base, raw)
	case "login", "admin_record":
		return normalizeAccount(base, raw)
	}

	var e fbEntry
	if err := json.Unmarshal(raw.Payload, &e); err != nil {
		return nil, err
	}
	base.OccurredAt = meta.UnixSec(e.Timestamp)
	title := bundle.FixMetaMojibake(e.Title)
	name := bundle.FixMetaMojibake(meta.FirstNonEmpty(e.Name, dataName(e.Data), title))

	switch raw.Kind {
	case "post", "group_post":
		ev := base
		ev.Type, ev.Category = "facebook.post", domain.CategoryContent
		ev.Body = bundle.FixMetaMojibake(meta.FirstNonEmpty(dataPost(e.Data), e.Title))
		ev.Title = "Facebook post"
		disc := ev.Body
		for _, m := range attachedMedia(e) {
			ev.Media = append(ev.Media, domain.Media{BlobKey: m.URI})
		}
		if dataPost(e.Data) == "" && len(ev.Media) > 0 {
			// Photo-only posts have no text of their own, only a generic title
			// ("… added a new photo."); without the attachment two of them in
			// the same second would collapse into one event.
			disc += ev.Media[0].BlobKey
		}
		if raw.Kind == "group_post" {
			ev.Title = meta.FirstNonEmpty(title, ev.Title)
			meta.Set(ev, "context", "group")
		}
		meta.Set(ev, "link", attachedLink(e))
		ev.ExternalID = meta.ID("post", e.Timestamp, disc)
		return meta.One(ev), nil

	case "comment":
		ev := base
		ev.Type, ev.Category = "facebook.comment", domain.CategoryContent
		ev.Body = bundle.FixMetaMojibake(dataComment(e.Data))
		ev.Title = meta.FirstNonEmpty(title, "Comment")
		ev.ExternalID = meta.ID("comment", e.Timestamp, ev.Body+ev.Title)
		meta.Set(ev, "group", bundle.FixMetaMojibake(dataGroup(e.Data)))
		return meta.One(ev), nil

	case "app_post":
		ev := base
		ev.Type, ev.Category = "facebook.app_post", domain.CategoryContent
		ev.Title = meta.FirstNonEmpty(title, "Post from an app")
		ev.ExternalID = meta.ID("app_post", e.Timestamp, title)
		meta.Set(ev, "app", bundle.FixMetaMojibake(attachedContext(e)))
		return meta.One(ev), nil

	case "reaction":
		ev := base
		ev.Type, ev.Category = "facebook.reaction", domain.CategoryReaction
		ev.Title = meta.FirstNonEmpty(title, "Reacted")
		ev.ExternalID = meta.ID("reaction", e.Timestamp, title)
		meta.Set(ev, "reaction", dataReaction(e.Data))
		ev.Targets = []domain.Target{{Relation: domain.RelationLiked, TargetSource: sourceID, TargetExternalID: title}}
		return meta.One(ev), nil

	case "friend":
		ev := base
		ev.Type, ev.Category = "facebook.friend", domain.CategoryConnection
		ev.Title = "Friended " + name
		ev.ExternalID = meta.ID("friend", e.Timestamp, name)
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: sourceID, TargetExternalID: name}}
		return meta.One(ev), nil

	case "friend_request_sent", "friend_request_received", "friend_request_rejected":
		direction := strings.TrimPrefix(raw.Kind, "friend_request_")
		ev := base
		ev.Type, ev.Category = "facebook.friend_request", domain.CategoryConnection
		ev.Title = map[string]string{
			"sent":     "Sent a friend request to ",
			"received": "Friend request from ",
			"rejected": "Rejected a friend request from ",
		}[direction] + name
		ev.ExternalID = meta.ID(raw.Kind, e.Timestamp, name)
		meta.Set(ev, "direction", direction)
		meta.Set(ev, "name", name)
		return meta.One(ev), nil

	case "follow", "page_like", "unfollow":
		ev := base
		ev.Type, ev.Category = "facebook.follow", domain.CategoryConnection
		ev.Title = "Followed " + name
		rel := domain.RelationFollowed
		switch raw.Kind {
		case "page_like":
			ev.Title, rel = "Liked the page "+name, domain.RelationLiked
			meta.Set(ev, "via", "page_like")
		case "unfollow":
			ev.Type, ev.Title = "facebook.unfollow", "Unfollowed "+name
		}
		ev.ExternalID = meta.ID(raw.Kind, e.Timestamp, name)
		ev.Targets = []domain.Target{{Relation: rel, TargetSource: sourceID, TargetExternalID: name, TargetURL: e.URL}}
		return meta.One(ev), nil

	case "group_join":
		group := bundle.FixMetaMojibake(meta.FirstNonEmpty(dataName(e.Data), e.Name))
		ev := base
		ev.Type, ev.Category = "facebook.group_join", domain.CategoryConnection
		// The export's own sentence also covers leaving a group, so prefer it.
		ev.Title = meta.FirstNonEmpty(title, "Joined the group "+group)
		ev.ExternalID = meta.ID("group_join", e.Timestamp, title+group)
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: sourceID, TargetExternalID: group}}
		return meta.One(ev), nil

	case "search":
		ev := base
		ev.Type, ev.Category = "facebook.search", domain.CategoryConsumption
		ev.Body = bundle.FixMetaMojibake(meta.FirstNonEmpty(dataText(e.Data), attachedText(e)))
		ev.Title = "Searched Facebook"
		ev.ExternalID = meta.ID("search", e.Timestamp, ev.Body)
		return meta.One(ev), nil

	case "profile_update":
		ev := base
		ev.Type, ev.Category = "facebook.profile_update", domain.CategoryProfile
		ev.Title = meta.FirstNonEmpty(title, "Updated profile")
		for _, m := range attachedMedia(e) {
			ev.Media = append(ev.Media, domain.Media{BlobKey: m.URI})
		}
		ev.ExternalID = meta.ID("profile_update", e.Timestamp, title)
		return meta.One(ev), nil
	}

	ev := base
	ev.Type, ev.Category = "facebook.unknown", domain.CategoryOther
	ev.ExternalID = "unknown:" + raw.ExternalID
	ev.Title = "Unrecognized Facebook record"
	return meta.One(ev), nil
}

// normalizeLabelReaction handles likes_and_reactions.json, which newer exports
// write in the label_values format rather than as reactions_v2 records.
func normalizeLabelReaction(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var r struct {
		Timestamp   int64        `json:"timestamp"`
		FBID        string       `json:"fbid"`
		LabelValues []labelValue `json:"label_values"`
	}
	if err := json.Unmarshal(raw.Payload, &r); err != nil {
		return nil, err
	}
	var reaction, url, name string
	for _, lv := range r.LabelValues {
		switch {
		case lv.Label == "Reaction":
			reaction = lv.Value
		case lv.Label == "URL":
			url = meta.FirstNonEmpty(lv.Href, lv.Value)
		case lv.Label == "Name" || lv.Label == "Title":
			name = meta.FirstNonEmpty(name, lv.Value)
		case len(lv.Dict) > 0: // Owner / Author / Group of the thing reacted to
			name = meta.FirstNonEmpty(name, meta.LabelFind(lv.Dict, "Name"))
		}
	}
	name = bundle.FixMetaMojibake(name)

	ev := base
	ev.Type, ev.Category = "facebook.reaction", domain.CategoryReaction
	ev.OccurredAt = meta.UnixSec(r.Timestamp)
	ev.Title = "Reacted"
	if name != "" {
		ev.Title = "Reacted to " + name
	}
	ev.ExternalID = meta.ID("reaction", r.Timestamp, meta.FirstNonEmpty(r.FBID, url, name))
	meta.Set(ev, "reaction", strings.ToUpper(reaction))
	ev.Targets = []domain.Target{{Relation: domain.RelationLiked, TargetSource: sourceID, TargetExternalID: meta.FirstNonEmpty(url, name, r.FBID), TargetURL: url}}
	return meta.One(ev), nil
}

// normalizeEvent dates an RSVP by the event's start: the export does not record
// when the response was given.
func normalizeEvent(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var v struct {
		Name  string `json:"name"`
		Start int64  `json:"start_timestamp"`
		End   int64  `json:"end_timestamp"`
	}
	if err := json.Unmarshal(raw.Payload, &v); err != nil {
		return nil, err
	}
	rsvp := strings.TrimPrefix(raw.Kind, "event_")
	name := bundle.FixMetaMojibake(v.Name)

	ev := base
	ev.Type, ev.Category = "facebook.event", domain.CategoryConnection
	ev.OccurredAt = meta.UnixSec(v.Start)
	ev.Title = map[string]string{
		"invited":    "Invited to ",
		"joined":     "Going to ",
		"declined":   "Declined ",
		"interested": "Interested in ",
	}[rsvp] + name
	ev.ExternalID = meta.ID(raw.Kind, v.Start, name)
	meta.Set(ev, "rsvp", rsvp)
	meta.Set(ev, "event", name)
	if v.End > 0 {
		meta.Set(ev, "ends_at", meta.UnixSec(v.End).Format(time.RFC3339))
	}
	return meta.One(ev), nil
}

func normalizePhoto(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var ph fbMedia
	if err := json.Unmarshal(raw.Payload, &ph); err != nil {
		return nil, err
	}
	ev := base
	ev.Type, ev.Category = "facebook.photo", domain.CategoryMedia
	ev.OccurredAt = meta.UnixSec(ph.Created)
	ev.Title = meta.FirstNonEmpty(bundle.FixMetaMojibake(ph.Title), "Photo")
	ev.Body = bundle.FixMetaMojibake(ph.Description)
	if ph.URI != "" {
		ev.Media = []domain.Media{{BlobKey: ph.URI}}
	}
	ev.ExternalID = meta.ID("photo", ph.Created, ph.URI)
	return meta.One(ev), nil
}

func normalizeApp(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var a struct {
		Name     string `json:"name"`
		Category string `json:"category"`
		Added    int64  `json:"added_timestamp"`
		Removed  int64  `json:"removed_timestamp"`
	}
	if err := json.Unmarshal(raw.Payload, &a); err != nil {
		return nil, err
	}
	name := bundle.FixMetaMojibake(a.Name)
	ev := base
	ev.Type, ev.Category = "facebook.app", domain.CategoryConnection
	ev.OccurredAt = meta.UnixSec(a.Added)
	ev.Title = "Connected the app " + name
	ev.ExternalID = meta.ID("app", a.Added, name)
	meta.Set(ev, "app", name)
	meta.Set(ev, "status", a.Category)
	if a.Removed > 0 {
		meta.Set(ev, "removed_at", meta.UnixSec(a.Removed).Format(time.RFC3339))
		if a.Added == 0 {
			// Removed apps often keep only their removal date.
			ev.OccurredAt, ev.Title = meta.UnixSec(a.Removed), "Removed the app "+name
		}
	}
	return meta.One(ev), nil
}

// normalizeAccount covers the security logs. record_details.json mixes security
// checkpoints with changes to name/email/phone; the latter carry extra_info and
// are profile updates.
func normalizeAccount(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var a struct {
		Action    string `json:"action"`
		Event     string `json:"event"`
		Timestamp int64  `json:"timestamp"`
		IP        string `json:"ip_address"`
		UserAgent string `json:"user_agent"`
		City      string `json:"city"`
		Region    string `json:"region"`
		Country   string `json:"country"`
		Site      string `json:"site"`
		SiteName  string `json:"site_name"`
		Session   struct {
			Created   int64  `json:"created_timestamp"`
			IP        string `json:"ip_address"`
			UserAgent string `json:"user_agent"`
		} `json:"session"`
		ExtraInfo map[string]any `json:"extra_info"`
	}
	if err := json.Unmarshal(raw.Payload, &a); err != nil {
		return nil, err
	}
	action := bundle.FixMetaMojibake(meta.FirstNonEmpty(a.Action, a.Event))
	ts := a.Timestamp
	if ts == 0 {
		ts = a.Session.Created
	}
	ip := meta.FirstNonEmpty(a.IP, a.Session.IP)

	ev := base
	ev.Type, ev.Category = "facebook.login", domain.CategoryOther
	ev.OccurredAt = meta.UnixSec(ts)
	ev.Title = meta.FirstNonEmpty(action, "Account activity")
	ev.ExternalID = meta.ID("login", ts, action+"|"+ip)
	if len(a.ExtraInfo) > 0 {
		ev.Type, ev.Category = "facebook.profile_update", domain.CategoryProfile
		ev.ExternalID = meta.ID("profile_update", ts, action)
		for k, v := range a.ExtraInfo {
			if s, ok := v.(string); ok {
				meta.Set(ev, k, bundle.FixMetaMojibake(s))
			}
		}
	}
	meta.Set(ev, "action", action)
	meta.Set(ev, "ip", ip)
	meta.Set(ev, "user_agent", meta.FirstNonEmpty(a.UserAgent, a.Session.UserAgent))
	meta.Set(ev, "site", meta.FirstNonEmpty(a.Site, a.SiteName))
	meta.Set(ev, "location", bundle.FixMetaMojibake(joinNonEmpty(", ", a.City, a.Region, a.Country)))
	return meta.One(ev), nil
}

// ---- helpers ----

func dataPost(d []fbData) string {
	for _, x := range d {
		if x.Post != "" {
			return x.Post
		}
	}
	return ""
}

func dataComment(d []fbData) string {
	for _, x := range d {
		if x.Comment.Comment != "" {
			return x.Comment.Comment
		}
	}
	return ""
}

func dataGroup(d []fbData) string {
	for _, x := range d {
		if x.Comment.Group != "" {
			return x.Comment.Group
		}
	}
	return ""
}

func dataReaction(d []fbData) string {
	for _, x := range d {
		if x.Reaction.Reaction != "" {
			return x.Reaction.Reaction
		}
	}
	return ""
}

func dataText(d []fbData) string {
	for _, x := range d {
		if x.Text != "" {
			return x.Text
		}
	}
	return ""
}

func dataName(d []fbData) string {
	for _, x := range d {
		if x.Name != "" {
			return x.Name
		}
	}
	return ""
}

func attachedMedia(e fbEntry) []fbMedia {
	var out []fbMedia
	for _, a := range e.Attachments {
		for _, d := range a.Data {
			if d.Media != nil && d.Media.URI != "" {
				out = append(out, *d.Media)
			}
		}
	}
	return out
}

func attachedLink(e fbEntry) string {
	for _, a := range e.Attachments {
		for _, d := range a.Data {
			if d.ExternalContext.URL != "" {
				return d.ExternalContext.URL
			}
		}
	}
	return ""
}

func attachedContext(e fbEntry) string {
	for _, a := range e.Attachments {
		for _, d := range a.Data {
			if d.ExternalContext.Name != "" {
				return d.ExternalContext.Name
			}
		}
	}
	return ""
}

func attachedText(e fbEntry) string {
	for _, a := range e.Attachments {
		if t := dataText(a.Data); t != "" {
			return t
		}
	}
	return ""
}

func joinNonEmpty(sep string, vals ...string) string {
	var out []string
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return strings.Join(out, sep)
}
