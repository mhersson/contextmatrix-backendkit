package webhookcore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	protocol "github.com/mhersson/contextmatrix-protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeTracker struct {
	n int
}

func (f fakeTracker) Count() int { return f.n }

func TestHealth_ReportsTrackerCountAndMaxConcurrent(t *testing.T) {
	c := NewCore(CoreConfig{
		Tracker:       fakeTracker{n: 1},
		MaxConcurrent: 7,
	})

	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	c.HandleHealth(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var hr protocol.HealthResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &hr))

	assert.True(t, hr.OK)
	assert.Equal(t, 1, hr.RunningContainers)
	assert.Equal(t, 7, hr.MaxConcurrent)
}

func TestHealth_NilTrackerReportsZero(t *testing.T) {
	c := NewCore(CoreConfig{MaxConcurrent: 5}) // no Tracker

	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	c.HandleHealth(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var hr protocol.HealthResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &hr))

	assert.True(t, hr.OK)
	assert.Equal(t, 0, hr.RunningContainers)
	assert.Equal(t, 5, hr.MaxConcurrent)
}

func TestReadyz_OKAndDraining(t *testing.T) {
	var draining atomic.Bool

	c := NewCore(CoreConfig{Draining: &draining})

	r1 := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w1 := httptest.NewRecorder()
	c.HandleReadyz(w1, r1)
	require.Equal(t, http.StatusOK, w1.Code)

	draining.Store(true)

	r2 := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w2 := httptest.NewRecorder()
	c.HandleReadyz(w2, r2)
	require.Equal(t, http.StatusServiceUnavailable, w2.Code)
	assert.Contains(t, w2.Body.String(), "draining")
}
