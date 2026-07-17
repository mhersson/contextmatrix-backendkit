// Package metrics defines the five Prometheus collectors shared by
// contextmatrix-agent and contextmatrix-chat, namespaced per backend via the
// namespace parameter to New (e.g. "cm_agent" or "cm_chat"). All metrics live
// on a dedicated prometheus.Registry (not the global default) so tests stay
// hermetic - each call to New constructs its own *Metrics.
//
// Label cardinality is bounded on purpose: no card_id / project labels;
// endpoint labels pass through NormalizeEndpoint against a per-backend
// allowlist; container outcome is a fixed enum owned by each backend;
// broadcaster drops are unlabeled.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics bundles the five Prometheus collectors shared by both backends. It
// is constructed once at serve startup and injected into the components that
// observe; components never reach for a global.
type Metrics struct {
	// Registry is the registerer these collectors live on, exposed so the admin
	// /metrics handler can be wired to the same registry.
	Registry *prometheus.Registry

	WebhookRequestsTotal   *prometheus.CounterVec
	WebhookRequestDuration *prometheus.HistogramVec
	ContainerDuration      *prometheus.HistogramVec
	RunningContainers      prometheus.Gauge
	BroadcasterDropsTotal  prometheus.Counter

	endpoints map[string]struct{}
}

// New registers the five shared collectors under namespace on a fresh
// registry and returns the bundle. The dedicated registry also carries the
// standard Go runtime + Process collectors so /metrics exposes go_* /
// process_* alongside the namespaced series - the dedicated-registry shape
// would otherwise drop them. endpoints is the per-backend allowlist consumed
// by NormalizeEndpoint.
func New(namespace string, endpoints []string) *Metrics {
	reg := prometheus.NewRegistry()
	factory := promauto.With(reg)

	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	set := make(map[string]struct{}, len(endpoints))
	for _, e := range endpoints {
		set[e] = struct{}{}
	}

	return &Metrics{
		Registry: reg,

		WebhookRequestsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: namespace + "_webhook_requests_total",
				Help: "Total webhook requests processed, labelled by endpoint, HTTP status, and a coarse outcome code.",
			},
			[]string{"endpoint", "status", "code"},
		),

		WebhookRequestDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    namespace + "_webhook_request_duration_seconds",
				Help:    "Wall-clock duration of webhook requests, in seconds.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"endpoint"},
		),

		ContainerDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name: namespace + "_container_duration_seconds",
				Help: "Wall-clock container lifetime from start to exit, in seconds.",
				Buckets: []float64{
					1, 5, 15, 30, 60,
					300, 600, 1800, 3600, 7200,
				},
			},
			[]string{"outcome"},
		),

		RunningContainers: factory.NewGauge(prometheus.GaugeOpts{
			Name: namespace + "_running_containers",
			Help: "Number of containers currently tracked as running.",
		}),

		BroadcasterDropsTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: namespace + "_broadcaster_drops_total",
			Help: "Total log entries dropped for slow SSE subscribers. Unlabeled to keep series cardinality at O(1).",
		}),

		endpoints: set,
	}
}

// NormalizeEndpoint collapses an arbitrary request path to one of the
// backend's well-known endpoints, or "other" for unknown paths.
func (m *Metrics) NormalizeEndpoint(path string) string {
	if _, ok := m.endpoints[path]; ok {
		return path
	}

	return "other"
}
