// Registration of the exec.fs methods on the executor server (design A06
// §4.2; the package doc is in server.go). The methods themselves are Tree
// methods (fs_unix.go): they run on Linux and macOS, where the executor
// runs, and report io_error elsewhere (tree_other.go).

package exec

import (
	"context"

	"warden.dev/warden/internal/execproto"
)

// RegisterFS registers exec.fs.read, list, search, write, patch and stat
// on s, served from t. In harness mode none of them is available (A06 §4.2,
// last column): each fails mode_forbidden, a root_check violation, without
// touching t.
func RegisterFS(s *Server, t *Tree, mode execproto.Mode) error {
	if mode == execproto.ModeHarness {
		for _, m := range []string{execproto.MethodFSRead, execproto.MethodFSList, execproto.MethodFSSearch, execproto.MethodFSWrite, execproto.MethodFSPatch, execproto.MethodFSStat} {
			if err := Handle(s, m, forbidden); err != nil {
				return err
			}
		}
		return nil
	}
	for _, err := range []error{
		Handle(s, execproto.MethodFSRead, t.Read),
		Handle(s, execproto.MethodFSList, t.List),
		Handle(s, execproto.MethodFSSearch, t.Search),
		Handle(s, execproto.MethodFSWrite, t.Write),
		Handle(s, execproto.MethodFSPatch, t.Patch),
		Handle(s, execproto.MethodFSStat, t.Stat),
	} {
		if err != nil {
			return err
		}
	}
	return nil
}

// forbidden answers an fs method in harness mode. Its params type accepts
// any object, so the refusal does not depend on whether the params would
// have been valid.
func forbidden(context.Context, map[string]any) (struct{}, error) {
	return struct{}{}, execproto.NewError(execproto.ErrModeForbidden, "file methods are not available in harness mode")
}
