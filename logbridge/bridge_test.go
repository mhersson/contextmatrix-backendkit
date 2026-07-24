package logbridge_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mhersson/contextmatrix-backendkit/logbridge"
	"github.com/mhersson/contextmatrix-harness/redact"
	protocol "github.com/mhersson/contextmatrix-protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testProject = "proj"
	testCard    = "PROJ-001"
	testSession = "sess-abc123"
)

// makeEvent encodes one worker stdout JSONL line for a given kind and data.
func makeEvent(kind string, data map[string]any) []byte {
	ev := map[string]any{
		"seq":  1,
		"kind": kind,
		"time": time.Now().Format(time.RFC3339),
	}
	if data != nil {
		ev["data"] = data
	}

	b, _ := json.Marshal(ev)

	return b
}

// discussionMapExtra is the agent's mob-session "discussion" arm, supplied as a
// MapExtra hook. Non-discussion kinds return ok=false so they fall through to
// the shared default skip - the seat_debug kind included, keeping it off the
// live stream by construction.
func discussionMapExtra(kind string, data map[string]any) (protocol.LogEntry, bool, bool) {
	if kind != "discussion" {
		return protocol.LogEntry{}, false, false
	}

	str := func(k string) string {
		s, _ := data[k].(string)

		return s
	}

	return protocol.LogEntry{
		Type:    "text",
		Content: str("content"),
		Agent:   str("agent"),
		Model:   str("model"),
	}, false, true
}

type mapRow struct {
	name       string
	line       []byte
	wantType   string // empty = expect skip
	wantModel  string
	wantToolID string
	wantUsage  *protocol.LogTokenUsage
	// awaiting expected value (-1 = not fired, 0 = false, 1 = true); agent
	// mode only.
	wantAwaiting int
	checkContent func(t *testing.T, content string)
}

