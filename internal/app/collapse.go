package app

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/johnnycube/archivist/internal/domain"
)

// Server-side collapsing of repetitive runs (EventRepository.Collapsed).
//
// High-volume consumption sources (Spotify plays, YouTube watches, browser
// visits) produce hundreds of thousands of near-identical events that bury
// everything else. A collapsed page therefore shows a maximal run of
// CONSECUTIVE events — consecutive in the timeline order, after all filters —
// that share source, type and category as ONE cluster item when the run is
// long enough (MinRun). Everything below lives here so that every repository
// and the contract suite use one definition of "run".
//
// A run continues from one event to the next (CollapseRule.Continues) while
//   - source_id, type and category are the same,
//   - both fall on the same day IN THE QUERY'S ZONE (Query.Loc, UTC when the
//     request names none; undated events form their own "day"), and
//   - the gap between them is at most Gap (Gap 0 = no limit within the day;
//     undated events have no gap).
//
// A page holds Limit ITEMS (clusters and single events mixed). A repository
// looks at a bounded window of CollapseWindow events per page and never at the
// whole archive, so a run longer than the window is cut at the page boundary
// and simply continues as a new cluster on the next page; clients merge a page's
// leading cluster into the previous page's trailing one when source, type and
// category match. The next-page cursor is the position after the LAST EVENT
// of the last item, so paging visits every event exactly once.

const (
	// MinRunConsumption is the shortest run of consumption events shown as a
	// cluster; MinRunOther the same for every other category (a burst of
	// upvotes, a long chat).
	MinRunConsumption = 3
	MinRunOther       = 8

	// DefaultCollapseGap and MaxCollapseGap bound CollapseRule.Gap.
	DefaultCollapseGap = 30 * time.Minute
	MaxCollapseGap     = 24 * time.Hour

	// CollapseWindow is how many events a repository examines for one collapsed
	// page: the per-page cost is bounded by it, whatever the archive's size.
	CollapseWindow = 2000

	// SampleTitleLen bounds one sample title (in characters).
	SampleTitleLen = 200
	// SampleTitles is how many distinct sample titles a cluster carries.
	SampleTitles = 3
)

// ErrBadCollapseGap is returned for a CollapseRule.Gap outside [0, MaxCollapseGap].
var ErrBadCollapseGap = errors.New("collapse gap must be between 0 and 24h")

// CollapseRule parameterises the run rule. The zero value has no gap limit; use
// DefaultCollapseRule for the default.
type CollapseRule struct {
	// Gap is the largest distance between two consecutive members of a run;
	// 0 means no limit within one local day.
	Gap time.Duration
}

// DefaultCollapseRule is the rule the API applies when the client sets none.
func DefaultCollapseRule() CollapseRule { return CollapseRule{Gap: DefaultCollapseGap} }

// Validate reports ErrBadCollapseGap for a gap outside [0, MaxCollapseGap].
func (r CollapseRule) Validate() error {
	if r.Gap < 0 || r.Gap > MaxCollapseGap {
		return fmt.Errorf("%w: %s", ErrBadCollapseGap, r.Gap)
	}
	return nil
}

// MinRun is the shortest run of the category that is shown as a cluster.
func (CollapseRule) MinRun(cat domain.Category) int {
	if cat == domain.CategoryConsumption {
		return MinRunConsumption
	}
	return MinRunOther
}

// Continues reports whether next (the event right after prev in timeline order)
// belongs to prev's run, with the day boundary drawn in loc (nil = UTC). It is
// the reference definition; a store that expresses it in a query must agree
// with it.
func (r CollapseRule) Continues(prev, next domain.StoredEvent, loc *time.Location) bool {
	if prev.SourceID != next.SourceID || prev.Type != next.Type || prev.Category != next.Category {
		return false
	}
	if prev.OccurredAt.IsZero() != next.OccurredAt.IsZero() {
		return false
	}
	if prev.OccurredAt.IsZero() {
		return true // undated events: one "day", no gap
	}
	if LocalDay(prev.OccurredAt, loc) != LocalDay(next.OccurredAt, loc) {
		return false
	}
	return r.Gap == 0 || prev.OccurredAt.Sub(next.OccurredAt) <= r.Gap
}

// LocalDay is the day rule's bucket and the histogram's: the event's calendar
// date in loc (nil = UTC). A day is whatever the zone says it is — 23 hours on
// a spring-forward day, 25 on a fall-back one.
func LocalDay(t time.Time, loc *time.Location) string {
	return t.In(Zone(loc)).Format(time.DateOnly)
}

