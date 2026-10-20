package webhookcore

import (
	"context"
	"net/http"
	"time"

	protocol "github.com/mhersson/contextmatrix-protocol"
)

// workerImageRevisionTimeout keeps a slow Docker lookup well inside CM's
// /health probe timeout.
const workerImageRevisionTimeout = 500 * time.Millisecond

func (c *Core) HandleHealth(w http.ResponseWriter, r *http.Request) {
	running := 0
	if c.tracker != nil {
		running = c.tracker.Count()
	}

	build := c.build

	resp := protocol.HealthResponse{
		OK:                true,
		RunningContainers: running,
		MaxConcurrent:     c.maxConcurrent,
		Build:             &build,
	}

	// Synchronous on purpose: a panicking hook stays inside net/http's
	// per-request recover instead of crashing serve from a goroutine.
	if c.workerImageRevision != nil {
		ctx, cancel := context.WithTimeout(r.Context(), workerImageRevisionTimeout)
		resp.WorkerImageRevision = c.workerImageRevision(ctx)

		cancel()
	}

	WriteJSON(w, http.StatusOK, resp)
}

// readyResponse is the /readyz body. It is a custom shape (not ErrorResponse)
// so the readiness probe stays self-describing for orchestrators.
type readyResponse struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

func (c *Core) HandleReadyz(w http.ResponseWriter, _ *http.Request) {
	if c.draining.Load() {
		WriteJSON(w, http.StatusServiceUnavailable, readyResponse{OK: false, Reason: "draining"})

		return
	}

	WriteJSON(w, http.StatusOK, readyResponse{OK: true})
}
