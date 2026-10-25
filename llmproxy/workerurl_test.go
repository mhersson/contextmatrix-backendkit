package llmproxy_test

import (
	"testing"

	"github.com/mhersson/contextmatrix-backendkit/llmproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateContainerURL(t *testing.T) {
	t.Parallel()

	valid := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"http", "http://serve.internal", "http://serve.internal"},
		{"https", "https://serve.internal", "https://serve.internal"},
		{"ipv6 literal", "http://[fd00::1]:19092", "http://[fd00::1]:19092"},
		{"trailing slash", "http://serve.internal:19092/", "http://serve.internal:19092"},
		{"hostname with port", "http://serve.internal:19092", "http://serve.internal:19092"},
	}
	for _, tt := range valid {
		t.Run("valid/"+tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := llmproxy.ValidateContainerURL(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	invalid := []struct{ name, in string }{
		{"ftp scheme", "ftp://serve.internal"},
		{"missing host", "http://"},
		{"path", "http://serve.internal/api"},
		{"query", "http://serve.internal?a=b"},
		{"fragment", "http://serve.internal#frag"},
		{"bare query", "http://serve.internal?"},
		{"bare fragment", "http://serve.internal#"},
		{"unspecified v4", "http://0.0.0.0:19092"},
		{"unspecified v6", "http://[::]:19092"},
		{"localhost", "http://localhost:19092"},
		{"LOCALHOST", "http://LOCALHOST:19092"},
		{"loopback range", "http://127.0.0.2:19092"},
		{"loopback v6", "http://[::1]:19092"},
		{"leading whitespace", " http://serve.internal"},
		{"trailing whitespace", "http://serve.internal "},
	}
	for _, tt := range invalid {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := llmproxy.ValidateContainerURL(tt.in)
			assert.Error(t, err)
		})
	}
}

func TestWorkerBaseURL(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "http://host.docker.internal:19092/llm",
		llmproxy.WorkerBaseURL("", 19092, llmproxy.DefaultPrefix), "empty falls back to the host gateway")
	assert.Equal(t, "https://serve.internal:8443/llm",
		llmproxy.WorkerBaseURL("https://serve.internal:8443", 19092, llmproxy.DefaultPrefix))
	assert.Equal(t, "https://serve.internal:8443/llm",
		llmproxy.WorkerBaseURL("https://serve.internal:8443/", 19092, llmproxy.DefaultPrefix))
}
