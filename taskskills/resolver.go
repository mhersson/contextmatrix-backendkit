// Package taskskills resolves ContextMatrix's task-skills onto a serve host: it
// fetches a {git_remote_url, ref} pointer from CM and shallow-clones it once
// into a cache dir the executor binds read-only into worker containers. The
// backend carries no task-skills config - CM is the single source of truth.
package taskskills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	protocol "github.com/mhersson/contextmatrix-protocol"
)

const requestTimeout = 15 * time.Second

// Resolver fetches the task-skills pointer from CM and clones it once, caching
// the resolved host dir for the process. The clone authenticates with the
// CM-provisioned token carried on the pointer - there is no local token
// source. Safe for concurrent use.
type Resolver struct {
	cmURL        string
	apiKey       string
	cacheDir     string
	endpointPath string
	http         *http.Client

	// cloner is the clone implementation; overridable in tests. Production uses
	// gitClone (a shallow git fetch+checkout with the CM-provisioned token).
	cloner func(ctx context.Context, gitURL, ref, dest, token string) error

	mu       sync.Mutex
	resolved string // cached host dir once a clone has succeeded
}

// NewResolver builds a Resolver. cmURL is ContextMatrix's base URL, apiKey the
// backend HMAC key, cacheDir a host directory the clone lands in (and the
// executor binds), endpointPath the signed-GET path CM serves the pointer on
// for this backend.
func NewResolver(cmURL, apiKey, cacheDir, endpointPath string) *Resolver {
	r := &Resolver{
		cmURL:        strings.TrimRight(cmURL, "/"),
		apiKey:       apiKey,
		cacheDir:     cacheDir,
		endpointPath: endpointPath,
		http:         &http.Client{Timeout: requestTimeout},
	}
	r.cloner = r.gitClone

	return r
}

// Resolve returns the host dir holding the task-skills, cloning on first use and
// caching the result. An error means "no skills this run"; failures are not
// cached, so the next trigger retries.
func (r *Resolver) Resolve(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.resolved != "" {
		return r.resolved, nil
	}

	p, err := r.fetchPointer(ctx)
	if err != nil {
		return "", err
	}

	gitURL, ref := p.GitRemoteURL, p.Ref

	if gitURL == "" {
		return "", fmt.Errorf("task-skills source has no git_remote_url")
	}

	if strings.HasPrefix(gitURL, "-") {
		return "", fmt.Errorf("task-skills git_remote_url begins with '-', which git interprets as a flag: %q", gitURL)
	}

	if strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("task-skills ref begins with '-', which git interprets as a flag: %q", ref)
	}

	token := p.Token
	if token == "" {
		return "", fmt.Errorf("CM did not provision a task-skills clone token")
	}

	dest := filepath.Join(r.cacheDir, "task-skills")
	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("clear skills cache: %w", err)
	}

	if err := r.cloner(ctx, gitURL, ref, dest, token); err != nil {
		return "", fmt.Errorf("clone task-skills: %w", err)
	}

	r.resolved = dest

	return dest, nil
}

// fetchPointer does a signed GET to the backend's task-skills-source endpoint.
func (r *Resolver) fetchPointer(ctx context.Context) (protocol.TaskSkillsSource, error) {
	uri, perr := requestURI(r.cmURL + r.endpointPath)
	if perr != nil {
		return protocol.TaskSkillsSource{}, perr
	}

	sig, ts := protocol.SignRequestHeaders(r.apiKey, http.MethodGet, uri, nil)

	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, r.cmURL+r.endpointPath, nil)
	if rerr != nil {
		return protocol.TaskSkillsSource{}, fmt.Errorf("create task-skills-source request: %w", rerr)
	}

	req.Header.Set(protocol.SignatureHeader, sig)
	req.Header.Set(protocol.TimestampHeader, ts)

	resp, derr := r.http.Do(req) //nolint:gosec // cmURL is operator config
	if derr != nil {
		return protocol.TaskSkillsSource{}, fmt.Errorf("fetch task-skills-source: %w", derr)
	}

	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return protocol.TaskSkillsSource{}, fmt.Errorf("task-skills-source returned %d", resp.StatusCode)
	}

	var p protocol.TaskSkillsSource
	if uerr := json.Unmarshal(body, &p); uerr != nil {
		return protocol.TaskSkillsSource{}, fmt.Errorf("parse task-skills-source: %w", uerr)
	}

	return p, nil
}

// gitClone does a one-shot shallow fetch+checkout of ref (a SHA, branch, or tag)
// into dest, authenticating with a per-invocation token passed as an
// http.extraheader via the subprocess env (see gitAuthEnv). When ref is empty it
// fetches the remote's default branch (HEAD).
func (r *Resolver) gitClone(ctx context.Context, gitURL, ref, dest, token string) error {
	// Reject dash-leading args before any exec: git interprets them as flags.
	// Both gitURL and ref are CM-sourced config that must not be option strings.
	if strings.HasPrefix(gitURL, "-") || strings.HasPrefix(ref, "-") {
		return fmt.Errorf("git clone: url and ref must not begin with '-' (got url=%q ref=%q)", gitURL, ref)
	}

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("mkdir skills dest: %w", err)
	}

	fetchRef := ref
	if fetchRef == "" {
		fetchRef = "HEAD"
	}

	steps := [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", gitURL},
		// '--' separates options from the ref spec so a ref that looks like a
		// flag is passed as a positional argument. Mirrors run.go's clone guard.
		{"fetch", "--depth", "1", "origin", "--", fetchRef},
		{"checkout", "-q", "FETCH_HEAD"},
	}

	for _, args := range steps {
		cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // args are code-fixed; gitURL/ref are CM-sourced config
		cmd.Dir = dest
		cmd.Env = gitAuthEnv(token)

		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
	}

	return nil
}

func requestURI(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse url %q: %w", raw, err)
	}

	if u.Path == "" {
		u.Path = "/"
	}

	return u.RequestURI(), nil
}