// sharedRows are the classification-table rows whose event payloads and
// LogEntry expectations are identical for both backends. The diverging
// awaiting_human row is added per mode.
func sharedRows() []mapRow {
	return []mapRow{
		{
			name: "model_response → text with content and model",
			line: makeEvent("model_response", map[string]any{
				"content": "Hello, world!",
				"model":   "test-model",
			}),
			wantType:     "text",
			wantModel:    "test-model",
			wantAwaiting: 0,
			checkContent: func(t *testing.T, content string) {
				t.Helper()
				assert.Equal(t, "Hello, world!", content)
			},
		},
		{
			name: "thinking → thinking with content",
			line: makeEvent("thinking", map[string]any{
				"content": "Let me reason about this.",
				"turn":    float64(2),
			}),
			wantType:     "thinking",
			wantAwaiting: 0,
			checkContent: func(t *testing.T, content string) {
				t.Helper()
				assert.Equal(t, "Let me reason about this.", content)
			},
		},
		{
			name: "tool_call → tool_call with id and formatted content",
			line: makeEvent("tool_call", map[string]any{
				"id":       "call_abc123",
				"name":     "bash",
				"raw_args": `{"cmd":"ls"}`,
			}),
			wantType:     "tool_call",
			wantToolID:   "call_abc123",
			wantAwaiting: 0,
			checkContent: func(t *testing.T, content string) {
				t.Helper()
				assert.Equal(t, `bash({"cmd":"ls"})`, content)
			},
		},
		{
			name: "tool_call truncated at 200 chars",
			line: func() []byte {
				bigArgs := `{"x":"` + strings.Repeat("a", 300) + `"}`

				return makeEvent("tool_call", map[string]any{
					"id":       "call_trunc",
					"name":     "bash",
					"raw_args": bigArgs,
				})
			}(),
			wantType:     "tool_call",
			wantToolID:   "call_trunc",
			wantAwaiting: 0,
			checkContent: func(t *testing.T, content string) {
				t.Helper()
				assert.LessOrEqual(t, len(content), 200)
			},
		},
		{
			name: "tool_call truncation is rune-safe on multi-byte content",
			line: func() []byte {
				// "é" is 2 bytes; the prefix `bash({"x":"` is 11 bytes, so the
				// 200-byte cut lands mid-rune unless truncation backs off.
				bigArgs := `{"x":"` + strings.Repeat("é", 300) + `"}`

				return makeEvent("tool_call", map[string]any{
					"id":       "call_mb",
					"name":     "bash",
					"raw_args": bigArgs,
				})
			}(),
			wantType:     "tool_call",
			wantToolID:   "call_mb",
			wantAwaiting: 0,
			checkContent: func(t *testing.T, content string) {
				t.Helper()
				assert.LessOrEqual(t, len(content), 200)
				assert.True(t, utf8.ValidString(content), "truncated content must be valid UTF-8")
			},
		},
		{
			name: "state_change summary truncation is rune-safe on multi-byte content",
			line: makeEvent("state_change", map[string]any{
				// "世" is 3 bytes; the JSON prefix `{"warning":"` is 12 bytes,
				// so the 200-byte cut lands mid-rune.
				"warning": strings.Repeat("世", 150),
			}),
			wantType:     "system",
			wantAwaiting: 0,
			checkContent: func(t *testing.T, content string) {
				t.Helper()
				assert.LessOrEqual(t, len(content), 200)
				assert.True(t, utf8.ValidString(content), "summarized content must be valid UTF-8")
			},
		},
		{
			name: "usage → usage with token counts and model",
			line: makeEvent("usage", map[string]any{
				"prompt_tokens":     float64(100),
				"completion_tokens": float64(50),
				"model":             "usage-model",
			}),
			wantType:     "usage",
			wantModel:    "usage-model",
			wantAwaiting: 0,
			wantUsage: &protocol.LogTokenUsage{
				InputTokens:  100,
				OutputTokens: 50,
			},
		},
		{
			name: "state_change other → system + awaiting=false",
			line: makeEvent("state_change", map[string]any{
				"stop":  "done",
				"turns": float64(5),
			}),
			wantType:     "system",
			wantAwaiting: 0,
			checkContent: func(t *testing.T, content string) {
				t.Helper()
				assert.NotEmpty(t, content)
			},
		},
		{
			name: "context_limit → system",
			line: makeEvent("context_limit", map[string]any{
				"prompt_tokens":  float64(80000),
				"context_window": float64(100000),
				"ratio":          float64(0.8),
				"threshold":      float64(0.85),
			}),
			wantType:     "system",
			wantAwaiting: 0,
		},
		{
			name: "error → stderr (awaiting flag untouched)",
			line: makeEvent("error", map[string]any{
				"error": "something went wrong",
			}),
			wantType: "stderr",
			// stderr-typed entries must NOT clear awaiting-human: a parked HITL
			// worker still logs errors while waiting for a human.
			wantAwaiting: -1,
			checkContent: func(t *testing.T, content string) {
				t.Helper()
				assert.Contains(t, content, "something went wrong")
			},
		},
		{
			name:         "model_request → skipped",
			line:         makeEvent("model_request", map[string]any{"turn": float64(1)}),
			wantType:     "",
			wantAwaiting: -1,
		},
		{
			name:         "tool_result → skipped",
			line:         makeEvent("tool_result", map[string]any{"id": "call_x"}),
			wantType:     "",
			wantAwaiting: -1,
		},
		{
			name:         "tool_repair → skipped",
			line:         makeEvent("tool_repair", map[string]any{"id": "call_x"}),
			wantType:     "",
			wantAwaiting: -1,
		},
		{
			name:         "user_input → skipped",
			line:         makeEvent("user_input", map[string]any{"message_id": "m1"}),
			wantType:     "",
			wantAwaiting: -1,
		},
		{
			name:         "verification → skipped",
			line:         makeEvent("verification", map[string]any{}),
			wantType:     "",
			wantAwaiting: -1,
		},
		{
			name:         "unknown kind → skipped",
			line:         makeEvent("future_kind", map[string]any{"x": "y"}),
			wantType:     "",
			wantAwaiting: -1,
		},
		{
			name:     "unparsable line → stderr passthrough (awaiting flag untouched)",
			line:     []byte("goroutine 1 [running]: panic: something bad happened"),
			wantType: "stderr",
			// stderr-typed: awaiting must not be cleared.
			wantAwaiting: -1,
			checkContent: func(t *testing.T, content string) {
				t.Helper()
				assert.Equal(t, "goroutine 1 [running]: panic: something bad happened", content)
			},
		},
	}
}

