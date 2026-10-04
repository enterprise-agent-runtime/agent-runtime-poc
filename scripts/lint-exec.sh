#!/usr/bin/env bash
# lint-exec.sh is the static half of INV-H and INV-J (CLAUDE.md §5):
#   TestExecutorOnlyViaSandbox  os/exec and other process starters only in
#                               internal/{sandbox,exec,worktree,harness}, each
#                               file with a "// sandboxed: <reason>" comment
#   TestNoListenTCP             no TCP/UDP listener outside cmd/warden-proxy
set -euo pipefail
cd "$(dirname "$0")/.."
go test -count=1 -run 'TestExecutorOnlyViaSandbox|TestNoListenTCP|TestRules_CatchKnownViolations' ./internal/archtest
