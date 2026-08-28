package logbridge_test

import (
	"sync"
	"testing"
	"time"

	"github.com/mhersson/contextmatrix-backendkit/logbridge"
	protocol "github.com/mhersson/contextmatrix-protocol"
	"github.com/stretchr/testify/assert"
)

// recvEntry drains one published entry or fails on timeout.
func recvEntry(t *testing.T, ch <-chan protocol.LogEntry) protocol.LogEntry {
	t.Helper()

	select {
	case e := <-ch:
		return e
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected a bridged entry but got none (timeout)")

		return protocol.LogEntry{}
	}
}

func sessionKeyOf(e protocol.LogEntry) string { return e.SessionID }

// TestRedactorRegistry_SessionKeyLifecycle covers the CM-provisioned-key
// masking hole. Worker stderr and unparsable stdout are bridged host-side with
// ONLY the log-bridge redactor applied (the in-worker redactor never sees
// them), so a per-session LLM key must be masked there from registration at
// chat-start until removal on container exit.
func TestRedactorRegistry_SessionKeyLifecycle(t *testing.T) {
	t.Parallel()

	const sessionKey = "sk-session-provisioned-111111"

	hub := logbridge.NewHub(sessionKeyOf, nil)
	_, ch := hub.Subscribe("")
	bridge := logbridge.NewBridge(logbridge.BridgeConfig{Hub: hub})

	registry := logbridge.NewRedactorRegistry(bridge)

	// Before any session key is registered, the payload key is NOT masked.
	bridge.BridgeLine(logbridge.Key{SessionID: testSession}, []byte("boot "+sessionKey), true)
	assert.Contains(t, recvEntry(t, ch).Content, sessionKey,
		"an unregistered session key must not be masked yet")

	// After chat-start registers the key, a stderr-path line is masked.
	registry.AddSessionKey(testSession, sessionKey)
	bridge.BridgeLine(logbridge.Key{SessionID: testSession}, []byte("panic: leaked "+sessionKey), true)

	got := recvEntry(t, ch)
	assert.NotContains(t, got.Content, sessionKey,
		"a registered session key must be masked on the stderr path")
	assert.Contains(t, got.Content, "[REDACTED]")

	// After session end the key is forgotten: a later line is NOT masked.
	registry.RemoveSessionKey(testSession)
	bridge.BridgeLine(logbridge.Key{SessionID: testSession}, []byte("ended "+sessionKey), true)
	assert.Contains(t, recvEntry(t, ch).Content, sessionKey,
		"a session key must no longer be masked after removal (registry no longer holds it)")
}

// TestRedactorRegistry_EmptyKeyIgnored verifies AddSessionKey ignores an empty
// key (a non-nil LLMEndpoint carrying no APIKey) so it never tracks a value that
// would widen nothing, and that a remove of an unregistered session is a no-op.
func TestRedactorRegistry_EmptyKeyIgnored(t *testing.T) {
	t.Parallel()

	hub := logbridge.NewHub(sessionKeyOf, nil)
	_, ch := hub.Subscribe("")
	bridge := logbridge.NewBridge(logbridge.BridgeConfig{Hub: hub})
	registry := logbridge.NewRedactorRegistry(bridge)

	registry.AddSessionKey(testSession, "")
	registry.RemoveSessionKey(testSession) // unregistered → no-op, must not panic

	bridge.BridgeLine(logbridge.Key{SessionID: testSession}, []byte("plain line"), true)
	assert.Equal(t, "plain line", recvEntry(t, ch).Content)
}

// TestRedactorRegistry_MultipleSessionKeysBothMasked is the regression guard
// for the multi-secret-per-session clobber: a chat session commonly carries
// BOTH a CM-provisioned LLM key and a CM-provisioned git-credentials bearer
// (two independent multi-user-mode features), registered under the SAME
// session ID via two separate AddSessionKey calls. The second call must not
// silently displace the first - both must stay masked until the session ends,
// and RemoveSessionKey(sessionID) must forget both together.
func TestRedactorRegistry_MultipleSessionKeysBothMasked(t *testing.T) {
	t.Parallel()

	const (
		llmKey    = "sk-llm-session-key-000000"
		gitBearer = "sess1.git-credentials-bearer-111111"
	)

	hub := logbridge.NewHub(sessionKeyOf, nil)
	_, ch := hub.Subscribe("")
	bridge := logbridge.NewBridge(logbridge.BridgeConfig{Hub: hub})
	registry := logbridge.NewRedactorRegistry(bridge)

	registry.AddSessionKey(testSession, llmKey)
	registry.AddSessionKey(testSession, gitBearer)

	bridge.BridgeLine(logbridge.Key{SessionID: testSession}, []byte("keys: "+llmKey+" and "+gitBearer), true)

	got := recvEntry(t, ch)
	assert.NotContains(t, got.Content, llmKey,
		"the first-registered key must still be masked after a second key is registered for the same session")
	assert.NotContains(t, got.Content, gitBearer,
		"the second-registered key must also be masked")

	registry.RemoveSessionKey(testSession)
	bridge.BridgeLine(logbridge.Key{SessionID: testSession}, []byte("after removal: "+llmKey+" and "+gitBearer), true)

	got = recvEntry(t, ch)
	assert.Contains(t, got.Content, llmKey, "removal must forget both keys, not just the last one")
	assert.Contains(t, got.Content, gitBearer, "removal must forget both keys, not just the last one")
}

