// Package rest is the driving (inbound) HTTP adapter. It translates HTTP to use
// case calls on *app.App and serves the web UI. It depends on the application
// layer only — no store, no provider, no crypto details.
package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/webui"
)

// CookieName is the session cookie on plain-HTTP (dev) deployments. On HTTPS
// it is never read: a cookie of this name can be planted by any sibling
// subdomain (Domain=.example.com), so only SecureCookieName counts there.
const CookieName = "archivist_session"

// SecureCookieName is the session cookie when cookies are Secure. The __Host-
// prefix makes browsers refuse the cookie unless it is Secure, Path=/ and has
// no Domain, so neither a sibling subdomain nor a plain-HTTP response can
// plant or overwrite it.
const SecureCookieName = "__Host-" + CookieName

// Request body deadlines, see withBodyDeadline.
const (
	DefaultBodyTimeout       = 30 * time.Second
	DefaultUploadIdleTimeout = 2 * time.Minute
)

// DefaultMaxZip caps one import upload (all parts together). Real social-media
// exports run to several GB; override with MAX_UPLOAD_MB.
const DefaultMaxZip = 8 << 30

// maxParts bounds how many ZIP parts one import may have.
const maxParts = 64

// Server is the HTTP adapter.
type Server struct {
	App     *app.App
	Secure  bool
	MaxZip  int64 // cap on one upload request, all parts together (bytes)
	MaxAge  int   // session cookie lifetime (seconds); keep equal to the session store's TTL
	Version string
	Ready   func(ctx context.Context) error // readiness probe (e.g. DB ping)
	Log     *slog.Logger                    // server-side error log (nil = slog.Default())

	// TrustedProxies is how many reverse proxies in front of the server append
	// to X-Forwarded-For. 0 = the header is ignored.
	TrustedProxies int
	// TrustedOrigins are extra Origin values ("https://host[:port]") accepted
	// by the CSRF check, for proxies that rewrite Host.
	TrustedOrigins []string
	// Metrics receives request metrics and serves /metrics (nil = off).
	Metrics Metrics
	// MetricsToken, when set, is the bearer token /metrics requires.
	MetricsToken string
	// BodyTimeout bounds receiving a request body; uploads instead time out
	// after UploadIdleTimeout without progress. 0 = no deadline.
	BodyTimeout       time.Duration
	UploadIdleTimeout time.Duration

	ipLimiter *limiterStore // keyed by route group + client IP
	csp       string        // Content-Security-Policy header, see buildCSP
	cspWarned sync.Once
}

// New builds an HTTP adapter over the application.
func New(a *app.App, secure bool) *Server {
	return &Server{
		App: a, Secure: secure, MaxZip: DefaultMaxZip,
		MaxAge:      int(app.DefaultSessionTTL / time.Second),
		Version:     "dev",
		BodyTimeout: DefaultBodyTimeout, UploadIdleTimeout: DefaultUploadIdleTimeout,
		ipLimiter: newLimiterStore(maxLimiterKeys),
	}
}

type ctxKey int

const userKey ctxKey = 1

