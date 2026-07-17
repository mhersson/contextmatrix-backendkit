# AGENTS.md - ContextMatrix BackendKit

Shared serve plumbing for the two ContextMatrix execution backends,
`contextmatrix-agent` and `contextmatrix-chat`: webhook auth, task-skills
resolution, the worker stdin frame codec, Prometheus metrics, and log
streaming. This file is the contributor contract; `README.md` holds the
design overview and package inventory.

## Boundary discipline

This module owns the serve plumbing shared by the two backends and nothing
about *how* each backend uses it. Orchestration, protocol wire shape, and the
inner agentic loop belong in `contextmatrix-protocol`, `contextmatrix-harness`,
and the consuming backends (`contextmatrix-agent`, `contextmatrix-chat`)
themselves, never here.

- Allowed imports: `contextmatrix-protocol`, `contextmatrix-harness/events`,
  `contextmatrix-harness/redact`, `prometheus/client_golang`, stdlib, and
  `testify` in tests only. Forbidden: `contextmatrix`, `-agent`, `-chat`, and
  any other external module.
- CI (`make deps-gate`, `scripts/deps-gate.sh`) enforces the import allowlist
  above and rejects any forbidden repo dependency mechanically; keeping other
  external dependencies out is a review-enforced convention. An import that
  trips the gate means fix the design, not the gate.

## Package map

- `webhookcore` - HMAC auth middleware, replay cache, the SSE `/logs`
  handler, and shared HTTP helpers.
- `taskskills` - signed-GET task-skills pointer fetch and shallow git clone.
- `frames` - the worker container stdin JSON Lines control-frame codec.
- `metrics` - the common Prometheus registry bundle, namespaced per backend.
- `logbridge` - the fan-out hub and harness-event-to-`protocol.LogEntry`
  classifier.

## Verification

```bash
go fix ./... && make fmt && make test && make test-race && make lint && make deps-gate && make build
```

## Documentation

- Document the current state - what exists now and why, not how it got here.
- Do not write doc comments on simple functions - if what it does is
  straightforward, the code itself is the documentation.
- Never use em-dashes; use hyphens (-).

## Commit discipline

Run before every commit:

```bash
go fix ./...   # adopt modern stdlib idioms
make test      # clean
make lint      # clean
make build     # builds
```

- **Never commit without explicit user approval.** No exceptions.
- Conventional commits: `type(scope): concise summary`. Always include a scope.
- Body uses bullet points for the what and why - no long paragraphs.
- Never reference plan phases, task numbers, or private card IDs in messages.
