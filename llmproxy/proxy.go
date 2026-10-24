package llmproxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// Config configures a Proxy.
type Config struct {
	// Prefix is the mount path, e.g. "/llm": leading slash, no trailing slash.
	Prefix string
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

	p := &Proxy{prefix: cfg.Prefix, log: log, reg: &registry{runs: map[string]*entry{}}}
	p.rp = &httputil.ReverseProxy{
		Rewrite:       p.rewrite,
		FlushInterval: -1,
		ErrorHandler:  p.upstreamError,
	}

	return p, nil
}

// Paths lists the endpoints the proxy serves, for the metrics allowlist.
func Paths(prefix string) []string {
	return []string{prefix + "/chat/completions", prefix + "/models"}
}

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
	p.log.Warn("llm proxy upstream failure", "error", err, "path", r.URL.Path)
	writeError(w, http.StatusBadGateway, "upstream unavailable")
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"message":%q}}`, msg)
}
