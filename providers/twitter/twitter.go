// Package twitter ingests a Twitter/X account archive ("Download an archive of
// your data"). Its data/*.js members are JSON arrays wrapped in a
// `window.YTD.<name>.partN = [...]` assignment, each element a single-key wrapper
// object ({"tweet": {...}}, {"dmConversation": {...}}); this provider strips the
// wrapper (bundle.StreamJSVarArray) and maps the known files to Events.
//
// Shapes follow the archive's own data/README.txt and the community parsers
// (timhutton/twitter-archive-parser, rust-utilities/twitter-archive):
//
//   - tweets.js (older: tweet.js; large accounts: tweets-part1.js, …): full_text
//     is always present, counts are strings, created_at is "Sat Aug 12 16:10:37
//     +0000 2023". There is no retweeted_status: retweets are recognised by the
//     "RT @user: " prefix. Media sits in data/tweets_media/<tweet_id>-<basename of
//     media_url_https> (videos: the basename of the best video_info variant),
//     which is what Media.BlobKey carries. deleted-tweets.js, community-tweet.js
//     and twitter-circle-tweet.js share the tweet shape (media dirs
//     deleted_tweets_media, community_tweet_media, twitter_circle_tweet_media).
//   - note-tweet.js holds the long-form text of tweets over 280 characters
//     ({noteTweet: {noteTweetId, createdAt, updatedAt, core: {text, …}}}); the
//     archive documents no join to the tweet, so it is its own event
//     (twitter.note_tweet) and a tweetId, when a variant carries one, is the
//     parent link.
//   - direct-messages.js / direct-messages-group.js: {dmConversation:
//     {conversationId, messages: [{messageCreate: {id, senderId, recipientId,
//     createdAt "2022-01-27T15:58:52.744Z", text, mediaUrls, urls[{url, expanded,
//     display}], reactions[{senderId, reactionKey, eventId, createdAt}]}}]}}. Group
//     conversations also hold joinConversation, participantsJoin,
//     participantsLeave and conversationNameUpdate events (twitter.dm_event). DM
//     media is data/direct_messages_media/<message_id>-<basename of mediaUrl>
//     (group: direct_messages_group_media). Sender ids stay raw: the account's own
//     id is emitted from account.js as twitter.account and the join is left to a
//     later stage.
//   - block.js {blocking: {accountId, userLink}} and mute.js {muting: …} carry no
//     date, nor do lists-*.js {userListInfo: {url}}, saved-search.js
//     {savedSearch: {savedSearchId, query}}, profile.js and verified.js: those
//     events are undated (OccurredAt zero).
//   - account.js {account: {accountId, username, accountDisplayName, createdAt,
//     createdVia, email}}, profile.js {profile: {description: {bio, website,
//     location}, avatarMediaUrl, headerMediaUrl}} and verified.js {verified:
//     {accountId, verified}} are the account's profile (twitter.account).
//
// Bookmarks are not part of the archive (no manifest dataType, README or
// community parser knows one). tweetdeck.js, ad-impressions.js,
// ad-engagements.js, personalization.js and the other files are captured
// generically by the importer as twitter.unknown, with a preview, not dropped.
package twitter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

const sourceID = "twitter"

// Twitter's created_at format, e.g. "Wed Oct 10 20:19:24 +0000 2018".
const twitterTimeLayout = "Mon Jan 02 15:04:05 -0700 2006"

type Provider struct{}

func New() *Provider { return &Provider{} }

func (p *Provider) Descriptor() domain.Descriptor {
	return domain.Descriptor{
		ID:             sourceID,
		DisplayName:    "Twitter / X",
		IngestionModes: []domain.Mode{domain.ModeBundle, domain.ModeOAuth},
		EventTypes: []domain.TypeDef{
			{Type: "twitter.tweet", Category: domain.CategoryContent, HasBody: true},
			{Type: "twitter.note_tweet", Category: domain.CategoryContent, HasBody: true},
			{Type: "twitter.moment", Category: domain.CategoryContent, HasBody: true},
			{Type: "twitter.dm", Category: domain.CategoryMessage, HasBody: true},
			{Type: "twitter.dm_event", Category: domain.CategoryOther},
			{Type: "twitter.like", Category: domain.CategoryReaction, NeedsTarget: true},
			{Type: "twitter.follow", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "twitter.follower", Category: domain.CategoryConnection},
			{Type: "twitter.block", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "twitter.mute", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "twitter.list", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "twitter.saved_search", Category: domain.CategoryConsumption, HasBody: true},
			{Type: "twitter.account", Category: domain.CategoryProfile},
			{Type: "twitter.unknown", Category: domain.CategoryOther},
		},
	}
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{SupportsBundle: true, NeedsEgress: []string{"api.twitter.com", "api.x.com"}}
}

