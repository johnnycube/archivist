// Package meta holds what the Facebook and Instagram providers share: both are
// Meta "Accounts Center" JSON exports with the same member matching needs, the
// same record-array layout and the same Messenger thread files.
package meta

import (
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

// ---- member matching ----

// BaseIs matches members by base name, case-insensitively, wherever they lie.
func BaseIs(names ...string) func(string) bool {
	return func(member string) bool {
		b := path.Base(member)
		for _, n := range names {
			if strings.EqualFold(b, n) {
				return true
			}
		}
		return false
	}
}

// BasePrefix matches .json members whose lower-cased base name starts with prefix.
func BasePrefix(prefix string) func(string) bool {
	return func(member string) bool {
		b := strings.ToLower(path.Base(member))
		return strings.HasPrefix(b, prefix) && strings.HasSuffix(b, ".json")
	}
}

// InDir reports whether member lies somewhere below a directory named dir.
func InDir(member, dir string) bool {
	return strings.Contains("/"+strings.ToLower(path.Dir(member))+"/", "/"+dir+"/")
}

// messageAttachmentDirs are the per-thread directories of a Messenger /
// Instagram Direct export (messages/inbox/<thread>/files/…); stickers_used and
// share hold the stickers and shared media Messenger writes beside them.
var messageAttachmentDirs = []string{"photos", "videos", "audio", "files", "gifs", "stickers_used", "share"}

// IsAttachment is the provider.AttachmentMatcher of both Meta providers: files
// that messages, posts and stories point at (Media.BlobKey is the member path)
// are stored even when they are no media by extension — a PDF sent in a chat.
// Data files are never attachments: an unmapped .json or .html has to stay
// visible as "<source>.unknown".
func IsAttachment(member string) bool {
	switch strings.ToLower(path.Ext(member)) {
	case ".json", ".html", ".htm":
		return false
	}
	if InDir(member, "messages") {
		for _, d := range messageAttachmentDirs {
			if InDir(member, d) {
				return true
			}
		}
		return false
	}
	// Facebook: your_facebook_activity/posts/media/…, …/posts/album/…; Instagram:
	// media/posts/…, media/stories/…, media/reels/…, media/archived_posts/…,
	// media/other/…, media/profile/…, media/recently_deleted/….
	return InDir(member, "media") || InDir(member, "stories") || InDir(member, "posts/album")
}

// ---- extraction ----

// Arrays streams every array listed in Kinds (array member name → record kind;
// "" is a top-level array), wherever it is nested in the member.
type Arrays struct {
	Matches func(member string) bool
	Kinds   map[string]string
}

// Arr builds the common extractor: one kind, found at the top level or under
// any of keys.
func Arr(kind string, match func(string) bool, keys ...string) Arrays {
	kinds := map[string]string{"": kind}
	for _, k := range keys {
		kinds[k] = kind
	}
	return Arrays{Matches: match, Kinds: kinds}
}

func (e Arrays) Match(member string) bool { return e.Matches(member) }

func (e Arrays) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	keys := make([]string, 0, len(e.Kinds))
	for k := range e.Kinds {
		keys = append(keys, k)
	}
	return bundle.StreamJSONArraysReport(r, keys, func(key string, el json.RawMessage) error {
		return emit(Rec(e.Kinds[key], el))
	}, bundle.ReportSkipped(member, emit))
}

// Rec hashes the kind into the dedupe key: Meta lists the same bare record
// ({name, start_timestamp}, {string_list_data: […]}) under several meanings
// (invited/joined, follower/following, …).
func Rec(kind string, payload []byte) provider.RawRecord {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(payload)
	return provider.RawRecord{Kind: kind, Payload: payload, ContentHash: hex.EncodeToString(h.Sum(nil))}
}

// ---- helpers ----

// ID builds a stable ExternalID from a record's kind, its timestamp and
// whatever else tells two records of the same instant apart.
func ID(kind string, ts int64, disc string) string {
	sum := sha256.Sum256([]byte(disc))
	return kind + ":" + strconv.FormatInt(ts, 10) + ":" + hex.EncodeToString(sum[:4])
}

func UnixSec(s int64) time.Time {
	if s <= 0 {
		return time.Time{}
	}
	return time.Unix(s, 0).UTC()
}

func One(ev domain.Event) []domain.Event { return []domain.Event{ev} }

// Unknown is the event for a record whose shape a provider did not recognize:
// the record stays visible as "<source>.unknown" with a short preview instead
// of failing the import. The id is derived from the content so several such
// records stay distinct.
func Unknown(sourceID string, raw provider.RawRecord) domain.Event {
	sum := sha256.Sum256(raw.Payload)
	preview := raw.Payload
	if len(preview) > 1024 {
		preview = preview[:1024]
	}
	return domain.Event{
		SourceID: sourceID, ExternalID: "unknown:" + raw.Kind + ":" + hex.EncodeToString(sum[:8]),
		Type: sourceID + ".unknown", Category: domain.CategoryOther,
		Title:    "Unrecognized record",
		Metadata: map[string]any{"kind": raw.Kind, "member": raw.Member, "preview": string(preview)},
	}
}

// Set records a metadata value, skipping empty ones. ev.Metadata must be non-nil.
func Set(ev domain.Event, k, v string) {
	if v != "" {
		ev.Metadata[k] = v
	}
}

func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// LabelValue is one entry of Meta's generic label_values export format.
type LabelValue struct {
	Label          string       `json:"label"`
	Value          string       `json:"value"`
	Href           string       `json:"href"`
	Title          string       `json:"title"`
	TimestampValue int64        `json:"timestamp_value"`
	Dict           []LabelValue `json:"dict"`
}

// LabelFind returns the first non-empty value labelled label, searching nested
// dicts depth-first.
func LabelFind(lvs []LabelValue, label string) string {
	for _, lv := range lvs {
		if lv.Label == label && lv.Value != "" {
			return lv.Value
		}
		if v := LabelFind(lv.Dict, label); v != "" {
			return v
		}
	}
	return ""
}