// Handler wires all routes and the middleware chain
// (observability → CSRF → security headers → body deadline → mux).
// It panics on a malformed TrustedOrigins entry: that is a configuration
// error, and serving without the origin the operator asked for would only
// fail later and less clearly.
func (s *Server) Handler() http.Handler {
	if s.ipLimiter == nil {
		s.ipLimiter = newLimiterStore(maxLimiterKeys)
	}
	index, _ := webui.Index() // no shell = no hash: buildCSP falls back
	s.buildCSP(index)
	csrf, err := s.crossOriginProtection()
	if err != nil {
		panic("rest: TrustedOrigins: " + err.Error())
	}
	mux := http.NewServeMux()
	// Credential endpoints are brute-forceable: rate-limit them per IP.
	mux.HandleFunc("POST /api/auth/signup", s.rateLimit("auth", 10, time.Minute, s.handleSignup))
	mux.HandleFunc("POST /api/auth/login", s.rateLimit("auth", 10, time.Minute, s.handleLogin))
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth/me", s.handleMe)
	mux.HandleFunc("GET /api/auth/options", s.handleAuthOptions)
	mux.HandleFunc("GET /api/auth/oidc", s.handleOIDCProviders)
	mux.HandleFunc("GET /api/auth/oidc/{id}/start", s.rateLimit("auth", 10, time.Minute, s.handleOIDCStart))
	mux.HandleFunc("GET /api/auth/oidc/{id}/callback", s.rateLimit("auth", 10, time.Minute, s.handleOIDCCallback))
	mux.Handle("POST /api/auth/oidc/{id}/link", s.requireUser(s.rateLimit("auth", 10, time.Minute, s.handleOIDCLinkStart)))
	mux.Handle("POST /api/auth/password", s.requireUser(s.rateLimit("auth", 10, time.Minute, s.handlePasswordChange)))
	mux.Handle("POST /api/auth/delete", s.requireUser(s.rateLimit("auth", 10, time.Minute, s.handleAccountDelete)))

	mux.HandleFunc("GET /api/sources", s.handleSources)
	mux.Handle("POST /api/import", s.requireUser(s.rateLimit("import", 30, time.Hour, s.handleImport)))
	mux.Handle("GET /api/imports", s.requireUser(s.handleImports))
	mux.Handle("GET /api/imports/{id}", s.requireUser(s.handleImportStatus))
	mux.Handle("POST /api/imports/{id}/reprocess", s.requireUser(s.handleImportReprocess))
	mux.Handle("POST /api/imports/{id}/cancel", s.requireUser(s.handleImportCancel))
	mux.Handle("DELETE /api/imports/{id}", s.requireUser(s.handleImportDelete))
	mux.Handle("GET /api/events", s.requireUser(s.handleEvents))
	mux.Handle("GET /api/events/histogram", s.requireUser(s.handleHistogram))
	mux.Handle("GET /api/events/{id}", s.requireUser(s.handleEvent))
	mux.Handle("GET /api/events/{id}/media/{idx}", s.requireUser(s.handleMedia))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.HandleFunc("GET /metrics", s.handleMetrics)

	mux.Handle("/", webui.Handler())
	return s.withObservability(csrf.Handler(s.withCommonHeaders(s.withBodyDeadline(mux))))
}

// ---- auth ----

type credsReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	var req credsReq
	if !decode(w, r, &req) {
		return
	}
	u, token, err := s.App.Auth.SignUp(r.Context(), req.Email, req.Password)
	switch {
	case errors.Is(err, app.ErrSignupDisabled):
		httpError(w, http.StatusForbidden, "signup is disabled on this server")
		return
	case errors.Is(err, app.ErrInvalidEmail):
		httpError(w, http.StatusBadRequest, "a valid email address is required")
		return
	case errors.Is(err, app.ErrWeakPassword):
		httpError(w, http.StatusBadRequest, weakPasswordReason(err))
		return
	case errors.Is(err, app.ErrEmailTaken):
		// Reveals that the email is registered — needed for a usable signup
		// form. The per-IP rate limit on this route bounds enumeration.
		httpError(w, http.StatusConflict, "email already registered")
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	s.replaceSession(w, r, token)
	writeJSON(w, http.StatusOK, toUserDTO(u, nil))
}

