// Package llmproxy keeps the real LLM API key out of worker containers. A
// backend registers the key with the proxy at launch and hands the worker a
// per-run stand-in token; the proxy swaps in the real bearer and forwards to
// the upstream LLM gateway or OpenRouter.
//
//	CM ──trigger (real key)──▶ serve (host)
//	                           ┌──────────────────────────────┐
//	                           │ keeps REAL KEY in memory     │
//	                           │ proxy: token T → REAL KEY    │
//	                           └──▲─────────────────┬─────────┘
//	           gives the container│only token T     │ Authorization: REAL KEY
//	           + proxy URL        │                 ▼
//	                 ┌────────────┼── worker ─┐   LLM gateway / OpenRouter
//	                 │ LLM_API_KEY = T (stand-in)
//	                 │ LLM_BASE_URL = http://host.docker.internal:<port>/llm
//	                 │ worker ──Authorization: T──▶ proxy on serve
//	                 │ bad dependency: reads T   │
//	                 └───────────┬───────────────┘
//	                             │ sends T out
//	                             ▼
//	           T at the gateway   → rejected (the gateway has never seen T)
//	           T at serve's proxy → reachable only inside the trusted network,
//	                                and dead once the run ends
//
// Wiring:
//   - Register at launch, keyed by run, with the run's [protocol.LLMEndpoint].
//   - Give the worker the token and "<container URL><prefix>" as its LLM base URL.
//   - Mount prefix+"/" wrapped only in RecordMetrics, outside Auth and
//     DrainGate, and put [Paths] on the metrics allowlist.
//   - Revoke on container exit and on launch failure.
//
// Config.CACertFile adds a PEM bundle to the system roots for upstream TLS, and
// the upstream connection honors HTTP_PROXY and HTTPS_PROXY from the serve
// process environment.
//
// Only POST <prefix>/chat/completions and GET <prefix>/models reach the
// upstream. Limits: Revoke stops new requests while a stream already in flight
// runs to its end; the token is readable inside the container, so the run can
// spend through the proxy until it is revoked.
package llmproxy
