package webhookcore

import (
	"container/list"
	"sync"
	"time"
)

// DedupCache remembers the (scope, messageID) pairs whose /message request has
// already been recorded, so a retry returns a cached ack instead of delivering
// the user frame a second time. scope is the namespace a message ID is unique
// within (a chat session, or an agent's project and card) and must not contain
// NUL. CheckAndRecord checks and records in one step, closing the race a split
// check-then-record would leave between two concurrent retries. A caller that
// gets a fresh result and then fails to deliver must call Rollback, or the
// cache would swallow the legitimate retry as a duplicate. A retry arriving
// mid-delivery is acked as a duplicate, so if that delivery then rolls back the
// message is lost unless the caller holds one per-scope lock across
// CheckAndRecord, delivery and Rollback. It is TTL- and capacity-bounded; an
// empty messageID NEVER dedups (the client opted out of at-most-once delivery).
// All methods are safe for concurrent use.
type DedupCache struct {
	mu       sync.Mutex
	ttl      time.Duration
	capacity int
	now      func() time.Time

	// entries is an insertion-ordered list, oldest at the front. index maps the
	// composite key to its list element for O(1) lookup.
	entries *list.List
	index   map[string]*list.Element
}

type dedupEntry struct {
	key    string
	stored time.Time
}

// dedupCacheOption configures a DedupCache.
type dedupCacheOption func(*DedupCache)

// withDedupClock injects a deterministic clock for tests.
func withDedupClock(now func() time.Time) dedupCacheOption {
	return func(c *DedupCache) {
		if now != nil {
			c.now = now
		}
	}
}

// NewDedupCache builds a dedup cache with the given TTL and capacity. A TTL
// <= 0 disables time-based expiry; a capacity <= 0 disables the hard cap.
func NewDedupCache(ttl time.Duration, capacity int, opts ...dedupCacheOption) *DedupCache {
	c := &DedupCache{
		ttl:      ttl,
		capacity: capacity,
		now:      time.Now,
		entries:  list.New(),
		index:    make(map[string]*list.Element),
	}
	for _, opt := range opts {
		opt(c)
	}

	return c
}

// dedupKey joins the fields with NUL, which scope must not contain, so they
// never collide across the boundary.
func dedupKey(scope, messageID string) string {
	return scope + "\x00" + messageID
}

// CheckAndRecord reports whether (scope, messageID) was already recorded inside
// the TTL window and, if not, records it. It returns true for a duplicate (the
// caller returns a cached ack) and false for a fresh pair (the caller must
// deliver, and Rollback on failure). An empty messageID returns false and
// records nothing.
func (c *DedupCache) CheckAndRecord(scope, messageID string) bool {
	if messageID == "" {
		return false
	}

	key := dedupKey(scope, messageID)

	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()

	if el, ok := c.index[key]; ok {
		entry := el.Value.(*dedupEntry)
		if c.ttl <= 0 || now.Sub(entry.stored) <= c.ttl {
			return true
		}
		// Expired: reap and record the incoming pair as fresh.
		c.entries.Remove(el)
		delete(c.index, key)
	}

	c.index[key] = c.entries.PushBack(&dedupEntry{key: key, stored: now})

	if c.capacity > 0 {
		for c.entries.Len() > c.capacity {
			oldest := c.entries.Front()
			if oldest == nil {
				break
			}

			c.entries.Remove(oldest)
			delete(c.index, oldest.Value.(*dedupEntry).key)
		}
	}

	return false
}

// Rollback removes a just-recorded (scope, messageID) pair so a retry after a
// failed delivery is not silenced. An empty messageID is a no-op.
func (c *DedupCache) Rollback(scope, messageID string) {
	if messageID == "" {
		return
	}

	key := dedupKey(scope, messageID)

	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.index[key]; ok {
		c.entries.Remove(el)
		delete(c.index, key)
	}
}