// assertCommon checks the mode-independent expectations of a mapped entry.
func assertCommon(t *testing.T, tt mapRow, got protocol.LogEntry) {
	t.Helper()

	assert.Equal(t, tt.wantType, got.Type, "Type mismatch")
	assert.False(t, got.Timestamp.IsZero(), "Timestamp must be set")

	if tt.wantModel != "" {
		assert.Equal(t, tt.wantModel, got.Model)
	}

	if tt.wantToolID != "" {
		assert.Equal(t, tt.wantToolID, got.ToolUseID)
	}

	if tt.wantUsage != nil {
		require.NotNil(t, got.Usage)
		assert.Equal(t, tt.wantUsage.InputTokens, got.Usage.InputTokens)
		assert.Equal(t, tt.wantUsage.OutputTokens, got.Usage.OutputTokens)
	}

	if tt.checkContent != nil {
		tt.checkContent(t, got.Content)
	}
}

// TestMappingTable covers every row of the kind→LogEntry spec in both key
// modes: agent (project-keyed, awaiting surfaced) and chat (session-keyed,
// awaiting suppressed).
func TestMappingTable(t *testing.T) {
	t.Parallel()

	t.Run("agent mode", func(t *testing.T) {
		t.Parallel()

		rows := append(sharedRows(), mapRow{
			name: "state_change awaiting_human → system + awaiting=true",
			line: makeEvent("state_change", map[string]any{
				"state": "awaiting_human",
				"turns": float64(3),
			}),
			wantType:     "system",
			wantAwaiting: 1,
			checkContent: func(t *testing.T, content string) {
				t.Helper()
				assert.Equal(t, "awaiting human input", content)
			},
		})

		for _, tt := range rows {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				hub := logbridge.NewHub(func(e protocol.LogEntry) string { return e.Project }, nil)
				_, ch := hub.Subscribe("")

				awaitingCalled := false
				awaitingVal := false
				bridge := logbridge.NewBridge(logbridge.BridgeConfig{
					Hub:                  hub,
					OnAwaiting:           func(_ logbridge.Key, awaiting bool) { awaitingCalled = true; awaitingVal = awaiting },
					SurfaceAwaitingHuman: true,
					MapExtra:             discussionMapExtra,
				})

				bridge.BridgeLine(logbridge.Key{Project: testProject, CardID: testCard}, tt.line, false)

				if tt.wantType == "" {
					select {
					case e := <-ch:
						t.Errorf("expected skip but got entry with type=%q", e.Type)
					case <-time.After(30 * time.Millisecond):
					}

					if tt.wantAwaiting == -1 {
						assert.False(t, awaitingCalled, "awaiting hook must not fire for skipped lines")
					}

					return
				}

				var got protocol.LogEntry
				select {
				case got = <-ch:
				case <-time.After(100 * time.Millisecond):
					t.Fatal("expected entry but got none (timeout)")
				}

				assertCommon(t, tt, got)
				assert.Equal(t, testProject, got.Project, "Project mismatch")
				assert.Equal(t, testCard, got.CardID, "CardID mismatch")

				switch tt.wantAwaiting {
				case 1:
					assert.True(t, awaitingCalled, "awaiting hook must fire")
					assert.True(t, awaitingVal, "awaiting must be true")
				case 0:
					assert.True(t, awaitingCalled, "awaiting hook must fire with false for bridged lines")
					assert.False(t, awaitingVal, "awaiting must be false")
				case -1:
					assert.False(t, awaitingCalled,
						"awaiting hook must NOT fire for stderr-typed entries (keeps a parked HITL worker alive)")
				}
			})
		}
	})

	t.Run("chat mode", func(t *testing.T) {
		t.Parallel()

		rows := append(sharedRows(), mapRow{
			name: "state_change awaiting_human → skipped (normal idle between chat turns)",
			line: makeEvent("state_change", map[string]any{
				"state": "awaiting_human",
				"turns": float64(3),
			}),
			wantType: "", // skipped
		})

		for _, tt := range rows {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				hub := logbridge.NewHub(func(e protocol.LogEntry) string { return e.SessionID }, nil)
				_, ch := hub.Subscribe("")
				bridge := logbridge.NewBridge(logbridge.BridgeConfig{Hub: hub})

				bridge.BridgeLine(logbridge.Key{SessionID: testSession}, tt.line, false)

				if tt.wantType == "" {
					select {
					case e := <-ch:
						t.Errorf("expected skip but got entry with type=%q", e.Type)
					case <-time.After(30 * time.Millisecond):
					}

					return
				}

				var got protocol.LogEntry
				select {
				case got = <-ch:
				case <-time.After(100 * time.Millisecond):
					t.Fatal("expected entry but got none (timeout)")
				}

				assertCommon(t, tt, got)
				assert.Equal(t, testSession, got.SessionID, "SessionID mismatch")
			})
		}
	})
}

