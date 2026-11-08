package rest

import (
	"container/list"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ---- rate limiting (token bucket per key, in-memory, single-node) ----

const (
	// maxLimiterKeys caps one limiter store. At the cap the least recently used
	// key is evicted — never the whole table.
	maxLimiterKeys = 50_000
	// limiterSweepEvery is how often idle keys are dropped.
	limiterSweepEvery = time.Minute
)

// limiterStore is a bounded set of golang.org/x/time/rate token buckets, one
// per key. A bucket that has refilled completely is indistinguishable from a
// new one, so dropping it loses nothing: that is the idle expiry.
type limiterStore struct {
	mu        sync.Mutex
	max       int
	entries   map[string]*list.Element // → *limiterEntry
	lru       *list.List               // front = most recently used
	lastSweep time.Time
	now       func() time.Time // test seam
}

type limiterEntry struct {
	key string
	lim *rate.Limiter
}

func newLimiterStore(maxKeys int) *limiterStore {
	return &limiterStore{max: maxKeys, entries: map[string]*list.Element{}, lru: list.New(), now: time.Now}
}

// reserve takes one token from key's bucket (n tokens, refilled evenly over
// per). When the bucket is empty it reports how long until the next token and
// takes nothing.
func (ls *limiterStore) reserve(key string, n int, per time.Duration) (retryAfter time.Duration, ok bool) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	now := ls.now()
	if now.Sub(ls.lastSweep) >= limiterSweepEvery {
		ls.sweep(now)
	}

	el, found := ls.entries[key]
	if found {
		ls.lru.MoveToFront(el)
	} else {
		if len(ls.entries) >= ls.max && now.Sub(ls.lastSweep) >= time.Second {
			ls.sweep(now)
		}
		for len(ls.entries) >= ls.max {
			ls.remove(ls.lru.Back())
		}
		el = ls.lru.PushFront(&limiterEntry{key: key, lim: rate.NewLimiter(rate.Every(per/time.Duration(n)), n)})
		ls.entries[key] = el
	}

	res := el.Value.(*limiterEntry).lim.ReserveN(now, 1)
	if !res.OK() {
		return per, false
	}
	if d := res.DelayFrom(now); d > 0 {
		res.CancelAt(now)
		return d, false
	}
	return 0, true
}

// sweep drops every key whose bucket is full again.
func (ls *limiterStore) sweep(now time.Time) {
	ls.lastSweep = now
	for el := ls.lru.Back(); el != nil; {
		prev := el.Prev()
		if lim := el.Value.(*limiterEntry).lim; lim.TokensAt(now) >= float64(lim.Burst()) {
			ls.remove(el)
		}
		el = prev
	}
}

func (ls *limiterStore) remove(el *list.Element) {
	delete(ls.entries, el.Value.(*limiterEntry).key)
	ls.lru.Remove(el)
}

func (ls *limiterStore) len() int {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return len(ls.entries)
}

// tooManyRequests answers 429 with the wait until the next token.
func tooManyRequests(w http.ResponseWriter, retryAfter time.Duration) {
	w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
	httpError(w, http.StatusTooManyRequests, "too many requests — slow down")
}

// rateLimit guards a handler: n hits per window per client IP. Routes that
// share a name share a bucket.
func (s *Server) rateLimit(name string, n int, window time.Duration, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if wait, ok := s.ipLimiter.reserve(name+"|"+ipRateKey(s.clientIP(r)), n, window); !ok {
			tooManyRequests(w, wait)
			return
		}
		next(w, r)
	}
}

// ipRateKey is the rate-limit identity of an address. One IPv6 customer
// routinely holds a whole /64, so the key is the /64, not the single address.
func ipRateKey(ip string) string {
	if a, err := netip.ParseAddr(ip); err == nil && a.Unmap().Is6() {
		if p, err := a.Prefix(64); err == nil {
			return p.String()
		}
	}
	return ip
}
