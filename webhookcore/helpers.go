package webhookcore

import (
	"encoding/json"
	"net/http"
	"strings"

	protocol "github.com/mhersson/contextmatrix-protocol"
)

// MaxRequestBodyBytes caps the body the auth middleware reads before HMAC
// verification. ContextMatrix caps /message content well under this; a larger
// body is a misbehaving or hostile client.
const MaxRequestBodyBytes = 1 << 20 // 1 MiB

// writeUnauthorized returns the single fixed 401 shape for every authentication
// failure. The specific cause is logged in Auth, never echoed to the client.
func writeUnauthorized(w http.ResponseWriter) {
	WriteError(w, http.StatusUnauthorized, protocol.CodeUnauthorized, "unauthorized")
}

// Decode unmarshals the (already auth-verified) request body into v. The body
// was re-injected by the auth middleware, so a normal read suffices. On a JSON
// error it writes a 400 and returns false.
func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		WriteError(w, http.StatusBadRequest, protocol.CodeInvalidJSON, "invalid JSON")

		return false
	}

	return true
}

// WriteJSON marshals v and writes it with the given status. A marshal failure
// falls back to a fixed internal-error body so the client always gets
// well-formed JSON.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")

	body, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"code":"internal","message":"response marshal failed"}`))

		return
	}

	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// WriteError serialises a protocol.ErrorResponse. msg must be a fixed,
// client-safe string, never raw err.Error() text.
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	WriteJSON(w, status, protocol.ErrorResponse{OK: false, Code: code, Message: msg})
}

// matchingTags returns the tags containing any of the filter substrings.
func matchingTags(tags, filters []string) []string {
	out := make([]string, 0, len(tags))

	for _, tag := range tags {
		for _, f := range filters {
			if strings.Contains(tag, f) {
				out = append(out, tag)

				break
			}
		}
	}

	return out
}
