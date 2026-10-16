//go:build linux

package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"warden.dev/warden/internal/exec"
	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/tools"
)

// These run the exec.fs methods the way the daemon calls them: through
// the M2-5 server and the internal/tools client over a net.Pipe, so the
// strict params decoding, the error mapping and the result encoding are
// all part of what is tested. The method logic is tested in depth by the
// internal tests (fs_linux_test.go); these pin what crosses the wire.

func fsClient(t *testing.T, mode execproto.Mode) (*tools.Client, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	work := base + "/work"
	must(t, os.MkdirAll(work+"/src", 0o700))
	must(t, os.WriteFile(work+"/src/app.ts", []byte("export const port = 3000\n"), 0o600))
	must(t, os.WriteFile(work+"/.env", []byte("API_KEY=fake-test-value\n"), 0o600))
	tree, err := exec.OpenTree([]exec.Root{{Name: execproto.RootWorktree, Path: work, Mode: execproto.RootRW}})
	must(t, err)
	t.Cleanup(func() { _ = tree.Close() })

	a, b := net.Pipe()
	s := exec.NewServer(a)
	must(t, exec.Handle(s, execproto.MethodHello, func(context.Context, execproto.HelloParams) (execproto.HelloResult, error) {
		return helloResult(), nil
	}))
	must(t, exec.RegisterFS(s, tree, mode))
	startServer(t, s, a)
	c := tools.New(context.Background(), b, nil)
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Hello(context.Background(), helloParams()); err != nil {
		t.Fatal(err)
	}
	return c, work
}

// TestExecProtocol_FSRoundTrip: each of the six methods once, as the agent
// loop sequences them (stat first, then the operation with
// expect_canonical, A06 §5.4).
func TestExecProtocol_FSRoundTrip(t *testing.T) {
	c, work := fsClient(t, execproto.ModeTool)
	ctx := context.Background()

	var st execproto.StatResult
	must(t, c.FS(ctx, execproto.MethodFSStat, execproto.StatParams{Path: "src/app.ts"}, &st))
	if !st.Exists || st.Resolved.Canonical != work+"/src/app.ts" {
		t.Fatalf("stat: %+v", st)
	}
	var rd execproto.ReadResult
	must(t, c.FS(ctx, execproto.MethodFSRead, execproto.ReadParams{Path: "src/app.ts", ExpectCanonical: st.Resolved.Canonical}, &rd))
	if rd.Content != "export const port = 3000\n" {
		t.Fatalf("read: %+v", rd)
	}
	var wr execproto.WriteResult
	must(t, c.FS(ctx, execproto.MethodFSWrite, execproto.WriteParams{Path: "src/new.ts", Content: "export {}\n", ExpectCanonical: work + "/src/new.ts"}, &wr))
	if !wr.Created || wr.BytesWritten != 10 {
		t.Fatalf("write: %+v", wr)
	}
	var pr execproto.PatchResult
	must(t, c.FS(ctx, execproto.MethodFSPatch, execproto.PatchParams{Patch: "--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1 +1 @@\n-export const port = 3000\n+export const port = 8080\n"}, &pr))
	if !pr.Applied || len(pr.Files) != 1 || pr.Files[0].RelPath != "src/app.ts" {
		t.Fatalf("patch: %+v", pr)
	}
	var sr execproto.SearchResult
	must(t, c.FS(ctx, execproto.MethodFSSearch, execproto.SearchParams{Query: "8080"}, &sr))
	if len(sr.Matches) != 1 || sr.Matches[0].RelPath != "src/app.ts" {
		t.Fatalf("search: %+v", sr)
	}
	var ls execproto.ListResult
	must(t, c.FS(ctx, execproto.MethodFSList, execproto.ListParams{Depth: 2}, &ls))
	var names []string
	for _, e := range ls.Entries {
		names = append(names, e.RelPath)
	}
	if strings.Join(names, ",") != ".env,src,src/app.ts,src/new.ts" {
		t.Fatalf("list: %v", names)
	}
}

