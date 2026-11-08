// Package reddit ingests a Reddit GDPR data export — a ZIP of CSV files, one per
// data type — and maps every row to a canonical Event.
//
// Reddit's developer API can't produce full history (listings cap at ~1000 items
// and omit votes/saves/subscriptions), so Reddit is a Bundle provider: the user
// requests their export at reddit.com/settings/data-request and uploads the ZIP.
// See docs/reddit.md.
//
// Column names vary across export vintages, so extraction reads by header name
// with fallbacks (pick) rather than fixed positions; unrecognized files are
// captured generically by the bundle importer, so nothing is dropped.
package reddit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

const sourceID = "reddit"

// Provider implements provider.BundleProvider for Reddit exports.
type Provider struct{}

func New() *Provider { return &Provider{} }

func (p *Provider) Descriptor() domain.Descriptor {
	return domain.Descriptor{
		ID:             sourceID,
		DisplayName:    "Reddit",
		IngestionModes: []domain.Mode{domain.ModeBundle},
		EventTypes: []domain.TypeDef{
			{Type: "reddit.post", Category: domain.CategoryContent, HasBody: true},
			{Type: "reddit.comment", Category: domain.CategoryContent, HasBody: true},
			{Type: "reddit.draft", Category: domain.CategoryContent, HasBody: true},
			{Type: "reddit.scheduled_post", Category: domain.CategoryContent, HasBody: true},
			{Type: "reddit.message", Category: domain.CategoryMessage, HasBody: true},
			{Type: "reddit.chat", Category: domain.CategoryMessage, HasBody: true},
			{Type: "reddit.vote.up", Category: domain.CategoryReaction, NeedsTarget: true},
			{Type: "reddit.vote.down", Category: domain.CategoryReaction, NeedsTarget: true},
			{Type: "reddit.vote", Category: domain.CategoryReaction, NeedsTarget: true},
			{Type: "reddit.poll_vote", Category: domain.CategoryReaction, NeedsTarget: true},
			{Type: "reddit.save", Category: domain.CategorySave, NeedsTarget: true},
			{Type: "reddit.hide", Category: domain.CategoryOther, NeedsTarget: true},
			{Type: "reddit.subscribe", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "reddit.friend", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "reddit.moderate", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "reddit.multireddit", Category: domain.CategoryConnection},
			{Type: "reddit.gild", Category: domain.CategoryOther, NeedsTarget: true},
			{Type: "reddit.unknown", Category: domain.CategoryOther},
		},
	}
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{SupportsBundle: true}
}

// Extractors maps each known CSV to a provider-local Kind. Unlisted files are
// captured generically by the importer.
func (p *Provider) Extractors() []provider.Extractor {
	return []provider.Extractor{
		csv("posts.csv", "post", "id", "permalink"),
		csv("comments.csv", "comment", "id", "permalink"),
		csv("drafts.csv", "draft", "id"),
		csv("scheduled_posts.csv", "scheduled_post", "id", "permalink"),
		csv("post_votes.csv", "post_vote", "id", "permalink"),
		csv("comment_votes.csv", "comment_vote", "id", "permalink"),
		csv("poll_votes.csv", "poll_vote", "post_id", "id", "permalink"),
		csv("saved_posts.csv", "saved_post", "id", "permalink"),
		csv("saved_comments.csv", "saved_comment", "id", "permalink"),
		csv("hidden_posts.csv", "hidden_post", "id", "permalink"),
		csv("subscribed_subreddits.csv", "subscribe", "subreddit"),
		csv("friends.csv", "friend", "username", "user"),
		csv("moderated_subreddits.csv", "moderate", "subreddit"),
		csv("approved_submitter_subreddits.csv", "moderate", "subreddit"),
		csv("multireddits.csv", "multireddit", "name", "display_name"),
		csv("gilded_content.csv", "gild", "id", "permalink"), // awards given (current name)
		csv("gold_received.csv", "gold_received", "id", "permalink"),
		csv("gilded_posts.csv", "gild", "id", "permalink"), // older names
		csv("gilded_comments.csv", "gild", "id", "permalink"),
		csv("messages.csv", "message", "id", "permalink"),
		csv("message_headers.csv", "message", "id", "permalink"),
		csv("chat_history.csv", "chat", "message_id", "id"),
	}
}

