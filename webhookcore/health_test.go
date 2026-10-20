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

// getHealth serves one /health request; a nil r is a plain GET /health.
func getHealth(t *testing.T, c *Core, r *http.Request) (*httptest.ResponseRecorder, protocol.HealthResponse) {
	t.Helper()

	if r == nil {
		r = httptest.NewRequest(http.MethodGet, "/health", nil)
	}

	w := httptest.NewRecorder()
	c.HandleHealth(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var hr protocol.HealthResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &hr))

	return w, hr
}

func TestHealth_ReportsTrackerCountAndMaxConcurrent(t *testing.T) {
	c := NewCore(CoreConfig{
		Tracker:       fakeTracker{n: 1},
		MaxConcurrent: 7,
	})

	_, hr := getHealth(t, c, nil)

	assert.True(t, hr.OK)
	assert.Equal(t, 1, hr.RunningContainers)
	assert.Equal(t, 7, hr.MaxConcurrent)
}

func TestHealth_ReportsConfiguredBuild(t *testing.T) {
	build := protocol.BuildInfo{
		Version:   "v1.2.0",
		Commit:    "0123456789abcdef0123456789abcdef01234567",
		BuildTime: "2026-10-21T10:00:00Z",
		Changes: []protocol.Change{{
			Number: 26, Kind: protocol.ChangeKindFeat,
			Title: "retain a per-key backlog", MergedAt: "2026-10-17T14:04:32Z",
		}},
	}

	_, hr := getHealth(t, NewCore(CoreConfig{Build: build}), nil)

	require.NotNil(t, hr.Build)
	assert.Equal(t, build, *hr.Build)
	assert.Empty(t, hr.WorkerImageRevision)
}

// CM reads "changes":[] as "no history recorded" and must never see null.
func TestHealth_UnsetBuildReportsEmptyChanges(t *testing.T) {
	w, _ := getHealth(t, NewCore(CoreConfig{}), nil)

	assert.Contains(t, w.Body.String(), `"build":{"changes":[]}`)
	assert.NotContains(t, w.Body.String(), "worker_image_revision")
}

func TestHealth_NilTrackerReportsZero(t *testing.T) {
	c := NewCore(CoreConfig{MaxConcurrent: 5}) // no Tracker

	_, hr := getHealth(t, c, nil)

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
