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

	if c.workerImageRevision != nil {
		resp.WorkerImageRevision = c.imageRevision(r.Context())
	}

	WriteJSON(w, http.StatusOK, resp)
}

// imageRevision runs the hook synchronously, never in a goroutine, so a hook
// that ignores its deadline cannot pile up goroutines. A panic, such as a
// typed-nil executor, is logged and reads as unknown so /health still answers.
func (c *Core) imageRevision(parent context.Context) (rev string) {
	ctx, cancel := context.WithTimeout(parent, workerImageRevisionTimeout)
	defer cancel()

	defer func() {
		if p := recover(); p != nil {
			c.logger.Error("worker image revision hook panicked", "panic", p)

			rev = ""
		}
	}()

	return c.workerImageRevision(ctx)
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
