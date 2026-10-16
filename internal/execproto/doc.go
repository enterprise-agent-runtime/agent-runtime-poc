// Package execproto holds the wire contract of the executor protocol
// warden.exec/1: WRD-04 §5 (executor protocol) and §9 (command profiles),
// WRD-16 §9, and design A06 §3.2 (profiles file), §4 (methods, schemas,
// error codes) and §6 (command allowlist re-check: shell strings and
// profile matching). Its place in the tree follows design A02 (package
// layout; owner decision 2026-10-16): execproto is the leaf shared by the
// daemon-side client in internal/tools and the in-sandbox server in
// internal/exec, which only cmd/warden-exec imports.
//
// Both sides of the protocol import it, and in M3 the policy engine reuses
// IsShellString and ProfileSet.Match so that the PDP and the executor agree
// (A06 §6 step 2 and Deviations). It is a leaf: standard library only,
// nothing from the daemon and nothing from internal/exec.
//
// One Go struct mirrors each $def of the A06 §4.3 schemas, with the same
// property names. Required properties are never omitempty; optional ones
// are omitempty unless their zero value is a meaningful explicit value, in
// which case they are pointers. Every object in A06 has
// additionalProperties: false, so Decode rejects unknown fields at every
// depth. The A06 schemas themselves are kept verbatim under
// testdata/schema and the golden tests check the structs against them.
package execproto
