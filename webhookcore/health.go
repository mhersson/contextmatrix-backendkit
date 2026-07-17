package webhookcore

import (
	"net/http"

	protocol "github.com/mhersson/contextmatrix-protocol"
)

func (c *Core) HandleHealth(w http.ResponseWriter, _ *http.Request) {
	running := 0
	if c.tracker != nil {
		running = c.tracker.Count()
	}

	WriteJSON(w, http.StatusOK, protocol.HealthResponse{
		OK:                true,
		RunningContainers: running,
		MaxConcurrent:     c.maxConcurrent,
	})
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
