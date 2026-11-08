package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"
)

// Size bounds of one Event's free-text fields. An export can carry a single
// enormous string — and some providers copy one record's text (a
// conversation's title, say) into every event derived from it, so a 1 MiB
// name across a thousand messages would become a gigabyte in memory and in
// the database. Real titles are a line, real messages kilobytes.
const (
	MaxTitleBytes    = 4 << 10
	MaxBodyBytes     = 1 << 20
	MaxMetadataBytes = 256 << 10
)

// MaxExternalIDBytes bounds an id a provider derives (ExternalID and the ids
// that refer to one: thread, parent, target). Some are built from URLs, which
// can run to kilobytes, and the store's unique index refuses keys of more
// than ~2.7 KB. A longer id is replaced by CompactExternalID's digest form.
const MaxExternalIDBytes = 512

// CompactExternalID returns id, or for an id over MaxExternalIDBytes a
// stable stand-in: "sha256:" and the hex digest of the id. Deterministic, so
// the same record gets the same id on every import (ADR 0016), and applied
// to references alike, so they still meet the event they refer to.
func CompactExternalID(id string) string {
	if len(id) <= MaxExternalIDBytes {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TruncatedKey is the Metadata key Capped records what it cut under:
// field name → original size in bytes.
const TruncatedKey = "truncated"

// Capped returns ev with Title, Body and Metadata within the bounds above,
// and whether anything had to be cut. Nothing goes silently: a cut is
// recorded in Metadata[TruncatedKey], and the original stays in the preserved
// archive, from which a reprocess re-derives the event. Text is cut at a rune
// boundary and ends in "…"; Metadata over its bound is replaced by the record
// of its size (its parts cannot be cut meaningfully).
func (ev Event) Capped() (Event, bool) {
	cut := map[string]any{}
	ev.compactIDs(cut)
	if len(ev.Title) > MaxTitleBytes {
		cut["title"] = len(ev.Title)
		ev.Title = truncateUTF8(ev.Title, MaxTitleBytes)
	}
	if len(ev.Body) > MaxBodyBytes {
		cut["body"] = len(ev.Body)
		ev.Body = truncateUTF8(ev.Body, MaxBodyBytes)
	}
	if len(ev.Metadata) > 0 {
		if b, err := json.Marshal(ev.Metadata); err == nil && len(b) > MaxMetadataBytes {
			cut["metadata"] = len(b)
			ev.Metadata = map[string]any{}
		}
	}
	if len(cut) == 0 {
		return ev, false
	}
	meta := make(map[string]any, len(ev.Metadata)+1)
	for k, v := range ev.Metadata {
		meta[k] = v
	}
	meta[TruncatedKey] = cut
	ev.Metadata = meta
	return ev, true
}

// compactIDs replaces over-long ids with their digest form; a replaced
// ExternalID is recorded in cut (its references are ids of other events and
// carry no record of their own).
func (ev *Event) compactIDs(cut map[string]any) {
	if c := CompactExternalID(ev.ExternalID); c != ev.ExternalID {
		cut["external_id"] = len(ev.ExternalID)
		ev.ExternalID = c
	}
	ev.ThreadExternalID = CompactExternalID(ev.ThreadExternalID)
	ev.ParentExternalID = CompactExternalID(ev.ParentExternalID)
	if len(ev.Targets) == 0 {
		return
	}
	targets := make([]Target, len(ev.Targets)) // never write through to the caller's slice
	copy(targets, ev.Targets)
	for i := range targets {
		targets[i].TargetExternalID = CompactExternalID(targets[i].TargetExternalID)
	}
	ev.Targets = targets
}

// truncateUTF8 cuts s to at most limit bytes, at a rune boundary, ending in "…".
func truncateUTF8(s string, limit int) string {
	const ellipsis = "…"
	i := limit - len(ellipsis)
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + ellipsis
}
