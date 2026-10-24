package llmproxy_test

import (
	"encoding/hex"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	protocol "github.com/mhersson/contextmatrix-protocol"

	"github.com/mhersson/contextmatrix-backendkit/llmproxy"
	"github.com/mhersson/contextmatrix-backendkit/metrics"
	"github.com/mhersson/contextmatrix-backendkit/webhookcore"
)

type seen struct {
	method, uri, host, auth, body string
	hasAuth                       bool
}

type recorded struct {
	mu   sync.Mutex
	reqs []seen
}

func (r *recorded) add(s seen) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.reqs = append(r.reqs, s)
}

func (r *recorded) all() []seen {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]seen(nil), r.reqs...)
}

func newProxy(t *testing.T, cfg llmproxy.Config) *llmproxy.Proxy {
	t.Helper()

	if cfg.Prefix == "" {
		cfg.Prefix = "/llm"
	}

	p, err := llmproxy.New(cfg)
	require.NoError(t, err)

	return p
}

func serve(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()

	s := httptest.NewServer(h)
	t.Cleanup(s.Close)

	return s
}

func call(t *testing.T, base, method, path, token string, body io.Reader) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, base+path, body)
	require.NoError(t, err)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	t.Cleanup(func() { _ = resp.Body.Close() })

	return resp
}

func capture(t *testing.T, h http.HandlerFunc) (*httptest.Server, *recorded) {
	t.Helper()

	rec := &recorded{}
	srv := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, has := r.Header["Authorization"]
		rec.add(seen{
			method: r.Method, uri: r.URL.RequestURI(), host: r.Host,
			auth: r.Header.Get("Authorization"), hasAuth: has, body: string(b),
		})

		if h != nil {
			h(w, r)
		}
	}))

	return srv, rec
}

// setup registers one run against a fresh capturing upstream mounted at base.
func setup(t *testing.T, apiKey, basePath string) (*httptest.Server, *recorded, string, *url.URL) {
	t.Helper()

	up, rec := capture(t, nil)
	p := newProxy(t, llmproxy.Config{})
	tok, err := p.Register("run", protocol.LLMEndpoint{Type: "openai", BaseURL: up.URL + basePath, APIKey: apiKey})
	require.NoError(t, err)

	upURL, err := url.Parse(up.URL)
	require.NoError(t, err)

	return serve(t, p), rec, tok, upURL
}

func TestUnknownTokenIs401(t *testing.T) {
	up, rec := capture(t, nil)
	p := newProxy(t, llmproxy.Config{})
	_, err := p.Register("run", protocol.LLMEndpoint{Type: "openai", BaseURL: up.URL})
	require.NoError(t, err)

	srv := serve(t, p)

	for _, tok := range []string{"", "junk", strings.Repeat("ab", 32)} {
		resp := call(t, srv.URL, http.MethodPost, "/llm/chat/completions", tok, nil)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "token %q", tok)
	}

	assert.Empty(t, rec.all())
}

func TestRevokedTokenIs401(t *testing.T) {
	up, _ := capture(t, nil)
	p := newProxy(t, llmproxy.Config{})
	tok, err := p.Register("run", protocol.LLMEndpoint{Type: "openai", BaseURL: up.URL})
	require.NoError(t, err)

	srv := serve(t, p)

	assert.Equal(t, http.StatusOK, call(t, srv.URL, http.MethodGet, "/llm/models", tok, nil).StatusCode)

	p.Revoke("run")
	assert.NotPanics(t, func() { p.Revoke("never") })

	assert.Equal(t, http.StatusUnauthorized, call(t, srv.URL, http.MethodGet, "/llm/models", tok, nil).StatusCode)
}

func TestReRegisterReplacesToken(t *testing.T) {
	up, _ := capture(t, nil)
	p := newProxy(t, llmproxy.Config{})
	ep := protocol.LLMEndpoint{Type: "openai", BaseURL: up.URL}

	first, err := p.Register("run", ep)
	require.NoError(t, err)

	second, err := p.Register("run", ep)
	require.NoError(t, err)

	srv := serve(t, p)

	assert.Equal(t, http.StatusUnauthorized, call(t, srv.URL, http.MethodGet, "/llm/models", first, nil).StatusCode)
	assert.Equal(t, http.StatusOK, call(t, srv.URL, http.MethodGet, "/llm/models", second, nil).StatusCode)
}

func TestTokenIs256BitHex(t *testing.T) {
	p := newProxy(t, llmproxy.Config{})
	ep := protocol.LLMEndpoint{Type: "openai", BaseURL: "http://example.com"}

	a, err := p.Register("a", ep)
	require.NoError(t, err)

	b, err := p.Register("b", ep)
	require.NoError(t, err)

	raw, err := hex.DecodeString(a)
	require.NoError(t, err)
	assert.Len(t, raw, 32)
	assert.NotEqual(t, a, b)
}

func TestKeyedUpstreamGetsRealBearer(t *testing.T) {
	srv, rec, tok, _ := setup(t, "real-key", "")

	call(t, srv.URL, http.MethodGet, "/llm/models", tok, nil)

	got := rec.all()
	require.Len(t, got, 1)
	assert.Equal(t, "Bearer real-key", got[0].auth)
}

