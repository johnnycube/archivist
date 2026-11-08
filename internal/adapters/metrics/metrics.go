// Package metrics is the Prometheus adapter: it keeps the counters the HTTP
// adapter and the import worker report, the queue gauges it reads from the
// job store at scrape time, and the Go runtime and process collectors, all in
// one registry of its own (nothing global), and serves them in the exposition
// format. Metric names start with archivist_.
package metrics

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/johnnycube/archivist/internal/app"
)

// Metrics is the registry and its instruments.
type Metrics struct {
	reg *prometheus.Registry

	httpInFlight prometheus.Gauge
	httpRequests *prometheus.CounterVec   // route, code class
	httpDuration *prometheus.HistogramVec // route

	imports        *prometheus.CounterVec   // source, outcome
	importDuration *prometheus.HistogramVec // source
	importEvents   *prometheus.CounterVec   // source
}

var _ app.ImportObserver = (*Metrics)(nil)

// QueueStats reads the import queue (app.Jobs.Stats); nil = no queue gauges.
type QueueStats func(ctx context.Context) (app.JobStats, error)

// queueTimeout bounds the store read behind the queue gauges per scrape.
const queueTimeout = 2 * time.Second

// New registers the instruments. version labels archivist_build_info.
func New(version string, queue QueueStats) *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		httpInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "archivist_http_in_flight_requests", Help: "HTTP requests being served right now.",
		}),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "archivist_http_requests_total", Help: "HTTP requests served, by route pattern and status class.",
		}, []string{"route", "code"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "archivist_http_request_duration_seconds", Help: "Time to serve an HTTP request, by route pattern.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}, []string{"route"}),
		imports: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "archivist_imports_total",
			Help: "Import runs, by source and outcome (done, cancelled, timeout, too_many_events, archive_error, server_fault).",
		}, []string{"source", "outcome"}),
		importDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "archivist_import_duration_seconds", Help: "Time one import run took, by source.",
			Buckets: []float64{1, 5, 15, 60, 300, 900, 1800, 3600, 3 * 3600, 6 * 3600, 12 * 3600},
		}, []string{"source"}),
		importEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "archivist_import_events_total", Help: "Events written by import runs, by source.",
		}, []string{"source"}),
	}
	reg.MustRegister(m.httpInFlight, m.httpRequests, m.httpDuration, m.imports, m.importDuration, m.importEvents,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "archivist_build_info", Help: "Always 1; the version is the label.",
			ConstLabels: prometheus.Labels{"version": version},
		}, func() float64 { return 1 }),
	)
	if queue != nil {
		reg.MustRegister(
			queueGauge(queue, "archivist_imports_queued", "Imports waiting for the worker (all users).",
				func(s app.JobStats) int { return s.Queued }),
			queueGauge(queue, "archivist_imports_running", "Imports being processed.",
				func(s app.JobStats) int { return s.Running }),
		)
	}
	return m
}

// queueGauge reads one figure of the queue at scrape time; a failed read
// reports -1, which an alert can tell from an empty queue.
func queueGauge(queue QueueStats, name, help string, pick func(app.JobStats) int) prometheus.GaugeFunc {
	return prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: name, Help: help}, func() float64 {
		ctx, cancel := context.WithTimeout(context.Background(), queueTimeout)
		defer cancel()
		st, err := queue(ctx)
		if err != nil {
			return -1
		}
		return float64(pick(st))
	})
}

// ServeHTTP serves the registry in the Prometheus exposition format.
func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}

// InFlight counts a request starting (+1) or ending (-1).
func (m *Metrics) InFlight(delta int) { m.httpInFlight.Add(float64(delta)) }

// ObserveRequest records a served request. route is the mux pattern
// ("GET /api/events/{id}"), never the raw path, so the label set stays small;
// "" (no route matched) is recorded as "unmatched".
func (m *Metrics) ObserveRequest(route string, status int, d time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	m.httpRequests.WithLabelValues(route, strconv.Itoa(status/100)+"xx").Inc()
	m.httpDuration.WithLabelValues(route).Observe(d.Seconds())
}

// ImportFinished records how an import run ended (app.ImportObserver).
func (m *Metrics) ImportFinished(source, outcome string, d time.Duration, events int) {
	m.imports.WithLabelValues(source, outcome).Inc()
	m.importDuration.WithLabelValues(source).Observe(d.Seconds())
	m.importEvents.WithLabelValues(source).Add(float64(events))
}
