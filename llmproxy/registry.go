package llmproxy

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/url"
	"sync"

	protocol "github.com/mhersson/contextmatrix-protocol"
)

const defaultBaseURL = "https://openrouter.ai/api/v1"

type entry struct {
	runKey string
	token  []byte
	base   string
	apiKey string
}

type registry struct {
	mu   sync.RWMutex
	runs map[string]*entry
}

// Register stores the upstream for runKey and returns a fresh token, replacing
// any token the key held before.
func (p *Proxy) Register(runKey string, up protocol.LLMEndpoint) (string, error) {
	base := up.BaseURL
	if base == "" {
		base = defaultBaseURL
	}

	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("llmproxy: invalid upstream base URL")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("llmproxy: generate token: %w", err)
	}

	tok := hex.EncodeToString(raw)

	p.reg.mu.Lock()
	p.reg.runs[runKey] = &entry{runKey: runKey, token: []byte(tok), base: base, apiKey: up.APIKey}
	p.reg.mu.Unlock()

	return tok, nil
}

// Revoke drops the token for runKey. An unknown key is a no-op.
func (p *Proxy) Revoke(runKey string) {
	p.reg.mu.Lock()
	delete(p.reg.runs, runKey)
	p.reg.mu.Unlock()
}

// lookup scans every entry so timing does not reveal which token prefix matched.
func (r *registry) lookup(token string) *entry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var found *entry

	for _, e := range r.runs {
		if subtle.ConstantTimeCompare(e.token, []byte(token)) == 1 {
			found = e
		}
	}

	return found
}