func TestKeylessUpstreamGetsNoBearer(t *testing.T) {
	srv, rec, tok, _ := setup(t, "", "")

	call(t, srv.URL, http.MethodGet, "/llm/models", tok, nil)

	got := rec.all()
	require.Len(t, got, 1)
	assert.False(t, got[0].hasAuth, "stand-in token must not reach a keyless upstream")
}

func TestRoutesExact(t *testing.T) {
	srv, rec, tok, _ := setup(t, "k", "")

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/llm/chat/completions"},
		{http.MethodPost, "/llm/models"},
		{http.MethodPost, "/llm/keys"},
		{http.MethodGet, "/llm/credits"},
		{http.MethodPost, "/llm/chat/completions/x"},
	} {
		resp := call(t, srv.URL, c.method, c.path, tok, nil)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "%s %s", c.method, c.path)
	}

	assert.Empty(t, rec.all())

	assert.Equal(t, http.StatusOK, call(t, srv.URL, http.MethodPost, "/llm/chat/completions", tok, strings.NewReader("{}")).StatusCode)
	assert.Equal(t, http.StatusOK, call(t, srv.URL, http.MethodGet, "/llm/models", tok, nil).StatusCode)
}

func TestUpstreamURLAndHost(t *testing.T) {
	srv, rec, tok, upURL := setup(t, "k", "/api/v1")

	call(t, srv.URL, http.MethodPost, "/llm/chat/completions?x=1", tok, strings.NewReader("{}"))

	got := rec.all()
	require.Len(t, got, 1)
	assert.Equal(t, "/api/v1/chat/completions?x=1", got[0].uri)
	assert.Equal(t, upURL.Host, got[0].host)
}

func TestRegisterRejectsBadBaseURL(t *testing.T) {
	p := newProxy(t, llmproxy.Config{})

	for _, base := range []string{"ftp://x", "not a url", "http://"} {
		_, err := p.Register("run", protocol.LLMEndpoint{Type: "openai", BaseURL: base})
		require.Error(t, err, "base %q", base)
	}

	_, err := p.Register("run", protocol.LLMEndpoint{Type: "openrouter", BaseURL: ""})
	require.NoError(t, err)
}

func TestNewRejectsBadPrefix(t *testing.T) {
	for _, prefix := range []string{"", "llm", "/llm/"} {
		_, err := llmproxy.New(llmproxy.Config{Prefix: prefix})
		require.Error(t, err, "prefix %q", prefix)
	}
}

func TestMountedMetricsLabel(t *testing.T) {
	up, _ := capture(t, nil)
	p := newProxy(t, llmproxy.Config{})
	tok, err := p.Register("run", protocol.LLMEndpoint{Type: "openai", BaseURL: up.URL})
	require.NoError(t, err)

	m := metrics.New("t", llmproxy.Paths("/llm"))
	core := webhookcore.NewCore(webhookcore.CoreConfig{Metrics: m})
	srv := serve(t, core.RecordMetrics(p.ServeHTTP))

	resp := call(t, srv.URL, http.MethodPost, "/llm/chat/completions", tok, strings.NewReader("{}"))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	got := testutil.ToFloat64(m.WebhookRequestsTotal.WithLabelValues("llm/chat/completions", "200", "success"))
	assert.InEpsilon(t, float64(1), got, 1e-9)
}

func TestCACertFileTrustsPrivateUpstream(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(up.Close)

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: up.Certificate().Raw})
	require.NoError(t, os.WriteFile(caFile, pemBytes, 0o600))

	for _, tc := range []struct {
		name   string
		cfg    llmproxy.Config
		status int
	}{
		{"with CA file", llmproxy.Config{CACertFile: caFile}, http.StatusOK},
		{"without CA file", llmproxy.Config{}, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newProxy(t, tc.cfg)
			tok, err := p.Register("run", protocol.LLMEndpoint{Type: "openai", BaseURL: up.URL})
			require.NoError(t, err)

			resp := call(t, serve(t, p).URL, http.MethodGet, "/llm/models", tok, nil)
			assert.Equal(t, tc.status, resp.StatusCode)
		})
	}
}

func TestNewRejectsBadCACertFile(t *testing.T) {
	dir := t.TempDir()
	noPEM := filepath.Join(dir, "empty.pem")
	require.NoError(t, os.WriteFile(noPEM, []byte("not a certificate"), 0o600))

	for _, path := range []string{filepath.Join(dir, "missing.pem"), noPEM} {
		_, err := llmproxy.New(llmproxy.Config{Prefix: "/llm", CACertFile: path})
		require.Error(t, err, "path %q", path)
	}
}

func TestDialFailureIs502(t *testing.T) {
	up := httptest.NewServer(http.NotFoundHandler())
	base := up.URL
	up.Close()

	p := newProxy(t, llmproxy.Config{})
	tok, err := p.Register("run", protocol.LLMEndpoint{Type: "openai", BaseURL: base})
	require.NoError(t, err)

	resp := call(t, serve(t, p).URL, http.MethodGet, "/llm/models", tok, nil)
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
}