// weakPasswordReason is the user-presentable reason of an app.ErrWeakPassword.
func weakPasswordReason(err error) string {
	var weak *app.WeakPasswordError
	if errors.As(err, &weak) {
		return weak.Reason
	}
	return "password does not meet the policy"
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req credsReq
	if !decode(w, r, &req) {
		return
	}
	// The use case throttles failed logins per (account, client) and per
	// account, on top of the route's per-IP limit; the client is the same key
	// the per-IP limiter uses. It also replaces the session the browser already
	// holds, atomically with creating the new one.
	previous := s.sessionToken(r)
	u, token, err := s.App.Auth.LogInGuarded(r.Context(), req.Email, req.Password, previous, ipRateKey(s.clientIP(r)))
	switch {
	case errors.Is(err, app.ErrTooManyAttempts):
		// Same answer whether or not the account exists (the use case counts
		// unknown emails the same way): status, body and Retry-After.
		var throttled *app.TooManyAttemptsError
		var wait time.Duration
		if errors.As(err, &throttled) {
			wait = throttled.RetryAfter
		}
		w.Header().Set("Retry-After", retryAfterSeconds(wait))
		httpError(w, http.StatusTooManyRequests, "too many failed login attempts; try again later")
		return
	case errors.Is(err, app.ErrInvalidCredentials):
		httpError(w, http.StatusUnauthorized, "invalid email or password")
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	s.setSession(w, token)
	writeJSON(w, http.StatusOK, toUserDTO(u, nil))
}

// retryAfterSeconds renders a Retry-After value: whole seconds, rounded up, at
// least 1.
func retryAfterSeconds(d time.Duration) string {
	return strconv.Itoa(max(int(math.Ceil(d.Seconds())), 1))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Revoke server-side: the token is dead even if a copy was stolen.
	s.revokeSessions(r, "")
	s.clearSession(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	uid := r.Context().Value(userKey).(string)
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decode(w, r, &req) {
		return
	}
	token, err := s.App.Auth.ChangePassword(r.Context(), uid, req.Current, req.New)
	switch {
	case s.throttled(w, err):
		return
	case errors.Is(err, app.ErrInvalidCredentials):
		httpError(w, http.StatusForbidden, "current password is wrong")
		return
	case errors.Is(err, app.ErrWeakPassword):
		httpError(w, http.StatusBadRequest, weakPasswordReason(err))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	// All old sessions are revoked; keep this one alive with the fresh token.
	s.setSession(w, token)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// throttled answers a TooManyAttemptsError (429 with Retry-After) and reports
// whether it did.
func (s *Server) throttled(w http.ResponseWriter, err error) bool {
	var t *app.TooManyAttemptsError
	if !errors.As(err, &t) {
		return false
	}
	w.Header().Set("Retry-After", retryAfterSeconds(t.RetryAfter))
	httpError(w, http.StatusTooManyRequests, "too many wrong passwords; try again later")
	return true
}

func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	uid := r.Context().Value(userKey).(string)
	var req struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	err := s.App.Auth.DeleteAccount(r.Context(), uid, req.Password)
	if s.throttled(w, err) {
		return
	}
	if errors.Is(err, app.ErrInvalidCredentials) {
		httpError(w, http.StatusForbidden, "password is wrong")
		return
	}
	if errors.Is(err, app.ErrJobBusy) {
		httpError(w, http.StatusConflict, "an import is queued or running; delete the account when it has finished")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.clearSession(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	token := s.sessionToken(r)
	if token == "" {
		httpError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	u, ok, err := s.App.Auth.CurrentUser(r.Context(), token)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		httpError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	linked, err := s.App.Auth.LinkedProviders(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(u, linked))
}

// ---- data ----

func (s *Server) handleSources(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sources": toSourceDTOs(s.App.Timeline.Sources())})
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	uid := r.Context().Value(userKey).(string)
	ctx := r.Context()
	source := r.URL.Query().Get("source")
	if source == "" {
		httpError(w, http.StatusBadRequest, "missing ?source")
		return
	}
	label := orDefault(r.URL.Query().Get("account"), "default")
	if err := checkText("account", label, maxLabelBytes); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}

	max := s.MaxZip
	if max <= 0 {
		max = DefaultMaxZip
	}
	if r.ContentLength > max {
		s.tooLarge(w, max)
		return
	}
	// Refuse before receiving gigabytes of upload: bad source, quota, full disk.
	// An upload without a declared length reserves the full cap.
	declared := r.ContentLength
	if declared < 0 {
		declared = max
	}
	release, err := s.App.Import.Admit(ctx, uid, source, declared)
	if err != nil {
		s.importError(w, r, err)
		return
	}
	defer release() // the upload is a job by then, or discarded

	// A multi-GB upload legitimately outlasts the body deadline of the other
	// routes; from here on (authenticated, admitted) only a stalled one times out.
	if s.UploadIdleTimeout > 0 {
		r.Body = &idleTimeoutBody{ReadCloser: r.Body, rc: http.NewResponseController(w), idle: s.UploadIdleTimeout}
	} else {
		_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	}
	r.Body = http.MaxBytesReader(w, r.Body, max)
	// Stream the multipart body straight into staging: no in-memory or
	// temp-file buffering of multi-GB archives. Every "archive" file field is
	// one part of the export. Staging survives restarts, and the background
	// worker removes the parts when done.
	mr, err := r.MultipartReader()
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	var parts []app.StagedPart
	// Cleanup must also happen when the client has gone away (ctx cancelled).
	discard := func() { s.App.Import.Discard(context.WithoutCancel(ctx), parts) }
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			discard()
			s.uploadReadError(w, err)
			return
		}
		if part.FormName() != "archive" || part.FileName() == "" {
			continue
		}
		if len(parts) >= maxParts {
			discard()
			httpError(w, http.StatusBadRequest, "too many archive parts")
			return
		}
		// Tell a failed read (the client's side) from a failed write (ours).
		body := &readErrRecorder{r: part}
		// The client's file name goes along (the use case sanitises it): some
		// exports carry data only there (WhatsApp on iOS: the chat's name).
		staged, err := s.App.Import.StageNamed(ctx, uid, part.FileName(), body)
		if err != nil {
			discard()
			if body.err != nil {
				s.uploadReadError(w, body.err)
			} else {
				s.serverError(w, r, err)
			}
			return
		}
		parts = append(parts, staged)
	}

	job, err := s.App.Import.Enqueue(ctx, app.ImportSpec{
		UserID: uid, Source: source, Label: label, Parts: parts,
	})
	if err != nil {
		discard()
		s.importError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toJobDTO(job))
}

// importError maps the import use case's typed errors; anything untyped is an
// infrastructure failure, not the client's fault.
func (s *Server) importError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, app.ErrUnknownSource):
		httpError(w, http.StatusBadRequest, "unknown source "+strconv.Quote(r.URL.Query().Get("source")))
	case errors.Is(err, app.ErrNoArchive):
		httpError(w, http.StatusBadRequest, "missing 'archive' file field")
	case errors.Is(err, app.ErrTooManyImports):
		w.Header().Set("Retry-After", "60")
		httpError(w, http.StatusTooManyRequests, "too many imports in progress; wait for one to finish")
	case errors.Is(err, app.ErrQuotaExceeded): // ErrStorageQuota and any future quota
		httpError(w, http.StatusRequestEntityTooLarge, "archive storage limit reached; delete an import to free space")
	case errors.Is(err, app.ErrInsufficientStorage):
		s.logger().Warn("upload refused: staging disk is full", "err", err)
		httpError(w, http.StatusInsufficientStorage, "the server does not have enough free disk space for this upload")
	default:
		s.serverError(w, r, err)
	}
}