// ---- extraction ----

func csv(file, kind string, idFields ...string) csvExtractor {
	return csvExtractor{file: file, kind: kind, idFields: idFields}
}

type csvExtractor struct {
	file     string
	kind     string
	idFields []string
}

func (e csvExtractor) Match(memberPath string) bool {
	return strings.EqualFold(path.Base(memberPath), e.file)
}

func (e csvExtractor) Extract(_ string, r io.Reader, emit func(provider.RawRecord) error) error {
	return bundle.ReadCSV(r, func(row map[string]string) error {
		payload, err := json.Marshal(row)
		if err != nil {
			return err
		}
		id := pick(row, e.idFields...)
		if id == "" {
			id = pick(row, "permalink", "url", "name")
		}
		return emit(provider.RawRecord{
			ExternalID: e.kind + ":" + id,
			Kind:       e.kind,
			OccurredAt: parseRedditTime(pick(row, "date", "created_at", "created", "timestamp")),
			Payload:    payload,
		})
	})
}

// ---- normalization ----

func (p *Provider) Normalize(_ context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	var row map[string]string
	if err := json.Unmarshal(raw.Payload, &row); err != nil {
		// Never drop: a payload that is not a CSV row becomes an unknown event.
		sum := sha256.Sum256(raw.Payload)
		return one(domain.Event{
			SourceID: sourceID, ExternalID: "unknown:" + firstNonEmpty(raw.ExternalID, hex.EncodeToString(sum[:8])),
			Type: "reddit.unknown", Category: domain.CategoryOther, Title: "Unrecognized Reddit record",
			Metadata: map[string]any{"member": raw.Member, "reason": "malformed row: " + err.Error(), "bytes": len(raw.Payload)},
		}), nil
	}
	base := domain.Event{
		SourceID:   sourceID,
		OccurredAt: raw.OccurredAt,
		Actor:      "self",
		Metadata:   map[string]any{},
	}
	if raw.Member != "" {
		base.Metadata["member"] = raw.Member
	}
	sub := pick(row, "subreddit", "subreddit_name")
	perma := permalink(pick(row, "permalink", "url"))
	id := pick(row, "id")

	switch raw.Kind {
	case "post", "scheduled_post":
		ev := base
		ev.Type = "reddit." + raw.Kind
		ev.Category = domain.CategoryContent
		ev.ExternalID = "post:" + id
		ev.Title = firstNonEmpty(pick(row, "title"), "Post in r/"+sub)
		ev.Body = pick(row, "body", "selftext", "text")
		ev.ThreadExternalID = "post:" + id
		meta(ev, "subreddit", sub)
		meta(ev, "url", pick(row, "url"))
		meta(ev, "permalink", perma)
		meta(ev, "ip", pick(row, "ip"))
		meta(ev, "gildings", pick(row, "gildings"))
		return one(ev), nil

	case "draft":
		ev := base
		ev.Type, ev.Category = "reddit.draft", domain.CategoryContent
		ev.ExternalID = "draft:" + id
		ev.Title = firstNonEmpty(pick(row, "title"), "Draft")
		ev.Body = pick(row, "body", "text")
		meta(ev, "subreddit", sub)
		return one(ev), nil

	case "comment":
		ev := base
		ev.Type, ev.Category = "reddit.comment", domain.CategoryContent
		ev.ExternalID = "comment:" + id
		ev.Body = pick(row, "body")
		ev.Title = "Comment in r/" + sub
		// "link" is the parent post's URL in the export and "parent" the parent
		// post or comment (URL or fullname); both are reduced to the ids the
		// post/comment events use so threads and replies resolve.
		link, parent := pick(row, "link", "link_id"), pick(row, "parent", "parent_id")
		ev.ThreadExternalID = refID(link, "post")
		ev.ParentExternalID = refID(parent, "")
		if parent != "" {
			ev.Targets = tgt(domain.RelationRepliedTo, ev.ParentExternalID, refURL(parent))
		}
		meta(ev, "subreddit", sub)
		meta(ev, "permalink", perma)
		meta(ev, "link", link)
		meta(ev, "ip", pick(row, "ip"))
		meta(ev, "gildings", pick(row, "gildings"))
		meta(ev, "media", pick(row, "media"))
		return one(ev), nil

	case "post_vote", "comment_vote":
		ev := base
		ev.Category = domain.CategoryReaction
		ev.ExternalID = raw.Kind + ":" + id
		dir := strings.ToLower(strings.TrimSpace(pick(row, "direction", "vote")))
		switch dir {
		case "down", "-1", "downvote", "false":
			ev.Type, ev.Targets = "reddit.vote.down", tgt(domain.RelationVotedDown, id, perma)
		case "up", "1", "upvote", "true":
			ev.Type, ev.Targets = "reddit.vote.up", tgt(domain.RelationVotedUp, id, perma)
		default:
			ev.Type, ev.Targets = "reddit.vote", tgt(domain.RelationLiked, id, perma)
		}
		ev.Title = "Voted on " + firstNonEmpty(perma, id)
		return one(ev), nil

	case "poll_vote":
		ev := base
		ev.Type, ev.Category = "reddit.poll_vote", domain.CategoryReaction
		ev.ExternalID = "poll_vote:" + pick(row, "post_id", "id")
		ev.Title = "Voted in a poll"
		ev.Targets = tgt(domain.RelationVotedUp, pick(row, "post_id", "id"), perma)
		meta(ev, "option", pick(row, "user_selection", "option", "selection"))
		return one(ev), nil

	case "saved_post", "saved_comment":
		ev := base
		ev.Type, ev.Category = "reddit.save", domain.CategorySave
		ev.ExternalID = raw.Kind + ":" + id
		ev.Title = "Saved " + firstNonEmpty(perma, id)
		ev.Targets = tgt(domain.RelationSaved, id, perma)
		return one(ev), nil

	case "hidden_post":
		ev := base
		ev.Type, ev.Category = "reddit.hide", domain.CategoryOther
		ev.ExternalID = "hidden:" + id
		ev.Title = "Hid a post"
		ev.Targets = tgt(domain.RelationSaved, id, perma)
		return one(ev), nil

	case "subscribe":
		ev := base
		ev.Type, ev.Category = "reddit.subscribe", domain.CategoryConnection
		ev.ExternalID = "subscribe:" + sub
		ev.Title = "Subscribed to r/" + sub
		ev.Targets = []domain.Target{{
			Relation: domain.RelationFollowed, TargetSource: sourceID,
			TargetExternalID: "r/" + sub, TargetURL: "https://www.reddit.com/r/" + sub,
		}}
		return one(ev), nil

	case "moderate":
		ev := base
		ev.Type, ev.Category = "reddit.moderate", domain.CategoryConnection
		ev.ExternalID = "moderate:" + sub
		ev.Title = "Moderator/approved in r/" + sub
		ev.Targets = []domain.Target{{
			Relation: domain.RelationFollowed, TargetSource: sourceID,
			TargetExternalID: "r/" + sub, TargetURL: "https://www.reddit.com/r/" + sub,
		}}
		return one(ev), nil

	case "friend":
		u := pick(row, "username", "user", "name")
		ev := base
		ev.Type, ev.Category = "reddit.friend", domain.CategoryConnection
		ev.ExternalID = "friend:" + u
		ev.Title = "Friended u/" + u
		ev.Targets = []domain.Target{{
			Relation: domain.RelationFollowed, TargetSource: sourceID,
			TargetExternalID: "u/" + u, TargetURL: "https://www.reddit.com/user/" + u,
		}}
		return one(ev), nil

	case "multireddit":
		name := pick(row, "name", "display_name")
		ev := base
		ev.Type, ev.Category = "reddit.multireddit", domain.CategoryConnection
		ev.ExternalID = "multi:" + name
		ev.Title = "Custom feed: " + name
		return one(ev), nil

	case "gild", "gold_received":
		ev := base
		ev.Type, ev.Category = "reddit.gild", domain.CategoryOther
		ev.ExternalID = raw.Kind + ":" + firstNonEmpty(id, perma, raw.ExternalID)
		ev.Title = "Awarded " + firstNonEmpty(perma, id)
		ev.Metadata["direction"] = "given"
		if raw.Kind == "gold_received" {
			ev.Title = "Received an award on " + firstNonEmpty(perma, id)
			ev.Metadata["direction"] = "received"
			ev.Actor = firstNonEmpty(pick(row, "from", "gilder", "awarder"), "self")
		}
		ev.Targets = tgt(domain.RelationLiked, id, perma)
		meta(ev, "award", pick(row, "award", "award_name", "gild_type", "type"))
		return one(ev), nil

	case "message":
		ev := base
		ev.Type, ev.Category = "reddit.message", domain.CategoryMessage
		ev.ExternalID = "message:" + firstNonEmpty(id, pick(row, "permalink"))
		ev.Title = firstNonEmpty(pick(row, "subject"), "Private message")
		ev.Body = pick(row, "body", "text")
		ev.ThreadExternalID = pick(row, "thread_id", "first_message", "parent")
		ev.Actor = firstNonEmpty(pick(row, "from"), "self")
		meta(ev, "to", pick(row, "to"))
		meta(ev, "permalink", perma)
		meta(ev, "ip", pick(row, "ip"))
		return one(ev), nil

	case "chat":
		ev := base
		ev.Type, ev.Category = "reddit.chat", domain.CategoryMessage
		ev.ExternalID = "chat:" + pick(row, "message_id", "id")
		ev.Body = pick(row, "message", "body", "text")
		ev.Actor = firstNonEmpty(pick(row, "username", "sender"), "self")
		ev.ThreadExternalID = pick(row, "channel_url", "thread_parent_message_id", "conversation_id")
		ev.Title = "Chat message"
		meta(ev, "channel", pick(row, "channel_url"))
		return one(ev), nil
	}

	ev := base
	ev.Type, ev.Category = "reddit.unknown", domain.CategoryOther
	ev.ExternalID = "unknown:" + raw.ExternalID
	ev.Title = "Unrecognized Reddit record"
	return one(ev), nil
}

