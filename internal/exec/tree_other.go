//go:build !linux && !darwin

package exec

import (
	"context"
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

// The exec.fs methods always fail on this OS (fs_unix.go has them).

// Read always fails on this OS.
func (*Tree) Read(context.Context, execproto.ReadParams) (execproto.ReadResult, error) {
	return execproto.ReadResult{}, unsupported()
}

// List always fails on this OS.
func (*Tree) List(context.Context, execproto.ListParams) (execproto.ListResult, error) {
	return execproto.ListResult{}, unsupported()
}

// Search always fails on this OS.
func (*Tree) Search(context.Context, execproto.SearchParams) (execproto.SearchResult, error) {
	return execproto.SearchResult{}, unsupported()
}

// Write always fails on this OS.
func (*Tree) Write(context.Context, execproto.WriteParams) (execproto.WriteResult, error) {
	return execproto.WriteResult{}, unsupported()
}

// Patch always fails on this OS.
func (*Tree) Patch(context.Context, execproto.PatchParams) (execproto.PatchResult, error) {
	return execproto.PatchResult{}, unsupported()
}

// Stat always fails on this OS.
func (*Tree) Stat(context.Context, execproto.StatParams) (execproto.StatResult, error) {
	return execproto.StatResult{}, unsupported()
}
