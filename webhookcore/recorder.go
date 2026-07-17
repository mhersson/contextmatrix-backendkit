package webhookcore

import (
	"net/http"
	"strconv"
	"time"
)

// statusRecorder wraps a ResponseWriter to capture the written HTTP status so
// recordMetrics can observe it after the handler returns. Flush and Unwrap are
// preserved so the SSE /logs handler keeps flushing and its write-deadline
// control still reaches the real writer.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (sr *statusRecorder) WriteHeader(code int) {
	if !sr.wrote {
		sr.status = code
		sr.wrote = true
	}

	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.wrote {
		sr.status = http.StatusOK
		sr.wrote = true
	}

	return sr.ResponseWriter.Write(b)
}

func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (sr *statusRecorder) Unwrap() http.ResponseWriter {
	return sr.ResponseWriter
}

// RecordMetrics wraps a handler and records request count + duration on the
// core's metrics bundle. A nil bundle makes it a pass-through. The endpoint
// label is normalised through metrics.NormalizeEndpoint (unknown paths collapse
// to "other") with the leading slash stripped so the label reads "trigger".
// code is a coarse success / error / rate_limited bucket from the HTTP status.
func (c *Core) RecordMetrics(next http.HandlerFunc) http.HandlerFunc {
	if c.metrics == nil {
		return next
	}

	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next(sr, r)

		endpoint := c.metrics.NormalizeEndpoint(r.URL.Path)
		if len(endpoint) > 0 && endpoint[0] == '/' {
			endpoint = endpoint[1:]
		}

		code := "success"
		if sr.status >= 400 {
			code = "error"
		}

		if sr.status == http.StatusTooManyRequests {
			code = "rate_limited"
		}

		c.metrics.WebhookRequestsTotal.WithLabelValues(endpoint, strconv.Itoa(sr.status), code).Inc()
		c.metrics.WebhookRequestDuration.WithLabelValues(endpoint).Observe(time.Since(start).Seconds())
	}
}
