package metrics_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/metrics"
	"github.com/johnnycube/archivist/internal/app"
)

func scrape(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("scrape = %d", rec.Code)
	}
	return rec.Body.String()
}

func TestMetricsReportRequestsImportsAndQueue(t *testing.T) {
	m := metrics.New("1.2.3", func(context.Context) (app.JobStats, error) { return app.JobStats{Queued: 4, Running: 1}, nil })
	m.InFlight(1)
	m.ObserveRequest("GET /api/events", 200, 30*time.Millisecond)
	m.ObserveRequest("", 404, time.Millisecond)
	m.ImportFinished("reddit", "done", 90*time.Second, 1200)
	m.ImportFinished("reddit", "server_fault", time.Second, 0)

	body := scrape(t, m)
	for _, want := range []string{
		`archivist_build_info{version="1.2.3"} 1`,
		`archivist_http_in_flight_requests 1`,
		`archivist_http_requests_total{code="2xx",route="GET /api/events"} 1`,
		`archivist_http_requests_total{code="4xx",route="unmatched"} 1`,
		`archivist_imports_total{outcome="done",source="reddit"} 1`,
		`archivist_imports_total{outcome="server_fault",source="reddit"} 1`,
		`archivist_import_events_total{source="reddit"} 1200`,
		`archivist_import_duration_seconds_count{source="reddit"} 2`,
		`archivist_imports_queued 4`,
		`archivist_imports_running 1`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape lacks %q", want)
		}
	}
}

func TestQueueGaugesReportAFailedRead(t *testing.T) {
	m := metrics.New("x", func(context.Context) (app.JobStats, error) { return app.JobStats{}, errors.New("db down") })
	if body := scrape(t, m); !strings.Contains(body, "archivist_imports_queued -1") {
		t.Errorf("a failed queue read is not reported as -1")
	}
	if body := scrape(t, metrics.New("x", nil)); strings.Contains(body, "archivist_imports_queued") {
		t.Error("queue gauges without a queue")
	}
}
