package webhookcore

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	protocol "github.com/mhersson/contextmatrix-protocol"

	"github.com/mhersson/contextmatrix-backendkit/logbridge"
	"github.com/mhersson/contextmatrix-backendkit/metrics"
)

// TrackerCounter reports how many containers a backend currently tracks as
// running. The health handler reads it; a nil Tracker reports zero.
type TrackerCounter interface {
	Count() int
}

// ImageLister lists the tagged images present in the node's local image store.
// The serve layer supplies a Docker-backed implementation; tests supply a fake.
type ImageLister interface {
	ListImages(ctx context.Context) ([]ImageSummary, error)
}

// ImageSummary is one tagged image in the node's local image store, in
// backend-neutral form. Tag filtering is the webhook layer's policy; the lister
// reports everything tagged.
type ImageSummary struct {
	Tags      []string
	Digests   []string
	CreatedAt int64
	SizeBytes int64
}

// CoreConfig carries the transport-core dependencies NewCore needs. Pointers may
// be shared with the serve layer; Core does not take ownership of their
// lifecycles.
type CoreConfig struct {
	APIKey            string
	Skew              time.Duration    // 0 -> protocol.DefaultMaxClockSkew
	Replay            *ReplayCache     // nil -> NewReplayCache(skew, 4096)
	Draining          *atomic.Bool     // nil -> new
	KeepaliveInterval time.Duration    // <=0 -> 15s default
	Metrics           *metrics.Metrics // nil disables instrumentation
	Logger            *slog.Logger     // nil -> slog.Default()
	Hub               *logbridge.Hub
	LogsFilterParam   string         // "project" | "session_id"
	LogsFilterAttr    string         // "project_filter" | "session_id"
	Tracker           TrackerCounter // nil-tolerant (health reports 0)
	MaxConcurrent     int
	Images            ImageLister
	ImageListFilters  []string
}

// Core is the shared HTTP transport surface both backends mount their lifecycle
// handlers onto: HMAC auth, the drain gate, request metrics, the SSE /logs
// stream, and the health/readiness/images probes. It owns no goroutines beyond
// the ones its handlers spawn; the replay janitor lives in its owner.
type Core struct {
	apiKey string
	skew   time.Duration

	replay   *ReplayCache
	draining *atomic.Bool

	// keepaliveInterval is the SSE comment heartbeat period. Zero means the
	// package default. Tests shrink it; production leaves it unset.
	keepaliveInterval time.Duration

	metrics *metrics.Metrics

	logger *slog.Logger

	hub             *logbridge.Hub
	logsFilterParam string
	logsFilterAttr  string

	tracker          TrackerCounter
	maxConcurrent    int
	images           ImageLister
	imageListFilters []string

	// sseShutdown is closed by CloseSSE at drain so every in-flight /logs handler
	// returns promptly (an SSE stream never idles, so http.Server.Shutdown would
	// otherwise block the full timeout). Guarded by sseShutdownOnce for idempotency.
	sseShutdown     chan struct{}
	sseShutdownOnce sync.Once
}

// NewCore wires a Core from its dependencies. The replay cache and draining flag
// are created if the caller leaves them nil so a bare CoreConfig still yields a
// usable Core (tests rely on this).
func NewCore(cfg CoreConfig) *Core {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	skew := cfg.Skew
	if skew == 0 {
		skew = protocol.DefaultMaxClockSkew
	}

	replay := cfg.Replay
	if replay == nil {
		replay = NewReplayCache(skew, 4096)
	}

	draining := cfg.Draining
	if draining == nil {
		draining = &atomic.Bool{}
	}

	return &Core{
		apiKey:            cfg.APIKey,
		skew:              skew,
		replay:            replay,
		draining:          draining,
		keepaliveInterval: cfg.KeepaliveInterval,
		metrics:           cfg.Metrics,
		logger:            logger,
		hub:               cfg.Hub,
		logsFilterParam:   cfg.LogsFilterParam,
		logsFilterAttr:    cfg.LogsFilterAttr,
		tracker:           cfg.Tracker,
		maxConcurrent:     cfg.MaxConcurrent,
		images:            cfg.Images,
		imageListFilters:  cfg.ImageListFilters,
		sseShutdown:       make(chan struct{}),
	}
}

// CloseSSE unblocks every in-flight /logs SSE handler. Wire it via
// httpServer.RegisterOnShutdown so SIGTERM drain returns promptly. Idempotent.
func (c *Core) CloseSSE() {
	c.sseShutdownOnce.Do(func() { close(c.sseShutdown) })
}

// AdminAuth exposes the HMAC verifier for the admin /metrics endpoint, which the
// serve layer mounts on a separate loopback listener. It reuses the same
// signed-GET verification, replay cache, and skew as the webhook routes - the
// signed-GET HMAC is real auth, preserved here.
func (c *Core) AdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return c.Auth(next)
}