// TestRunStateMapping pins the SurfaceRunState contract: run-state events that
// are dropped by default become ephemeral "status" frames when the flag is set,
// so a chat-mode consumer can drive a working indicator. The default-off rows
// are already pinned by TestMappingTable's chat mode.
func TestRunStateMapping(t *testing.T) {
	t.Parallel()

	rows := []struct {
		name        string
		line        []byte
		wantContent string
	}{
		{
			name:        "user_input → status working",
			line:        makeEvent("user_input", map[string]any{"message_id": "m1"}),
			wantContent: "working",
		},
		{
			name:        "model_request → status working",
			line:        makeEvent("model_request", map[string]any{"turn": float64(1)}),
			wantContent: "working",
		},
		{
			name: "awaiting_human → status idle",
			line: makeEvent("state_change", map[string]any{
				"state": "awaiting_human",
				"turns": float64(3),
			}),
			wantContent: "idle",
		},
	}

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hub := logbridge.NewHub(func(e protocol.LogEntry) string { return e.SessionID }, nil)
			_, ch := hub.Subscribe("")
			bridge := logbridge.NewBridge(logbridge.BridgeConfig{Hub: hub, SurfaceRunState: true})

			bridge.BridgeLine(logbridge.Key{SessionID: testSession}, tt.line, false)

			var got protocol.LogEntry
			select {
			case got = <-ch:
			case <-time.After(100 * time.Millisecond):
				t.Fatal("expected a status frame but got none (timeout)")
			}

			assert.Equal(t, "status", got.Type)
			assert.Equal(t, tt.wantContent, got.Content)
			assert.Equal(t, testSession, got.SessionID)
			assert.False(t, got.Timestamp.IsZero())
		})
	}
}

// TestRunStateDoesNotOverrideAwaitingHuman pins the precedence rule: when both
// SurfaceAwaitingHuman and SurfaceRunState are set (not a real deployment
// config), the agent's awaiting_human system entry wins.
func TestRunStateDoesNotOverrideAwaitingHuman(t *testing.T) {
	t.Parallel()

	hub := logbridge.NewHub(func(e protocol.LogEntry) string { return e.Project }, nil)
	_, ch := hub.Subscribe("")
	bridge := logbridge.NewBridge(logbridge.BridgeConfig{
		Hub:                  hub,
		SurfaceAwaitingHuman: true,
		SurfaceRunState:      true,
	})

	bridge.BridgeLine(logbridge.Key{Project: testProject},
		makeEvent("state_change", map[string]any{"state": "awaiting_human"}), false)

	var got protocol.LogEntry
	select {
	case got = <-ch:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected an entry (timeout)")
	}

	assert.Equal(t, "system", got.Type)
	assert.Equal(t, "awaiting human input", got.Content)
}

// TestStderrStream verifies that isStderr=true produces a stderr frame with
// the raw line redacted and stamped with the key.
func TestStderrStream(t *testing.T) {
	t.Parallel()

	const secret = "supersecrettoken"

	hub := logbridge.NewHub(func(e protocol.LogEntry) string { return e.Project }, nil)
	_, ch := hub.Subscribe("")
	red := redact.New([]string{secret})
	bridge := logbridge.NewBridge(logbridge.BridgeConfig{Hub: hub, Redactor: red})

	rawLine := []byte("error: auth failed with " + secret)
	bridge.BridgeLine(logbridge.Key{Project: testProject, CardID: testCard}, rawLine, true)

	var got protocol.LogEntry
	select {
	case got = <-ch:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected entry but got none")
	}

	assert.Equal(t, "stderr", got.Type)
	assert.Equal(t, testProject, got.Project)
	assert.Equal(t, testCard, got.CardID)
	assert.NotContains(t, got.Content, secret, "secret must be redacted")
	assert.Contains(t, got.Content, "[REDACTED]")
}

