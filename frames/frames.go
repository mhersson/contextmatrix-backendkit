// Package frames defines the JSON Lines control protocol written to a worker
// container's stdin by the host service and read by the work command. It is
// shared by contextmatrix-agent and contextmatrix-chat - both ends of each
// stream are our code, so it is NOT part of contextmatrix-protocol. Unknown
// frame types are skipped for forward compatibility; each backend passes its
// own accepted type set to NewReader so the two hosts keep their exact,
// independent rejection behavior.
package frames

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	TypeUserMessage = "user_message"
	TypePromote     = "promote"
	TypeEndSession  = "end_session"
	TypeClear       = "clear"

	// MaxLine bounds one encoded frame line; /message content is capped at
	// 8 KiB by ContextMatrix, so 64 KiB is generous headroom. The reader's
	// scanner rejects longer lines fatally, so Write refuses to emit them.
	MaxLine = 64 * 1024
)

// ErrFrameTooLarge is returned by Write when the encoded line would exceed
// MaxLine. Nothing is written in that case, so a single oversized frame
// cannot tear down the session.
var ErrFrameTooLarge = errors.New("frame exceeds maximum line size")

type Frame struct {
	Type      string `json:"type"`
	Content   string `json:"content,omitempty"`
	MessageID string `json:"message_id,omitempty"`
}

// Write encodes one frame as a single JSON line. The host service is the
// sole writer per container stdin, so atomicity beyond a single Write call
// is not required.
func Write(w io.Writer, f Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("encode frame: %w", err)
	}

	if len(b)+1 > MaxLine {
		return fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, len(b)+1)
	}

	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}

	return nil
}

type Reader struct {
	sc       *bufio.Scanner
	accepted map[string]struct{}
}

// NewReader creates a Reader that yields only frames whose Type is in
// accepted; everything else is skipped like an unknown type.
func NewReader(r io.Reader, accepted ...string) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), MaxLine)

	set := make(map[string]struct{}, len(accepted))
	for _, t := range accepted {
		set[t] = struct{}{}
	}

	return &Reader{sc: sc, accepted: set}
}

// Next returns the next accepted frame, skipping malformed lines and types
// outside the accepted set. io.EOF when the stream ends. A line exceeding
// MaxLine fails the scanner and returns a non-EOF error - a hard stop,
// unlike shorter malformed lines which are skipped.
func (r *Reader) Next() (Frame, error) {
	for r.sc.Scan() {
		var f Frame
		if err := json.Unmarshal(r.sc.Bytes(), &f); err != nil {
			continue
		}

		if _, ok := r.accepted[f.Type]; ok {
			return f, nil
		}

		continue
	}

	if err := r.sc.Err(); err != nil {
		return Frame{}, fmt.Errorf("read frame: %w", err)
	}

	return Frame{}, io.EOF
}