// TestExecProtocol_FSErrorsOnTheWire: refusals reach the daemon as A06
// §4.4 errors with their violation classification (which the daemon turns
// into sandbox.violation events), deny-listed content never appears in any
// result or error, and params outside the schema are refused before any
// path is touched.
func TestExecProtocol_FSErrorsOnTheWire(t *testing.T) {
	c, _ := fsClient(t, execproto.ModeTool)
	ctx := context.Background()
	cases := []struct {
		name      string
		method    string
		params    any
		code      execproto.Code
		violation execproto.ViolationKind
	}{
		{"read .env", execproto.MethodFSRead, execproto.ReadParams{Path: ".env"}, execproto.ErrDenyList, execproto.ViolationDenyList},
		{"read escape", execproto.MethodFSRead, execproto.ReadParams{Path: "../../etc/passwd"}, execproto.ErrPathEscape, execproto.ViolationPathEscape},
		{"write .git", execproto.MethodFSWrite, execproto.WriteParams{Path: ".git/config", Content: "x"}, execproto.ErrOutsideRoots, execproto.ViolationRootCheck},
		{"write too large", execproto.MethodFSWrite, execproto.WriteParams{Path: "big", Content: strings.Repeat("x", execproto.MaxWriteBytes+1)}, execproto.ErrTooLarge, ""},
		{"write marker", execproto.MethodFSWrite, execproto.WriteParams{Path: "k", Content: "[REDACTED:k]"}, execproto.ErrRedactionMarker, ""},
		{"list over the depth cap", execproto.MethodFSList, execproto.ListParams{Depth: 5}, execproto.ErrInvalidArgument, ""},
		{"search bad RE2", execproto.MethodFSSearch, execproto.SearchParams{Query: "(", Regex: true}, execproto.ErrInvalidArgument, ""},
		{"patch fails", execproto.MethodFSPatch, execproto.PatchParams{Patch: "--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1 +1 @@\n-nope\n+x\n"}, execproto.ErrPatchFailed, ""},
		{"stat scratch not mounted", execproto.MethodFSStat, execproto.StatParams{Path: "x", Root: execproto.RootScratch}, execproto.ErrOutsideRoots, execproto.ViolationRootCheck},
	}
	for _, c2 := range cases {
		err := c.FS(ctx, c2.method, c2.params, nil)
		var e *execproto.Error
		if !errors.As(err, &e) || e.Code != c2.code || e.Data.Kind != c2.violation || e.Data.Violation != (c2.violation != "") {
			t.Errorf("%s: %v, want %s (violation %q)", c2.name, err, c2.code.Name(), c2.violation)
			continue
		}
		if strings.Contains(e.Error(), "fake-test-value") || strings.Contains(e.Data.Detail, "/") {
			t.Errorf("%s: detail quotes content or a path: %q", c2.name, e.Data.Detail)
		}
	}

	// Strict decoding: an unknown key is invalid params, not a call.
	err := c.Call(ctx, execproto.MethodFSRead, json.RawMessage(`{"path":"src/app.ts","follow":true}`), nil)
	var re *execproto.RPCError
	if !errors.As(err, &re) || re.Code != execproto.RPCInvalidParams {
		t.Errorf("unknown key: %v, want invalid params", err)
	}

	// The deny-listed value never reaches a result either.
	var sr execproto.SearchResult
	must(t, c.FS(ctx, execproto.MethodFSSearch, execproto.SearchParams{Query: "fake-test-value"}, &sr))
	b, _ := json.Marshal(sr)
	if strings.Contains(string(b), "fake-test-value") || sr.Skipped.Denied != 1 {
		t.Errorf("search result: %s", b)
	}
}

// TestExecProtocol_FSHarnessModeForbidden: A06 §4.2, last column. A
// harness sandbox runs a vendor CLI; the file methods are not its
// interface, and every one of them fails mode_forbidden, a root_check
// violation, whatever its params.
func TestExecProtocol_FSHarnessModeForbidden(t *testing.T) {
	c, work := fsClient(t, execproto.ModeHarness)
	ctx := context.Background()
	for _, m := range []string{execproto.MethodFSRead, execproto.MethodFSList, execproto.MethodFSSearch, execproto.MethodFSWrite, execproto.MethodFSPatch, execproto.MethodFSStat} {
		err := c.FS(ctx, m, map[string]any{"path": "src/app.ts", "content": "x", "patch": "x", "query": "x"}, nil)
		var e *execproto.Error
		if !errors.As(err, &e) || e.Code != execproto.ErrModeForbidden || e.Data.Kind != execproto.ViolationRootCheck {
			t.Errorf("%s in harness mode: %v", m, err)
		}
	}
	if _, err := os.Stat(work + "/x"); err == nil {
		t.Error("a harness-mode call wrote")
	}
}

// TestRegisterFS_Twice: the method table is fixed; registering the file
// methods a second time is an error, not a silent replacement.
func TestRegisterFS_Twice(t *testing.T) {
	tree, err := exec.OpenTree([]exec.Root{{Name: execproto.RootWorktree, Path: t.TempDir(), Mode: execproto.RootRW}})
	must(t, err)
	defer tree.Close()
	for _, mode := range []execproto.Mode{execproto.ModeTool, execproto.ModeHarness} {
		a, _ := net.Pipe()
		s := exec.NewServer(a)
		must(t, exec.RegisterFS(s, tree, mode))
		if exec.RegisterFS(s, tree, mode) == nil {
			t.Errorf("%s: second RegisterFS succeeded", mode)
		}
		_ = a.Close()
	}
}
