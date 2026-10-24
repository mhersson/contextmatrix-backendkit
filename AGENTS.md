# AGENTS.md - ContextMatrix BackendKit

Shared serve plumbing for the ContextMatrix backends `contextmatrix-agent`,
`contextmatrix-chat` and `contextmatrix-architect`: webhook auth, task-skills
resolution, the worker stdin frame codec, Prometheus metrics, and log
streaming. This file is the contributor contract; `README.md` holds the
design overview and package inventory.

## Boundary discipline

This module owns the serve plumbing shared by the backends and nothing
about *how* each backend uses it. Orchestration, protocol wire shape, and the
inner agentic loop belong in `contextmatrix-protocol`, `contextmatrix-harness`,
and the consuming backends (`contextmatrix-agent`, `contextmatrix-chat`,
`contextmatrix-architect`) themselves, never here.

- Allowed imports: `contextmatrix-protocol`, `contextmatrix-harness/events`,
  `contextmatrix-harness/redact`, `prometheus/client_golang`, stdlib, and
  `testify` in tests only. Forbidden: `contextmatrix`, `-agent`, `-chat`,
  `-architect`, and any other external module.
- CI (`make deps-gate`, `scripts/deps-gate.sh`) enforces the import allowlist
  above and rejects any forbidden repo dependency mechanically; keeping other
  external dependencies out is a review-enforced convention. An import that
  trips the gate means fix the design, not the gate.

## Package map

- `webhookcore` - HMAC auth middleware, replay cache, the `/message` dedup
  cache, the SSE `/logs` handler, and shared HTTP helpers.
- `taskskills` - signed-GET task-skills pointer fetch and shallow git clone.
- `frames` - the worker container stdin JSON Lines control-frame codec.
- `metrics` - the common Prometheus registry bundle, namespaced per backend.
- `logbridge` - the fan-out hub and harness-event-to-`protocol.LogEntry`
  classifier. A backend can retain a per-key backlog replayed to a late consumer.

## Tests

- Tests must exercise behavior. A test that only fails when someone rewords a
  string, renames a heading, or edits prose is not a test - do not write it, and
  delete it when found. Asserting exact text is legitimate only where the text is
  a wire contract a consumer parses; if no code reads it, no test should pin it.

## Verification

```bash
make fmt && make test && make test-race && make lint && make deps-gate && make build
```

## Documentation

- Documentation must always be true: every statement matches the code at
  HEAD, and a change that makes a doc line false updates it in the same
  commit.
- Document the current state - what exists now and why, not how it got here.
- Comments explain only non-obvious decisions, constraints, safety invariants,
  or workarounds. Do not narrate what code does or record change history; git
  holds history. Keep comments to one or two tight lines unless a longer
  explanation is genuinely necessary. Rewrite or delete if you find comments
  that don't follow this rule.
- Never use em-dashes; use hyphens (-).
- Never reference plan phases, task numbers, or private card IDs in doc comments.

## Commit discipline

Run before every commit:

```bash
make test      # clean
make lint      # clean
make build     # builds
```

- **Never commit to `main`.** Work on a feature branch; tasks of a plan or
  scope the user approved commit there without asking again. Never push
  without explicit user approval.
- Conventional commits: `type(scope): concise summary`. Always include a scope.
- Body uses bullet points for the what and why - no long paragraphs.
- Never reference plan phases, task numbers, or private card IDs in messages.