// TestRedactorRegistry_RedactLineLifecycle covers the secondary-sink use case:
// RedactLine applies the registry's composed redactor to a caller-supplied
// line, tracking the same lifecycle the bridge path does - unmasked before
// registration, masked while registered, unmasked after removal.
func TestRedactorRegistry_RedactLineLifecycle(t *testing.T) {
	t.Parallel()

	const sessionKey = "sk-session-provisioned-222222"

	hub := logbridge.NewHub(sessionKeyOf, nil)
	bridge := logbridge.NewBridge(logbridge.BridgeConfig{Hub: hub})
	registry := logbridge.NewRedactorRegistry(bridge)

	// Before registration the key passes through unmasked.
	got := registry.RedactLine("boot " + sessionKey)
	assert.Contains(t, got, sessionKey,
		"an unregistered session key must not be masked yet")

	// After registration the key is masked without going through the bridge.
	registry.AddSessionKey(testSession, sessionKey)
	got = registry.RedactLine("leaked " + sessionKey)
	assert.NotContains(t, got, sessionKey,
		"a registered session key must be masked by RedactLine")
	assert.Contains(t, got, "[REDACTED]")

	// After removal the same value passes through unmasked again.
	registry.RemoveSessionKey(testSession)
	got = registry.RedactLine("ended " + sessionKey)
	assert.Contains(t, got, sessionKey,
		"a session key must no longer be masked after removal")
}

// TestRedactorRegistry_RedactLineEmptyKeyIgnored verifies the empty-key rule
// carries over to RedactLine: AddSessionKey with an empty key is ignored so
// nothing is masked, and removing an unregistered session is a no-op.
func TestRedactorRegistry_RedactLineEmptyKeyIgnored(t *testing.T) {
	t.Parallel()

	hub := logbridge.NewHub(sessionKeyOf, nil)
	bridge := logbridge.NewBridge(logbridge.BridgeConfig{Hub: hub})
	registry := logbridge.NewRedactorRegistry(bridge)

	registry.AddSessionKey(testSession, "")
	got := registry.RedactLine("plain line")
	assert.Equal(t, "plain line", got,
		"an empty key must not register any masking")

	registry.RemoveSessionKey(testSession) // unregistered -> no-op, must not panic
	assert.Equal(t, "plain line", registry.RedactLine("plain line"),
		"a session with no registered key must not mask anything")
}

// TestRedactorRegistry_RedactLineShortKeyIgnored verifies the minimum-length
// rule carries over to RedactLine: redact.New ignores secrets shorter than six
// bytes, so a registered short key must not register any masking.
func TestRedactorRegistry_RedactLineShortKeyIgnored(t *testing.T) {
	t.Parallel()

	hub := logbridge.NewHub(sessionKeyOf, nil)
	bridge := logbridge.NewBridge(logbridge.BridgeConfig{Hub: hub})
	registry := logbridge.NewRedactorRegistry(bridge)

	registry.AddSessionKey(testSession, "abc")
	// Called inside the registered window: a normal key would be masked here
	// (see RedactLineLifecycle), so an unchanged line can only mean the short
	// key was dropped at registration time.
	got := registry.RedactLine("line with abc")
	assert.Equal(t, "line with abc", got,
		"a key shorter than the redact minimum must not register any masking")

	registry.RemoveSessionKey(testSession) // unregistered -> no-op, must not panic
	assert.Equal(t, "line with abc", registry.RedactLine("line with abc"),
		"a session with no registered key must not mask anything")
}

// TestRedactorRegistry_RedactLineConcurrentWithRebuild exercises RedactLine
// against parallel AddSessionKey/RemoveSessionKey rebuilds. Every result must
// be a well-formed output of either the old or the new redactor: the secret is
// masked once registered and unmasked only after its removal completes, with
// no torn output or panic.
func TestRedactorRegistry_RedactLineConcurrentWithRebuild(t *testing.T) {
	t.Parallel()

	const sessionKey = "sk-session-provisioned-333333"

	line := "stderr panic: leaked " + sessionKey

	hub := logbridge.NewHub(sessionKeyOf, nil)
	bridge := logbridge.NewBridge(logbridge.BridgeConfig{Hub: hub})
	registry := logbridge.NewRedactorRegistry(bridge)

	var wg sync.WaitGroup

	for range 4 {
		wg.Go(func() {
			for range 200 {
				got := registry.RedactLine(line)
				switch got {
				case line:
					// Old redactor (before registration or after removal).
				default:
					assert.Equal(t, "stderr panic: leaked [REDACTED]", got,
						"masked output must be a well-formed result of the new redactor")
				}
			}
		})
	}

	for range 20 {
		wg.Go(func() {
			registry.AddSessionKey(testSession, sessionKey)
			registry.RemoveSessionKey(testSession)
		})
	}

	wg.Wait()
}
