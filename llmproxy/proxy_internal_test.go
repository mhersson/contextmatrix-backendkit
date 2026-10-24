package llmproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	protocol "github.com/mhersson/contextmatrix-protocol"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEmptyBaseURLIsOpenRouter(t *testing.T) {
	p, err := New(Config{Prefix: "/llm"})
	require.NoError(t, err)

	tok, err := p.Register("run", protocol.LLMEndpoint{Type: "openrouter"})
	require.NoError(t, err)

	var got string

	p.rp.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r.URL.String()

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("{}")),
			Request:    r,
		}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/llm/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "https://openrouter.ai/api/v1/chat/completions", got)
}
