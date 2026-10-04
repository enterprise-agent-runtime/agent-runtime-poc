#!/usr/bin/env bash
# make.sh holds the logic of every build target (CLAUDE.md §12, design A17).
# The Makefile (GNU make on Linux/macOS/CI) and make.ps1 (Windows without
# make) only dispatch here, so the two entry points cannot drift apart.
#
#   scripts/make.sh <target>
set -euo pipefail
cd "$(dirname "$0")/.."

STATICCHECK_VERSION=v0.8.1 # honnef.co/go/tools 2026.2.1; a tool, not a module dependency
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}
LDFLAGS="-s -w -X warden.dev/warden/internal/buildinfo.Version=${VERSION} -X warden.dev/warden/internal/buildinfo.Commit=${COMMIT}"
BIN=${BIN:-bin}

step() { printf '\n== %s\n' "$*"; }

lint() {
	step "gofmt"
	local dirs unformatted
	dirs=$(go list -f '{{.Dir}}' ./...)
	# shellcheck disable=SC2086
	unformatted=$(gofmt -l $dirs internal/archtest/testdata)
	if [ -n "$unformatted" ]; then
		echo "gofmt needed on:"; echo "$unformatted"; return 1
	fi
	# Type-check and vet for every target OS on whatever OS runs this, so a
	# Windows-only or Linux-only break is caught before CI (CLAUDE.md §3).
	for goos in linux darwin windows; do
		step "go vet (GOOS=$goos)"
		GOOS=$goos CGO_ENABLED=0 go vet ./...
	done
	step "staticcheck"
	go run "honnef.co/go/tools/cmd/staticcheck@${STATICCHECK_VERSION}" ./...
	step "import rules and OS confinement"
	scripts/lint-imports.sh
	step "INV-H exec rule and INV-J listener rule"
	scripts/lint-exec.sh
	step "API schemas and YAML examples"
	local schema_pkgs=()
	for d in internal/api internal/config; do [ -d "$d" ] && schema_pkgs+=("./$d/...")
	done
	if [ ${#schema_pkgs[@]} -gt 0 ]; then
		go test -count=1 -run 'TestSchema' "${schema_pkgs[@]}"
	fi
}

unit() {
	step "go test ./..."
	if [ "${RACE:-0}" = 1 ]; then
		go test -race -count=1 ./...
	else
		go test -count=1 ./...
	fi
}

ui_test() {
	if [ -f apps/desktop/package.json ]; then
		step "UI component tests"
		(cd apps/desktop && npm ci && npm test)
	else
		step "UI component tests: apps/desktop not present yet (M6)"
	fi
}

build() {
	step "go build -> ${BIN}/"
	mkdir -p "$BIN"
	CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$BIN/" ./cmd/...
}

not_yet() {
	echo "make $1: lands in $2 (see docs/PROGRESS.md)" >&2
	return 2
}

case "${1:-check}" in
check) lint && unit && ui_test ;;
lint) lint ;;
test) unit ;;
ui-test) ui_test ;;
build) build ;;
run-daemon) build && "$BIN/wardend" --foreground ;;
cover)
	step "coverage"
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	;;
integration) go test -count=1 -tags integration ./... ;;
escape-check) not_yet escape-check M2 ;;
smoke) not_yet smoke M4 ;;
fixtures) not_yet fixtures M4 ;;
images) not_yet images M2 ;;
e2e) not_yet e2e M3 ;;
desktop) not_yet desktop M6 ;;
*)
	echo "unknown target: $1" >&2
	exit 2
	;;
esac
