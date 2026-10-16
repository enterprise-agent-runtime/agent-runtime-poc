package archtest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

// repoRoot is the repository root relative to this package.
const repoRoot = "../.."

func modulePath(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if m, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(m)
		}
	}
	t.Fatal("no module line in go.mod")
	return ""
}

func scanRepo(t *testing.T) []File {
	t.Helper()
	files, err := Scan(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("scanned no files; wrong root?")
	}
	return files
}

func report(t *testing.T, vs []Violation) {
	t.Helper()
	for _, v := range vs {
		t.Error(v)
	}
}

// TestExecutorOnlyViaSandbox is INV-H (CLAUDE.md §5): no process is created
// outside internal/sandbox, internal/exec, internal/worktree and
// internal/harness, and every file there that does so says why.
func TestExecutorOnlyViaSandbox(t *testing.T) { report(t, ExecRule(scanRepo(t))) }

// TestNoListenTCP is INV-J (CLAUDE.md §5): the daemon never listens on a host
// TCP port; only cmd/warden-proxy (inside the task's internal network) and
// the forwarder file internal/exec/forward/listen.go (inside the sandbox's
// network namespace, C-46) may.
func TestNoListenTCP(t *testing.T) { report(t, ListenRule(scanRepo(t))) }

// TestImportRules enforces the import rules of CLAUDE.md §6.
func TestImportRules(t *testing.T) { report(t, ImportRules(modulePath(t), scanRepo(t))) }

// TestOSSelectionConfined keeps OS differences inside internal/platform,
// internal/sandbox, internal/secrets and the executor (CLAUDE.md §3, §6, C-61).
func TestOSSelectionConfined(t *testing.T) { report(t, OSRule(scanRepo(t))) }

