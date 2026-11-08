package app

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/domain"

	"github.com/google/uuid"
)

// Page-size rule of the timeline. It is defined here once; TimelineService
// applies it before a Query reaches a repository, and repositories cap
// defensively with the same ClampLimit.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 500
)

// ErrBadCursor is returned for a pagination cursor that was not issued by the
// timeline (truncated, hand-edited, from another version). It is the caller's
// fault: transports map it to a 400, never a 500.
var ErrBadCursor = errors.New("malformed pagination cursor")

// ClampLimit applies the page-size rule: no (or a nonsensical) limit means
// DefaultPageLimit, and no page is larger than MaxPageLimit.
func ClampLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultPageLimit
	case n > MaxPageLimit:
		return MaxPageLimit
	}
	return n
}

// Normalize returns the query as the repositories expect it: limit clamped,
// search trimmed, cursor validated (ErrBadCursor).
func (q Query) Normalize() (Query, error) {
	q.Limit = ClampLimit(q.Limit)
	q.Search = strings.TrimSpace(q.Search)
	if q.Cursor != "" {
		if _, err := DecodeCursor(q.Cursor); err != nil {
			return q, err
		}
	}
	return q, nil
}

// Cursor is a keyset position in the timeline order — (occurred_at desc, id
// desc), undated events last: "continue after this event". The encoding is
// owned by the application layer so that every repository issues and accepts
// the same cursors and a malformed one is rejected before it reaches a store.
type Cursor struct {
	// Undated: the position is among the undated events (the tail of the
	// timeline); OccurredAt is then meaningless.
	Undated    bool
	OccurredAt time.Time
	ID         string
}

// CursorAfter is the cursor that continues a timeline after e.
func CursorAfter(e domain.StoredEvent) Cursor { return CursorAt(e.OccurredAt, e.ID) }

// CursorAt is the cursor that continues a timeline after the event with the
// given time (zero = undated) and id.
func CursorAt(occurredAt time.Time, id string) Cursor {
	if occurredAt.IsZero() {
		return Cursor{Undated: true, ID: id}
	}
	return Cursor{OccurredAt: occurredAt.UTC(), ID: id}
}

// CompareTimeline orders two events in timeline order — (occurred_at desc, id
// desc), undated events last: negative when a comes first. Stores sort with
// this rule (or its SQL form).
func CompareTimeline(a, b domain.StoredEvent) int {
	az, bz := a.OccurredAt.IsZero(), b.OccurredAt.IsZero()
	switch {
	case az != bz:
		if bz {
			return -1
		}
		return 1
	case !az:
		if c := a.OccurredAt.Compare(b.OccurredAt); c != 0 {
			return -c
		}
	}
	return strings.Compare(b.ID, a.ID)
}

// Encode renders the cursor as the opaque token handed to clients.
func (c Cursor) Encode() string {
	raw := "U|" + c.ID
	if !c.Undated {
		raw = "D|" + c.OccurredAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID
	}
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a token produced by Cursor.Encode. Every failure wraps
// ErrBadCursor. Event ids are uuid-shaped in every store, so anything else in
// the id position is rejected here instead of failing a uuid cast in a
// database.
func DecodeCursor(s string) (Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: not base64url", ErrBadCursor)
	}
	parts := strings.SplitN(string(b), "|", 3)
	var c Cursor
	switch {
	case len(parts) == 2 && parts[0] == "U":
		c = Cursor{Undated: true, ID: parts[1]}
	case len(parts) == 3 && parts[0] == "D":
		ts, err := time.Parse(time.RFC3339Nano, parts[1])
		if err != nil {
			return Cursor{}, fmt.Errorf("%w: bad timestamp", ErrBadCursor)
		}
		c = Cursor{OccurredAt: ts.UTC(), ID: parts[2]}
	default:
		return Cursor{}, fmt.Errorf("%w: unknown layout", ErrBadCursor)
	}
	if !uuidShaped(c.ID) {
		return Cursor{}, fmt.Errorf("%w: bad event id", ErrBadCursor)
	}
	return c, nil
}

// Admits reports whether e comes strictly after the cursor in timeline order,
// i.e. belongs to the pages that follow it. (Stores that can express the
// comparison in a query do that instead; this is the reference definition.)
func (c Cursor) Admits(e domain.StoredEvent) bool {
	switch {
	case c.Undated:
		return e.OccurredAt.IsZero() && e.ID < c.ID
	case e.OccurredAt.IsZero():
		return true // every undated event follows every dated one
	}
	if cmp := e.OccurredAt.Compare(c.OccurredAt); cmp != 0 {
		return cmp < 0
	}
	return e.ID < c.ID
}

// uuidShaped reports whether s is a UUID in the canonical 8-4-4-4-12 form, the
// only form the stores hand out (uuid.Validate alone also admits URN, braced
// and undashed spellings).
func uuidShaped(s string) bool {
	return len(s) == 36 && uuid.Validate(s) == nil
}
