package rest_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/metrics"
	"github.com/johnnycube/archivist/internal/adapters/rest"
	"github.com/johnnycube/archivist/internal/app"
)

// request is a POST/GET builder for the header-sensitive tests below.
func request(h http.Handler, method, url, body string, hdr map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		if k == "RemoteAddr" {
			req.RemoteAddr = v
			continue
		}
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func signedIn(h http.Handler, cookies ...*http.Cookie) bool {
	return request(h, "GET", "/api/auth/me", "", nil, cookies...).Code == http.StatusOK
}

// ---- CSRF ----

func TestCSRF(t *testing.T) {
	srv := rest.New(newApp(t), false)
	srv.TrustedOrigins = []string{"https://archive.example.org"}
	h := srv.Handler()
	// httptest requests carry Host: example.com.
	for _, tc := range []struct {
		name   string
		method string
		hdr    map[string]string
		want   int
	}{
		{"cross-site POST", "POST", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, 403},
		{"cross-site POST without Origin", "POST", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"same-site (sibling subdomain) POST", "POST", map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://blog.example.com"}, 403},
		{"old browser, foreign Origin", "POST", map[string]string{"Origin": "https://evil.example"}, 403},
		{"old browser, opaque Origin", "POST", map[string]string{"Origin": "null"}, 403},
		{"Sec-Fetch-Site wins over a matching Origin", "POST", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://example.com"}, 403},
		{"cross-site DELETE", "DELETE", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},

		{"same-origin POST", "POST", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://example.com"}, 200},
		{"user-initiated POST", "POST", map[string]string{"Sec-Fetch-Site": "none"}, 200},
		{"old browser, Origin matches Host", "POST", map[string]string{"Origin": "http://example.com"}, 200},
		{"non-browser client", "POST", nil, 200},
		{"trusted origin", "POST", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://archive.example.org"}, 200},
		{"trusted origin, old browser", "POST", map[string]string{"Origin": "https://archive.example.org"}, 200},
		{"cross-site GET is not state-changing", "GET", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, 200},
		{"cross-site HEAD", "HEAD", map[string]string{"Sec-Fetch-Site": "cross-site"}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := "/api/auth/logout"
			switch tc.method {
			case "GET", "HEAD":
				url = "/api/sources"
			case "DELETE":
				url = "/api/imports/x"
			}
			rec := request(h, tc.method, url, "", tc.hdr)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == 403 && !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("rejection is not the API's JSON error: %q", rec.Body.String())
			}
		})
	}
}

// The point of it all: a forged cross-site request must not act on the session.
func TestCSRFForgedLogoutLeavesSessionAlive(t *testing.T) {
	h := newHandler(t)
	c := signup(t, h)
	rec := request(h, "POST", "/api/auth/logout", "", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, c)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("forged logout = %d, want 403", rec.Code)
	}
	if !signedIn(h, c) {
		t.Error("the forged logout revoked the session")
	}
}

func TestInvalidTrustedOriginPanics(t *testing.T) {
	srv := rest.New(newApp(t), false)
	srv.TrustedOrigins = []string{"archive.example.org/path"}
	defer func() {
		if recover() == nil {
			t.Error("Handler accepted a malformed trusted origin")
		}
	}()
	srv.Handler()
}

// ---- response headers ----

func TestSecurityHeaders(t *testing.T) {
	for _, secure := range []bool{true, false} {
		rec := request(rest.New(newApp(t), secure).Handler(), "GET", "/api/sources", "", nil)
		h := rec.Header()
		for name, want := range map[string]string{
			"Cross-Origin-Opener-Policy": "same-origin",
			"Referrer-Policy":            "no-referrer",
			"X-Content-Type-Options":     "nosniff",
			"X-Frame-Options":            "DENY",
			"Permissions-Policy":         "camera=(), microphone=(), geolocation=()",
		} {
			if got := h.Get(name); got != want {
				t.Errorf("secure=%v: %s = %q, want %q", secure, name, got, want)
			}
		}
		if h.Get("Content-Security-Policy") == "" {
			t.Errorf("secure=%v: no CSP", secure)
		}
		wantHSTS := ""
		if secure {
			wantHSTS = "max-age=31536000; includeSubDomains"
		}
		if got := h.Get("Strict-Transport-Security"); got != wantHSTS {
			t.Errorf("secure=%v: HSTS = %q, want %q", secure, got, wantHSTS)
		}
	}
}

// ---- /readyz, /metrics ----

func TestReadyzDoesNotLeakTheCause(t *testing.T) {
	var logs bytes.Buffer
	srv := rest.New(newApp(t), false)
	srv.Log = slog.New(slog.NewTextHandler(&logs, nil))
	srv.Ready = func(context.Context) error { return errSecret }
	rec := request(srv.Handler(), "GET", "/readyz", "", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d, want 503", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "hunter2") || strings.Contains(body, "db.internal") {
		t.Errorf("readyz leaks the cause: %s", body)
	}
	if !strings.Contains(logs.String(), "hunter2") {
		t.Errorf("the cause was not logged; log:\n%s", logs.String())
	}

	srv.Ready = func(context.Context) error { return nil }
	if rec := request(srv.Handler(), "GET", "/readyz", "", nil); rec.Code != 200 || rec.Body.String() != "ready" {
		t.Errorf("healthy readyz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestMetricsToken(t *testing.T) {
	const token = "s3cret-scrape-token-0001"
	srv := rest.New(newApp(t), false)
	if rec := request(srv.Handler(), "GET", "/metrics", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("metrics without a metrics adapter = %d, want 404", rec.Code)
	}
	srv.Metrics = metrics.New("test", nil)
	if rec := request(srv.Handler(), "GET", "/metrics", "", nil); rec.Code != 200 {
		t.Fatalf("metrics without a configured token = %d, want 200", rec.Code)
	}

	srv.MetricsToken = token
	h := srv.Handler()
	for name, auth := range map[string]string{
		"no header":      "",
		"wrong token":    "Bearer nope",
		"prefix of it":   "Bearer " + token[:10],
		"longer":         "Bearer " + token + "x",
		"not bearer":     "Basic " + token,
		"bare token":     token,
		"empty bearer":   "Bearer ",
		"case-different": "Bearer " + strings.ToUpper(token),
	} {
		rec := request(h, "GET", "/metrics", "", map[string]string{"Authorization": auth})
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: metrics = %d (WWW-Authenticate %q), want 401", name, rec.Code, rec.Header().Get("WWW-Authenticate"))
		}
		if strings.Contains(rec.Body.String(), "archivist_") {
			t.Errorf("%s: the 401 carries metrics", name)
		}
	}
	request(h, "GET", "/api/events/histogram", "", nil) // a 401, counted under its pattern
	rec := request(h, "GET", "/metrics", "", map[string]string{"Authorization": "Bearer " + token})
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `archivist_http_requests_total{code="4xx",route="GET /api/events/histogram"}`) ||
		!strings.Contains(body, `archivist_build_info{version="test"} 1`) || !strings.Contains(body, "go_goroutines") {
		t.Errorf("metrics with the token = %d: %s", rec.Code, body)
	}
}

// ---- session cookie ----

const creds = `{"email":"me@x.com","password":"password123"}`

func TestSecureCookieUsesHostPrefix(t *testing.T) {
	h := rest.New(newApp(t), true).Handler()
	rec := request(h, "POST", "/api/auth/signup", creds, nil)
	if rec.Code != 200 {
		t.Fatalf("signup = %d", rec.Code)
	}
	c := cookieNamed(rec, "__Host-archivist_session")
	if c == nil {
		t.Fatalf("no __Host- session cookie; got %v", rec.Result().Cookies())
	}
	// What browsers demand of a __Host- cookie — they drop it otherwise.
	if !c.Secure || c.Path != "/" || c.Domain != "" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie = %+v, want Secure, Path=/, no Domain, HttpOnly, SameSite=Lax", c)
	}
	if cookieNamed(rec, rest.CookieName) != nil {
		t.Error("the legacy-named cookie is still being issued")
	}
	if !signedIn(h, c) {
		t.Error("the __Host- cookie does not authenticate")
	}
	if rec := request(h, "GET", "/api/events", "", nil, c); rec.Code != 200 {
		t.Errorf("events with the __Host- cookie = %d", rec.Code)
	}
}

func TestPlainHTTPCookieKeepsItsName(t *testing.T) {
	rec := request(newHandler(t), "POST", "/api/auth/signup", creds, nil)
	if c := cookieNamed(rec, rest.CookieName); c == nil || c.Secure {
		t.Errorf("dev cookie = %+v, want %s without Secure", c, rest.CookieName)
	}
	if cookieNamed(rec, rest.SecureCookieName) != nil {
		t.Error("a __Host- cookie without Secure would be dropped by browsers")
	}
}

// On HTTPS only the __Host- cookie counts. A plain archivist_session cookie
// can be planted by any sibling subdomain (Domain=.example.com): honouring it
// would let that subdomain log the victim into the attacker's account.
func TestPlainCookieIsIgnoredOnHTTPS(t *testing.T) {
	h := rest.New(newApp(t), true).Handler()
	token := cookieNamed(request(h, "POST", "/api/auth/signup", creds, nil), rest.SecureCookieName).Value
	planted := &http.Cookie{Name: rest.CookieName, Value: token}

	if rec := request(h, "GET", "/api/events", "", nil, planted); rec.Code != http.StatusUnauthorized {
		t.Errorf("events with a plain cookie on HTTPS = %d, want 401", rec.Code)
	}
	rec := request(h, "GET", "/api/auth/me", "", nil, planted)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("me with a plain cookie on HTTPS = %d, want 401", rec.Code)
	}
	if c := cookieNamed(rec, rest.SecureCookieName); c != nil {
		t.Errorf("me promoted a plain cookie to the __Host- name: %+v", c)
	}
	if !signedIn(h, &http.Cookie{Name: rest.SecureCookieName, Value: token}) {
		t.Error("the __Host- session itself must still work")
	}
}

