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