// TestStderrDoesNotClearAwaiting proves that stderr output - raw stderr, the
// error-kind mapping, and unparsable lines - leaves the awaiting-human flag
// untouched, while a real agent-progress entry clears it. A parked HITL worker
// keeps logging to stderr; clearing awaiting on those lines would let the idle
// watchdog reap a container that is legitimately waiting for a human.
func TestStderrDoesNotClearAwaiting(t *testing.T) {
	t.Parallel()

	newBridge := func() (*logbridge.Bridge, *bool, *bool) {
		called := false
		val := false
		hub := logbridge.NewHub(func(e protocol.LogEntry) string { return e.Project }, nil)
		b := logbridge.NewBridge(logbridge.BridgeConfig{
			Hub:                  hub,
			OnAwaiting:           func(_ logbridge.Key, awaiting bool) { called = true; val = awaiting },
			SurfaceAwaitingHuman: true,
		})

		return b, &called, &val
	}

	key := logbridge.Key{Project: testProject, CardID: testCard}

	t.Run("raw stderr line does not fire the awaiting hook", func(t *testing.T) {
		t.Parallel()

		b, called, _ := newBridge()
		b.BridgeLine(key, []byte("worker slog: heartbeat warning"), true)

		assert.False(t, *called, "raw stderr must not clear awaiting")
	})

	t.Run("error-kind event does not fire the awaiting hook", func(t *testing.T) {
		t.Parallel()

		b, called, _ := newBridge()
		b.BridgeLine(key, makeEvent("error", map[string]any{"error": "boom"}), false)

		assert.False(t, *called, "error-kind stderr must not clear awaiting")
	})

	t.Run("text event fires the awaiting hook with false", func(t *testing.T) {
		t.Parallel()

		b, called, val := newBridge()
		b.BridgeLine(key,
			makeEvent("model_response", map[string]any{"content": "progress", "model": "m"}), false)

		assert.True(t, *called, "agent-progress entry must fire the awaiting hook")
		assert.False(t, *val, "agent-progress entry clears awaiting (false)")
	})

	t.Run("tool_call event fires the awaiting hook with false", func(t *testing.T) {
		t.Parallel()

		b, called, val := newBridge()
		b.BridgeLine(key,
			makeEvent("tool_call", map[string]any{"id": "c1", "name": "bash", "raw_args": "{}"}), false)

		assert.True(t, *called, "tool_call entry must fire the awaiting hook")
		assert.False(t, *val, "tool_call entry clears awaiting (false)")
	})
}

// TestRedaction ensures secrets never appear in bridged frames.
func TestRedaction(t *testing.T) {
	t.Parallel()

	const secret = "my-secret-api-key"

	hub := logbridge.NewHub(func(e protocol.LogEntry) string { return e.Project }, nil)
	_, ch := hub.Subscribe("")
	red := redact.New([]string{secret})
	bridge := logbridge.NewBridge(logbridge.BridgeConfig{Hub: hub, Redactor: red})

	key := logbridge.Key{Project: testProject, CardID: testCard}

	t.Run("model_response content redacted", func(t *testing.T) {
		line := makeEvent("model_response", map[string]any{
			"content": "The key is " + secret + " and it works",
			"model":   "m",
		})
		bridge.BridgeLine(key, line, false)

		select {
		case got := <-ch:
			assert.NotContains(t, got.Content, secret)
			assert.Contains(t, got.Content, "[REDACTED]")
		case <-time.After(100 * time.Millisecond):
			t.Fatal("expected entry")
		}
	})

	t.Run("thinking content redacted", func(t *testing.T) {
		line := makeEvent("thinking", map[string]any{
			"content": "internal reasoning mentions " + secret,
		})
		bridge.BridgeLine(key, line, false)

		select {
		case got := <-ch:
			assert.Equal(t, "thinking", got.Type)
			assert.NotContains(t, got.Content, secret)
			assert.Contains(t, got.Content, "[REDACTED]")
		case <-time.After(100 * time.Millisecond):
			t.Fatal("expected entry")
		}
	})

	t.Run("raw stderr redacted", func(t *testing.T) {
		bridge.BridgeLine(key, []byte("fatal: "+secret), true)

		select {
		case got := <-ch:
			assert.NotContains(t, got.Content, secret)
			assert.Contains(t, got.Content, "[REDACTED]")
		case <-time.After(100 * time.Millisecond):
			t.Fatal("expected entry")
		}
	})
}