func TestLoginRevokesThePreviousSession(t *testing.T) {
	for _, secure := range []bool{false, true} {
		h := rest.New(newApp(t), secure).Handler()
		name := rest.CookieName
		if secure {
			name = rest.SecureCookieName
		}
		old := cookieNamed(request(h, "POST", "/api/auth/signup", creds, nil), name)

		// A failed login changes nothing.
		if rec := request(h, "POST", "/api/auth/login", `{"email":"me@x.com","password":"wrong-password"}`, nil, old); rec.Code != 401 {
			t.Fatalf("bad login = %d", rec.Code)
		}
		if !signedIn(h, old) {
			t.Fatalf("secure=%v: a failed login revoked the existing session", secure)
		}

		rec := request(h, "POST", "/api/auth/login", creds, nil, old)
		if rec.Code != 200 {
			t.Fatalf("login = %d: %s", rec.Code, rec.Body.String())
		}
		fresh := cookieNamed(rec, name)
		if fresh == nil || fresh.Value == old.Value {
			t.Fatalf("secure=%v: login did not issue a new session", secure)
		}
		if signedIn(h, old) {
			t.Errorf("secure=%v: the session the browser held before the login is still valid", secure)
		}
		if !signedIn(h, fresh) {
			t.Errorf("secure=%v: the new session does not work", secure)
		}
	}
}

