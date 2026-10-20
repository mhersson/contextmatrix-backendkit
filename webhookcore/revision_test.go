package webhookcore

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

type ctxKey struct{}

type fakeLabeler struct {
	labels map[string]string
	err    error
	gotRef string
	gotCtx context.Context //nolint:containedctx // records what the hook passed through
}

func (f *fakeLabeler) ImageLabels(ctx context.Context, ref string) (map[string]string, error) {
	f.gotRef, f.gotCtx = ref, ctx

	return f.labels, f.err
}

func TestImageRevisionHook(t *testing.T) {
	const (
		ref = "contextmatrix-agent-worker:latest"
		sha = "fedcba9876543210fedcba9876543210fedcba98"
	)

	tests := []struct {
		name   string
		labels map[string]string
		err    error
		want   string
	}{
		{"label present", map[string]string{ImageRevisionLabel: sha}, nil, sha},
		{"label padded", map[string]string{ImageRevisionLabel: "  " + sha + "\n"}, nil, sha},
		{"label missing", map[string]string{"org.opencontainers.image.title": "w"}, nil, ""},
		{"nil labels", nil, nil, ""},
		{"inspect error", map[string]string{ImageRevisionLabel: sha}, errors.New("No such image"), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeLabeler{labels: tt.labels, err: tt.err}

			hook := ImageRevisionHook(f, ref)

			assert.Equal(t, tt.want, hook(t.Context()))
			assert.Equal(t, ref, f.gotRef)
		})
	}
}

func TestImageRevisionHook_NilWhenUnwired(t *testing.T) {
	assert.Nil(t, ImageRevisionHook(nil, "contextmatrix-agent-worker:latest"))
	assert.Nil(t, ImageRevisionHook(&fakeLabeler{}, ""))
}

// HandleHealth's deadline only bounds the Docker call if the hook passes its
// context through.
func TestImageRevisionHook_PassesContext(t *testing.T) {
	f := &fakeLabeler{}
	ctx := context.WithValue(t.Context(), ctxKey{}, "v")

	ImageRevisionHook(f, "contextmatrix-agent-worker:latest")(ctx)

	assert.Equal(t, "v", f.gotCtx.Value(ctxKey{}))
}
