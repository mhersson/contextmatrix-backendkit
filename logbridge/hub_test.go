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

type countingDrops struct{ n int }

func (c *countingDrops) ObserveDrop() { c.n++ }

func sessionKey(e protocol.LogEntry) string { return e.SessionID }

func drain(t *testing.T, ch <-chan protocol.LogEntry, want int) []string {
	t.Helper()

	out := make([]string, 0, want)

	for len(out) < want {
		select {
		case e := <-ch:
			out = append(out, e.Content)
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("got %d entries, want %d", len(out), want)
		}
	}

	return out
}

func assertNone(t *testing.T, ch <-chan protocol.LogEntry) {
	t.Helper()

	select {
	case e := <-ch:
		t.Fatalf("unexpected entry %q", e.Content)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestHubRetain(t *testing.T) {
	t.Parallel()

	entry := func(content string) protocol.LogEntry {
		return protocol.LogEntry{Type: "system", SessionID: "s1", Content: content}
	}

	t.Run("retained entries replay ahead of live ones", func(t *testing.T) {
		t.Parallel()

		hub := logbridge.NewHub(sessionKey, nil)
		hub.Retain("s1")
		hub.Publish(entry("a"))
		hub.Publish(entry("b"))

		_, ch := hub.Subscribe("s1")
		hub.Publish(entry("c"))

		assert.Equal(t, []string{"a", "b", "c"}, drain(t, ch, 3))
	})

	t.Run("without retain nothing is kept", func(t *testing.T) {
		t.Parallel()

		hub := logbridge.NewHub(sessionKey, nil)
		hub.Publish(entry("a"))

		_, ch := hub.Subscribe("s1")
		assertNone(t, ch)
	})

	t.Run("release discards the backlog", func(t *testing.T) {
		t.Parallel()

		hub := logbridge.NewHub(sessionKey, nil)
		hub.Retain("s1")
		hub.Publish(entry("a"))
		hub.Release("s1")

		_, ch := hub.Subscribe("s1")
		assertNone(t, ch)
	})

	t.Run("an attached subscriber takes delivery and nothing is replayed later", func(t *testing.T) {
		t.Parallel()

		hub := logbridge.NewHub(sessionKey, nil)
		hub.Retain("s1")

		id, ch := hub.Subscribe("s1")
		hub.Publish(entry("a"))
		assert.Equal(t, []string{"a"}, drain(t, ch, 1))

		hub.Unsubscribe(id)
		hub.Publish(entry("b"))

		_, again := hub.Subscribe("s1")
		assert.Equal(t, []string{"b"}, drain(t, again, 1), "only what was published while detached")
	})

	t.Run("an all-entries subscriber does not count as attached", func(t *testing.T) {
		t.Parallel()

		hub := logbridge.NewHub(sessionKey, nil)
		hub.Retain("s1")

		_, all := hub.Subscribe("")
		hub.Publish(entry("a"))
		assert.Equal(t, []string{"a"}, drain(t, all, 1))

		_, ch := hub.Subscribe("s1")
		assert.Equal(t, []string{"a"}, drain(t, ch, 1), "the keyed subscriber still gets the backlog")
	})

	t.Run("the backlog is capped and drops are observed", func(t *testing.T) {
		t.Parallel()

		obs := &countingDrops{}
		hub := logbridge.NewHub(sessionKey, obs)
		hub.Retain("s1")

		for i := range 257 {
			hub.Publish(entry(fmt.Sprintf("%d", i)))
		}

		_, ch := hub.Subscribe("s1")
		got := drain(t, ch, 256)
		assert.Equal(t, "1", got[0], "the oldest entry was dropped")
		assert.Equal(t, "256", got[255])
		assert.Equal(t, 1, obs.n)
	})

	t.Run("retain is a no-op for an empty key or a keyless hub", func(t *testing.T) {
		t.Parallel()

		hub := logbridge.NewHub(nil, nil)
		hub.Retain("s1")
		hub.Publish(entry("a"))

		_, ch := hub.Subscribe("s1")
		assertNone(t, ch)
	})
}