// Extractors lists the data/*.js files that are mapped. Every name also
// matches its split parts (tweets-part1.js, direct-messages-part1.js).
func (p *Provider) Extractors() []provider.Extractor {
	return []provider.Extractor{
		jsExtractor{file: "tweets", inner: "tweet", kind: "tweet"},
		jsExtractor{file: "tweet", inner: "tweet", kind: "tweet"}, // pre-2023 name
		jsExtractor{file: "deleted-tweets", inner: "tweet", kind: "tweet"},
		jsExtractor{file: "community-tweet", inner: "tweet", kind: "tweet"},
		jsExtractor{file: "twitter-circle-tweet", inner: "tweet", kind: "tweet"},
		jsExtractor{file: "note-tweet", inner: "noteTweet", kind: "note_tweet"},
		jsExtractor{file: "like", inner: "like", kind: "like"},
		jsExtractor{file: "following", inner: "following", kind: "following"},
		jsExtractor{file: "follower", inner: "follower", kind: "follower"},
		jsExtractor{file: "block", inner: "blocking", kind: "block"},
		jsExtractor{file: "mute", inner: "muting", kind: "mute"},
		jsExtractor{file: "lists-created", inner: "userListInfo", kind: "list_created"},
		jsExtractor{file: "lists-member", inner: "userListInfo", kind: "list_member"},
		jsExtractor{file: "lists-subscribed", inner: "userListInfo", kind: "list_subscribed"},
		jsExtractor{file: "saved-search", inner: "savedSearch", kind: "saved_search"},
		jsExtractor{file: "moment", inner: "moment", kind: "moment"},
		jsExtractor{file: "account", inner: "account", kind: "account"},
		jsExtractor{file: "profile", inner: "profile", kind: "profile"},
		jsExtractor{file: "verified", inner: "verified", kind: "verified"},
		dmExtractor{file: "direct-messages", group: false},
		dmExtractor{file: "direct-message", group: false}, // direct-message-part1.js
		dmExtractor{file: "direct-messages-group", group: true},
	}
}

// ---- extraction ----

var partSuffix = regexp.MustCompile(`^(.*?)(?:-part\d+)?\.js$`)

// matchJS reports whether member is data/<file>.js or one of its split parts
// data/<file>-part<N>.js.
func matchJS(member, file string) bool {
	m := partSuffix.FindStringSubmatch(strings.ToLower(path.Base(member)))
	return m != nil && m[1] == strings.ToLower(file)
}

// jsExtractor reads one window.YTD.*.js file and emits each element's inner
// object (unwrapped from its single top-level key) as a RawRecord.
type jsExtractor struct {
	file  string // base name without .js
	inner string // the single key wrapping each element, e.g. "tweet"
	kind  string
}

func (e jsExtractor) Match(memberPath string) bool { return matchJS(memberPath, e.file) }

func (e jsExtractor) Extract(_ string, r io.Reader, emit func(provider.RawRecord) error) error {
	return bundle.StreamJSVarArray(r, func(raw json.RawMessage) error {
		return emit(provider.RawRecord{Kind: e.kind, Payload: unwrap(raw, e.inner)})
	})
}

// unwrap returns the value under key when raw is a single-key wrapper object
// holding it, else raw itself.
func unwrap(raw json.RawMessage, key string) json.RawMessage {
	var wrap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return raw
	}
	if inner, ok := wrap[key]; ok {
		return inner
	}
	if len(wrap) == 1 {
		for _, v := range wrap {
			return v
		}
	}
	return raw
}

// dmExtractor flattens {dmConversation: {conversationId, messages: [...]}} into
// one record per message or conversation event.
type dmExtractor struct {
	file  string
	group bool
}

func (e dmExtractor) Match(memberPath string) bool { return matchJS(memberPath, e.file) }

// dmRecord is the payload of a "dm" or "dm_event" record.
type dmRecord struct {
	ConversationID string          `json:"conversation_id"`
	Group          bool            `json:"group"`
	Event          string          `json:"event"` // messageCreate, participantsJoin, …
	Body           json.RawMessage `json:"body"`
	Index          int             `json:"index"` // position in the conversation (for events without an id)
}

