package rest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The body deadline bounds receiving the body, not the handler: once the body
// is in, a handler that outlasts BodyTimeout must not see its context
// cancelled (net/http cancels it when a read deadline fires on the idle
// connection).
func TestBodyDeadlineDoesNotCancelSlowHandlers(t *testing.T) {
	s := &Server{BodyTimeout: 100 * time.Millisecond}
	ts := httptest.NewServer(s.withBodyDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Errorf("read body: %v", err)
		}
		select {
		case <-r.Context().Done():
			w.WriteHeader(http.StatusInternalServerError)
		case <-time.After(400 * time.Millisecond):
			w.WriteHeader(http.StatusNoContent)
		}
	})))
	defer ts.Close()

	for _, body := range []string{`{"some":"json"}`, ""} {
		resp, err := http.Post(ts.URL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("body %q: %v", body, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("body %q: status = %d, want 204 (request context was cancelled mid-handler)", body, resp.StatusCode)
		}
	}
}
