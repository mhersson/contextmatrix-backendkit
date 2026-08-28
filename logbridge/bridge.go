// Package logbridge converts worker event JSONL into protocol.LogEntry frames
// and fans them out to /logs SSE subscribers. The hub filters by an injected
// key extractor (project for the agent, session for chat) and the bridge stamps
// an explicit Key onto every entry; awaiting-human handling and extra event
// kinds are supplied by the consuming backend as configuration.
package logbridge

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/mhersson/contextmatrix-harness/redact"
	protocol "github.com/mhersson/contextmatrix-protocol"
)

// Key identifies the origin of a log line. Each non-empty field is stamped onto
// the published entry; zero fields stay empty.
type Key struct {
	Project   string
	CardID    string
	SessionID string
}

func (k Key) stamp(e *protocol.LogEntry) {
	if k.Project != "" {
		e.Project = k.Project
	}

	if k.CardID != "" {
		e.CardID = k.CardID
	}

	if k.SessionID != "" {
		e.SessionID = k.SessionID
	}
}

// Bridge maps one worker output line to zero or one published LogEntry.
type Bridge struct {
	hub                  *Hub
	redactor             atomic.Pointer[redact.Redactor]
	onAwaiting           func(key Key, awaiting bool)
	surfaceAwaitingHuman bool
	surfaceRunState      bool
	mapExtra             func(kind string, data map[string]any) (entry protocol.LogEntry, awaiting, ok bool)
}

// BridgeConfig configures a Bridge. Redactor may be nil (no redaction) and is
// swappable via SetRedactor. OnAwaiting fires after every non-stderr publish; a
// nil OnAwaiting never fires. SurfaceAwaitingHuman decides whether an
// awaiting_human state change becomes a system entry that arms the flag (agent)
// or is skipped entirely (chat). MapExtra classifies event kinds the shared
// switch does not handle and is tried before the default skip. SurfaceRunState
// maps run-state events onto ephemeral "status" frames instead of dropping them:
// user_input and model_request become {Type: "status", Content: "working"} and
// the awaiting_human state change becomes {Type: "status", Content: "idle"}.
// Off by default; the chat backend enables it so CM can drive a working
// indicator. On awaiting_human, SurfaceAwaitingHuman takes precedence when both
// are set.
type BridgeConfig struct {
	Hub                  *Hub
	Redactor             *redact.Redactor
	OnAwaiting           func(key Key, awaiting bool)
	SurfaceAwaitingHuman bool
	SurfaceRunState      bool
	MapExtra             func(kind string, data map[string]any) (entry protocol.LogEntry, awaiting, ok bool)
}

// NewBridge creates a Bridge from cfg.
func NewBridge(cfg BridgeConfig) *Bridge {
	b := &Bridge{
		hub:                  cfg.Hub,
		onAwaiting:           cfg.OnAwaiting,
		surfaceAwaitingHuman: cfg.SurfaceAwaitingHuman,
		surfaceRunState:      cfg.SurfaceRunState,
		mapExtra:             cfg.MapExtra,
	}
	b.redactor.Store(cfg.Redactor)

	return b
}

// SetRedactor atomically swaps the redactor used for all lines bridged after
// the call. Safe for concurrent use with BridgeLine - the RedactorRegistry
// calls it on every session-secret add/remove so the masked set tracks the
// live sessions without a restart.
func (b *Bridge) SetRedactor(r *redact.Redactor) {
	b.redactor.Store(r)
}