// TestRules_CatchKnownViolations runs every rule over testdata/repo, a tree
// seeded with one instance of each forbidden pattern and a few allowed ones.
// It exists because a static check that never fires looks exactly like a
// clean repository: this proves each rule actually detects what it guards.
func TestRules_CatchKnownViolations(t *testing.T) {
	files, err := Scan("testdata/repo")
	if err != nil {
		t.Fatal(err)
	}
	// Every INV-J message names the full allowlist, so whoever meets one knows
	// where a listener may live without reading this package.
	for _, v := range ListenRule(files) {
		if !strings.HasSuffix(v.Detail, "; allowed only in cmd/warden-proxy and internal/exec/forward/listen.go") {
			t.Errorf("INV-J message does not name the allowlist: %s", v)
		}
	}
	// The R4 message states the rule exactly, including the subpackages.
	for _, v := range ImportRules("warden.dev/warden", files) {
		if strings.Contains(v.Detail, "R4") && !strings.HasPrefix(v.Detail, "R4: only internal/exec/... and cmd/warden-exec may import internal/exec/...") {
			t.Errorf("R4 message does not state the rule: %s", v)
		}
	}
	var got []string
	for _, vs := range [][]Violation{
		ExecRule(files), ListenRule(files), OSRule(files), ImportRules("warden.dev/warden", files),
	} {
		for _, v := range vs {
			got = append(got, v.Rule+" "+v.Rel)
		}
	}
	sort.Strings(got)
	want := []string{
		"INV-H internal/sandbox/nocomment/x.go", // os/exec without justification
		"INV-H internal/store/spawn.go",         // aliased os.StartProcess outside sandbox packages
		"INV-H internal/store/x_test.go",        // tests are not exempt from INV-H
		"INV-J internal/api/listen.go",          // net.Listen("tcp", ...)
		"INV-J internal/api/listen.go",          // http.ListenAndServe
		// internal/exec/forward/listen.go (the C-46 forwarder) listens on TCP and
		// is allowed; the exemption is that one file, not its package or parent.
		"INV-J internal/exec/forward/other.go", // net.ListenTCP beside the forwarder file
		"INV-J internal/exec/other.go",         // net.Listen("tcp", ...) elsewhere in the executor
		// Gaps found in review of 6bdf845: the method form s.ListenAndServe()
		// on an http.Server is invisible to a call check, so the type is flagged
		// (cmd/warden-proxy/server.go and internal/api/server_test.go are
		// allowed); a dot-import hides every call from the analyser.
		"INV-H internal/sandbox/dot/x.go",    // import . "os/exec", even with a justification
		"INV-H internal/store/dotsyscall.go", // import . "syscall" hides ForkExec
		"INV-J internal/api/dothttp.go",      // import . "net/http" hides ListenAndServe
		"INV-J internal/api/dotnet.go",       // import . "net" hides Listen
		"INV-J internal/api/server.go",       // http.Server, method-form ListenAndServe
		"imports apps/desktop/x.go",          // Go under apps/
		"imports internal/exec/bad.go",       // exec importing the daemon
		// Design A02 §5 R3/R4. The review of 6bdf845 showed that the file-level
		// listener exemption is only safe if no daemon package can link the
		// forwarder: cmd/wardend importing internal/exec/forward and calling an
		// exported listener would pass ListenRule. R4 closes that. Allowed here:
		// internal/exec/proto.go and cmd/warden-exec/ok.go (execproto, exec/...,
		// x/sys per D-041, buildinfo for the binary only) and
		// internal/sandbox/proto.go (execproto is not internal/exec).
		"imports cmd/warden-exec/importsstore.go", // R3: the executor binary imports a daemon package
		"imports cmd/wardend/importsexec.go",      // R4: the daemon links executor code
		"imports internal/exec/buildinfo.go",      // R3/R10: buildinfo reaches exec only through exec.Config
		"imports internal/exec/thirdparty.go",     // R3: third-party module other than x/sys
		"imports internal/sandbox/importsfwd.go",  // R4: a daemon package links the forwarder
		// Re-review of fb26514..788c9e2: two ways around R4. (1) A library
		// package under cmd/warden-exec could import the forwarder and be
		// imported by the daemon. (2) Scan skipped docs/, fixtures/ and the
		// like at any depth, although the go tool compiles them, so every rule
		// was blind there. Round 3 found the same hole at the root: docs/fwd
		// was skipped yet compiled, so internal/api/importsdocs.go linked the
		// forwarder silently. Scan now skips exactly what the go tool skips:
		// testdata/ and names starting with "." or "_", at any depth
		// (internal/api/testdata/l.go, internal/api/_old/l.go,
		// internal/api/_x.go). The importer itself breaks no rule; the R4
		// violation in docs/fwd/f.go is what cuts the chain.
		"imports cmd/warden-exec/lib/lib.go", // only package main under cmd/warden-exec
		"imports docs/fwd/f.go",              // R4 in a root-level docs/ tree
		"imports internal/api/fixtures/x.go", // R4 inside a nested fixtures/ dir
		"imports internal/api/importslib.go", // nothing imports a cmd/ package
		"INV-J internal/api/docs/l.go",       // net.Listen("tcp") inside a nested docs/ dir
		// Re-review minors. Other listener entry points; httptest servers are
		// for test files only. Previously untested branches: dot-imports of os
		// and x/sys/unix, and R4 applying to test files. R3 does not apply to
		// executor test files (A02 "!$test"): internal/exec/x_test.go and
		// cmd/warden-exec/main_test.go import internal/store and are allowed.
		"INV-H internal/store/dotos.go",      // import . "os" hides StartProcess
		"INV-H internal/store/dotunix.go",    // import . "golang.org/x/sys/unix" hides Exec
		"INV-J internal/api/filelistener.go", // net.FileListener
		"INV-J internal/api/httptest.go",     // net/http/httptest in a non-test file
		"INV-J internal/api/syslisten.go",    // syscall.Listen
		"INV-J internal/api/unixbind.go",     // unix.Bind
		"INV-J internal/api/winlisten.go",    // windows.Listen
		"imports internal/api/x_test.go",     // R4 includes test files
		// Round 3: a TLS listener is a TCP listener. Raw system calls by number
		// can create processes or sockets past every named check, so they are
		// confined to the sandbox packages plus the platform terminal files
		// (internal/sandbox/ok/raw.go and internal/platform/term_linux.go are
		// allowed; internal/platform/other.go is not). //go:linkname reaches
		// unexported runtime and syscall code; its allowlist is empty.
		"INV-H internal/platform/other.go",       // syscall.Syscall beside the allowed terminal file
		"INV-H internal/store/linkname.go",       // //go:linkname
		"INV-H internal/store/rawsyscall.go",     // syscall.Syscall
		"INV-H internal/store/rawsyscall.go",     // syscall.Syscall6
		"INV-H internal/store/rawsyscall.go",     // syscall.RawSyscall
		"INV-H internal/store/rawsyscall.go",     // syscall.RawSyscall6
		"INV-H internal/store/rawsyscall.go",     // syscall.SyscallN
		"INV-H internal/store/rawunix.go",        // unix.Syscall
		"INV-H internal/store/rawwindows.go",     // windows.SyscallN
		"INV-J internal/api/dottls.go",           // import . "crypto/tls" hides Listen
		"INV-J internal/api/tls.go",              // tls.Listen
		"INV-J internal/api/tls.go",              // tls.NewListener
		"imports internal/policy/bad.go",         // policy importing a provider
		"imports internal/providers/bad/bad.go",  // third-party import in a provider
		"imports internal/providers/bad/bad.go",  // provider importing internal/store
		"os-selection internal/api/os.go",        // //go:build linux
		"os-selection internal/api/os.go",        // runtime.GOOS
		"os-selection internal/api/x_windows.go", // file-name suffix
		// internal/exec/sys_linux.go and cmd/warden-exec/main_darwin.go select by
		// OS and are allowed (C-61); these two are still rejected.
		"os-selection internal/executor/x_linux.go", // a name prefix of internal/exec is not internal/exec
		"os-selection internal/store/x_darwin.go",   // OS suffix outside the allowed dirs
	}
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("violations differ\n got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestScan_FailsClosedOnLinks exists because filepath.WalkDir does not follow
// symlinks or Windows junctions while the go tool resolves package
// directories through them (re-review round 3): a linked directory was
// compiled into the binaries and invisible to every rule. Scan does not
// follow links; it reports each one, and any other irregular entry, as a
// "scan" violation, so a tree containing one cannot pass. Links inside names
// the go tool ignores (here _ignored/) are not reported.
//
// The links are created at test time: git on Windows does not check them out
// reliably. Junctions cannot be created without os/exec (mklink) or raw
// reparse-point syscalls, which INV-H forbids here; Go reports them as
// fs.ModeIrregular, the same branch as a device or socket.
func TestScan_FailsClosedOnLinks(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"pkg/a.go", "_ignored/b.go"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{
		"dirlink":          "pkg",  // a package directory reached through a link
		"pkg/filelink.go":  "a.go", // a source file reached through a link
		"_ignored/link.go": "b.go", // ignored by the go tool, so not reported
	}
	for link, target := range links {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Skipf("this OS refused to create a symlink (%v); on Windows that needs Developer Mode or SeCreateSymbolicLinkPrivilege, so the check is untested here", err)
		}
	}
	_, err := Scan(root)
	var se *ScanError
	if !errors.As(err, &se) {
		t.Fatalf("Scan over a tree with links returned %v, want a *ScanError", err)
	}
	var got []string
	for _, v := range se.Violations {
		got = append(got, v.Rule+" "+v.Rel)
	}
	sort.Strings(got)
	want := []string{"scan dirlink", "scan pkg/filelink.go"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("irregular entries differ\n got: %v\nwant: %v", got, want)
	}
}

// TestScan_FailsClosedOnIrregularEntries is the same guard as
// TestScan_FailsClosedOnLinks on an in-memory tree, so it runs on every OS,
// including Windows without Developer Mode where that test skips. It covers
// every irregular type: Windows junctions and other reparse points arrive as
// fs.ModeIrregular.
func TestScan_FailsClosedOnIrregularEntries(t *testing.T) {
	fsys := fstest.MapFS{
		"pkg/a.go":          {Data: []byte("package pkg\n")},
		"dirlink":           {Mode: fs.ModeSymlink},
		"pkg/junction":      {Mode: fs.ModeIrregular},
		"pkg/dev":           {Mode: fs.ModeDevice},
		"pkg/fifo":          {Mode: fs.ModeNamedPipe},
		"pkg/sock":          {Mode: fs.ModeSocket},
		"_ignored/link":     {Mode: fs.ModeSymlink}, // go tool ignores _ names
		"pkg/testdata/link": {Mode: fs.ModeSymlink}, // and testdata
		"pkg/.hidden/link":  {Mode: fs.ModeSymlink}, // and . names
	}
	files, err := scanFS(fsys)
	var se *ScanError
	if !errors.As(err, &se) {
		t.Fatalf("scanFS over a tree with irregular entries returned %v, want a *ScanError", err)
	}
	var got []string
	for _, v := range se.Violations {
		got = append(got, v.Rule+" "+v.Rel)
	}
	sort.Strings(got)
	want := []string{"scan dirlink", "scan pkg/dev", "scan pkg/fifo", "scan pkg/junction", "scan pkg/sock"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("irregular entries differ\n got: %v\nwant: %v", got, want)
	}
	if len(files) != 1 || files[0].Rel != "pkg/a.go" {
		t.Errorf("regular files still scanned: got %v", files)
	}
}