// maxLabelBytes bounds an import's account label (a name like "work").
const maxLabelBytes = 200

// readErrRecorder remembers the first non-EOF error of the reader it wraps.
type readErrRecorder struct {
	r   io.Reader
	err error
}

func (e *readErrRecorder) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF && e.err == nil {
		e.err = err
	}
	return n, err
}

// uploadReadError answers a failed read of the request body: too large or a
// broken/aborted upload — the client's problem either way.
func (s *Server) uploadReadError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		s.tooLarge(w, tooBig.Limit)
		return
	}
	httpError(w, http.StatusBadRequest, "upload could not be read (interrupted or malformed)")
}

func (s *Server) tooLarge(w http.ResponseWriter, limit int64) {
	httpError(w, http.StatusRequestEntityTooLarge,
		"upload exceeds the "+strconv.FormatInt(limit>>20, 10)+" MB limit")
}

func (s *Server) handleImports(w http.ResponseWriter, r *http.Request) {
	uid := r.Context().Value(userKey).(string)
	jobs, err := s.App.Import.RecentJobs(r.Context(), uid, 20)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": toJobDTOs(jobs)})
}

func (s *Server) handleImportStatus(w http.ResponseWriter, r *http.Request) {
	uid := r.Context().Value(userKey).(string)
	job, err := s.App.Import.GetJob(r.Context(), uid, r.PathValue("id"))
	switch {
	case errors.Is(err, app.ErrNotFound):
		httpError(w, http.StatusNotFound, "job not found")
	case err != nil:
		s.serverError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, toJobDTO(job))
	}
}

// handleImportReprocess requeues a finished import; the worker re-derives its
// events from the preserved original archive.
func (s *Server) handleImportReprocess(w http.ResponseWriter, r *http.Request) {
	uid := r.Context().Value(userKey).(string)
	job, err := s.App.Import.ReprocessJob(r.Context(), uid, r.PathValue("id"))
	switch {
	case errors.Is(err, app.ErrNotFound):
		httpError(w, http.StatusNotFound, "job not found")
	case errors.Is(err, app.ErrJobBusy):
		httpError(w, http.StatusConflict, "import is queued or running")
	case err != nil:
		s.serverError(w, r, err)
	default:
		writeJSON(w, http.StatusAccepted, toJobDTO(job))
	}
}