// BridgeLine maps one worker output line (stdout JSONL event or raw stderr)
// to zero or one published LogEntry, stamped from key and time.Now().
func (b *Bridge) BridgeLine(key Key, line []byte, isStderr bool) {
	if isStderr {
		entry := protocol.LogEntry{
			Timestamp: time.Now(),
			Type:      "stderr",
			Content:   b.redactor.Load().Apply(string(line)),
		}
		key.stamp(&entry)
		b.publish(entry, false)

		return
	}

	var ev struct {
		Kind string         `json:"kind"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		// Unparsable (e.g. panic stack trace) - surface as stderr.
		entry := protocol.LogEntry{
			Timestamp: time.Now(),
			Type:      "stderr",
			Content:   b.redactor.Load().Apply(string(line)),
		}
		key.stamp(&entry)
		b.publish(entry, false)

		return
	}

	entry, awaiting, skip := b.mapEvent(ev.Kind, ev.Data)
	if skip {
		return
	}

	entry.Timestamp = time.Now()
	key.stamp(&entry)
	entry.Content = b.redactor.Load().Apply(entry.Content)

	b.publish(entry, awaiting)
}

// mapEvent converts a parsed event kind+data into a LogEntry.
// Returns skip=true for kinds that are deliberately not bridged.
// awaiting is only meaningful when skip=false.
func (b *Bridge) mapEvent(kind string, data map[string]any) (entry protocol.LogEntry, awaiting, skip bool) {
	switch kind {
	case "model_response":
		content := strField(data, "content")
		if strings.TrimSpace(content) == "" {
			// Pure tool-call turn - no text to show; skip the empty frame.
			return protocol.LogEntry{}, false, true
		}

		return protocol.LogEntry{
			Type:    "text",
			Content: content,
			Model:   strField(data, "model"),
		}, false, false

	case "thinking":
		// Model reasoning, emitted once per turn when the model produced it.
		// Content is redacted centrally by BridgeLine, so don't redact here.
		// It is agent progress, not an awaiting state: awaiting=false, skip=false.
		return protocol.LogEntry{
			Type:    "thinking",
			Content: strField(data, "content"),
		}, false, false

	case "tool_call":
		id := strField(data, "id")
		name := strField(data, "name")
		args := strField(data, "raw_args")

		content := name + "(" + args + ")"
		if dispatched, ok := data["dispatched"].(bool); ok && !dispatched {
			// Interrupt or post-terminal skip: the harness paired this with a
			// tool_result but never actually ran the call. Say so - a bare
			// name() would read as executed.
			content += " [not run - the turn ended first]"
		}

		content = truncate(content, 200)

		return protocol.LogEntry{
			Type:      "tool_call",
			Content:   content,
			ToolUseID: id,
		}, false, false

	case "usage":
		// The four counts are disjoint: prompt_tokens excludes the cached portion,
		// which arrives in its own buckets. Dropping the cache fields makes a
		// consumer that sums them - CM's chat context gauge and its cost line -
		// read a fraction of the true prompt on any cached turn.
		inputTokens := int64Field(data, "prompt_tokens")
		outputTokens := int64Field(data, "completion_tokens")
		cacheReadTokens := int64Field(data, "cache_read_tokens")
		cacheCreateTokens := int64Field(data, "cache_creation_tokens")

		return protocol.LogEntry{
			Type:  "usage",
			Model: strField(data, "model"),
			Usage: &protocol.LogTokenUsage{
				InputTokens:       inputTokens,
				OutputTokens:      outputTokens,
				CacheReadTokens:   cacheReadTokens,
				CacheCreateTokens: cacheCreateTokens,
			},
		}, false, false

	case "state_change":
		if strField(data, "state") == "awaiting_human" {
			if b.surfaceAwaitingHuman {
				return protocol.LogEntry{
					Type:    "system",
					Content: "awaiting human input",
				}, true, false
			}

			if b.surfaceRunState {
				return protocol.LogEntry{Type: "status", Content: "idle"}, false, false
			}

			// Normal idle between chat turns - not a transcript entry.
			return protocol.LogEntry{}, false, true
		}

		return protocol.LogEntry{
			Type:    "system",
			Content: summarizeData(data),
		}, false, false

	case "context_limit":
		return protocol.LogEntry{
			Type:    "system",
			Content: summarizeData(data),
		}, false, false

	case "error":
		content := strField(data, "error")
		if defect := strField(data, "suspected_upstream_defect"); defect != "" {
			content += " (suspected upstream defect: " + defect + ")"
		}

		return protocol.LogEntry{
			Type:    "stderr",
			Content: content,
		}, false, false

	case "model_request", "user_input":
		if b.surfaceRunState {
			return protocol.LogEntry{Type: "status", Content: "working"}, false, false
		}

		return protocol.LogEntry{}, false, true

	// Transcript-only kinds - not bridged.
	case "tool_result", "tool_repair", "verification":
		return protocol.LogEntry{}, false, true

	default:
		if b.mapExtra != nil {
			if e, aw, ok := b.mapExtra(kind, data); ok {
				return e, aw, false
			}
		}

		// Unknown future kinds: skip silently.
		return protocol.LogEntry{}, false, true
	}
}

// publish delivers e to the hub and fires the awaiting hook.
//
// stderr-typed entries (raw stderr, unparsable lines, and the error-kind
// mapping) never touch the awaiting flag: a parked HITL worker still logs to
// stderr (e.g. a heartbeat warning from its slog), and clearing awaiting on
// such a line would let the idle watchdog reap a container that is legitimately
// waiting for a human. Only real agent-progress entries (text, tool_call,
// usage, non-awaiting system) clear it; the awaiting_human system entry sets it.
func (b *Bridge) publish(e protocol.LogEntry, awaiting bool) {
	b.hub.Publish(e)

	if b.onAwaiting != nil && e.Type != "stderr" {
		b.onAwaiting(Key{Project: e.Project, CardID: e.CardID, SessionID: e.SessionID}, awaiting)
	}
}

// strField extracts a string value from data, returning "" if absent or
// not a string.
func strField(data map[string]any, key string) string {
	if data == nil {
		return ""
	}

	v, ok := data[key]
	if !ok {
		return ""
	}

	s, _ := v.(string)

	return s
}

// int64Field extracts a numeric value (JSON numbers unmarshal as float64)
// from data, returning 0 if absent or not numeric.
func int64Field(data map[string]any, key string) int64 {
	if data == nil {
		return 0
	}

	v, ok := data[key]
	if !ok {
		return 0
	}

	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}

	return 0
}

// summarizeData produces a brief human-readable string from event data for
// system-type frames where no dedicated field carries the message.
func summarizeData(data map[string]any) string {
	if len(data) == 0 {
		return ""
	}

	b, err := json.Marshal(data)
	if err != nil {
		return ""
	}

	return truncate(string(b), 200)
}

// truncate cuts s to at most limit bytes without splitting a multi-byte rune:
// the cut point backs off past any continuation bytes so the result is
// always valid UTF-8 (assuming s is).
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return s[:cut]
}
