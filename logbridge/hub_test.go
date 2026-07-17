package logbridge_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/mhersson/contextmatrix-backendkit/logbridge"
	protocol "github.com/mhersson/contextmatrix-protocol"
	"github.com/stretchr/testify/assert"
)

// TestHubSubscribers verifies fan-out, key filtering, drop-on-full, and
// Unsubscribe in both key modes.
func TestHubSubscribers(t *testing.T) {
	t.Parallel()

	t.Run("project-keyed", func(t *testing.T) {
		t.Parallel()

		keyOf := func(e protocol.LogEntry) string { return e.Project }

		t.Run("all-project subscriber receives entries from any project", func(t *testing.T) {
			t.Parallel()

			hub := logbridge.NewHub(keyOf, nil)
			_, ch := hub.Subscribe("") // empty = all
			hub.Publish(protocol.LogEntry{Type: "text", Project: "proj-a", Content: "hello"})

			select {
			case got := <-ch:
				assert.Equal(t, "proj-a", got.Project)
			case <-time.After(100 * time.Millisecond):
				t.Fatal("expected entry")
			}
		})

		t.Run("project-filtered subscriber only receives matching project", func(t *testing.T) {
			t.Parallel()

			hub := logbridge.NewHub(keyOf, nil)
			_, chA := hub.Subscribe("proj-a")
			_, chB := hub.Subscribe("proj-b")

			hub.Publish(protocol.LogEntry{Type: "text", Project: "proj-a", Content: "for a"})

			select {
			case got := <-chA:
				assert.Equal(t, "proj-a", got.Project)
			case <-time.After(100 * time.Millisecond):
				t.Fatal("proj-a subscriber did not receive")
			}

			// proj-b should not receive proj-a's entry.
			select {
			case e := <-chB:
				t.Errorf("proj-b received unexpected entry: %+v", e)
			case <-time.After(30 * time.Millisecond):
				// correct: no delivery
			}
		})

		t.Run("full subscriber drops without stalling", func(t *testing.T) {
			t.Parallel()

			hub := logbridge.NewHub(keyOf, nil)
			_, ch := hub.Subscribe("")

			// Publish more entries than the channel buffer without consuming.
			done := make(chan struct{})

			go func() {
				defer close(done)

				for i := range 300 {
					hub.Publish(protocol.LogEntry{Type: "text", Content: fmt.Sprintf("msg-%d", i)})
				}
			}()

			// The goroutine must complete without blocking.
			select {
			case <-done:
				// success
			case <-time.After(2 * time.Second):
				t.Fatal("Publish blocked on full subscriber")
			}

			_ = ch
		})

		t.Run("Unsubscribe closes channel", func(t *testing.T) {
			t.Parallel()

			hub := logbridge.NewHub(keyOf, nil)
			id, ch := hub.Subscribe("")
			hub.Unsubscribe(id)

			_, open := <-ch
			assert.False(t, open, "channel must be closed after Unsubscribe")
		})

		t.Run("directly published user entry is not redacted", func(t *testing.T) {
			t.Parallel()

			const secret = "plain-secret-1234"

			hub := logbridge.NewHub(keyOf, nil)
			_, ch := hub.Subscribe("")

			hub.Publish(protocol.LogEntry{
				Timestamp: time.Now(),
				Project:   testProject,
				CardID:    testCard,
				Type:      "user",
				Content:   "user said: " + secret,
			})

			select {
			case got := <-ch:
				assert.Equal(t, "user", got.Type)
				assert.Contains(t, got.Content, secret, "user content must NOT be redacted")
			case <-time.After(100 * time.Millisecond):
				t.Fatal("expected user entry")
			}
		})
	})

	t.Run("session-keyed", func(t *testing.T) {
		t.Parallel()

		keyOf := func(e protocol.LogEntry) string { return e.SessionID }

		t.Run("all-session subscriber receives entries from any session", func(t *testing.T) {
			t.Parallel()

			hub := logbridge.NewHub(keyOf, nil)
			_, ch := hub.Subscribe("") // empty = all
			hub.Publish(protocol.LogEntry{Type: "text", SessionID: "sess-a", Content: "hello"})

			select {
			case got := <-ch:
				assert.Equal(t, "sess-a", got.SessionID)
			case <-time.After(100 * time.Millisecond):
				t.Fatal("expected entry")
			}
		})

		t.Run("session-filtered subscriber only receives matching session", func(t *testing.T) {
			t.Parallel()

			hub := logbridge.NewHub(keyOf, nil)
			_, chA := hub.Subscribe("sess-a")
			_, chB := hub.Subscribe("sess-b")

			hub.Publish(protocol.LogEntry{Type: "text", SessionID: "sess-a", Content: "for a"})

			select {
			case got := <-chA:
				assert.Equal(t, "sess-a", got.SessionID)
			case <-time.After(100 * time.Millisecond):
				t.Fatal("sess-a subscriber did not receive")
			}

			// sess-b should not receive sess-a's entry.
			select {
			case e := <-chB:
				t.Errorf("sess-b received unexpected entry: %+v", e)
			case <-time.After(30 * time.Millisecond):
				// correct: no delivery
			}
		})

		t.Run("full subscriber drops without stalling", func(t *testing.T) {
			t.Parallel()

			hub := logbridge.NewHub(keyOf, nil)
			_, ch := hub.Subscribe("")

			// Publish more entries than the channel buffer without consuming.
			done := make(chan struct{})

			go func() {
				defer close(done)

				for i := range 300 {
					hub.Publish(protocol.LogEntry{Type: "text", Content: fmt.Sprintf("msg-%d", i)})
				}
			}()

			// The goroutine must complete without blocking.
			select {
			case <-done:
				// success
			case <-time.After(2 * time.Second):
				t.Fatal("Publish blocked on full subscriber")
			}

			_ = ch
		})

		t.Run("Unsubscribe closes channel", func(t *testing.T) {
			t.Parallel()

			hub := logbridge.NewHub(keyOf, nil)
			id, ch := hub.Subscribe("")
			hub.Unsubscribe(id)

			_, open := <-ch
			assert.False(t, open, "channel must be closed after Unsubscribe")
		})
	})
}
