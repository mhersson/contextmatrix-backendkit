package webhookcore

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDedup_CheckAndRecordThenRollback(t *testing.T) {
	d := NewDedupCache(time.Minute, 16)

	assert.False(t, d.CheckAndRecord("scope-1", "m1"))
	assert.True(t, d.CheckAndRecord("scope-1", "m1"))

	// Rollback undoes the record, so a retry after a failed delivery is fresh.
	d.Rollback("scope-1", "m1")
	assert.False(t, d.CheckAndRecord("scope-1", "m1"))
	assert.True(t, d.CheckAndRecord("scope-1", "m1"))
}

func TestDedup_EmptyMessageIDNeverDedups(t *testing.T) {
	d := NewDedupCache(time.Minute, 16)

	assert.False(t, d.CheckAndRecord("scope-1", ""))
	assert.False(t, d.CheckAndRecord("scope-1", ""))
	assert.False(t, d.CheckAndRecord("scope-1", ""))

	d.Rollback("scope-1", "")
	assert.Equal(t, 0, d.entries.Len())
}

func TestDedup_KeyedByBothFields(t *testing.T) {
	d := NewDedupCache(time.Minute, 16)

	assert.False(t, d.CheckAndRecord("scope-1", "m1"))
	assert.True(t, d.CheckAndRecord("scope-1", "m1"))
	assert.False(t, d.CheckAndRecord("scope-2", "m1"))
}

func TestDedup_BoundaryShiftIsDistinct(t *testing.T) {
	d := NewDedupCache(time.Minute, 16)

	assert.False(t, d.CheckAndRecord("ab", "c"))
	assert.False(t, d.CheckAndRecord("a", "bc"))
	assert.True(t, d.CheckAndRecord("ab", "c"))
	assert.True(t, d.CheckAndRecord("a", "bc"))
}

func TestDedup_CapacityEvictsOldest(t *testing.T) {
	d := NewDedupCache(time.Hour, 2)

	assert.False(t, d.CheckAndRecord("s", "a"))
	assert.False(t, d.CheckAndRecord("s", "b"))
	assert.False(t, d.CheckAndRecord("s", "x")) // evicts "a"

	// Assert the survivors before "a": re-recording "a" would evict the oldest
	// survivor and disturb these assertions.
	assert.True(t, d.CheckAndRecord("s", "b"))
	assert.True(t, d.CheckAndRecord("s", "x"))
	assert.False(t, d.CheckAndRecord("s", "a"))
}

func TestDedup_TTLExpiry(t *testing.T) {
	now := time.Unix(0, 0)
	d := NewDedupCache(time.Minute, 16, withDedupClock(func() time.Time { return now }))

	assert.False(t, d.CheckAndRecord("s", "m1"))
	require.True(t, d.CheckAndRecord("s", "m1"))

	now = now.Add(2 * time.Minute)

	assert.False(t, d.CheckAndRecord("s", "m1"))
}

func TestDedup_ConcurrentCheckAndRecordAdmitsOne(t *testing.T) {
	const (
		rounds  = 1000
		workers = 8
	)

	d := NewDedupCache(time.Minute, 0)

	for round := range rounds {
		id := strconv.Itoa(round)

		var (
			fresh atomic.Int32
			start = make(chan struct{})
			wg    sync.WaitGroup
		)

		for range workers {
			wg.Add(1)

			go func() {
				defer wg.Done()

				<-start

				if !d.CheckAndRecord("scope", id) {
					fresh.Add(1)
				}
			}()
		}

		close(start)
		wg.Wait()

		require.Equal(t, int32(1), fresh.Load(), "round %d", round)
	}
}

func TestDedup_NonPositiveTTLNeverExpires(t *testing.T) {
	now := time.Unix(0, 0)
	d := NewDedupCache(0, 16, withDedupClock(func() time.Time { return now }))

	assert.False(t, d.CheckAndRecord("s", "m1"))

	now = now.Add(10 * 365 * 24 * time.Hour)

	assert.True(t, d.CheckAndRecord("s", "m1"))
}

func TestDedup_NonPositiveCapacityNeverEvicts(t *testing.T) {
	d := NewDedupCache(time.Hour, 0)

	const n = 5000

	for i := range n {
		require.False(t, d.CheckAndRecord("s", strconv.Itoa(i)))
	}

	for i := range n {
		require.True(t, d.CheckAndRecord("s", strconv.Itoa(i)), "entry %d evicted", i)
	}
}
