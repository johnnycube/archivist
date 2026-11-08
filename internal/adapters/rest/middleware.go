package rest

import (
	"crypto/sha256"
	"crypto/subtle"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// ---- client identity ----

// clientIP returns the caller's IP. Forwarding headers are client-controlled
// except for what our own proxies appended, so only the entry added by the
// outermost trusted proxy counts: with TrustedProxies = N that is the Nth
// X-Forwarded-For entry from the right, over all header lines. Anything else —
// N = 0, a chain shorter than N, an unparsable entry — falls back to the TCP
// peer address.
func (s *Server) clientIP(r *http.Request) string {
	if n := s.TrustedProxies; n > 0 {
		hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		if len(hops) >= n {
			if ip, ok := parseHop(hops[len(hops)-n]); ok {
				return ip.String()
			}
		}
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap().WithZone("").String()
	}
	return r.RemoteAddr
}

// parseHop parses one X-Forwarded-For entry: an IP, optionally with a port
// (some proxies append one).
func parseHop(hop string) (netip.Addr, bool) {
	hop = strings.TrimSpace(hop)
	ip, err := netip.ParseAddr(hop)
	if err != nil {
		ap, perr := netip.ParseAddrPort(hop)
		if perr != nil {
			return netip.Addr{}, false
		}
		ip = ap.Addr()
	}
	return ip.Unmap().WithZone(""), true
}

// ---- CSRF ----

// crossOriginProtection is the standard library's CSRF defence: state-changing
// requests that a browser marks as cross-origin (Sec-Fetch-Site, or Origin vs
// Host for old browsers) are refused; requests with neither header are
// non-browser clients and pass. TrustedOrigins are exact Origin values
// ("https://host[:port]") that are allowed although they differ from Host —
// for a reverse proxy that rewrites Host.
func (s *Server) crossOriginProtection() (*http.CrossOriginProtection, error) {
	cop := http.NewCrossOriginProtection()
	for _, o := range s.TrustedOrigins {
		if err := cop.AddTrustedOrigin(o); err != nil {
			return nil, err
		}
	}
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpError(w, http.StatusForbidden, "cross-origin request rejected")
	}))
	return cop, nil
}

// ---- request body deadline ----

// withBodyDeadline bounds how long a client may take to send a request body
// (the headers are bounded by http.Server.ReadHeaderTimeout). The upload route
// swaps the fixed deadline for an idle timeout, see handleImport.
//
// Only requests that have a body get a deadline, and net/http lifts it once the
// body has been read to its end. That matters: from then on the server watches
// the connection in the background, and a read deadline firing there cancels
// the request context — it would turn into a timeout for slow handlers.
func (s *Server) withBodyDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.BodyTimeout > 0 && r.Body != nil && r.Body != http.NoBody {
			// An error means the writer has no deadlines (tests): nothing to bound.
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(s.BodyTimeout))
		}
		next.ServeHTTP(w, r)
	})
}

// idleTimeoutBody turns the connection's read deadline into an idle timeout
// while a large body streams in: the deadline moves along with the reads, so
// only a stalled client times out.
type idleTimeoutBody struct {
	io.ReadCloser
	rc       *http.ResponseController
	idle     time.Duration
	extendAt time.Time
	eof      bool // net/http has lifted the deadline; leave it alone (see withBodyDeadline)
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	if now := time.Now(); !b.eof && !now.Before(b.extendAt) {
		_ = b.rc.SetReadDeadline(now.Add(b.idle))
		b.extendAt = now.Add(b.idle / 2)
	}
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.eof = true
	}
	return n, err
}

// ---- response headers ----

func (s *Server) withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if s.Secure {
			// Secure cookies mean an HTTPS deployment; browsers ignore HSTS
			// received over plain HTTP.
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		h.Set("Content-Security-Policy", s.csp) // see csp.go
		next.ServeHTTP(w, r)
	})
}

// ---- request logging + metrics ----

// Metrics is what the HTTP adapter reports requests to and serves at
// /metrics (adapters/metrics implements it). nil: requests go uncounted and
// /metrics answers 404.
type Metrics interface {
	http.Handler
	InFlight(delta int)
	// ObserveRequest records a served request under its route pattern.
	ObserveRequest(route string, status int, d time.Duration)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

// Unwrap lets http.ResponseController reach the real writer.
func (sr *statusRecorder) Unwrap() http.ResponseWriter { return sr.ResponseWriter }

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (s *Server) withObservability(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		if s.Metrics != nil {
			s.Metrics.InFlight(1)
			defer s.Metrics.InFlight(-1)
		}

		sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sr, r)

		dur := time.Since(start)
		if s.Metrics != nil {
			// The mux has set the matched pattern on r by now.
			s.Metrics.ObserveRequest(r.Pattern, sr.status, dur)
		}
		// Static assets are noise; log API and health traffic only.
		if strings.HasPrefix(r.URL.Path, "/api/") || sr.status >= 400 {
			slog.Info("http",
				"method", r.Method, "path", r.URL.Path, "status", sr.status,
				"dur_ms", dur.Milliseconds(), "ip", s.clientIP(r))
		}
	})
}

// handleMetrics serves Prometheus metrics; with MetricsToken set, only to
// scrapers that present it as a bearer token.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.MetricsToken != "" {
		got, isBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		// Compare digests: constant time, and the token's length stays hidden.
		g, want := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(s.MetricsToken))
		if !isBearer || subtle.ConstantTimeCompare(g[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			httpError(w, http.StatusUnauthorized, "metrics token required")
			return
		}
	}
	if s.Metrics == nil {
		httpError(w, http.StatusNotFound, "metrics are not enabled")
		return
	}
	s.Metrics.ServeHTTP(w, r)
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.Ready != nil {
		if err := s.Ready(r.Context()); err != nil {
			// The cause (DSN host, driver error, …) is for the log, not for
			// unauthenticated callers.
			s.logger().Warn("readiness probe failed", "err", err)
			httpError(w, http.StatusServiceUnavailable, "not ready")
			return
		}
	}
	w.Write([]byte("ready"))
}
