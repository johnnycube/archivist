package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/domain"
)

// ErrNoMedia is returned when an event has no stored media at that index.
var ErrNoMedia = errors.New("no such media")

// ErrQueryTimeout is returned when a read of the archive runs longer than
// TimelineQueryTimeout. Narrower filters help.
var ErrQueryTimeout = errors.New("the query took too long; narrow it down (fewer sources, a shorter date range, more specific search words)")

// timedOut turns an error from a read that ran out of TimelineQueryTimeout
// into ErrQueryTimeout — whatever the store made of the cancellation (a
// driver may report it as its own error, not as the context's).
func timedOut(ctx context.Context, err error) error {
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrQueryTimeout
	}
	return err
}

// TimelineService is the use case for reading the archive.
type TimelineService struct {
	Events  EventRepository
	Catalog ProviderCatalog
	Blobs   Blobs
	// Jobs resolves media paths to their blobs (the import ledgers); nil =
	// media is looked up under its path.
	Jobs Jobs
	// QueryTimeout bounds one read (0 = TimelineQueryTimeout).
	QueryTimeout time.Duration
}

// withTimeout derives the context one read of the archive runs under.
func (s TimelineService) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	d := s.QueryTimeout
	if d <= 0 {
		d = TimelineQueryTimeout
	}
	return context.WithTimeout(ctx, d)
}

// Query returns a keyset page of Events for the query's user.
// The query is normalized first (Query.Normalize): a malformed cursor yields
// ErrBadCursor — the caller's fault — without touching the store.
//
// Total and Facets come with the first page only (no cursor): the following
// pages of the same query have the same ones, and recomputing them per page
// means a count and three GROUP BYs over the user's whole filtered archive for
// every 50 events scrolled.
func (s TimelineService) Query(ctx context.Context, q Query) (Page, error) {
	q, err := q.Normalize()
	if err != nil {
		return Page{}, err
	}
	q.SkipSummary = q.Cursor != ""
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	page, err := s.Events.Timeline(ctx, q)
	return page, timedOut(ctx, err)
}

// TimelineQueryTimeout bounds one read of the archive (a page, a histogram),
// so a pathological filter cannot hold a database connection, which every
// user shares, for minutes. A timeout surfaces as context.DeadlineExceeded.
const TimelineQueryTimeout = 30 * time.Second

// QueryCollapsed is Query with runs of repetitive events folded into clusters
// (see CollapseRule). A rule with a gap outside [0, MaxCollapseGap] yields
// ErrBadCollapseGap.
func (s TimelineService) QueryCollapsed(ctx context.Context, q Query, rule CollapseRule) (CollapsedPage, error) {
	if err := rule.Validate(); err != nil {
		return CollapsedPage{}, err
	}
	q, err := q.Normalize()
	if err != nil {
		return CollapsedPage{}, err
	}
	q.SkipSummary = q.Cursor != ""
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	page, err := s.Events.Collapsed(ctx, q, rule)
	return page, timedOut(ctx, err)
}

// HistogramKey selects what a histogram's counts are keyed by.
type HistogramKey string

const (
	HistogramBySource   HistogramKey = "source"
	HistogramByCategory HistogramKey = "category"
)

// Histogram returns per-day counts of dated events matching q, keyed by
// source or by category, for the overview chart.
func (s TimelineService) Histogram(ctx context.Context, q Query, by HistogramKey) ([]HistogramBucket, error) {
	// A histogram is not paginated: only the filters matter.
	q.Cursor, q.Limit = "", 0
	q.Search = strings.TrimSpace(q.Search)
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	var buckets []HistogramBucket
	var err error
	if by == HistogramByCategory {
		buckets, err = s.Events.HistogramByCategory(ctx, q)
	} else {
		buckets, err = s.Events.Histogram(ctx, q)
	}
	return buckets, timedOut(ctx, err)
}

// Event returns a single Event of the user, or ErrNotFound.
func (s TimelineService) Event(ctx context.Context, userID, id string) (domain.StoredEvent, error) {
	ev, ok, err := s.Events.Get(ctx, userID, id)
	if err == nil && !ok {
		err = ErrNotFound
	}
	if err != nil {
		return domain.StoredEvent{}, err
	}
	return ev, nil
}

// Sources lists the available source descriptors.
func (s TimelineService) Sources() []domain.Descriptor {
	return s.Catalog.Descriptors()
}

// Media streams a stored media attachment of the user's event. The blob key
// returned lets the transport guess a content type from its extension. Both
// "nothing there" cases are typed, so a transport needs no knowledge of the
// blob store: the user having no such event yields ErrNotFound; the event
// having no stored media at idx — no blob store, index out of range, media that
// was never captured, or its blob gone — yields ErrNoMedia.
func (s TimelineService) Media(ctx context.Context, userID, eventID string, idx int) (io.ReadCloser, string, error) {
	ev, err := s.Event(ctx, userID, eventID)
	if err != nil {
		return nil, "", err
	}
	if s.Blobs == nil || idx < 0 || idx >= len(ev.Media) || ev.Media[idx].BlobKey == "" {
		return nil, "", ErrNoMedia
	}
	// Events name media by path; the import that wrote the event knows which
	// blob that path is in its archive. Media stored before content
	// addressing (or seeded) lies under the path itself.
	path := ev.SourceID + "/" + ev.Media[idx].BlobKey
	key := path
	if s.Jobs != nil {
		k, ok, err := s.Jobs.MediaKey(ctx, userID, ev.ImportJobID, path)
		if err != nil {
			return nil, "", err
		}
		if ok {
			key = k
		}
	}
	rc, err := s.Blobs.Open(ctx, userID, key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", fmt.Errorf("%w: %w", ErrNoMedia, err)
	}
	if err != nil {
		return nil, "", err
	}
	return rc, path, nil
}