// ---- rate limits ----

func loginFrom(h http.Handler, ip, email, password string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)
	return request(h, "POST", "/api/auth/login", body, map[string]string{"RemoteAddr": ip + ":40000"})
}

func TestRateLimitRetryAfter(t *testing.T) {
	h := newHandler(t)
	for i := range 10 {
		if rec := loginFrom(h, "198.51.100.1", fmt.Sprintf("u%d@x.com", i), "whatever-pw"); rec.Code != 401 {
			t.Fatalf("login %d = %d, want 401", i, rec.Code)
		}
	}
	rec := loginFrom(h, "198.51.100.1", "u10@x.com", "whatever-pw")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th login in a minute = %d, want 429", rec.Code)
	}
	// 10/min refills one token every 6s: that is the wait, not the whole window.
	if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 1 || secs > 6 {
		t.Errorf("Retry-After = %q, want 1..6 seconds", rec.Header().Get("Retry-After"))
	}
	if rec := loginFrom(h, "198.51.100.2", "u11@x.com", "whatever-pw"); rec.Code != 401 {
		t.Errorf("another IP = %d, want 401 (own bucket)", rec.Code)
	}
	// One IPv6 /64 is one client.
	for i := range 10 {
		loginFrom(h, fmt.Sprintf("[2001:db8:0:1::%x]", i+1), fmt.Sprintf("v%d@x.com", i), "whatever-pw")
	}
	if rec := loginFrom(h, "[2001:db8:0:1:ffff::1]", "v10@x.com", "whatever-pw"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("rotating inside one IPv6 /64 = %d, want 429", rec.Code)
	}
}

