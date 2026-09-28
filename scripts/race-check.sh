#!/usr/bin/env bash
# race-check.sh — M1 correctness gate: build + vet + race-enabled tests.
#
# Runs the server's production test suite under the Go race detector.
# Exit non-zero on any build/vet/test failure (intended as a local gate until
# CI exists).
#
# Scope note: ./tests/performance is intentionally EXCLUDED. It is an upstream
# mockd benchmark that forks a real server binary and waits ~5s for readiness;
# on this machine it fails identically WITHOUT -race ("server not ready after
# 5s"), independent of any data race or this project's changes. Folding it in
# would keep the gate red for environmental reasons. Revisit separately if
# that benchmark is ever ported to this environment.
set -euo pipefail

# Resolve repo root (server/) regardless of where the script is invoked from.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${HERE}/.." && pwd)"
cd "${ROOT}"

echo "==> [1/3] go build ./..."
go build ./...

echo "==> [2/3] go vet ./..."
go vet ./...

# Scope: the packages MockNetPack owns and M1 actually touched (matches the
# documented canonical test command in 阶段性总结: ./pkg/admin/... ./pkg/store/...).
# We deliberately do NOT run the whole ./pkg/...: upstream mockd internals such
# as pkg/testing, pkg/tunnel, pkg/websocket bind fixed local ports (e.g. 4281)
# and can fail on "address already in use" when a detached smoke server lingers,
# which is environmental and unrelated to this project's correctness.

echo "==> [3/3] go test -race -count=1 ./pkg/store/... ./pkg/admin/... ./tests/unit"
go test -race -count=1 ./pkg/store/... ./pkg/admin/... ./tests/unit

echo
echo "race-check: OK (no data race; build/vet/tests green)"
