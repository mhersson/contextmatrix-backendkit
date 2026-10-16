package logbridge

import (
	"sync"

	protocol "github.com/mhersson/contextmatrix-protocol"
)

const subBufSize = 256

// sub is one active subscriber.
type sub struct {
	ch     chan protocol.LogEntry
	filter string // empty = all
}

// DropObserver is notified once per LogEntry dropped because a subscriber's
// channel was full or a retained backlog overflowed. The serve layer supplies a
// Prometheus-backed adapter; the interface keeps logbridge free of any metrics
// dependency.
type DropObserver interface {
	ObserveDrop()
}

// Hub fans out LogEntry frames to registered subscribers.
// mu protects subs, nextID and retained.
type Hub struct {
	mu           sync.Mutex
	subs         map[int]*sub
	nextID       int
	keyOf        func(protocol.LogEntry) string
	dropObserver DropObserver
	// retained holds a per-key backlog for keys a backend asked to retain:
	// entries published while no subscriber carries that exact filter wait
	// here and are replayed, in order, ahead of live entries to the next
	// subscriber with that filter. A backend uses it for the window between
	// its session start and the consumer's first /logs connect, and for a
	// consumer that reconnects.
	retained map[string][]protocol.LogEntry
}

// NewHub creates a ready Hub. keyOf extracts the filter key from an entry
// (project for the agent, session for chat); a nil keyOf makes every filter
// match all entries. obs is notified each time a full subscriber channel or a
// full retained backlog forces a drop; a nil obs disables drop observation.
func NewHub(keyOf func(protocol.LogEntry) string, obs DropObserver) *Hub {
	return &Hub{subs: make(map[int]*sub), keyOf: keyOf, dropObserver: obs, retained: make(map[string][]protocol.LogEntry)}
}

// Retain starts keeping a backlog for key, so entries published while no
// subscriber has that exact filter reach the next one instead of being
// dropped. Idempotent, and a no-op for an empty key or a hub without keyOf.
// The backlog holds at most subBufSize entries: beyond that the oldest is
// dropped and the DropObserver notified.
func (h *Hub) Retain(key string) {
	if key == "" || h.keyOf == nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.retained[key]; !ok {
		h.retained[key] = nil
	}
}

// Release stops retaining key and discards its backlog. Idempotent.
func (h *Hub) Release(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	delete(h.retained, key)
}

// Subscribe registers a subscriber. An empty filter receives all entries
// regardless of key. A retained backlog for filter is queued on the new
// channel before any live entry and then cleared, so a replayed entry is never
// delivered twice. Returns an opaque id for Unsubscribe.
func (h *Hub) Subscribe(filter string) (int, <-chan protocol.LogEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()

	held := h.retained[filter]
	ch := make(chan protocol.LogEntry, subBufSize+len(held))

	for _, e := range held {
		ch <- e
	}

	if _, ok := h.retained[filter]; ok {
		h.retained[filter] = nil
	}

	h.nextID++
	id := h.nextID
	h.subs[id] = &sub{ch: ch, filter: filter}

	return id, ch
}

// Unsubscribe removes the subscriber and closes its channel.
func (h *Hub) Unsubscribe(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if s, ok := h.subs[id]; ok {
		delete(h.subs, id)
		close(s.ch)
	}
}

// Publish delivers e to all matching subscribers. Per-subscriber delivery is
// non-blocking: a full channel is silently dropped. When no subscriber carries
// e's exact key and the key is retained, e joins that key's backlog instead;
// an all-entries subscriber does not count as attached.
func (h *Hub) Publish(e protocol.LogEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var key string
	if h.keyOf != nil {
		key = h.keyOf(e)
	}

	attached := false

	for _, s := range h.subs {
		if s.filter != "" && h.keyOf != nil && s.filter != key {
			continue
		}

		if s.filter != "" && s.filter == key {
			attached = true
		}

		select {
		case s.ch <- e:
		default:
			h.observeDrop()
		}
	}

	if attached {
		return
	}

	backlog, ok := h.retained[key]
	if !ok {
		return
	}

	if len(backlog) >= subBufSize {
		backlog = backlog[1:]

		h.observeDrop()
	}

	h.retained[key] = append(backlog, e)
}

func (h *Hub) observeDrop() {
	if h.dropObserver != nil {
		h.dropObserver.ObserveDrop()
	}
}
