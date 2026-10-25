package webhookcore

import (
	"testing"
	"time"

	protocol "github.com/mhersson/contextmatrix-protocol"
	"github.com/stretchr/testify/assert"
)

// TestNewCore_DefaultReplayTTLCoversSkew guards replay protection: a seen pair
// must outlive the window in which its timestamp is still accepted. A pair
// stamped up to DefaultMaxFutureSkew ahead stays valid until its timestamp plus
// skew, so the default cache TTL is asserted to cover skew plus the future
// allowance.
func TestNewCore_SuppliedReplayTTLIsRaisedToFloor(t *testing.T) {
	const skew = 5 * time.Minute

	floor := skew + protocol.DefaultMaxFutureSkew

	tests := []struct {
		name string
		ttl  time.Duration
		want time.Duration
	}{
		{"equal to skew is raised", skew, floor},
		{"longer is kept", time.Hour, time.Hour},
		{"no expiry stays no expiry", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCore(CoreConfig{Skew: skew, Replay: NewReplayCache(tt.ttl, 16)})

			assert.Equal(t, tt.want, c.replay.ttl)
		})
	}
}

func TestNewCore_DefaultReplayTTLCoversSkew(t *testing.T) {
	tests := []struct {
		name string
		skew time.Duration
		want time.Duration
	}{
		{"zero skew uses protocol default", 0, protocol.DefaultMaxClockSkew},
		{"explicit skew", 7 * time.Minute, 7 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCore(CoreConfig{Skew: tt.skew})

			assert.Equal(t, tt.want, c.skew)
			assert.GreaterOrEqual(t, c.replay.ttl, c.skew+protocol.DefaultMaxFutureSkew)
		})
	}
}

// TestNewCore_RaisesTTLWhileJanitorRuns covers a backend that starts the
// cache's janitor before building the core: the TTL raise must not race it.
func TestNewCore_RaisesTTLWhileJanitorRuns(t *testing.T) {
	cache := NewReplayCache(time.Minute, 16, withReplaySweepInterval(time.Millisecond))
	stop := cache.StartJanitor()

	defer stop()

	c := NewCore(CoreConfig{Skew: time.Minute, Replay: cache})

	time.Sleep(20 * time.Millisecond)

	assert.Equal(t, time.Minute+protocol.DefaultMaxFutureSkew, c.replay.ttl)
}