// handleImportCancel cancels a queued import at once and asks the worker to
// stop a running one.
func (s *Server) handleImportCancel(w http.ResponseWriter, r *http.Request) {
	uid := r.Context().Value(userKey).(string)
	job, err := s.App.Import.CancelJob(r.Context(), uid, r.PathValue("id"))
	switch {
	case errors.Is(err, app.ErrNotFound):
		httpError(w, http.StatusNotFound, "job not found")
	case errors.Is(err, app.ErrJobFinished):
		httpError(w, http.StatusConflict, "import has already finished")
	case err != nil:
		s.serverError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, toJobDTO(job))
	}
}

// handleImportDelete removes an import, its preserved archive and its events.
func (s *Server) handleImportDelete(w http.ResponseWriter, r *http.Request) {
	uid := r.Context().Value(userKey).(string)
	deleted, err := s.App.Import.DeleteJob(r.Context(), uid, r.PathValue("id"))
	switch {
	case errors.Is(err, app.ErrNotFound):
		httpError(w, http.StatusNotFound, "job not found")
	case errors.Is(err, app.ErrJobBusy):
		httpError(w, http.StatusConflict, "import is running; try again when it finishes")
	case err != nil:
		s.serverError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted_events": deleted})
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	query, err := timelineQuery(r, true)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	collapse, rule, err := collapseParams(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if collapse {
		page, err := s.App.Timeline.QueryCollapsed(r.Context(), query, rule)
		if err != nil {
			s.readError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, toCollapsedPageDTO(page))
		return
	}
	page, err := s.App.Timeline.Query(r.Context(), query)
	if err != nil {
		s.readError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPageDTO(page))
}

func (s *Server) handleHistogram(w http.ResponseWriter, r *http.Request) {
	query, err := timelineQuery(r, false)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	by, err := histogramKey(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	buckets, err := s.App.Timeline.Histogram(r.Context(), query, by)
	if err != nil {
		s.readError(w, r, err)
		return
	}
	if by == app.HistogramByCategory {
		writeJSON(w, http.StatusOK, map[string]any{"buckets": toCategoryBucketDTOs(buckets)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"buckets": toBucketDTOs(buckets)})
}

// readError answers a failed read of the archive: a bad cursor is the
// client's (400), a query over its time budget a temporary refusal (503).
func (s *Server) readError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, app.ErrBadCursor):
		httpError(w, http.StatusBadRequest, "bad cursor")
	case errors.Is(err, app.ErrQueryTimeout):
		httpError(w, http.StatusServiceUnavailable, app.ErrQueryTimeout.Error())
	default:
		s.serverError(w, r, err)
	}
}

func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	uid := r.Context().Value(userKey).(string)
	se, err := s.App.Timeline.Event(r.Context(), uid, r.PathValue("id"))
	switch {
	case errors.Is(err, app.ErrNotFound):
		httpError(w, http.StatusNotFound, "event not found")
	case err != nil:
		s.serverError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, toEventDTO(se))
	}
}

// ---- helpers ----

// cookieName is the name sessions are written under.
func (s *Server) cookieName() string {
	if s.Secure {
		return SecureCookieName
	}
	return CookieName
}

// sessionToken returns the request's session token: the cookie of the one
// name sessions are written under ("" = none).
func (s *Server) sessionToken(r *http.Request) string {
	if c, err := r.Cookie(s.cookieName()); err == nil {
		return c.Value
	}
	return ""
}

func (s *Server) sessionCookie(name, value string, maxAge int) *http.Cookie {
	// Secure follows COOKIE_SECURE: on in every deployment behind TLS, off only
	// for plain-HTTP development (`make run`), see cmd/server/config.go.
	// nosemgrep: go.lang.security.audit.net.cookie-missing-secure.cookie-missing-secure
	return &http.Cookie{
		Name: name, Value: value, Path: "/", HttpOnly: true,
		Secure: s.Secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	}
}

// setSession writes the session cookie.
func (s *Server) setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, s.sessionCookie(s.cookieName(), token, s.MaxAge))
}

// clearSession expires the session cookie.
func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, s.sessionCookie(s.cookieName(), "", -1))
}

// revokeSessions revokes server-side the session the request's cookie refers
// to, unless it is keep. A stolen copy of the token is dead afterwards.
func (s *Server) revokeSessions(r *http.Request, keep string) {
	token := s.sessionToken(r)
	if token == "" || token == keep {
		return
	}
	if err := s.App.Auth.LogOut(context.WithoutCancel(r.Context()), token); err != nil {
		s.logger().Warn("revoke session", "err", err)
	}
}

