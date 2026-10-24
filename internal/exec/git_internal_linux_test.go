//go:build linux

// sandboxed: this test builds the executor's own process table and asks it
// to run the fixed command "git config --list" for exec.git.run; nothing
// here runs an agent-requested process (INV-H).

package exec

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"warden.dev/warden/internal/execproto"
)

// TestGit_NoExecByFDIsUnsupported (review of 41a3818): where the shim
// cannot execute by descriptor (macOS until M7, execByFD false), Spawn
// refuses with io_error; exec.git.run must refuse the same way before it
// starts anything. Without the check the shim exits 127 and checkRepo
// reported invalid_state "configuration has a key…", the wrong code and
// the wrong reason. The OS constant is read through Git.byFD so the Linux
// suite can take the macOS path.
func TestGit_NoExecByFDIsUnsupported(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"work", "git", "empty"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tree, err := OpenTree([]Root{{Name: execproto.RootWorktree, Path: base + "/work", Mode: execproto.RootRW}})
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	limits, err := ParseChildLimits("cpu=60,fsize=1048576,nofile=64,core=0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := NewProcs(ctx, ProcConfig{Tree: tree, Mode: execproto.ModeTool, Limits: limits, MaxProcs: 2, Env: []string{"PATH=/usr/bin:/bin"}}, func(execproto.IONotification) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	cfg := GitConfig{GitDir: base + "/git", SessionBranch: "warden/01jaxr8q7m2v9ktc3f6yh5n0pb", BaseCommit: "0000000000000000000000000000000000000000", HooksDir: base + "/empty"}
	g := &Git{p: p, cfg: cfg, args: gitArgs{gitDir: cfg.GitDir, work: base + "/work", hooks: cfg.HooksDir}, path: "/usr/bin:/bin", byFD: false}
	want := noExecByFD().(*execproto.Error)
	err = g.checkRepo(ctx, time.Now().Add(time.Minute))
	got, ok := err.(*execproto.Error)
	if !ok || got.Code != want.Code || got.Data.Detail != want.Data.Detail {
		t.Errorf("checkRepo without execution by descriptor: %v, want %v", err, want)
	}
	if _, err := os.Stat("/usr/bin/git"); err == nil {
		g.byFD = true
		if err := g.checkRepo(ctx, time.Now().Add(time.Minute)); err != nil && codeOf(err) == want.Code && detailOfErr(err) == want.Data.Detail {
			t.Errorf("with execution by descriptor the refusal still came: %v", err)
		}
	}
}

func detailOfErr(err error) string {
	if e, ok := err.(*execproto.Error); ok {
		return e.Data.Detail
	}
	return ""
}
