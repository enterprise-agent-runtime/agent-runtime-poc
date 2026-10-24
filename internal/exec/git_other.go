//go:build !linux && !darwin

package exec

import (
	"context"

	"warden.dev/warden/internal/execproto"
)

// RegisterGit registers exec.git.run on s. On this OS the executor does not
// run (CLAUDE.md §3: on a Windows host it runs in the L2 Linux container),
// so the method always fails, like the process methods (proc_other.go).
func RegisterGit(s *Server, _ *Procs, cfg GitConfig) error {
	if err := cfg.check(); err != nil {
		return err
	}
	return Handle(s, execproto.MethodGitRun, func(context.Context, execproto.GitRunParams) (execproto.GitRunResult, error) {
		return execproto.GitRunResult{}, unsupported()
	})
}
