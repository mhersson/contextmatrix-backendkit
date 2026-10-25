package taskskills

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// endpointPath is the signed-GET path the resolver is constructed with.
const endpointPath = "/api/agent/task-skills-source"

func TestResolveFetchesPointerClonesAndCaches(t *testing.T) {
	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		assert.Equal(t, endpointPath, r.URL.Path)
		assert.NotEmpty(t, r.Header.Get("X-Signature-256"), "the GET is HMAC-signed")

		_ = json.NewEncoder(w).Encode(map[string]string{
			"git_remote_url": "https://example.test/skills.git",
			"ref":            "abc123",
			"token":          "tok",
		})
	}))
	defer srv.Close()

	var gotURL, gotRef, gotDest, gotTok string

	cloner := func(_ context.Context, url, ref, dest, token string) error {
		gotURL, gotRef, gotDest, gotTok = url, ref, dest, token

		return nil
	}

	r := NewResolver(srv.URL, "key", t.TempDir(), endpointPath)
	r.cloner = cloner

	dir, err := r.Resolve(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, dir)
	assert.Equal(t, "https://example.test/skills.git", gotURL)
	assert.Equal(t, "abc123", gotRef)
	assert.Equal(t, dir, gotDest)
	assert.Equal(t, "tok", gotTok)

	// Second call is cached: no second pointer fetch, no second clone.
	dir2, err := r.Resolve(context.Background())
	require.NoError(t, err)
	assert.Equal(t, dir, dir2)
	assert.Equal(t, int32(1), hits.Load(), "pointer fetched once; result cached")
}

func TestResolveEmptyPointerYieldsNoSkills(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"git_remote_url": "", "ref": ""})
	}))
	defer srv.Close()

	r := NewResolver(srv.URL, "key", t.TempDir(), endpointPath)
	r.cloner = func(context.Context, string, string, string, string) error { return nil }

	_, err := r.Resolve(context.Background())
	require.Error(t, err, "an empty remote URL means there is no skills source")
}

// TestResolveRejectsDashLeadingRefBeforeCloner pins the Resolve-level dash
// guard: a git URL or ref beginning with '-' (which git would interpret as a
// flag) is rejected before the cloner is ever invoked. The fake cloner
// t.Fatals if reached, so the guard must sit upstream of the clone call.
func TestResolveRejectsDashLeadingRefBeforeCloner(t *testing.T) {
	t.Run("dash-leading ref is rejected", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"git_remote_url": "https://example.test/skills.git",
				"ref":            "-upload-pack=/tmp/pwn",
			})
		}))
		defer srv.Close()

		r := NewResolver(srv.URL, "key", t.TempDir(), endpointPath)
		r.cloner = func(_ context.Context, _, _, _, _ string) error {
			t.Fatal("cloner must not be called with a dash-leading ref")

			return nil
		}

		_, err := r.Resolve(context.Background())
		require.Error(t, err, "dash-leading ref must be rejected before exec")
	})

	t.Run("dash-leading URL is rejected", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"git_remote_url": "--upload-pack=/tmp/pwn",
				"ref":            "abc123",
			})
		}))
		defer srv.Close()

		r := NewResolver(srv.URL, "key", t.TempDir(), endpointPath)
		r.cloner = func(_ context.Context, _, _, _, _ string) error {
			t.Fatal("cloner must not be called with a dash-leading URL")

			return nil
		}

		_, err := r.Resolve(context.Background())
		require.Error(t, err, "dash-leading URL must be rejected before exec")
	})
}

func TestResolveDoesNotCacheFailure(t *testing.T) {
	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)

			return
		}

		_ = json.NewEncoder(w).Encode(map[string]string{
			"git_remote_url": "https://example.test/s.git",
			"ref":            "r",
			"token":          "tok",
		})
	}))
	defer srv.Close()

	r := NewResolver(srv.URL, "key", t.TempDir(), endpointPath)
	r.cloner = func(context.Context, string, string, string, string) error { return nil }

	_, err := r.Resolve(context.Background())
	require.Error(t, err)

	_, err = r.Resolve(context.Background())
	require.NoError(t, err, "a prior failure is not cached; the next call retries")
}

// TestResolveDetachedFromCallerContext pins that Resolve runs the pointer fetch
// and clone on a context detached from the caller's cancellation, with its own
// bounded deadline. A caller disconnecting mid-clone must not abort the clone;
// the result must still be cached on success.
func TestResolveDetachedFromCallerContext(t *testing.T) {
	cloneStarted := make(chan struct{})
	cloneProceed := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"git_remote_url": "https://example.test/skills.git",
			"ref":            "abc123",
			"token":          "tok",
		})
	}))
	defer srv.Close()

	r := NewResolver(srv.URL, "key", t.TempDir(), endpointPath)
	r.cloner = func(ctx context.Context, _, _, _, _ string) error {
		// The derived context must have a bounded deadline.
		deadline, ok := ctx.Deadline()
		assert.True(t, ok, "clone context must have a deadline")
		assert.True(t, deadline.After(time.Now()), "clone deadline must be in the future")

		// Signal that the clone has started.
		close(cloneStarted)
		// Block until the test tells us to proceed.
		<-cloneProceed

		// The context must still be alive (not cancelled).
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		return nil
	}

	callerCtx, callerCancel := context.WithCancel(context.Background())
	defer callerCancel()

	type result struct {
		dir string
		err error
	}

	resCh := make(chan result, 1)

	go func() {
		dir, err := r.Resolve(callerCtx)
		resCh <- result{dir, err}
	}()

	// Wait for the clone to start, then cancel the caller's context.
	<-cloneStarted
	callerCancel()

	// Now unblock the clone.
	close(cloneProceed)

	res := <-resCh
	require.NoError(t, res.err)
	assert.NotEmpty(t, res.dir)

	// Second call returns the cached result.
	dir2, err := r.Resolve(context.Background())
	require.NoError(t, err)
	assert.Equal(t, res.dir, dir2)
}