// SampleTitle is the text a cluster samples from a member: its title, or its
// body when there is no title, cut to SampleTitleLen characters; "" when it
// has neither.
func SampleTitle(e domain.Event) string {
	s := e.Title
	if s == "" {
		s = e.Body
	}
	if utf8.RuneCountInString(s) > SampleTitleLen {
		rs := []rune(s)
		s = string(rs[:SampleTitleLen])
	}
	return s
}

// Cluster is one collapsed run, as far as it lies within a page. First/Last
// are the run's first and last members IN TIMELINE ORDER: First is the newest
// event, Last the oldest, and the page continues after Last.
type Cluster struct {
	SourceID string
	Type     string
	Category domain.Category
	Count    int
	FirstAt  time.Time // zero for a run of undated events
	LastAt   time.Time
	FirstID  string
	LastID   string
	// SampleTitles are the first SampleTitles distinct non-empty sample
	// titles (SampleTitle) of the members, in timeline order. Never nil.
	SampleTitles []string
}

// cursorAfter continues the timeline after the cluster's last member.
func (c Cluster) cursorAfter() Cursor { return CursorAt(c.LastAt, c.LastID) }

// CollapsedItem is one item of a collapsed page: exactly one of Event and
// Cluster is set.
type CollapsedItem struct {
	Event   *domain.StoredEvent
	Cluster *Cluster
}

// CollapsedPage is one keyset page of the collapsed timeline. Total and Facets
// describe the filtered set of EVENTS, as Page's do; Items is never nil.
type CollapsedPage struct {
	Items      []CollapsedItem
	NextCursor string
	Facets     Facets
	Total      int
}

// Run is a maximal run inside one scan window, the unit a repository hands to
// CollapseRule.Page. Its Cluster summarises the run whatever its length; the
// members of a run too short to be a cluster are loaded through Page's expand.
type Run struct{ Cluster }

// ExpandRuns loads the members of the runs at the given indexes (into the
// slice Page was given), each in timeline order, for runs shown as events.
type ExpandRuns func(ctx context.Context, idx []int) ([][]domain.StoredEvent, error)

// Page assembles the items and next cursor of one collapsed page from the
// maximal runs of one scan window, in timeline order. windowFull says the
// window held CollapseWindow events, so more may follow and the window's last
// run may be cut: that run is then left for the next page — unless it is the
// window's only run, which is emitted cut (a run longer than the window). limit
// is the number of items wanted; a run shorter than MinRun becomes one item per
// event, and the page may end inside such a run.
func (r CollapseRule) Page(ctx context.Context, runs []Run, windowFull bool, limit int, expand ExpandRuns) ([]CollapsedItem, string, error) {
	more := false
	if windowFull {
		more = true
		if len(runs) > 1 {
			runs = runs[:len(runs)-1]
		}
	}
	// Decide the page first, so that only the short runs it shows are loaded.
	type slot struct {
		run  int
		take int // members shown, for a short run
	}
	var plan []slot
	var short []int
	n := 0
	for i, run := range runs {
		if n >= limit {
			more = true
			break
		}
		if run.Count >= r.MinRun(run.Category) {
			plan = append(plan, slot{run: i})
			n++
			continue
		}
		take := min(run.Count, limit-n)
		if take < run.Count {
			more = true
		}
		plan = append(plan, slot{run: i, take: take})
		short = append(short, i)
		n += take
		if take < run.Count {
			break
		}
	}
	var members [][]domain.StoredEvent
	if len(short) > 0 {
		var err error
		if members, err = expand(ctx, short); err != nil {
			return nil, "", err
		}
		if len(members) != len(short) {
			return nil, "", fmt.Errorf("collapse: expand returned %d runs, want %d", len(members), len(short))
		}
	}
	items := make([]CollapsedItem, 0, n)
	var next Cursor
	k := 0
	for _, s := range plan {
		run := runs[s.run]
		if s.take == 0 {
			c := run.Cluster
			if c.SampleTitles == nil {
				c.SampleTitles = []string{}
			}
			items = append(items, CollapsedItem{Cluster: &c})
			next = c.cursorAfter()
			continue
		}
		evs := members[k]
		k++
		if len(evs) != run.Count || evs[0].ID != run.FirstID || evs[len(evs)-1].ID != run.LastID {
			return nil, "", fmt.Errorf("collapse: run %s..%s expanded to %d events, want %d", run.FirstID, run.LastID, len(evs), run.Count)
		}
		for i := range evs[:s.take] {
			items = append(items, CollapsedItem{Event: &evs[i]})
		}
		next = CursorAfter(evs[s.take-1])
	}
	if !more || len(items) == 0 {
		return items, "", nil
	}
	return items, next.Encode(), nil
}