// Rotating a spoofed X-Forwarded-For must not buy a fresh bucket, with or
// without a trusted proxy in front.
func TestRateLimitIgnoresSpoofedForwardedFor(t *testing.T) {
	for _, proxies := range []int{0, 1} {
		srv := rest.New(newApp(t), false)
		srv.TrustedProxies = proxies
		h := srv.Handler()
		var last int
		for i := range 11 {
			xff := fmt.Sprintf("10.9.8.%d", i) // attacker-chosen
			if proxies == 1 {
				xff += ", 198.51.100.77" // appended by our proxy: the real peer
			}
			body := fmt.Sprintf(`{"email":"u%d@x.com","password":"whatever-pw"}`, i)
			last = request(h, "POST", "/api/auth/login", body, map[string]string{"X-Forwarded-For": xff}).Code
		}
		if last != http.StatusTooManyRequests {
			t.Errorf("trusted proxies = %d: 11th login with a rotated X-Forwarded-For = %d, want 429", proxies, last)
		}
	}
}

// fastHasher keeps the throttle tests about the throttle: a hundred Argon2
// verifications are not what they measure.
type fastHasher struct{}

func (fastHasher) Hash(pw string) (string, error) { return "H:" + pw, nil }
func (fastHasher) Verify(enc, pw string) bool     { return enc == "H:"+pw }
func (fastHasher) NeedsRehash(string) bool        { return false }

// newThrottleHandler is a server with me@x.com signed up whose per-IP limiter
// never fires (its clock jumps a minute per look), which leaves the use case's
// login throttle as the only source of a 429.
func newThrottleHandler(t *testing.T, trustedProxies int) http.Handler {
	t.Helper()
	deps := newDeps(t)
	deps.Hasher = fastHasher{}
	srv := rest.New(app.New(deps), false)
	srv.TrustedProxies = trustedProxies
	now := time.Now()
	srv.SetLimiterClock(func() time.Time { now = now.Add(time.Minute); return now })
	h := srv.Handler()
	signup(t, h) // me@x.com
	return h
}

type loginOutcome struct {
	Code       int
	Body       string
	RetryAfter string
}

func outcome(rec *httptest.ResponseRecorder) loginOutcome {
	return loginOutcome{rec.Code, rec.Body.String(), rec.Header().Get("Retry-After")}
}

