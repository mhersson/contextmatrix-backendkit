package webhookcore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	protocol "github.com/mhersson/contextmatrix-protocol"
)

// defaultKeepaliveInterval is the SSE comment heartbeat period. NewCore does not
// default the interval, so a zero value must be resolved here before it reaches
// time.NewTicker. Tests shrink it.
const defaultKeepaliveInterval = 15 * time.Second

// HandleLogs streams protocol.LogEntry frames as Server-Sent Events, filtered by
// the configured filter query (empty = all). The query param name and the slog
// attr labelling the filter are Core seams (project/project_filter for the
// agent, session_id/session_id for chat).
//
// It writes ": connected\n\n" and flushes IMMEDIATELY: ContextMatrix's
// session-log client gives up after a handful of rapid connect failures, so the
// instant comment marks the stream healthy before any real frame arrives.
// Thereafter each entry is one "data: <json>\n\n" event; a keepalive comment is
// emitted on the ticker; and client disconnect (r.Context().Done()) unsubscribes
// and returns.
func (c *Core) HandleLogs(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, protocol.CodeInternal, "streaming not supported")

		return
	}

	filter := r.URL.Query().Get(c.logsFilterParam)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Subscribe before the connected comment so receiving that line is a
	// client-observable guarantee that the subscription is already live.
	id, ch := c.hub.Subscribe(filter)
	defer c.hub.Unsubscribe(id)

	// Mark the stream healthy immediately so the CM client's rapid-failure
	// counter never trips on a slow first frame.
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		c.logger.Debug("SSE initial write failed", "error", err)

		return
	}

	flusher.Flush()

	interval := c.keepaliveInterval
	if interval <= 0 {
		interval = defaultKeepaliveInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	c.logger.Info("SSE log client connected", c.logsFilterAttr, filter, "remote_addr", r.RemoteAddr)

	for {
		select {
		case <-r.Context().Done():
			c.logger.Info("SSE log client disconnected", c.logsFilterAttr, filter, "remote_addr", r.RemoteAddr)

			return

		case <-c.sseShutdown:
			c.logger.Info("SSE log client closed on drain", c.logsFilterAttr, filter, "remote_addr", r.RemoteAddr)

			return

		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				c.logger.Debug("SSE keepalive write failed", "error", err)

				return
			}

			flusher.Flush()

		case entry, ok := <-ch:
			if !ok {
				return
			}

			data, err := json.Marshal(entry)
			if err != nil {
				c.logger.Debug("SSE marshal failed", "error", err)

				continue
			}

			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				c.logger.Debug("SSE event write failed", "error", err)

				return
			}

			flusher.Flush()
		}
	}
}
