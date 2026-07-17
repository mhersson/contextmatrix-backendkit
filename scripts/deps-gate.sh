#!/usr/bin/env bash
# Enforces the backendkit boundary invariant:
#   1. the module depends on no backend or server repo
#   2. harness usage stays within events/redact
set -euo pipefail

bad_repo=$(go list -deps ./... \
  | grep -E 'mhersson/(contextmatrix$|contextmatrix/|contextmatrix-agent|contextmatrix-chat|contextmatrix-runner|contextmatrix-githubauth)' || true)
if [ -n "${bad_repo}" ]; then
  echo "FAIL: forbidden repo dependency:" >&2
  echo "${bad_repo}" >&2
  exit 1
fi

bad_harness=$(go list -deps ./... \
  | grep '^github.com/mhersson/contextmatrix-harness/' \
  | grep -vE '/(events|redact)$' || true)
if [ -n "${bad_harness}" ]; then
  echo "FAIL: harness import outside {events,redact}:" >&2
  echo "${bad_harness}" >&2
  exit 1
fi

echo "deps-gate: ok"
