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
// channel was full. The serve layer supplies a Prometheus-backed adapter; the
// interface keeps logbridge free of any metrics dependency.
type DropObserver interface {
	ObserveDrop()
}

// Hub fans out LogEntry frames to registered subscribers.
// mu protects subs and nextID.
type Hub struct {
	mu           sync.Mutex
	subs         map[int]*sub
	nextID       int
	keyOf        func(protocol.LogEntry) string
	dropObserver DropObserver
}

// NewHub creates a ready Hub. keyOf extracts the filter key from an entry
// (project for the agent, session for chat); a nil keyOf makes every filter
// match all entries. obs is notified each time a full subscriber channel forces
// a drop; a nil obs disables drop observation.
func NewHub(keyOf func(protocol.LogEntry) string, obs DropObserver) *Hub {
	return &Hub{subs: make(map[int]*sub), keyOf: keyOf, dropObserver: obs}
}

// Subscribe registers a subscriber. An empty filter receives all entries
// regardless of key. Returns an opaque id for Unsubscribe.
func (h *Hub) Subscribe(filter string) (int, <-chan protocol.LogEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.nextID++
	id := h.nextID
	ch := make(chan protocol.LogEntry, subBufSize)
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
// non-blocking: a full channel is silently dropped.
func (h *Hub) Publish(e protocol.LogEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, s := range h.subs {
		if s.filter != "" && h.keyOf != nil && s.filter != h.keyOf(e) {
			continue
		}

		select {
		case s.ch <- e:
		default:
			if h.dropObserver != nil {
				h.dropObserver.ObserveDrop()
			}
		}
	}
}
