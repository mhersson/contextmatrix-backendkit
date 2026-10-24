package llmproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config configures a Proxy.
type Config struct {
	// Prefix is the mount path, e.g. "/llm": leading slash, no trailing slash.
	Prefix string
	// CACertFile is a PEM bundle trusted for upstream TLS on top of the system pool.
	CACertFile string
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

// Proxy forwards worker LLM calls to the upstream registered for the caller's token.
type Proxy struct {
	prefix string
	log    *slog.Logger
	reg    *registry
	rp     *httputil.ReverseProxy
}

type ctxKey struct{}

// New builds a Proxy. Prefix must start with "/" and must not end with one.
func New(cfg Config) (*Proxy, error) {
	if len(cfg.Prefix) < 2 || cfg.Prefix[0] != '/' || strings.HasSuffix(cfg.Prefix, "/") {
		return nil, errors.New("llmproxy: prefix must start with / and not end with /")
	}

	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Every run shares the pooled connection, so a dead one must be
		// detected before the worker's stall timeout.
		HTTP2: &http.HTTP2Config{SendPingTimeout: 30 * time.Second},
	}

	if cfg.CACertFile != "" {
		pool, err := loadPool(cfg.CACertFile)
		if err != nil {
			return nil, err
		}

		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}

	p := &Proxy{prefix: cfg.Prefix, log: log, reg: &registry{runs: map[string]*entry{}}}
	p.rp = &httputil.ReverseProxy{
		Rewrite:       p.rewrite,
		Transport:     tr,
		FlushInterval: -1,
		ErrorHandler:  p.upstreamError,
		ErrorLog:      slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	return p, nil
}

func loadPool(path string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("llmproxy: read CA file: %w", err)
	}

	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}

	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("llmproxy: CA file holds no PEM certificate")
	}

	return pool, nil
}

// Paths lists the endpoints the proxy serves, for the metrics allowlist.
func Paths(prefix string) []string {
	return []string{prefix + "/chat/completions", prefix + "/models"}
}

// ServeHTTP answers 401 to a request without a live token before any route
// check; only POST <prefix>/chat/completions and GET <prefix>/models are
// forwarded, anything else is 404.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

	var e *entry
	if ok {
		e = p.reg.lookup(token)
	}

	if e == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")

		return
	}

	suffix, ok := strings.CutPrefix(r.URL.Path, p.prefix)
	if !ok || !allowed(r.Method, suffix) {
		writeError(w, http.StatusNotFound, "not found")

		return
	}

	p.rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, e)))
}

func allowed(method, suffix string) bool {
	return (method == http.MethodPost && suffix == "/chat/completions") ||
		(method == http.MethodGet && suffix == "/models")
}

func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	e := pr.In.Context().Value(ctxKey{}).(*entry)
	suffix := strings.TrimPrefix(pr.In.URL.Path, p.prefix)

	// Register validated the base, so concatenation always parses.
	out, _ := url.Parse(e.base + suffix)
	out.RawQuery = pr.In.URL.RawQuery
	pr.Out.URL = out
	pr.Out.Host = ""

	pr.Out.Header.Del("Authorization")

	if e.apiKey != "" {
		pr.Out.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
}

func (p *Proxy) upstreamError(w http.ResponseWriter, r *http.Request, err error) {
	level := slog.LevelWarn
	if errors.Is(err, context.Canceled) {
		level = slog.LevelDebug
	}

	p.log.Log(r.Context(), level, "llm proxy upstream failure",
		"run", r.Context().Value(ctxKey{}).(*entry).runKey, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusBadGateway, "upstream unavailable")
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"message":%q}}`, msg)
}
