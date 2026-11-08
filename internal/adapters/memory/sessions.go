package memory

import (
	"context"
	"sync"
	"time"

	"github.com/johnnycube/archivist/internal/app"
)

// Sessions is an in-memory app.Sessions, keyed by token hash (the application
// layer never hands a store the token itself).
type Sessions struct {
	// IdleTTL ends sessions no Lookup has used for that long (<= 0: never).
	// Set it before the store is used.
	IdleTTL time.Duration

	mu  sync.Mutex
	m   map[string]session // token hash → session
	ttl time.Duration
}

type session struct {
	userID   string
	expires  time.Time
	lastSeen time.Time
}

// NewSessions returns an in-memory session store with the given TTL.
func NewSessions(ttl time.Duration) *Sessions {
	if ttl <= 0 {
		ttl = app.DefaultSessionTTL
	}
	return &Sessions{m: map[string]session{}, ttl: ttl}
}

var _ app.Sessions = (*Sessions)(nil)

// live is THE expiry rule: absolute TTL, and the idle TTL if one is set.
func (s *Sessions) live(sess session, now time.Time) bool {
	if !now.Before(sess.expires) {
		return false
	}
	return s.IdleTTL <= 0 || now.Before(sess.lastSeen.Add(s.IdleTTL))
}

func (s *Sessions) Create(ctx context.Context, tokenHash, userID string, now time.Time) error {
	return s.CreateReplacing(ctx, tokenHash, userID, "", now)
}

func (s *Sessions) CreateReplacing(_ context.Context, tokenHash, userID, previousHash string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, sess := range s.m { // opportunistic expiry sweep
		if !s.live(sess, now) {
			delete(s.m, h)
		}
	}
	if prev, ok := s.m[previousHash]; ok && prev.userID == userID {
		delete(s.m, previousHash)
	}
	s.m[tokenHash] = session{userID: userID, expires: now.Add(s.ttl), lastSeen: now}
	return nil
}

func (s *Sessions) Lookup(_ context.Context, tokenHash string, now time.Time) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[tokenHash]
	if !ok || !s.live(sess, now) {
		return "", false, nil
	}
	if now.Sub(sess.lastSeen) >= app.SessionTouchInterval {
		sess.lastSeen = now
		s.m[tokenHash] = sess
	}
	return sess.userID, true, nil
}

func (s *Sessions) Revoke(_ context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, tokenHash)
	return nil
}

func (s *Sessions) RevokeUser(_ context.Context, userID string) error {
	s.deleteUser(userID)
	return nil
}

func (s *Sessions) deleteUser(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, sess := range s.m {
		if sess.userID == userID {
			delete(s.m, h)
		}
	}
}
