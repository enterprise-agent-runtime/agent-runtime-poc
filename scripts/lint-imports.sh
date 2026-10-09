#!/usr/bin/env bash
# lint-imports.sh enforces the import rules of CLAUDE.md §6 and keeps OS
# selection inside internal/platform, internal/sandbox and internal/secrets.
# The checks are Go AST tests in internal/archtest so they behave the same on
# Linux, macOS and Windows; TestRules_CatchKnownViolations proves they fire.
set -euo pipefail
cd "$(dirname "$0")/.."
go test -count=1 -run 'TestImportRules|TestOSSelectionConfined|TestRules_CatchKnownViolations' ./internal/archtest