// ---- helpers ----

func one(ev domain.Event) []domain.Event { return []domain.Event{ev} }

func tgt(rel domain.Relation, id, url string) []domain.Target {
	return []domain.Target{{Relation: rel, TargetSource: sourceID, TargetExternalID: id, TargetURL: url}}
}

func meta(ev domain.Event, k, v string) {
	if v != "" {
		ev.Metadata[k] = v
	}
}

func pick(row map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := row[k]; ok {
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
		}
	}
	return ""
}

// refID reduces a reference to a post or comment — a permalink URL
// (…/comments/<post>/<slug>/<comment>/), a fullname (t3_<post>, t1_<comment>) or
// a bare id — to the "post:<id>" / "comment:<id>" form the post and comment
// events use as ExternalID. Anything else is returned as it is. kind is what a
// bare id refers to ("" leaves it untouched).
func refID(ref, kind string) string {
	ref = strings.TrimSpace(ref)
	switch {
	case ref == "":
		return ""
	case strings.HasPrefix(ref, "t3_"):
		return "post:" + ref[3:]
	case strings.HasPrefix(ref, "t1_"):
		return "comment:" + ref[3:]
	case strings.HasPrefix(ref, "http://"), strings.HasPrefix(ref, "https://"), strings.HasPrefix(ref, "/r/"), strings.HasPrefix(ref, "/comments/"):
		p := ref
		if i := strings.Index(p, "://"); i >= 0 {
			p = p[i+3:]
			if j := strings.IndexByte(p, '/'); j >= 0 {
				p = p[j:]
			}
		}
		if i := strings.IndexAny(p, "?#"); i >= 0 {
			p = p[:i]
		}
		segs := strings.Split(strings.Trim(p, "/"), "/")
		for i, s := range segs {
			if s != "comments" || i+1 >= len(segs) {
				continue
			}
			rest := segs[i+1:]
			if len(rest) >= 3 && rest[2] != "" {
				return "comment:" + rest[2]
			}
			return "post:" + rest[0]
		}
		return ref
	case kind != "" && !strings.Contains(ref, ":"):
		return kind + ":" + ref
	}
	return ref
}

// refURL is the absolute URL of a reference when it is one, else "".
func refURL(ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "/r/") {
		return permalink(ref)
	}
	return ""
}

func permalink(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}
	return "https://www.reddit.com" + s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseRedditTime parses Reddit's export timestamps, e.g. "2023-01-15 10:20:30 UTC",
// with several fallbacks (ISO, epoch seconds handled by callers if needed).
func parseRedditTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		"2006-01-02 15:04:05 MST",
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05",
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
