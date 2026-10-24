package llmproxy_test

import (
	"bufio"
	"bytes"
	"context"
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
	"time"

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

// mounted serves p the way adopters do: behind the metrics middleware.
func mounted(t *testing.T, p *llmproxy.Proxy) (*httptest.Server, *metrics.Metrics) {
	t.Helper()

	m := metrics.New("t", llmproxy.Paths("/llm"))
	core := webhookcore.NewCore(webhookcore.CoreConfig{Metrics: m})

	return serve(t, core.RecordMetrics(p.ServeHTTP)), m
}

func TestMountedMetricsLabel(t *testing.T) {
	up, _ := capture(t, nil)
	p := newProxy(t, llmproxy.Config{})
	tok, err := p.Register("run", protocol.LLMEndpoint{Type: "openai", BaseURL: up.URL})
	require.NoError(t, err)

	srv, m := mounted(t, p)

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

// gate blocks upstream handlers until opened. Register open with t.Cleanup after
// the servers so it runs first and a blocked handler never wedges Server.Close.
type gate struct {
	ch   chan struct{}
	once sync.Once
}

func newGate() *gate { return &gate{ch: make(chan struct{})} }

func (g *gate) open() { g.once.Do(func() { close(g.ch) }) }

func startStream(ctx context.Context, t *testing.T, url, token string, body io.Reader) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/llm/chat/completions", body)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	t.Cleanup(func() { _ = resp.Body.Close() })

	return resp
}

// lineReader delivers the response lines over a channel so tests can wait with a timer.
func lineReader(resp *http.Response) <-chan string {
	lines := make(chan string, 64)

	go func() {
		defer close(lines)

		sc := bufio.NewReader(resp.Body)
		for {
			l, err := sc.ReadString('\n')
			if l != "" {
				lines <- strings.TrimRight(l, "\n")
			}

			if err != nil {
				return
			}
		}
	}()

	return lines
}

func nextLine(t *testing.T, lines <-chan string, within time.Duration) string {
	t.Helper()

	select {
	case l, ok := <-lines:
		require.True(t, ok, "stream ended early")

		return l
	case <-time.After(within):
		require.FailNow(t, "no line within deadline")

		return ""
	}
}

func TestSSEFlushAndKeepalive(t *testing.T) {
	hold := newGate()
	up := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		fl := w.(http.Flusher)

		_, _ = io.WriteString(w, ": keepalive\n\n")

		fl.Flush()

		_, _ = io.WriteString(w, "data: {\"x\":1}\n\n")

		fl.Flush()

		select {
		case <-hold.ch:
		case <-r.Context().Done():
		}
	}))

	p := newProxy(t, llmproxy.Config{})
	tok, err := p.Register("run", protocol.LLMEndpoint{Type: "openai", BaseURL: up.URL})
	require.NoError(t, err)

	srv, _ := mounted(t, p)
	t.Cleanup(hold.open)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	lines := lineReader(startStream(ctx, t, srv.URL, tok, strings.NewReader("{}")))

	assert.Equal(t, ": keepalive", nextLine(t, lines, 2*time.Second))
	assert.Empty(t, nextLine(t, lines, 2*time.Second))
	assert.JSONEq(t, `{"x":1}`, strings.TrimPrefix(nextLine(t, lines, 2*time.Second), "data: "))
}

func TestBodiesByteIdentical(t *testing.T) {
	const respBody = `{ "usage" :{"prompt_tokens":10,"completion_tokens":5,"cost":0.000123456789} ,"id":"gen-1",  "choices":[ ]}`

	var chunk bytes.Buffer
	for i := range 4096 {
		chunk.WriteByte(byte('A' + i%26))
	}

	reqBody := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,`)
	for len(reqBody) < 3<<20 {
		reqBody = append(reqBody, chunk.Bytes()...)
	}

	reqBody = append(reqBody, `"}}]}]}`...)

	var got []byte

	up := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, respBody)
	}))

	p := newProxy(t, llmproxy.Config{})
	tok, err := p.Register("run", protocol.LLMEndpoint{Type: "openai", BaseURL: up.URL})
	require.NoError(t, err)

	srv, _ := mounted(t, p)

	resp := call(t, srv.URL, http.MethodPost, "/llm/chat/completions", tok, bytes.NewReader(reqBody))
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, bytes.Equal(reqBody, got), "request body altered (%d vs %d bytes)", len(reqBody), len(got))
	assert.True(t, bytes.Equal([]byte(respBody), out), "response body altered: %q", out)
}

func TestStatusErrorBodyRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		retryAfter string
		body       string
	}{
		{"400 routing failure", 400, "", `{"error":{"message":"m","metadata":{"failed_routing_step":"provider"}}}`},
		{"429 seconds", 429, "7", `{"error":{"message":"slow down"}}`},
		{"503 http-date", 503, "Wed, 21 Oct 2026 07:28:00 GMT", `{"error":{"message":"busy"}}`},
		{"401 from upstream", 401, "", `{"error":{"message":"bad key"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}

				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))

			p := newProxy(t, llmproxy.Config{})
			tok, err := p.Register("run", protocol.LLMEndpoint{Type: "openai", BaseURL: up.URL})
			require.NoError(t, err)

			srv, _ := mounted(t, p)

			resp := call(t, srv.URL, http.MethodPost, "/llm/chat/completions", tok, strings.NewReader("{}"))
			out, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			assert.Equal(t, tc.status, resp.StatusCode)
			assert.Equal(t, tc.body, string(out))
			assert.Equal(t, tc.retryAfter, resp.Header.Get("Retry-After"))
		})
	}
}

func TestConcurrentStreamsKeepTheirOwnKey(t *testing.T) {
	const perRun = 8

	all := newGate()

	var (
		mu      sync.Mutex
		arrived int
		bearers = map[string][]string{}
	)

	up := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		run := r.Header.Get("X-Run")
		bearers[run] = append(bearers[run], r.Header.Get("Authorization"))
		arrived++

		if arrived == 2*perRun {
			all.open()
		}
		mu.Unlock()

		select {
		case <-all.ch:
		case <-r.Context().Done():
			return
		}

		_, _ = io.WriteString(w, "data: ok\n\n")
		w.(http.Flusher).Flush()
	}))

	p := newProxy(t, llmproxy.Config{})
	tokens := map[string]string{}

	for _, run := range []string{"a", "b"} {
		tok, err := p.Register("key-"+run, protocol.LLMEndpoint{Type: "openai", BaseURL: up.URL, APIKey: "up-" + run})
		require.NoError(t, err)

		tokens[run] = tok
	}

	srv, _ := mounted(t, p)
	t.Cleanup(all.open)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	errs := make(chan error, 2*perRun)

	var wg sync.WaitGroup

	for run, tok := range tokens {
		for range perRun {
			wg.Add(1)

			go func() {
				defer wg.Done()

				req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/llm/chat/completions", strings.NewReader("{}"))
				if err != nil {
					errs <- err

					return
				}

				req.Header.Set("Authorization", "Bearer "+tok)
				req.Header.Set("X-Run", run)

				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					errs <- err

					return
				}
				defer resp.Body.Close()

				_, err = io.Copy(io.Discard, resp.Body)
				errs <- err
			}()
		}
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}

	mu.Lock()
	defer mu.Unlock()

	for _, run := range []string{"a", "b"} {
		require.Len(t, bearers[run], perRun, "run %s", run)

		for _, got := range bearers[run] {
			assert.Equal(t, "Bearer up-"+run, got)
		}
	}
}

func TestClientHangupCancelsUpstream(t *testing.T) {
	hold := newGate()
	cancelled := make(chan struct{})

	up := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()

		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-hold.ch:
		}
	}))

	p := newProxy(t, llmproxy.Config{})
	tok, err := p.Register("run", protocol.LLMEndpoint{Type: "openai", BaseURL: up.URL})
	require.NoError(t, err)

	srv, _ := mounted(t, p)
	t.Cleanup(hold.open)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lines := lineReader(startStream(ctx, t, srv.URL, tok, strings.NewReader("{}")))
	assert.Equal(t, "data: first", nextLine(t, lines, 2*time.Second))

	cancel()

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "upstream request not cancelled after client hangup")
	}
}