func (e dmExtractor) Extract(_ string, r io.Reader, emit func(provider.RawRecord) error) error {
	return bundle.StreamJSVarArray(r, func(raw json.RawMessage) error {
		var conv struct {
			ConversationID string            `json:"conversationId"`
			Messages       []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(unwrap(raw, "dmConversation"), &conv); err != nil || (conv.ConversationID == "" && conv.Messages == nil) {
			return emit(provider.RawRecord{Kind: "unknown", Payload: raw})
		}
		for i, m := range conv.Messages {
			var wrap map[string]json.RawMessage
			if err := json.Unmarshal(m, &wrap); err != nil || len(wrap) != 1 {
				return emit(provider.RawRecord{Kind: "unknown", Payload: m})
			}
			for event, body := range wrap {
				kind := "dm_event"
				if event == "messageCreate" {
					kind = "dm"
				}
				payload, err := json.Marshal(dmRecord{ConversationID: conv.ConversationID, Group: e.group, Event: event, Body: body, Index: i})
				if err != nil {
					return err
				}
				if err := emit(provider.RawRecord{Kind: kind, Payload: payload}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ---- on-disk shapes ----

type tweet struct {
	IDStr               string   `json:"id_str"`
	ID                  any      `json:"id"`
	FullText            string   `json:"full_text"`
	Text                string   `json:"text"`
	CreatedAt           string   `json:"created_at"`
	Lang                string   `json:"lang"`
	Source              string   `json:"source"`
	FavoriteCount       any      `json:"favorite_count"`
	RetweetCount        any      `json:"retweet_count"`
	Retweeted           bool     `json:"retweeted"`
	PossiblySensitive   bool     `json:"possibly_sensitive"`
	InReplyToStatusID   string   `json:"in_reply_to_status_id_str"`
	InReplyToUserID     string   `json:"in_reply_to_user_id_str"`
	InReplyToScreenName string   `json:"in_reply_to_screen_name"`
	DisplayTextRange    []any    `json:"display_text_range"`
	Entities            entities `json:"entities"`
	ExtendedEntities    struct {
		Media []media `json:"media"`
	} `json:"extended_entities"`
	EditInfo struct {
		Initial struct {
			EditTweetIDs   []string `json:"editTweetIds"`
			EditableUntil  string   `json:"editableUntil"`
			EditsRemaining string   `json:"editsRemaining"`
			IsEditEligible bool     `json:"isEditEligible"`
		} `json:"initial"`
	} `json:"edit_info"`
	CommunityID string `json:"community_id"`
}

type entities struct {
	Hashtags []struct {
		Text string `json:"text"`
	} `json:"hashtags"`
	UserMentions []struct {
		ScreenName string `json:"screen_name"`
		Name       string `json:"name"`
		IDStr      string `json:"id_str"`
	} `json:"user_mentions"`
	URLs  []urlEntity `json:"urls"`
	Media []media     `json:"media"`
}

type urlEntity struct {
	URL         string `json:"url"`
	ExpandedURL string `json:"expanded_url"`
	DisplayURL  string `json:"display_url"`
}

type media struct {
	IDStr         string `json:"id_str"`
	Type          string `json:"type"` // photo, video, animated_gif
	MediaURL      string `json:"media_url"`
	MediaURLHTTPS string `json:"media_url_https"`
	URL           string `json:"url"`
	ExpandedURL   string `json:"expanded_url"`
	VideoInfo     struct {
		Variants []struct {
			Bitrate     any    `json:"bitrate"`
			ContentType string `json:"content_type"`
			URL         string `json:"url"`
		} `json:"variants"`
	} `json:"video_info"`
}

type noteTweet struct {
	NoteTweetID string `json:"noteTweetId"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
	Lifecycle   any    `json:"lifecycle"`
	Core        struct {
		Text     string `json:"text"`
		Hashtags []any  `json:"hashtags"`
		Mentions []any  `json:"mentions"`
		URLs     []any  `json:"urls"`
	} `json:"core"`
	// community-archive's variant of the same file.
	TweetID          string `json:"tweetId"`
	NoteTweetResults struct {
		Text string `json:"text"`
	} `json:"noteTweetResults"`
}

type like struct {
	TweetID     string `json:"tweetId"`
	FullText    string `json:"fullText"`
	ExpandedURL string `json:"expandedUrl"`
}

type relation struct {
	AccountID string `json:"accountId"`
	UserLink  string `json:"userLink"`
}

type userListInfo struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

type savedSearch struct {
	SavedSearchID string `json:"savedSearchId"`
	Query         string `json:"query"`
}

type moment struct {
	MomentID    string          `json:"momentId"`
	CreatedAt   string          `json:"createdAt"`
	CreatedBy   string          `json:"createdBy"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Tweets      json.RawMessage `json:"tweets"`
	CoverMedia  json.RawMessage `json:"coverMediaUrls"`
}

type account struct {
	AccountID          string `json:"accountId"`
	Username           string `json:"username"`
	AccountDisplayName string `json:"accountDisplayName"`
	CreatedAt          string `json:"createdAt"`
	CreatedVia         string `json:"createdVia"`
	Email              string `json:"email"`
}

type profile struct {
	Description struct {
		Bio      string `json:"bio"`
		Website  string `json:"website"`
		Location string `json:"location"`
	} `json:"description"`
	AvatarMediaURL string `json:"avatarMediaUrl"`
	HeaderMediaURL string `json:"headerMediaUrl"`
}

type verified struct {
	AccountID string `json:"accountId"`
	Verified  bool   `json:"verified"`
}

type messageCreate struct {
	ID          string   `json:"id"`
	SenderID    string   `json:"senderId"`
	RecipientID string   `json:"recipientId"`
	CreatedAt   string   `json:"createdAt"`
	Text        string   `json:"text"`
	MediaURLs   []string `json:"mediaUrls"`
	URLs        []struct {
		URL      string `json:"url"`
		Expanded string `json:"expanded"`
		Display  string `json:"display"`
	} `json:"urls"`
	Reactions []struct {
		SenderID    string `json:"senderId"`
		ReactionKey string `json:"reactionKey"`
		EventID     string `json:"eventId"`
		CreatedAt   string `json:"createdAt"`
	} `json:"reactions"`
	EditHistory json.RawMessage `json:"editHistory"`
}

type dmEvent struct {
	InitiatingUserID     string   `json:"initiatingUserId"`
	UserIDs              []string `json:"userIds"`
	ParticipantsSnapshot []string `json:"participantsSnapshot"`
	Name                 string   `json:"name"`
	CreatedAt            string   `json:"createdAt"`
}

// ---- normalization ----

func (p *Provider) Normalize(_ context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	base := domain.Event{SourceID: sourceID, Actor: "self", Metadata: map[string]any{}}
	if raw.Member != "" {
		base.Metadata["member"] = raw.Member
	}

	switch raw.Kind {
	case "tweet":
		var t tweet
		if err := json.Unmarshal(raw.Payload, &t); err != nil {
			return unknown(raw, "malformed tweet: "+err.Error()), nil
		}
		id := firstNonEmpty(t.IDStr, scalar(t.ID))
		if id == "" {
			return unknown(raw, "tweet without id"), nil
		}
		ev := base
		ev.Type, ev.Category = "twitter.tweet", domain.CategoryContent
		ev.ExternalID = "tweet:" + id
		ev.ThreadExternalID = "tweet:" + id
		ev.Body = firstNonEmpty(t.FullText, t.Text)
		ev.Title = "Tweet"
		ev.OccurredAt = parseTwitterTime(t.CreatedAt)
		if user, rest, ok := retweetOf(ev.Body); ok {
			ev.Title = "Retweet of @" + user
			ev.Metadata["retweet"] = true
			ev.Metadata["retweeted_user"] = user
			ev.Metadata["retweeted_text"] = rest
		}
		if t.InReplyToStatusID != "" {
			ev.Title = "Reply"
			if t.InReplyToScreenName != "" {
				ev.Title = "Reply to @" + t.InReplyToScreenName
			}
			ev.ParentExternalID = "tweet:" + t.InReplyToStatusID
			ev.ThreadExternalID = "tweet:" + t.InReplyToStatusID
			url := "https://twitter.com/i/web/status/" + t.InReplyToStatusID
			if t.InReplyToScreenName != "" {
				url = "https://twitter.com/" + t.InReplyToScreenName + "/status/" + t.InReplyToStatusID
			}
			ev.Targets = []domain.Target{{Relation: domain.RelationRepliedTo, TargetSource: sourceID, TargetExternalID: "tweet:" + t.InReplyToStatusID, TargetURL: url}}
			set(ev, "in_reply_to_user_id", t.InReplyToUserID)
			set(ev, "in_reply_to_screen_name", t.InReplyToScreenName)
		}
		switch file := strings.ToLower(path.Base(raw.Member)); {
		case strings.HasPrefix(file, "deleted-tweets"):
			ev.Metadata["deleted"] = true
			ev.Title = "Deleted tweet"
		case strings.HasPrefix(file, "community-tweet"):
			ev.Metadata["community"] = true
			ev.Title = "Community tweet"
		case strings.HasPrefix(file, "twitter-circle-tweet"):
			ev.Metadata["circle"] = true
			ev.Title = "Circle tweet"
		}
		set(ev, "lang", t.Lang)
		set(ev, "source", stripTags(t.Source))
		set(ev, "favorite_count", scalar(t.FavoriteCount))
		set(ev, "retweet_count", scalar(t.RetweetCount))
		set(ev, "community_id", t.CommunityID)
		if t.PossiblySensitive {
			ev.Metadata["possibly_sensitive"] = true
		}
		if len(t.EditInfo.Initial.EditTweetIDs) > 1 {
			ev.Metadata["edit_tweet_ids"] = t.EditInfo.Initial.EditTweetIDs
		}
		if tags := hashtags(t.Entities); len(tags) > 0 {
			ev.Metadata["hashtags"] = tags
		}
		if mentions := mentionsOf(t.Entities); len(mentions) > 0 {
			ev.Metadata["mentions"] = mentions
		}
		if urls := expandedURLs(t.Entities.URLs); len(urls) > 0 {
			ev.Metadata["urls"] = urls
		}
		mediaList := t.ExtendedEntities.Media
		if len(mediaList) == 0 {
			mediaList = t.Entities.Media
		}
		for _, m := range mediaList {
			ev.Media = append(ev.Media, tweetMedia(raw.Member, id, m))
		}
		return []domain.Event{ev}, nil

	case "note_tweet":
		var n noteTweet
		if err := json.Unmarshal(raw.Payload, &n); err != nil {
			return unknown(raw, "malformed note tweet: "+err.Error()), nil
		}
		text := firstNonEmpty(n.Core.Text, n.NoteTweetResults.Text)
		id := firstNonEmpty(n.NoteTweetID, n.TweetID)
		if id == "" && text == "" {
			return unknown(raw, "note tweet without id or text"), nil
		}
		if id == "" {
			id = digest(text)
		}
		ev := base
		ev.Type, ev.Category = "twitter.note_tweet", domain.CategoryContent
		ev.ExternalID = "note_tweet:" + id
		ev.Title = "Long-form tweet text"
		ev.Body = text
		ev.OccurredAt = parseTwitterTime(n.CreatedAt)
		ev.ThreadExternalID = ev.ExternalID
		if n.TweetID != "" {
			ev.ParentExternalID = "tweet:" + n.TweetID
			ev.ThreadExternalID = "tweet:" + n.TweetID
		}
		set(ev, "updated_at", n.UpdatedAt)
		if n.Lifecycle != nil {
			ev.Metadata["lifecycle"] = n.Lifecycle
		}
		return []domain.Event{ev}, nil

	case "like":
		var l like
		if err := json.Unmarshal(raw.Payload, &l); err != nil || l.TweetID == "" {
			return unknown(raw, "malformed like"), nil
		}
		ev := base
		ev.Type, ev.Category = "twitter.like", domain.CategoryReaction
		ev.ExternalID = "like:" + l.TweetID
		ev.Title = "Liked a tweet"
		ev.Body = l.FullText
		ev.Targets = []domain.Target{{
			Relation: domain.RelationLiked, TargetSource: sourceID,
			TargetExternalID: "tweet:" + l.TweetID, TargetURL: firstNonEmpty(l.ExpandedURL, "https://twitter.com/i/web/status/"+l.TweetID),
		}}
		return []domain.Event{ev}, nil

	case "following", "follower", "block", "mute":
		var f relation
		if err := json.Unmarshal(raw.Payload, &f); err != nil || f.AccountID == "" {
			return unknown(raw, "malformed "+raw.Kind+" record"), nil
		}
		ev := base
		ev.Category = domain.CategoryConnection
		ev.ExternalID = raw.Kind + ":" + f.AccountID
		target := []domain.Target{{Relation: domain.RelationFollowed, TargetSource: sourceID, TargetExternalID: f.AccountID, TargetURL: userLink(f)}}
		switch raw.Kind {
		case "following":
			ev.Type, ev.Title, ev.Targets = "twitter.follow", "Followed an account", target
			ev.ExternalID = "follow:" + f.AccountID // unchanged from earlier versions
		case "follower":
			ev.Type, ev.Title, ev.Actor = "twitter.follower", "Gained a follower", f.AccountID
			ev.Metadata["user_link"] = userLink(f)
		case "block":
			ev.Type, ev.Title, ev.Targets = "twitter.block", "Blocked an account", target
		case "mute":
			ev.Type, ev.Title, ev.Targets = "twitter.mute", "Muted an account", target
		}
		set(ev, "account_id", f.AccountID)
		return []domain.Event{ev}, nil

	case "list_created", "list_member", "list_subscribed":
		var l userListInfo
		if err := json.Unmarshal(raw.Payload, &l); err != nil || (l.URL == "" && l.Name == "") {
			return unknown(raw, "malformed list record"), nil
		}
		rel := strings.TrimPrefix(raw.Kind, "list_")
		ev := base
		ev.Type, ev.Category = "twitter.list", domain.CategoryConnection
		ev.ExternalID = "list:" + rel + ":" + firstNonEmpty(l.URL, l.Name)
		name := firstNonEmpty(l.Name, listSlug(l.URL), "a list")
		switch rel {
		case "created":
			ev.Title = "Created list " + name
		case "member":
			ev.Title = "Added to list " + name
		default:
			ev.Title = "Subscribed to list " + name
		}
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: sourceID, TargetExternalID: l.URL, TargetURL: l.URL}}
		ev.Metadata["relation"] = rel
		set(ev, "url", l.URL)
		set(ev, "name", l.Name)
		return []domain.Event{ev}, nil

	case "saved_search":
		var s savedSearch
		if err := json.Unmarshal(raw.Payload, &s); err != nil || (s.SavedSearchID == "" && s.Query == "") {
			return unknown(raw, "malformed saved search"), nil
		}
		ev := base
		ev.Type, ev.Category = "twitter.saved_search", domain.CategoryConsumption
		ev.ExternalID = "saved_search:" + firstNonEmpty(s.SavedSearchID, digest(s.Query))
		ev.Title = "Saved search"
		ev.Body = s.Query
		return []domain.Event{ev}, nil

	case "moment":
		var m moment
		if err := json.Unmarshal(raw.Payload, &m); err != nil || (m.MomentID == "" && m.Title == "") {
			return unknown(raw, "malformed moment"), nil
		}
		ev := base
		ev.Type, ev.Category = "twitter.moment", domain.CategoryContent
		ev.ExternalID = "moment:" + firstNonEmpty(m.MomentID, digest(m.Title, m.CreatedAt))
		ev.Title = "Moment: " + firstNonEmpty(m.Title, "untitled")
		ev.Body = m.Description
		ev.OccurredAt = parseTwitterTime(m.CreatedAt)
		ev.ThreadExternalID = ev.ExternalID
		set(ev, "created_by", m.CreatedBy)
		if len(m.Tweets) > 0 && string(m.Tweets) != "null" {
			ev.Metadata["tweets"] = m.Tweets
		}
		if len(m.CoverMedia) > 0 && string(m.CoverMedia) != "null" {
			ev.Metadata["cover_media"] = m.CoverMedia
		}
		return []domain.Event{ev}, nil

	case "account":
		var a account
		if err := json.Unmarshal(raw.Payload, &a); err != nil || (a.AccountID == "" && a.Username == "") {
			return unknown(raw, "malformed account record"), nil
		}
		ev := base
		ev.Type, ev.Category = "twitter.account", domain.CategoryProfile
		ev.ExternalID = "account:" + firstNonEmpty(a.AccountID, a.Username)
		ev.Title = "Account @" + firstNonEmpty(a.Username, a.AccountID)
		ev.OccurredAt = parseTwitterTime(a.CreatedAt)
		set(ev, "account_id", a.AccountID)
		set(ev, "username", a.Username)
		set(ev, "display_name", a.AccountDisplayName)
		set(ev, "created_via", a.CreatedVia)
		set(ev, "email", a.Email)
		return []domain.Event{ev}, nil

	case "profile":
		var pr profile
		if err := json.Unmarshal(raw.Payload, &pr); err != nil {
			return unknown(raw, "malformed profile record"), nil
		}
		ev := base
		ev.Type, ev.Category = "twitter.account", domain.CategoryProfile
		ev.ExternalID = "profile:self" // profile.js carries no account id; one per archive
		ev.Title = "Profile"
		ev.Body = pr.Description.Bio
		set(ev, "website", pr.Description.Website)
		set(ev, "location", pr.Description.Location)
		set(ev, "avatar_url", pr.AvatarMediaURL)
		set(ev, "header_url", pr.HeaderMediaURL)
		for _, u := range []string{pr.AvatarMediaURL, pr.HeaderMediaURL} {
			if u != "" {
				ev.Media = append(ev.Media, domain.Media{URL: u, BlobKey: path.Join(path.Dir(raw.Member), "profile_media", basename(u))})
			}
		}
		return []domain.Event{ev}, nil

	case "verified":
		var v verified
		if err := json.Unmarshal(raw.Payload, &v); err != nil || v.AccountID == "" {
			return unknown(raw, "malformed verified record"), nil
		}
		ev := base
		ev.Type, ev.Category = "twitter.account", domain.CategoryProfile
		ev.ExternalID = "verified:" + v.AccountID
		ev.Title = "Verification status"
		ev.Metadata["verified"] = v.Verified
		set(ev, "account_id", v.AccountID)
		return []domain.Event{ev}, nil

	case "dm", "dm_event":
		return normalizeDM(base, raw)
	}
	return unknown(raw, "unrecognized record kind "+strconv.Quote(raw.Kind)), nil
}

func normalizeDM(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var r dmRecord
	if err := json.Unmarshal(raw.Payload, &r); err != nil {
		return unknown(raw, "malformed DM record: "+err.Error()), nil
	}
	ev := base
	ev.ThreadExternalID = "dm_conversation:" + r.ConversationID
	ev.Metadata["conversation_id"] = r.ConversationID
	if r.Group {
		ev.Metadata["group"] = true
	}

	if r.Event == "messageCreate" {
		var m messageCreate
		if err := json.Unmarshal(r.Body, &m); err != nil {
			return unknown(raw, "malformed messageCreate: "+err.Error()), nil
		}
		ev.Type, ev.Category = "twitter.dm", domain.CategoryMessage
		id := m.ID
		if id == "" {
			id = digest(r.ConversationID, m.SenderID, m.CreatedAt, m.Text)
		}
		ev.ExternalID = "dm:" + id
		ev.OccurredAt = parseTwitterTime(m.CreatedAt)
		ev.Actor = firstNonEmpty(m.SenderID, "self")
		ev.Title = "Direct message"
		if r.Group {
			ev.Title = "Group direct message"
		}
		ev.Body = m.Text
		set(ev, "sender_id", m.SenderID)
		set(ev, "recipient_id", m.RecipientID)
		if len(m.URLs) > 0 {
			var urls []map[string]string
			for _, u := range m.URLs {
				urls = append(urls, map[string]string{"url": u.URL, "expanded": u.Expanded, "display": u.Display})
			}
			ev.Metadata["urls"] = urls
		}
		if len(m.Reactions) > 0 {
			var reactions []map[string]string
			for _, re := range m.Reactions {
				reactions = append(reactions, map[string]string{"sender_id": re.SenderID, "reaction": re.ReactionKey, "event_id": re.EventID, "created_at": re.CreatedAt})
			}
			ev.Metadata["reactions"] = reactions
		}
		if len(m.EditHistory) > 0 && string(m.EditHistory) != "null" {
			ev.Metadata["edit_history"] = m.EditHistory
		}
		mediaDir := "direct_messages_media"
		if r.Group {
			mediaDir = "direct_messages_group_media"
		}
		for _, u := range m.MediaURLs {
			if u == "" {
				continue
			}
			ev.Media = append(ev.Media, domain.Media{URL: u, BlobKey: path.Join(path.Dir(raw.Member), mediaDir, id+"-"+basename(u))})
		}
		return []domain.Event{ev}, nil
	}

	var e dmEvent
	if err := json.Unmarshal(r.Body, &e); err != nil {
		return unknown(raw, "malformed DM event: "+err.Error()), nil
	}
	ev.Type, ev.Category = "twitter.dm_event", domain.CategoryOther
	ev.OccurredAt = parseTwitterTime(e.CreatedAt)
	ev.Actor = firstNonEmpty(e.InitiatingUserID, "self")
	ev.ExternalID = "dm_event:" + r.ConversationID + ":" + digest(r.Event, e.CreatedAt, e.InitiatingUserID, e.Name, strings.Join(e.UserIDs, ","), strconv.Itoa(r.Index))
	ev.Metadata["event"] = r.Event
	switch r.Event {
	case "joinConversation":
		ev.Title = "Joined a group conversation"
	case "participantsJoin":
		ev.Title = "Participants joined the conversation"
	case "participantsLeave":
		ev.Title = "Participants left the conversation"
	case "conversationNameUpdate":
		ev.Title = "Conversation renamed to " + strconv.Quote(e.Name)
	default:
		ev.Title = "Conversation event: " + r.Event
		ev.Metadata["body"] = r.Body
	}
	set(ev, "initiating_user_id", e.InitiatingUserID)
	set(ev, "name", e.Name)
	if len(e.UserIDs) > 0 {
		ev.Metadata["user_ids"] = e.UserIDs
	}
	if len(e.ParticipantsSnapshot) > 0 {
		ev.Metadata["participants"] = e.ParticipantsSnapshot
	}
	return []domain.Event{ev}, nil
}

// ---- helpers ----

// unknown is the never-drop fallback: a record Normalize cannot make sense of
// becomes a twitter.unknown event with a preview of the payload, instead of an
// error that would abort the whole import.
func unknown(raw provider.RawRecord, reason string) []domain.Event {
	sum := sha256.Sum256(raw.Payload)
	id := raw.ExternalID
	if id == "" {
		id = hex.EncodeToString(sum[:8])
	}
	ev := domain.Event{
		SourceID: sourceID, ExternalID: "unknown:" + id,
		Type: "twitter.unknown", Category: domain.CategoryOther,
		Title:    "Unrecognized Twitter record",
		Metadata: map[string]any{"reason": reason, "kind": raw.Kind, "preview": preview(raw.Payload), "bytes": len(raw.Payload)},
	}
	if raw.Member != "" {
		ev.Metadata["member"] = raw.Member
	}
	return []domain.Event{ev}
}

const previewRunes = 240

func preview(b []byte) string {
	s := strings.ToValidUTF8(string(b), "")
	if utf8.RuneCountInString(s) <= previewRunes {
		return s
	}
	return string([]rune(s)[:previewRunes]) + "…"
}

// tweetMedia maps one media entity to a Media whose BlobKey is the archive
// member the importer stored it under: <media dir>/<tweet id>-<file name>. The
// media dir sits next to the tweets file and is named after it (tweets_media,
// tweet_media, deleted_tweets_media, community_tweet_media,
// twitter_circle_tweet_media).
func tweetMedia(member, tweetID string, m media) domain.Media {
	url := firstNonEmpty(m.MediaURLHTTPS, m.MediaURL)
	name := basename(url)
	mime := ""
	switch m.Type {
	case "video", "animated_gif":
		best, bitrate := "", -1
		for _, v := range m.VideoInfo.Variants {
			b, _ := strconv.Atoi(scalar(v.Bitrate))
			if v.URL != "" && (b > bitrate || best == "") {
				best, bitrate = v.URL, b
				mime = v.ContentType
			}
		}
		if best != "" {
			url, name = best, basename(best)
		}
	case "photo":
		mime = mimeByExt(name)
	}
	dir := strings.TrimSuffix(strings.ToLower(path.Base(member)), ".js")
	if i := strings.Index(dir, "-part"); i >= 0 {
		dir = dir[:i]
	}
	dir = strings.ReplaceAll(dir, "-", "_") + "_media" // tweets → tweets_media, deleted-tweets → deleted_tweets_media
	md := domain.Media{URL: url, MIME: mime}
	if name != "" {
		md.BlobKey = path.Join(path.Dir(member), dir, tweetID+"-"+name)
	}
	return md
}

// basename is the last path segment of a URL without query or fragment.
func basename(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	if u == "" {
		return ""
	}
	return path.Base(u)
}

func mimeByExt(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	}
	return ""
}

// retweetOf recognises the "RT @user: text" form the archive uses for retweets
// (there is no retweeted_status object).
func retweetOf(text string) (user, rest string, ok bool) {
	if !strings.HasPrefix(text, "RT @") {
		return "", "", false
	}
	rest = text[len("RT @"):]
	i := strings.Index(rest, ":")
	if i <= 0 {
		return "", "", false
	}
	user = rest[:i]
	if strings.ContainsAny(user, " \n\t") {
		return "", "", false
	}
	return user, strings.TrimSpace(rest[i+1:]), true
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

// stripTags renders the "source" field ('<a href="…">Twitter Web App</a>') as text.
func stripTags(s string) string {
	return strings.TrimSpace(html.UnescapeString(tagRE.ReplaceAllString(s, "")))
}

func hashtags(e entities) []string {
	var out []string
	for _, h := range e.Hashtags {
		if h.Text != "" {
			out = append(out, h.Text)
		}
	}
	return out
}

func mentionsOf(e entities) []string {
	var out []string
	for _, m := range e.UserMentions {
		if m.ScreenName != "" {
			out = append(out, m.ScreenName)
		}
	}
	return out
}

func expandedURLs(urls []urlEntity) []string {
	var out []string
	for _, u := range urls {
		if v := firstNonEmpty(u.ExpandedURL, u.URL); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func userLink(f relation) string {
	if f.UserLink != "" {
		return f.UserLink
	}
	return "https://twitter.com/intent/user?user_id=" + f.AccountID
}

// listSlug is the last segment of a list URL (https://twitter.com/x/lists/<slug>).
func listSlug(u string) string {
	b := basename(u)
	if b == "." || b == "/" {
		return ""
	}
	return b
}

func set(ev domain.Event, k, v string) {
	if v != "" {
		ev.Metadata[k] = v
	}
}

// scalar renders a string-or-number field ("favorite_count": "0" or 0).
func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:6])
}

// parseTwitterTime reads both timestamp forms the archive uses: tweets'
// "Wed Oct 10 20:19:24 +0000 2018" and the ISO 8601 "2022-01-27T15:58:52.744Z"
// of DMs, account.js and note tweets.
func parseTwitterTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, l := range []string{twitterTimeLayout, time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006.01.02"} {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
