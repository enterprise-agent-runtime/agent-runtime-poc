//go:build !linux && !darwin

package exec

import (
	"os"

	"warden.dev/warden/internal/execproto"
)

// Tree is unsupported here. The executor runs only inside a Linux or macOS
// sandbox; on a Windows host it runs in the L2 Linux container (CLAUDE.md
// §3), never natively, so there is no race-free open to offer. The type
// exists so that the package compiles and the pure Resolve is tested on
// every OS.
type Tree struct{}

func unsupported() error {
	return execproto.NewError(execproto.ErrIOError, "the executor does not run on this OS")
}

// OpenTree always fails on this OS.
func OpenTree([]Root) (*Tree, error) { return nil, unsupported() }

// Close does nothing.
func (*Tree) Close() error { return nil }

// Openat2 is false.
func (*Tree) Openat2() bool { return false }

// Resolve always fails on this OS.
func (*Tree) Resolve(string, execproto.RootName, string) (execproto.Resolved, error) {
	return execproto.Resolved{}, unsupported()
}

// OpenCanonical always fails on this OS.
func (*Tree) OpenCanonical(execproto.Resolved, int) (*os.File, error) { return nil, unsupported() }
