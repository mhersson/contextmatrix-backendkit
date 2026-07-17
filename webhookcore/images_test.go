package webhookcore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	protocol "github.com/mhersson/contextmatrix-protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeImageLister struct {
	summaries []ImageSummary
	err       error
}

func (f *fakeImageLister) ListImages(_ context.Context) ([]ImageSummary, error) {
	return f.summaries, f.err
}

// imagesMux mounts HandleImages behind Auth, mirroring the signed GET /images
// route the serve layer wires.
func imagesMux(c *Core) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /images", c.Auth(c.HandleImages))

	return mux
}

// newImagesCore builds a minimal Core with the images dependency wired.
func newImagesCore(images ImageLister) *Core {
	return NewCore(CoreConfig{
		APIKey:           testAPIKey,
		Images:           images,
		ImageListFilters: []string{"contextmatrix-agent"},
	})
}

func signedGetImages(t *testing.T, c *Core, target string) *httptest.ResponseRecorder {
	t.Helper()

	r := httptest.NewRequest(http.MethodGet, target, nil)
	signReq(t, r, testAPIKey, nil, nowTS())

	w := httptest.NewRecorder()
	imagesMux(c).ServeHTTP(w, r)

	return w
}

func TestImages_FiltersPerTagAndMaps(t *testing.T) {
	c := newImagesCore(&fakeImageLister{summaries: []ImageSummary{
		{
			Tags:      []string{"contextmatrix-agent-worker:go-node"},
			Digests:   []string{"contextmatrix-agent-worker@sha256:abc"},
			CreatedAt: 1750000000,
			SizeBytes: 2_560_000_000,
		},
		{Tags: []string{"harbor.example/apps/contextmatrix:latest"}}, // no matching tag: dropped
		{Tags: []string{"contextmatrix-agent-worker:dev", "unrelated:tag"}},
	}})

	w := signedGetImages(t, c, "/images")
	require.Equal(t, http.StatusOK, w.Code)

	var resp protocol.ListImagesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.True(t, resp.OK)
	require.Len(t, resp.Images, 2)
	assert.Equal(t, []string{"contextmatrix-agent-worker:go-node"}, resp.Images[0].Tags)
	assert.Equal(t, []string{"contextmatrix-agent-worker@sha256:abc"}, resp.Images[0].Digests)
	assert.Equal(t, int64(1750000000), resp.Images[0].Created)
	assert.Equal(t, int64(2_560_000_000), resp.Images[0].Size)
	// Non-matching tag pruned from the mixed image.
	assert.Equal(t, []string{"contextmatrix-agent-worker:dev"}, resp.Images[1].Tags)
}

func TestImages_DockerErrorReturns502Generic(t *testing.T) {
	c := newImagesCore(&fakeImageLister{err: errors.New("daemon exploded: secret detail")})

	w := signedGetImages(t, c, "/images")
	require.Equal(t, http.StatusBadGateway, w.Code)

	var resp protocol.ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, protocol.CodeUpstreamFailure, resp.Code)
	assert.NotContains(t, resp.Message, "secret detail")
}

func TestImages_RequiresSignature(t *testing.T) {
	c := newImagesCore(&fakeImageLister{})

	r := httptest.NewRequest(http.MethodGet, "/images", nil)
	w := httptest.NewRecorder()
	imagesMux(c).ServeHTTP(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestImages_NilListerReturns500(t *testing.T) {
	c := NewCore(CoreConfig{APIKey: testAPIKey})

	w := signedGetImages(t, c, "/images")
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
