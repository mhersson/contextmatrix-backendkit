package webhookcore

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	protocol "github.com/mhersson/contextmatrix-protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mhersson/contextmatrix-backendkit/logbridge"
)

// logsMode parameterizes the SSE suite over both backend framings: the agent
// filters /logs by ?project= and keys the hub on LogEntry.Project; chat filters
// by ?session_id= and keys on LogEntry.SessionID. The slog attr name differs too
// (project_filter vs session_id), wired through the Core seam.
type logsMode struct {
	name        string
	filterParam string
	filterAttr  string
	keyOf       func(protocol.LogEntry) string
	setKey      func(e *protocol.LogEntry, v string)
}

var logsModes = []logsMode{
	{
		name:        "agent",
		filterParam: "project",
		filterAttr:  "project_filter",
		keyOf:       func(e protocol.LogEntry) string { return e.Project },
		setKey:      func(e *protocol.LogEntry, v string) { e.Project = v },
	},
	{
		name:        "chat",
		filterParam: "session_id",
		filterAttr:  "session_id",
		keyOf:       func(e protocol.LogEntry) string { return e.SessionID },
		setKey:      func(e *protocol.LogEntry, v string) { e.SessionID = v },
	},
}

// logsMux mounts HandleLogs behind Auth, mirroring how the serve layer wires the
// signed GET /logs route so the signed-query-in-base-string path is exercised.
func logsMux(c *Core) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /logs", c.Auth(c.HandleLogs))

	return mux
}

func TestLogs_SSEStream(t *testing.T) {
	for _, m := range logsModes {
		t.Run(m.name, func(t *testing.T) {
			hub := logbridge.NewHub(m.keyOf, nil)

			c := NewCore(CoreConfig{
				APIKey:            testAPIKey,
				Skew:              protocol.DefaultMaxClockSkew,
				Hub:               hub,
				LogsFilterParam:   m.filterParam,
				LogsFilterAttr:    m.filterAttr,
				KeepaliveInterval: 40 * time.Millisecond, // shrunk so the test sees a keepalive fast
			})

			ts := httptest.NewServer(logsMux(c))
			defer ts.Close()

			// Signed GET WITH the query string - the query is part of the base string.
			uri := "/logs?" + m.filterParam + "=proj"
			target := ts.URL + uri

			req, err := http.NewRequest(http.MethodGet, target, nil)
			require.NoError(t, err)
			require.Equal(t, uri, req.URL.RequestURI())

			now := nowTS()
			sig := protocol.SignPayloadWithTimestamp(testAPIKey, http.MethodGet, uri, nil, now)
			req.Header.Set(protocol.SignatureHeader, "sha256="+sig)
			req.Header.Set(protocol.TimestampHeader, now)

			resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed below
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

			defer func() { _ = resp.Body.Close() }()

			type line struct {
				text string
				err  error
			}

			lines := make(chan line, 32)

			go func() {
				sc := bufio.NewScanner(resp.Body)
				for sc.Scan() {
					lines <- line{text: sc.Text()}
				}

				lines <- line{err: sc.Err()}
			}()

			readUntil := func(pred func(string) bool, what string) {
				t.Helper()

				deadline := time.After(3 * time.Second)

				for {
					select {
					case l := <-lines:
						require.NoError(t, l.err, "stream ended before "+what)

						if pred(l.text) {
							return
						}
					case <-deadline:
						t.Fatalf("timed out waiting for %s", what)
					}
				}
			}

			// 1. The connected comment must arrive immediately.
			readUntil(func(s string) bool { return s == ": connected" }, "connected comment")

			// Give the subscription a beat, then publish a frame.
			time.Sleep(10 * time.Millisecond)

			entry := protocol.LogEntry{
				Timestamp: time.Now(),
				Type:      "text",
				Content:   "hello from worker",
			}
			m.setKey(&entry, "proj")
			hub.Publish(entry)

			// 2. The published frame must arrive as a data: line.
			readUntil(func(s string) bool {
				return strings.HasPrefix(s, "data:") && strings.Contains(s, "hello from worker")
			}, "data frame")

			// 3. A keepalive comment must arrive on the shrunken interval.
			readUntil(func(s string) bool { return s == ": keepalive" }, "keepalive comment")
		})
	}
}

func TestHandleLogs_ReturnsOnSSEShutdown(t *testing.T) {
	for _, m := range logsModes {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()

			c := NewCore(CoreConfig{
				Hub:             logbridge.NewHub(m.keyOf, nil),
				LogsFilterParam: m.filterParam,
				LogsFilterAttr:  m.filterAttr,
			})

			req := httptest.NewRequest(http.MethodGet, "/logs", nil) // context.Background: never cancels
			rec := httptest.NewRecorder()                            // implements http.Flusher

			done := make(chan struct{})

			go func() {
				c.HandleLogs(rec, req)
				close(done)
			}()

			c.CloseSSE() // closing the channel keeps the select case ready even if it lands first

			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("HandleLogs did not return after CloseSSE; SSE drain signal ignored")
			}
		})
	}
}

func TestLogs_Filter(t *testing.T) {
	for _, m := range logsModes {
		t.Run(m.name, func(t *testing.T) {
			hub := logbridge.NewHub(m.keyOf, nil)

			c := NewCore(CoreConfig{
				APIKey:            testAPIKey,
				Skew:              protocol.DefaultMaxClockSkew,
				Hub:               hub,
				LogsFilterParam:   m.filterParam,
				LogsFilterAttr:    m.filterAttr,
				KeepaliveInterval: time.Hour, // no keepalive noise
			})

			ts := httptest.NewServer(logsMux(c))
			defer ts.Close()

			uri := "/logs?" + m.filterParam + "=proj"
			target := ts.URL + uri

			req, err := http.NewRequest(http.MethodGet, target, nil)
			require.NoError(t, err)

			now := nowTS()
			sig := protocol.SignPayloadWithTimestamp(testAPIKey, http.MethodGet, uri, nil, now)
			req.Header.Set(protocol.SignatureHeader, "sha256="+sig)
			req.Header.Set(protocol.TimestampHeader, now)

			resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed below
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)

			defer func() { _ = resp.Body.Close() }()

			lines := make(chan string, 32)

			go func() {
				sc := bufio.NewScanner(resp.Body)
				for sc.Scan() {
					lines <- sc.Text()
				}
			}()

			// Wait for the connected comment.
			require.Eventually(t, func() bool {
				select {
				case l := <-lines:
					return l == ": connected"
				default:
					return false
				}
			}, time.Second, 5*time.Millisecond)

			time.Sleep(10 * time.Millisecond)

			// A frame for a different key must NOT be delivered.
			other := protocol.LogEntry{Type: "text", Content: "other-key"}
			m.setKey(&other, "other")
			hub.Publish(other)

			// A frame for our key MUST be delivered.
			ours := protocol.LogEntry{Type: "text", Content: "our-key"}
			m.setKey(&ours, "proj")
			hub.Publish(ours)

			deadline := time.After(2 * time.Second)

			for {
				select {
				case l := <-lines:
					if strings.Contains(l, "other-key") {
						t.Fatal("received a frame for a filtered-out key")
					}

					if strings.Contains(l, "our-key") {
						return // success: our frame arrived, the other did not precede it
					}
				case <-deadline:
					t.Fatal("timed out waiting for our-key frame")
				}
			}
		})
	}
}