// TestDiscussionMapping pins the mob-session live-transcript bridging routed
// through MapExtra: kind "discussion" maps to a text entry carrying the speaker
// in Agent and the speaker's LLM slug in Model, while the seat-debug kind is
// never bridged.
func TestDiscussionMapping(t *testing.T) {
	t.Parallel()

	newBridge := func() (*logbridge.Bridge, <-chan protocol.LogEntry) {
		hub := logbridge.NewHub(func(e protocol.LogEntry) string { return e.Project }, nil)
		_, ch := hub.Subscribe("")
		b := logbridge.NewBridge(logbridge.BridgeConfig{
			Hub:                  hub,
			OnAwaiting:           func(logbridge.Key, bool) {},
			SurfaceAwaitingHuman: true,
			MapExtra:             discussionMapExtra,
		})

		return b, ch
	}

	key := logbridge.Key{Project: testProject, CardID: testCard}

	t.Run("discussion → text with agent and model", func(t *testing.T) {
		t.Parallel()

		bridge, ch := newBridge()
		bridge.BridgeLine(key, makeEvent("discussion", map[string]any{
			"agent":   "seat-2",
			"lens":    "security",
			"model":   "z-ai/glm-5.2",
			"round":   float64(1),
			"content": "the token comparison is not constant-time",
		}), false)

		var got protocol.LogEntry
		select {
		case got = <-ch:
		case <-time.After(100 * time.Millisecond):
			t.Fatal("expected a bridged discussion entry (timeout)")
		}

		assert.Equal(t, "text", got.Type)
		assert.Equal(t, "seat-2", got.Agent)
		assert.Equal(t, "z-ai/glm-5.2", got.Model)
		assert.Equal(t, "the token comparison is not constant-time", got.Content)
	})

	t.Run("discussion without model → Model stays empty", func(t *testing.T) {
		t.Parallel()

		bridge, ch := newBridge()
		bridge.BridgeLine(key, makeEvent("discussion", map[string]any{
			"agent":   "human",
			"round":   float64(2),
			"content": "human interjection",
		}), false)

		var got protocol.LogEntry
		select {
		case got = <-ch:
		case <-time.After(100 * time.Millisecond):
			t.Fatal("expected a bridged discussion entry (timeout)")
		}

		assert.Equal(t, "human", got.Agent)
		assert.Empty(t, got.Model)
	})

	t.Run("seat_debug → skipped", func(t *testing.T) {
		t.Parallel()

		bridge, ch := newBridge()
		bridge.BridgeLine(key, makeEvent("seat_debug", map[string]any{
			"seat_kind": "tool_call",
			"content":   "internal seat chatter",
		}), false)

		select {
		case e := <-ch:
			t.Errorf("seat_debug must not be bridged; got type=%q", e.Type)
		case <-time.After(30 * time.Millisecond):
		}
	})
}

// TestSetRedactorAppliesToSubsequentLines verifies that swapping the redactor
// via SetRedactor takes effect for lines bridged after the swap and fully
// replaces (not merges with) the prior redactor - matching the
// RedactorRegistry rebuilding the redactor on every session add/remove.
func TestSetRedactorAppliesToSubsequentLines(t *testing.T) {
	t.Parallel()

	hub := logbridge.NewHub(func(e protocol.LogEntry) string { return e.SessionID }, nil)
	_, ch := hub.Subscribe("")
	bridge := logbridge.NewBridge(logbridge.BridgeConfig{
		Hub:      hub,
		Redactor: redact.New([]string{"initial-secret-val"}),
	})

	key := logbridge.Key{SessionID: testSession}

	bridge.BridgeLine(key, []byte("leaked initial-secret-val here"), true)

	got := <-ch
	assert.Equal(t, "leaked [REDACTED] here", got.Content)

	bridge.SetRedactor(redact.New([]string{"rotated-secret-val"}))

	bridge.BridgeLine(key, []byte("leaked rotated-secret-val here"), true)

	got = <-ch
	assert.Equal(t, "leaked [REDACTED] here", got.Content)

	// Old secret no longer masked once the redactor is swapped: proves a full
	// replace, not a merge.
	bridge.BridgeLine(key, []byte("leaked initial-secret-val here"), true)

	got = <-ch
	assert.Equal(t, "leaked initial-secret-val here", got.Content)
}
