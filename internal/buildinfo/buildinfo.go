// Package buildinfo carries the version stamped into every binary at link
// time (design A17 §3, `-X` ldflags set by scripts/make.sh). It is reported
// by system.version and in runtime.start events (WRD-09 §3).
package buildinfo

import "runtime"

// Set with -ldflags "-X warden.dev/warden/internal/buildinfo.Version=…".
var (
	Version = "dev"
	Commit  = "0000000" // seven hex digits: system.version requires a commit-shaped value
)

// GoVersion is the toolchain the binary was built with.
func GoVersion() string { return runtime.Version() }
