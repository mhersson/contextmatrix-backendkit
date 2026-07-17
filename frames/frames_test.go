package frames_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/mhersson/contextmatrix-backendkit/frames"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	var sb strings.Builder

	require.NoError(t, frames.Write(&sb, frames.Frame{Type: frames.TypeUserMessage, Content: "hi there", MessageID: "m1"}))
	require.NoError(t, frames.Write(&sb, frames.Frame{Type: frames.TypePromote}))
	require.NoError(t, frames.Write(&sb, frames.Frame{Type: frames.TypeEndSession}))

	r := frames.NewReader(strings.NewReader(sb.String()), frames.TypeUserMessage, frames.TypePromote, frames.TypeEndSession)

	f1, err := r.Next()
	require.NoError(t, err)
	assert.Equal(t, frames.Frame{Type: frames.TypeUserMessage, Content: "hi there", MessageID: "m1"}, f1)

	f2, err := r.Next()
	require.NoError(t, err)
	assert.Equal(t, frames.TypePromote, f2.Type)

	f3, err := r.Next()
	require.NoError(t, err)
	assert.Equal(t, frames.TypeEndSession, f3.Type)

	_, err = r.Next()
	require.ErrorIs(t, err, io.EOF)
}

func TestEncodeDecodeRoundTripChatAcceptedSet(t *testing.T) {
	var sb strings.Builder

	require.NoError(t, frames.Write(&sb, frames.Frame{Type: frames.TypeUserMessage, Content: "hi there", MessageID: "m1"}))
	require.NoError(t, frames.Write(&sb, frames.Frame{Type: frames.TypeClear}))

	r := frames.NewReader(strings.NewReader(sb.String()), frames.TypeUserMessage, frames.TypeClear)

	f1, err := r.Next()
	require.NoError(t, err)
	assert.Equal(t, frames.Frame{Type: frames.TypeUserMessage, Content: "hi there", MessageID: "m1"}, f1)

	f2, err := r.Next()
	require.NoError(t, err)
	assert.Equal(t, frames.TypeClear, f2.Type)

	_, err = r.Next()
	require.ErrorIs(t, err, io.EOF)
}

func TestReaderSkipsGarbageAndUnknownTypes(t *testing.T) {
	in := "not json\n{\"type\":\"future_thing\"}\n{\"type\":\"user_message\",\"content\":\"ok\"}\n"
	r := frames.NewReader(strings.NewReader(in), frames.TypeUserMessage, frames.TypePromote, frames.TypeEndSession)

	f, err := r.Next()
	require.NoError(t, err)
	assert.Equal(t, "ok", f.Content) // garbage + unknown types skipped, not fatal
}

func TestReaderOversizedLineIsFatal(t *testing.T) {
	in := strings.Repeat("a", frames.MaxLine+1) + "\n"
	r := frames.NewReader(strings.NewReader(in), frames.TypeUserMessage, frames.TypePromote, frames.TypeEndSession)

	_, err := r.Next()
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
}

func TestWriteRejectsOversizedFrame(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	err := frames.Write(&buf, frames.Frame{Type: frames.TypeUserMessage, Content: strings.Repeat("x", frames.MaxLine)})

	require.ErrorIs(t, err, frames.ErrFrameTooLarge)
	assert.Zero(t, buf.Len(), "nothing must be written on an oversized frame")
}

func TestReaderRejectsTypesOutsideAcceptedSet(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, frames.Write(&buf, frames.Frame{Type: frames.TypeClear}))
	require.NoError(t, frames.Write(&buf, frames.Frame{Type: frames.TypePromote}))

	r := frames.NewReader(&buf, frames.TypeUserMessage, frames.TypePromote, frames.TypeEndSession)
	f, err := r.Next()
	require.NoError(t, err)
	assert.Equal(t, frames.TypePromote, f.Type, "clear must be skipped by the agent set")

	_, err = r.Next()
	assert.ErrorIs(t, err, io.EOF)
}