// replaceSession starts the session of a fresh signup (a login replaces the
// previous session inside the use case, see handleLogin). Whatever
// session the browser held before is revoked, not just overwritten: on a
// shared browser the previous user's token must not stay valid.
func (s *Server) replaceSession(w http.ResponseWriter, r *http.Request, token string) {
	s.revokeSessions(r, token)
	s.setSession(w, token)
}

func (s *Server) requireUser(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.sessionToken(r)
		if token == "" {
			httpError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		uid, ok, err := s.App.Auth.Authenticate(r.Context(), token)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if !ok {
			httpError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, uid)))
	})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func csv(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// errBadParam is a query parameter the client got wrong; its message is the
// 400 response.
type errBadParam struct{ msg string }

func (e errBadParam) Error() string { return e.msg }

// timelineQuery reads the timeline filters of an authenticated request. A
// value that cannot be understood is an error (errBadParam), never silently
// ignored: a dropped filter would show the user MORE than they asked for.
// paged adds the pagination parameters (limit, cursor).
//
// tz is read first because it decides what the other parameters MEAN: a
// date-only from/to is a calendar day in that zone, and the zone travels on
// the query (app.Query.Loc) to the histogram's buckets and the collapse rule.
func timelineQuery(r *http.Request, paged bool) (app.Query, error) {
	p := r.URL.Query()
	for _, name := range []string{"source", "category", "type", "q", "tz", "cursor"} {
		if err := checkText(name, p.Get(name), maxParamBytes); err != nil {
			return app.Query{}, err
		}
	}
	if len(p.Get("q")) > maxSearchBytes {
		return app.Query{}, errBadParam{fmt.Sprintf("q is too long: at most %d bytes", maxSearchBytes)}
	}
	q := app.Query{
		UserID:     r.Context().Value(userKey).(string),
		Sources:    csv(p.Get("source")),
		Categories: csv(p.Get("category")),
		Types:      csv(p.Get("type")),
		Search:     p.Get("q"),
	}
	for _, c := range q.Categories {
		if !domain.Category(c).Valid() {
			return q, errBadParam{fmt.Sprintf("bad category %q", c)}
		}
	}
	loc, err := parseZone(p.Get("tz"))
	if err != nil {
		return q, err
	}
	q.Loc = loc
	if q.From, _, err = parseTime("from", p.Get("from"), loc); err != nil {
		return q, err
	}
	to, dateOnly, err := parseTime("to", p.Get("to"), loc)
	if err != nil {
		return q, err
	}
	if dateOnly {
		// "through the end of that day", not its first instant. The day is a
		// CALENDAR day in the zone, so this steps to the next local midnight
		// rather than adding 24 hours: a spring-forward day is 23 hours long
		// and a fall-back day 25. It stops a MICROsecond short because that is
		// the finest instant every store can hold (PostgreSQL rounds a
		// timestamptz to microseconds, so a nanosecond short of midnight would
		// round up to midnight there and quietly admit the next day's first
		// event, which the in-memory store would exclude).
		to = to.AddDate(0, 0, 1).Add(-time.Microsecond)
	}
	q.To = to
	if !paged {
		return q, nil
	}
	q.Cursor = p.Get("cursor")
	if v := strings.TrimSpace(p.Get("limit")); v != "" {
		// 0 means the default; anything above the maximum is clamped by the
		// use case.
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return q, errBadParam{fmt.Sprintf("bad limit %q: want a whole number, 0 or more", v)}
		}
		q.Limit = n
	}
	return q, nil
}

// Bounds of free-text query parameters. A search of a few words is a few dozen
// bytes; every word of q becomes a term the database matches five times per
// page (count, three facets, rows), so it is capped well below the URL limit.
const (
	maxParamBytes  = 4096
	maxSearchBytes = 512
)

// checkText refuses a parameter the stores cannot take: invalid UTF-8 or a
// NUL byte make PostgreSQL reject the statement (a 500 where the memory store
// answers 200), and anything longer than limit is not a real filter.
func checkText(name, v string, limit int) error {
	switch {
	case len(v) > limit:
		return errBadParam{fmt.Sprintf("%s is too long: at most %d bytes", name, limit)}
	case !utf8.ValidString(v):
		return errBadParam{fmt.Sprintf("%s is not valid UTF-8", name)}
	case strings.ContainsRune(v, 0):
		return errBadParam{fmt.Sprintf("%s contains a NUL byte", name)}
	}
	return nil
}

