// Package archtest holds the repository's static architecture checks: the
// import rules of CLAUDE.md §6, INV-H (no process creation outside the
// sandbox packages, CLAUDE.md §5 and §9 rule 1), INV-J (no host TCP/UDP
// listener outside cmd/warden-proxy) and the confinement of OS selection to
// internal/platform, internal/sandbox and internal/secrets. Design A02
// ("internal/archtest").
//
// The checks parse source files with go/parser instead of grepping, so they
// behave identically on the Linux, macOS and Windows CI runners, and they
// read every file regardless of build constraints, so an import or a call
// hidden behind a GOOS tag is still seen. scripts/lint-imports.sh and
// scripts/lint-exec.sh run these tests by name.
package archtest
