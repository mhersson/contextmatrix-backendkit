package llmproxy

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// DefaultPrefix is the path a backend mounts the proxy at.
const DefaultPrefix = "/llm"

// ValidateContainerURL checks container_serve_url, the base URL workers use to
// reach serve, and returns it without a trailing slash. Empty is valid: the
// caller then falls back to the host gateway via WorkerBaseURL.
func ValidateContainerURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("must use http or https")
	}

	host := u.Hostname()
	if host == "" {
		return "", errors.New("must include a host")
	}

	if u.Path != "" && u.Path != "/" {
		return "", errors.New("must not carry a path")
	}

	// A bare "?" or "#" leaves an empty query or fragment in u but still
	// corrupts the appended prefix, so check raw.
	if strings.ContainsAny(raw, "?#") {
		return "", errors.New("must not carry a query or fragment")
	}

	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() {
			return "", errors.New("must not be an unspecified address")
		}

		if ip.IsLoopback() {
			return "", errors.New("must not be a loopback address: workers would reach themselves")
		}
	} else if strings.EqualFold(host, "localhost") {
		return "", errors.New("must not be localhost: workers would reach themselves")
	}

	return strings.TrimSuffix(raw, "/"), nil
}

// WorkerBaseURL is the LLM base URL handed to a worker: containerURL (a value
// ValidateContainerURL returned) or, when empty, the host gateway on port, plus
// prefix. An empty result would send the stand-in token to the worker type's
// default upstream, hence the fallback.
func WorkerBaseURL(containerURL string, port int, prefix string) string {
	base := strings.TrimSuffix(containerURL, "/")
	if base == "" {
		base = "http://" + net.JoinHostPort("host.docker.internal", strconv.Itoa(port))
	}

	return base + prefix
}