// collapseParams reads the collapsing switches of GET /api/events: collapse
// (absent, "1" or "true") and collapse_gap (a Go duration in [0, 24h]; 0 = no
// gap limit; default app.DefaultCollapseGap). The gap is validated even when
// collapsing is off — a parameter the server cannot understand is never
// silently ignored.
func collapseParams(r *http.Request) (collapse bool, rule app.CollapseRule, err error) {
	p := r.URL.Query()
	switch v := strings.TrimSpace(p.Get("collapse")); v {
	case "":
	case "1", "true":
		collapse = true
	default:
		return false, rule, errBadParam{fmt.Sprintf("bad collapse %q: want 1 or true", v)}
	}
	rule = app.DefaultCollapseRule()
	if v := strings.TrimSpace(p.Get("collapse_gap")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return false, rule, errBadParam{fmt.Sprintf("bad collapse_gap %q: want a duration such as 30m or 2h", v)}
		}
		rule.Gap = d
		if err := rule.Validate(); err != nil {
			return false, rule, errBadParam{fmt.Sprintf("bad collapse_gap %q: want 0 to 24h", v)}
		}
	}
	return collapse, rule, nil
}

// histogramKey reads ?by= of GET /api/events/histogram: source (default) or
// category.
func histogramKey(r *http.Request) (app.HistogramKey, error) {
	switch v := strings.TrimSpace(r.URL.Query().Get("by")); v {
	case "", "source":
		return app.HistogramBySource, nil
	case "category":
		return app.HistogramByCategory, nil
	default:
		return "", errBadParam{fmt.Sprintf("bad by %q: want source or category", v)}
	}
}

// parseTime parses a from/to bound: RFC 3339, or a date (YYYY-MM-DD), which is
// what the SPA sends. A date names a CALENDAR DAY in loc (nil = UTC), so
// from=2024-03-01&tz=Europe/Berlin is that day's first instant there, not UTC
// midnight; an RFC 3339 value carries its own offset and is unaffected. Empty
// means unbounded.
func parseTime(param, s string, loc *time.Location) (t time.Time, dateOnly bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, nil
	}
	if t, err := time.ParseInLocation(time.DateOnly, s, app.Zone(loc)); err == nil {
		return t, true, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), false, nil
	}
	return time.Time{}, false, errBadParam{fmt.Sprintf("bad %s %q: want YYYY-MM-DD or an RFC 3339 timestamp", param, s)}
}

// maxZoneName bounds ?tz=: the longest IANA name is 32 characters
// ("America/Argentina/ComodRivadavia"), so anything much longer is junk that
// never needs to reach the zone loader.
const maxZoneName = 64

// parseZone reads ?tz=, the VIEWER'S time zone: the zone the query's day
// boundaries are drawn in (histogram buckets, the collapse rule's "same day",
// and a date-only from/to). Absent or empty means UTC, so a request that names
// no zone behaves exactly as it always has. The value is an IANA name
// ("Europe/Berlin"); "Local" is refused because it would mean the SERVER's
// zone, which is not the viewer's, and stores hand the name on to a database.
func parseZone(s string) (*time.Location, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.UTC, nil
	}
	// Nothing absurd reaches the loader, and nothing absurd is echoed back
	// either: an over-long or control-character value is reported without
	// quoting it.
	if len(s) > maxZoneName {
		return nil, errBadParam{"bad tz: want an IANA time zone name such as Europe/Berlin"}
	}
	for _, r := range s {
		if r < ' ' || r == 0x7f {
			return nil, errBadParam{"bad tz: want an IANA time zone name such as Europe/Berlin"}
		}
	}
	if s != "Local" {
		if loc, err := time.LoadLocation(s); err == nil {
			return loc, nil
		}
	}
	return nil, errBadParam{fmt.Sprintf("bad tz %q: want an IANA time zone name such as Europe/Berlin", s)}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func (s *Server) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// serverError answers an unexpected failure. The cause is logged, never sent:
// error text can carry SQL, file paths or hostnames.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.logger().Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	httpError(w, http.StatusInternalServerError, "internal server error")
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
