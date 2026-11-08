package app

import (
	"context"
	"sync"
)

// uploadGate tracks the uploads streaming into staging right now, in this
// process. Usage (Jobs.Usage) only sees an upload once it has become a job;
// without the gate, any number of parallel uploads of one user would each pass
// the quota and free-disk checks, and fill the staging disk — every other
// user's uploads and reprocesses with it — before Enqueue refused them.
type uploadGate struct {
	mu       sync.Mutex
	reserved int64 // declared bytes of every upload in flight
	users    map[string]inFlight
}

type inFlight struct {
	count int
	bytes int64
}

func newUploadGate() *uploadGate { return &uploadGate{users: map[string]inFlight{}} }

// count is how many uploads of userID are in flight.
func (g *uploadGate) count(userID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.users[userID].count
}

// Admit decides whether userID may start uploading an import of source, of
// declaredBytes (the request's length; the caller passes the upload cap when
// the client declared none). It checks the source, the user's quotas counting
// their uploads in flight, and the free staging space counting everybody's
// uploads in flight — and reserves the upload in all three until release is
// called, which the caller does once the upload has become a job or is
// discarded. Admit is the early refusal; Enqueue (Jobs.CreateWithin) remains
// the atomic enforcement of the quotas.
func (s ImportService) Admit(ctx context.Context, userID, source string, declaredBytes int64) (release func(), err error) {
	noop := func() {}
	if !s.Catalog.Known(source) {
		return noop, ErrUnknownSource
	}
	declaredBytes = max(declaredBytes, 0)
	usage, err := s.usage(ctx, userID)
	if err != nil {
		return noop, err
	}
	g := s.Gate
	if g == nil { // a service built without New: no gate, checks only
		return noop, s.admitWithin(ctx, usage, inFlight{}, 0, declaredBytes)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	mine := g.users[userID]
	if err := s.admitWithin(ctx, usage, mine, g.reserved, declaredBytes); err != nil {
		return noop, err
	}
	g.reserved += declaredBytes
	g.users[userID] = inFlight{count: mine.count + 1, bytes: mine.bytes + declaredBytes}
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.reserved -= declaredBytes
			u := g.users[userID]
			if u.count <= 1 {
				delete(g.users, userID)
				return
			}
			g.users[userID] = inFlight{count: u.count - 1, bytes: u.bytes - declaredBytes}
		})
	}, nil
}

// usage reads the user's job usage, or nothing when no quota needs it.
func (s ImportService) usage(ctx context.Context, userID string) (JobUsage, error) {
	if s.Limits.MaxActiveImports <= 0 && s.Limits.MaxArchiveBytes <= 0 {
		return JobUsage{}, nil
	}
	return s.Jobs.Usage(ctx, userID)
}

// admitWithin is the decision of Admit: the quotas with the user's uploads in
// flight, then the disk with everybody's reserved bytes.
func (s ImportService) admitWithin(ctx context.Context, usage JobUsage, mine inFlight, reserved, declared int64) error {
	if limit := s.Limits.MaxActiveImports; limit > 0 && usage.Active+mine.count >= limit {
		return ErrTooManyImports
	}
	if limit := s.Limits.MaxArchiveBytes; limit > 0 && usage.Stored()+mine.bytes+declared > limit {
		return ErrStorageQuota
	}
	return s.Staging.CheckSpace(ctx, reserved+declared)
}
