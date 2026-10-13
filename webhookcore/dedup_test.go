package webhookcore

import (
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
	const workers = 64

	d := NewDedupCache(time.Minute, 16)

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

			if !d.CheckAndRecord("scope", "m1") {
				fresh.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	assert.Equal(t, int32(1), fresh.Load())
}