// 10 failures of one client against one account lock that pair — and only it.
func TestLoginThrottleLocksOnlyTheClientAccountPair(t *testing.T) {
	h := newThrottleHandler(t, 0)
	const attacker, owner = "198.51.100.7", "203.0.113.50"

	attack := func(email string) (out []loginOutcome) {
		for range app.MaxLoginFailuresPair + 1 {
			out = append(out, outcome(loginFrom(h, attacker, email, "wrong-password")))
		}
		return out
	}
	known := attack("me@x.com")
	for i, o := range known {
		if want := map[bool]int{true: 401, false: 429}[i < app.MaxLoginFailuresPair]; o.Code != want {
			t.Fatalf("attempt %d on a real account = %d, want %d", i+1, o.Code, want)
		}
	}
	// The wait is until the oldest failure leaves the window, in whole seconds.
	window := int(app.LoginAttemptWindow / time.Second)
	if secs, err := strconv.Atoi(known[len(known)-1].RetryAfter); err != nil || secs < window-30 || secs > window {
		t.Errorf("Retry-After = %q, want about %d seconds", known[len(known)-1].RetryAfter, window)
	}

	// No oracle: an unknown email is throttled exactly like a registered one —
	// status, body and Retry-After (give or take the second the test took).
	unknown := attack("nobody@x.com")
	for i := range known {
		k, u := known[i], unknown[i]
		ks, _ := strconv.Atoi(k.RetryAfter)
		us, _ := strconv.Atoi(u.RetryAfter)
		if k.Code != u.Code || k.Body != u.Body || (k.RetryAfter == "") != (u.RetryAfter == "") || ks-us > 2 || us-ks > 2 {
			t.Errorf("attempt %d: unknown account = %+v, real account = %+v; want identical answers", i+1, u, k)
		}
	}

	// The locked client stays locked whatever it sends: the right password,
	// another spelling of the email, another address of its IPv6 /64.
	for _, email := range []string{"me@x.com", "ME@X.com", "  me@x.com "} {
		if rec := loginFrom(h, attacker, email, "password123"); rec.Code != http.StatusTooManyRequests {
			t.Errorf("login as %q from the locked client = %d, want 429", email, rec.Code)
		}
	}
	// The owner, coming from elsewhere, gets in: one client can lock out only
	// itself.
	if rec := loginFrom(h, owner, "me@x.com", "password123"); rec.Code != http.StatusOK {
		t.Errorf("owner from another address = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// Other accounts are not affected, not even for the locked client.
	if rec := loginFrom(h, attacker, "other@x.com", "wrong-password"); rec.Code != http.StatusUnauthorized {
		t.Errorf("another account from the locked client = %d, want 401", rec.Code)
	}
}

// The client of the throttle is the client of the per-IP limiter: the address
// a trusted proxy reports, and a whole IPv6 /64.
func TestLoginThrottleClientKey(t *testing.T) {
	h := newThrottleHandler(t, 1)
	from := func(xff, password string) int {
		body := fmt.Sprintf(`{"email":"me@x.com","password":%q}`, password)
		return request(h, "POST", "/api/auth/login", body, map[string]string{"X-Forwarded-For": xff}).Code
	}
	for i := range app.MaxLoginFailuresPair {
		// The spoofable left part of the header rotates; the hop our proxy
		// appended rotates inside one /64.
		if code := from(fmt.Sprintf("10.0.0.%d, 2001:db8:0:1::%x", i, i+1), "wrong-password"); code != 401 {
			t.Fatalf("failure %d = %d, want 401", i+1, code)
		}
	}
	if code := from("2001:db8:0:1:ffff::1", "password123"); code != http.StatusTooManyRequests {
		t.Errorf("same /64 after %d failures = %d, want 429", app.MaxLoginFailuresPair, code)
	}
	if code := from("2001:db8:0:2::1", "password123"); code != http.StatusOK {
		t.Errorf("another /64 = %d, want 200", code)
	}
}

// Guessing spread over many clients is bounded per account.
func TestLoginThrottlePerAccount(t *testing.T) {
	h := newThrottleHandler(t, 0)
	ip := func(i int) string { return fmt.Sprintf("198.51.%d.%d", 100+i/250, 1+i%250) }
	for i := range app.MaxLoginFailuresAccount {
		if rec := loginFrom(h, ip(i), "me@x.com", "wrong-password"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d = %d, want 401", i+1, rec.Code)
		}
	}
	rec := loginFrom(h, "203.0.113.50", "me@x.com", "password123")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("owner after %d failures over all clients = %d (Retry-After %q), want 429",
			app.MaxLoginFailuresAccount, rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := loginFrom(h, "203.0.113.50", "other@x.com", "wrong-password"); rec.Code != http.StatusUnauthorized {
		t.Errorf("another account = %d, want 401", rec.Code)
	}
}

// ---- request body deadline ----

// rawRequest writes the chunks of one HTTP request to a fresh connection,
// pausing between them, and returns the response status (0 = the connection
// was closed without one) and how long the server took to answer after the
// last chunk.
func rawRequest(t *testing.T, addr string, pause time.Duration, chunks ...[]byte) (status int, waited time.Duration) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for i, chunk := range chunks {
		if i > 0 {
			time.Sleep(pause)
		}
		if _, err := conn.Write(chunk); err != nil {
			break // the server has already given up on us
		}
	}
	start := time.Now()
	conn.SetReadDeadline(start.Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return 0, time.Since(start)
	}
	resp.Body.Close()
	return resp.StatusCode, time.Since(start)
}

func TestStalledBodyTimesOut(t *testing.T) {
	srv := rest.New(newApp(t), false)
	srv.BodyTimeout = 200 * time.Millisecond
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	head := "POST /api/auth/login HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 200\r\n\r\n"
	status, waited := rawRequest(t, ts.Listener.Addr().String(), 0, []byte(head+`{"email":"me@`)) // …and never the rest
	if waited > 3*time.Second {
		t.Fatalf("the server waited %v on a stalled body (status %d); want it cut off after ~200ms", waited, status)
	}
	if status != 0 && status != http.StatusBadRequest {
		t.Errorf("stalled body = %d, want 400 or a closed connection", status)
	}
}

func multipartUpload(t *testing.T, zip []byte) (contentType string, body []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("archive", "part.zip")
	fw.Write(zip)
	mw.Close()
	return mw.FormDataContentType(), buf.Bytes()
}

func TestSlowUploadOutlivesBodyTimeout(t *testing.T) {
	uploadDir := t.TempDir()
	srv := rest.New(app.New(newDepsIn(t, uploadDir)), false)
	srv.BodyTimeout = 150 * time.Millisecond
	srv.UploadIdleTimeout = 5 * time.Second
	h := srv.Handler()
	c := signup(t, h)
	ts := httptest.NewServer(h)
	defer ts.Close()

	ct, body := multipartUpload(t, zipBytes(t))
	head := fmt.Sprintf("POST /api/import?source=reddit HTTP/1.1\r\nHost: x\r\nCookie: %s=%s\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n",
		c.Name, c.Value, ct, len(body))
	third := len(body) / 3
	// Three pauses of 2× the body timeout: far past the fixed deadline, always
	// within the idle timeout.
	status, _ := rawRequest(t, ts.Listener.Addr().String(), 300*time.Millisecond,
		[]byte(head), body[:third], body[third:2*third], body[2*third:])
	if status != http.StatusAccepted {
		t.Fatalf("slow but steady upload = %d, want 202", status)
	}
}

func TestStalledUploadTimesOut(t *testing.T) {
	uploadDir := t.TempDir()
	srv := rest.New(app.New(newDepsIn(t, uploadDir)), false)
	srv.UploadIdleTimeout = 200 * time.Millisecond
	h := srv.Handler()
	c := signup(t, h)
	ts := httptest.NewServer(h)
	defer ts.Close()

	ct, body := multipartUpload(t, zipBytes(t))
	head := fmt.Sprintf("POST /api/import?source=reddit HTTP/1.1\r\nHost: x\r\nCookie: %s=%s\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n",
		c.Name, c.Value, ct, len(body))
	status, waited := rawRequest(t, ts.Listener.Addr().String(), 0, append([]byte(head), body[:len(body)/2]...))
	if waited > 3*time.Second {
		t.Fatalf("the server waited %v on a stalled upload (status %d)", waited, status)
	}
	if status != 0 && status != http.StatusBadRequest {
		t.Errorf("stalled upload = %d, want 400 or a closed connection", status)
	}
	// The handler has returned by the time the response/close is observed, but
	// give its cleanup a moment on a loaded machine.
	deadline := time.Now().Add(2 * time.Second)
	for stagedFiles(uploadDir) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := stagedFiles(uploadDir); n != 0 {
		t.Errorf("stalled upload left %d staged files behind", n)
	}
}
