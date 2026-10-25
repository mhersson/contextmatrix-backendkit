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
			default:
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
			default:
				t.Fatal("proj-a subscriber did not receive")
			}

			// proj-b should not receive proj-a's entry.
			select {
			case e := <-chB:
				t.Errorf("proj-b received unexpected entry: %+v", e)
			default:
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
			default:
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
			default:
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
			default:
				t.Fatal("sess-a subscriber did not receive")
			}

			// sess-b should not receive sess-a's entry.
			select {
			case e := <-chB:
				t.Errorf("sess-b received unexpected entry: %+v", e)
			default:
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
		default:
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
	default:
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
		assertNone(t, ch)
	})

	t.Run("a replayed backlog is not replayed to a later subscriber", func(t *testing.T) {
		t.Parallel()

		hub := logbridge.NewHub(sessionKey, nil)
		hub.Retain("s1")
		hub.Publish(entry("a"))

		first, ch := hub.Subscribe("s1")
		assert.Equal(t, []string{"a"}, drain(t, ch, 1))
		hub.Unsubscribe(first)

		_, again := hub.Subscribe("s1")
		assertNone(t, again)
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

	t.Run("retain is a no-op for a keyless hub", func(t *testing.T) {
		t.Parallel()

		hub := logbridge.NewHub(nil, nil)
		hub.Retain("s1")
		hub.Publish(entry("a"))

		_, ch := hub.Subscribe("s1")
		assertNone(t, ch)
	})

	t.Run("retain is a no-op for an empty key", func(t *testing.T) {
		t.Parallel()

		hub := logbridge.NewHub(sessionKey, nil)
		hub.Retain("")
		hub.Publish(protocol.LogEntry{Type: "system", Content: "a"})

		_, ch := hub.Subscribe("")
		assertNone(t, ch)
	})

	t.Run("retain is idempotent", func(t *testing.T) {
		t.Parallel()

		hub := logbridge.NewHub(sessionKey, nil)
		hub.Retain("s1")
		hub.Publish(entry("a"))
		hub.Retain("s1")

		_, ch := hub.Subscribe("s1")
		assert.Equal(t, []string{"a"}, drain(t, ch, 1))
	})

	t.Run("an unread backlog returns when the subscriber leaves", func(t *testing.T) {
		t.Parallel()

		hub := logbridge.NewHub(sessionKey, nil)
		hub.Retain("s1")
		hub.Publish(entry("a"))

		id, _ := hub.Subscribe("s1")
		hub.Unsubscribe(id)

		_, ch := hub.Subscribe("s1")
		assert.Equal(t, []string{"a"}, drain(t, ch, 1))
	})

	t.Run("unread live entries requeue ahead of later ones", func(t *testing.T) {
		t.Parallel()

		hub := logbridge.NewHub(sessionKey, nil)
		hub.Retain("s1")

		id, _ := hub.Subscribe("s1")
		hub.Publish(entry("a"))
		hub.Publish(entry("b"))
		hub.Unsubscribe(id)
		hub.Publish(entry("c"))

		_, ch := hub.Subscribe("s1")
		assert.Equal(t, []string{"a", "b", "c"}, drain(t, ch, 3))
	})

	t.Run("nothing requeues while another subscriber holds the key", func(t *testing.T) {
		t.Parallel()

		hub := logbridge.NewHub(sessionKey, nil)
		hub.Retain("s1")

		first, _ := hub.Subscribe("s1")
		_, other := hub.Subscribe("s1")
		hub.Publish(entry("a"))
		hub.Unsubscribe(first)

		assert.Equal(t, []string{"a"}, drain(t, other, 1))
		assertNone(t, other)

		_, third := hub.Subscribe("s1")
		assertNone(t, third)
	})

	t.Run("a requeue past the cap keeps the newest and drops the oldest", func(t *testing.T) {
		t.Parallel()

		obs := &countingDrops{}
		hub := logbridge.NewHub(sessionKey, obs)
		hub.Retain("s1")

		for i := range 256 {
			hub.Publish(entry(fmt.Sprintf("%d", i)))
		}

		id, _ := hub.Subscribe("s1")

		for i := 256; i < 261; i++ {
			hub.Publish(entry(fmt.Sprintf("%d", i)))
		}

		assert.Zero(t, obs.n)
		hub.Unsubscribe(id)
		assert.Equal(t, 5, obs.n, "each trimmed entry is observed")

		_, ch := hub.Subscribe("s1")
		got := drain(t, ch, 256)
		assert.Equal(t, "5", got[0], "the oldest entries were dropped")
		assert.Equal(t, "260", got[255])
		assertNone(t, ch)
	})
}