// ---- CM-provisioned clone token ----------------------------------------------

// TestResolveUsesCMProvisionedTokenWhenPresent pins that the resolver clones
// with the token carried on the task-skills-source response.
func TestResolveUsesCMProvisionedTokenWhenPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"git_remote_url":   "https://example.test/skills.git",
			"ref":              "abc123",
			"token":            "cm-provisioned-token",
			"token_expires_at": "2026-07-03T18:00:00Z",
		})
	}))
	defer srv.Close()

	var gotTok string

	r := NewResolver(srv.URL, "key", t.TempDir(), endpointPath)
	r.cloner = func(_ context.Context, _, _, _, token string) error {
		gotTok = token

		return nil
	}

	_, err := r.Resolve(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "cm-provisioned-token", gotTok)
}

// TestResolveErrorsWhenTokenAbsent pins the fail-closed guard: a pointer
// without a CM-provisioned clone token fails the resolve
// and never reaches the cloner.
func TestResolveErrorsWhenTokenAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"git_remote_url": "https://example.test/skills.git",
			"ref":            "abc123",
		})
	}))
	defer srv.Close()

	r := NewResolver(srv.URL, "key", t.TempDir(), endpointPath)
	r.cloner = func(context.Context, string, string, string, string) error {
		t.Fatal("cloner must not be called without a clone token")

		return nil
	}

	_, err := r.Resolve(context.Background())
	require.Error(t, err)
}

// TestResolveTokenExpiresAtAbsentMalformed pins that token_expires_at is
// informational only: the resolver does not parse it, so an absent or
// malformed value must not fail a resolve that carries a token.
func TestResolveTokenExpiresAtAbsentMalformed(t *testing.T) {
	cases := []struct {
		name     string
		response map[string]string
	}{
		{
			name: "token_expires_at absent",
			response: map[string]string{
				"git_remote_url": "https://example.test/skills.git",
				"ref":            "abc123",
				"token":          "cm-provisioned-token",
			},
		},
		{
			name: "token_expires_at malformed",
			response: map[string]string{
				"git_remote_url":   "https://example.test/skills.git",
				"ref":              "abc123",
				"token":            "cm-provisioned-token",
				"token_expires_at": "not-a-timestamp",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(tc.response)
			}))
			defer srv.Close()

			var gotTok string

			r := NewResolver(srv.URL, "key", t.TempDir(), endpointPath)
			r.cloner = func(_ context.Context, _, _, _, token string) error {
				gotTok = token

				return nil
			}

			_, err := r.Resolve(context.Background())
			require.NoError(t, err, "expiry is informational for the clone; the resolver must not parse it")
			assert.Equal(t, "cm-provisioned-token", gotTok)
		})
	}
}

// TestGitCloneShallowAndTokenNotPersisted runs the real clone path against a
// local bare repo and checks the token reaches git only through the process
// env: it must not land anywhere under the checkout's .git.
func TestGitCloneShallowAndTokenNotPersisted(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}

	// Keep the user's git config out of the test.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	runGit := func(dir string, args ...string) {
		t.Helper()

		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir

		out, runErr := cmd.CombinedOutput()
		require.NoError(t, runErr, string(out))
	}

	work := t.TempDir()
	runGit(work, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(work, "skill.md"), []byte("one"), 0o644))
	runGit(work, "add", ".")
	runGit(work, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-m", "one")
	require.NoError(t, os.WriteFile(filepath.Join(work, "skill.md"), []byte("two"), 0o644))
	runGit(work, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-am", "two")

	bare := filepath.Join(t.TempDir(), "skills.git")
	runGit(work, "clone", "-q", "--bare", work, bare)

	const token = "tok-secret-0123456789"

	dest := filepath.Join(t.TempDir(), "task-skills")
	r := NewResolver("http://localhost", "key", t.TempDir(), endpointPath)

	require.NoError(t, r.gitClone(context.Background(), "file://"+bare, "", dest, token))

	got, err := os.ReadFile(filepath.Join(dest, "skill.md"))
	require.NoError(t, err)
	assert.Equal(t, "two", string(got))

	_, err = os.Stat(filepath.Join(dest, ".git", "shallow"))
	require.NoError(t, err, "the checkout is shallow")

	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))

	require.NoError(t, filepath.WalkDir(filepath.Join(dest, ".git"), func(p string, d fs.DirEntry, walkErr error) error {
		require.NoError(t, walkErr)

		if d.IsDir() {
			return nil
		}

		data, readErr := os.ReadFile(p) //nolint:gosec // test-owned temp dir
		require.NoError(t, readErr)
		assert.NotContains(t, string(data), token, p)
		assert.NotContains(t, string(data), encoded, p)

		return nil
	}))
}
