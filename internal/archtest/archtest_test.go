package archtest

import (
	"errors"
	"fmt"
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
	files, module, err := scanTree(os.DirFS(repoRoot))
	if err != nil {
		t.Fatal(err)
	}
	// The ignored-path import check uses the module path Scan read; if it
	// read none or a wrong one, that check would be silently off (round 5).
	if want := modulePath(t); module != want {
		t.Fatalf("Scan used module path %q, go.mod declares %q", module, want)
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
		if strings.Contains(v.Detail, "R6") && !strings.HasPrefix(v.Detail, "R6: internal/execproto/... imports only the standard library") {
			t.Errorf("R6 message does not state the rule: %s", v)
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
		"INV-H internal/store/execveat.go",      // unix.Execveat outside the sandbox packages
		"INV-H internal/store/execveat.go",      // unix.Fexecve outside the sandbox packages
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
		// Review of M2-4b: isStdlib("C") was true (no dot in the path), so
		// cgo, with arbitrary C code in the preamble, passed R3 and R6 as if
		// it were the standard library. "C" is not stdlib.
		"imports internal/exec/cgo.go",      // R3: import "C"
		"imports internal/execproto/cgo.go", // R6: import "C"
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
		"INV-H internal/platform/other.go",   // syscall.Syscall beside the allowed terminal file
		"INV-H internal/store/linkname.go",   // //go:linkname
		"INV-H internal/store/rawsyscall.go", // syscall.Syscall
		"INV-H internal/store/rawsyscall.go", // syscall.Syscall6
		"INV-H internal/store/rawsyscall.go", // syscall.RawSyscall
		"INV-H internal/store/rawsyscall.go", // syscall.RawSyscall6
		"INV-H internal/store/rawsyscall.go", // syscall.SyscallN
		"INV-H internal/store/rawunix.go",    // unix.Syscall
		"INV-H internal/store/rawwindows.go", // windows.SyscallN
		"INV-J internal/api/dottls.go",       // import . "crypto/tls" hides Listen
		"INV-J internal/api/tls.go",          // tls.Listen
		"INV-J internal/api/tls.go",          // tls.NewListener
		// Round 4: the lists were incomplete. More process starters and
		// numbered syscalls; //go:linkname without a following space; and DLL
		// loading, which reaches any Windows API (CreateProcessW, bind) by
		// name. DLL loading is allowed in the sandbox packages and in the
		// exact platform files that need it (internal/sandbox/ok/dll.go and
		// internal/platform/disk_windows.go here); internal/platform/dll_other.go
		// is not.
		"INV-H internal/platform/dll_other.go", // syscall.NewLazyDLL beside the allowed files
		"INV-H internal/store/dll.go",          // syscall.NewLazyDLL
		"INV-H internal/store/dll.go",          // syscall.LoadDLL
		"INV-H internal/store/dll.go",          // syscall.MustLoadDLL
		"INV-H internal/store/dll.go",          // syscall.LoadLibrary
		"INV-H internal/store/dll.go",          // syscall.GetProcAddress
		"INV-H internal/store/dllwin.go",       // windows.NewLazySystemDLL
		"INV-H internal/store/dllwin.go",       // windows.LoadLibraryEx
		"INV-H internal/store/linkname2.go",    // //go:linknamestd
		"INV-H internal/store/procsyscall.go",  // syscall.CreateProcess
		"INV-H internal/store/procsyscall.go",  // syscall.CreateProcessAsUser
		"INV-H internal/store/rawmore.go",      // syscall.Syscall9
		"INV-H internal/store/rawmore.go",      // syscall.Syscall12
		"INV-H internal/store/rawmore.go",      // syscall.Syscall15
		"INV-H internal/store/rawmore.go",      // syscall.Syscall18
		"INV-H internal/store/rawmoreunix.go",  // unix.SyscallNoError
		"INV-H internal/store/rawmoreunix.go",  // unix.RawSyscallNoError
		"INV-H internal/store/shell.go",        // windows.ShellExecute
		"INV-H internal/store/shell.go",        // windows.ShellExecuteEx
		// Round 5: golang.org/x/sys/execabs is a drop-in for os/exec (its Cmd
		// is exec.Cmd) and net/http/cgi starts processes through os/exec, so
		// both are treated exactly like os/exec (internal/sandbox/ok/abs.go
		// and ok/cgi.go carry a justification and are allowed). The DLL and
		// proc types can be built as struct literals, bypassing the loader
		// functions, so naming them is a DLL use.
		"INV-H internal/sandbox/nocomment/abs.go", // execabs without a justification
		"INV-H internal/store/cgi.go",             // net/http/cgi outside the sandbox packages
		"INV-H internal/store/dlltypes.go",        // syscall.LazyDLL
		"INV-H internal/store/dlltypes.go",        // syscall.DLL
		"INV-H internal/store/dlltypes.go",        // syscall.LazyProc
		"INV-H internal/store/dlltypes.go",        // syscall.Proc
		"INV-H internal/store/dlltypes.go",        // windows.LazyDLL
		"INV-H internal/store/dotexecabs.go",      // execabs imported outside the sandbox packages
		"INV-H internal/store/dotexecabs.go",      // import . "golang.org/x/sys/execabs"
		"INV-H internal/store/execabs.go",         // execabs outside the sandbox packages
		// Final review: go/build and go/importer run go list and pkg-config
		// through os/exec, and plugin loads code at run time; all three are
		// execImports (internal/sandbox/ok/plugin.go is justified and allowed).
		"INV-H internal/store/dotgobuild.go",      // go/build imported outside the sandbox packages
		"INV-H internal/store/dotgobuild.go",      // import . "go/build"
		"INV-H internal/store/dotgoimporter.go",   // go/importer imported outside the sandbox packages
		"INV-H internal/store/dotgoimporter.go",   // import . "go/importer"
		"INV-H internal/store/imp_go_build.go",    // go/build
		"INV-H internal/store/imp_go_importer.go", // go/importer
		"INV-H internal/store/imp_plugin.go",      // plugin
		// M2-4b, design A02 §5 R6: internal/execproto/... is a stdlib-only
		// leaf, because the deny-list (internal/execproto/denylist, D-014)
		// and the wire types are linked into the daemon, the policy engine,
		// the sandbox mount layer and the executor alike (INV-I). A
		// module-internal import would drag daemon code into the executor;
		// a third-party one, a dependency outside R3. Allowed:
		// internal/execproto/ok.go (stdlib only) and
		// internal/execproto/x_test.go (test files may import jsonschema
		// for the golden schema tests).
		"imports internal/execproto/denylist/thirdparty.go", // R6: third-party module, in a subpackage
		"imports internal/execproto/store.go",               // R6: module-internal package
		"imports internal/policy/bad.go",                    // policy importing a provider
		"imports internal/providers/bad/bad.go",             // third-party import in a provider
		"imports internal/providers/bad/bad.go",             // provider importing internal/store
		"os-selection internal/api/os.go",                   // //go:build linux
		"os-selection internal/api/os.go",                   // runtime.GOOS
		"os-selection internal/api/x_windows.go",            // file-name suffix
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

// TestScan_RefusesWhatTheGoToolHides exists because of round 4's full
// bypass: the go tool leaves testdata and "."/"_" names out of ./... pattern
// matching only, and still compiles such a package whenever something
// imports it. Probe: internal/store/_impl with exec.Command and
// net.Listen("tcp"), imported by internal/store/hidden_use.go, passed every
// rule while go list -deps ./cmd/wardend listed it. Scan cannot read what it
// skips, so it refuses the import instead. It also refuses non-Go sources
// (assembly, cgo C/C++/Objective-C, .syso objects): they are linked into the
// binaries and no rule here can read them. Third-party import paths are not
// affected. This is an in-memory tree because a refusal stops every rule,
// which would stop TestRules_CatchKnownViolations too.
func TestScan_RefusesWhatTheGoToolHides(t *testing.T) {
	src := func(imp string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte("package api\n\nimport _ \"" + imp + "\"\n")}
	}
	fsys := fstest.MapFS{
		"go.mod":                     {Data: []byte("module warden.dev/warden\n\ngo 1.26\n")},
		"internal/api/_old/l.go":     {Data: []byte("package old\n")},
		"internal/api/importsold.go": src("warden.dev/warden/internal/api/_old"),
		"internal/api/importstd.go":  src("warden.dev/warden/internal/api/testdata/pkg"),
		"internal/api/importsdot.go": src("warden.dev/warden/internal/.hidden"),
		"internal/api/ok.go":         src("warden.dev/warden/internal/model"),
		"internal/api/thirdparty.go": src("github.com/example/_examples/x"), // not ours: unaffected
		"internal/api/asm_amd64.s":   {Data: []byte("TEXT ·f(SB),0,$0\n")},
		"internal/api/asm_arm64.S":   {},
		"internal/api/blob.syso":     {},
		"internal/api/c.c":           {},
		"internal/api/c.cc":          {},
		"internal/api/c.cpp":         {},
		"internal/api/c.h":           {},
		"internal/api/m.m":           {},
		// Round 5: the rest of the go tool's non-Go source set.
		"internal/api/a.sx":             {},
		"internal/api/c.cxx":            {},
		"internal/api/c.hh":             {},
		"internal/api/c.hpp":            {},
		"internal/api/c.hxx":            {},
		"internal/api/f.f":              {},
		"internal/api/f.F":              {},
		"internal/api/f.for":            {},
		"internal/api/f.f90":            {},
		"internal/api/s.swig":           {},
		"internal/api/s.swigcxx":        {},
		"internal/api/README.md":        {}, // not a source the toolchain links
		"internal/api/_ignored/x.c":     {}, // inside an ignored name: never read by the go tool either
		"internal/api/testdata/pkg/x.c": {},
	}
	_, err := scanFS(fsys)
	var se *ScanError
	if !errors.As(err, &se) {
		t.Fatalf("scanFS returned %v, want a *ScanError", err)
	}
	var got []string
	for _, v := range se.Violations {
		got = append(got, v.Rule+" "+v.Rel)
	}
	sort.Strings(got)
	want := []string{
		"scan internal/api/asm_amd64.s", "scan internal/api/asm_arm64.S", "scan internal/api/blob.syso",
		"scan internal/api/c.c", "scan internal/api/c.cc", "scan internal/api/c.cpp", "scan internal/api/c.h",
		"scan internal/api/importsdot.go", "scan internal/api/importsold.go", "scan internal/api/importstd.go",
		"scan internal/api/m.m",
		"scan internal/api/a.sx", "scan internal/api/c.cxx", "scan internal/api/c.hh", "scan internal/api/c.hpp",
		"scan internal/api/c.hxx", "scan internal/api/f.F", "scan internal/api/f.f", "scan internal/api/f.f90",
		"scan internal/api/f.for", "scan internal/api/s.swig", "scan internal/api/s.swigcxx",
	}
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("refused entries differ\n got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestScan_RefusesLocalReplacesAndWorkspaces exists because a go.mod
// "replace" with a filesystem target, or a go.work file, compiles code from a
// directory archtest never scans (outside the tree, or a module the scan
// treats as foreign) into the binaries (round 5). Scan refuses both. It also
// guards modulePathOf: a module line with a comment or quotes must still
// yield the path, and a go.mod whose module line cannot be read is refused
// rather than silently switching the ignored-path import check off. The
// path targets carry versions so that each localPath branch is tested on its
// own; g has no version, which alone makes a target a directory.
func TestScan_RefusesLocalReplacesAndWorkspaces(t *testing.T) {
	gomod := `module warden.dev/warden // the PoC module

go 1.26

replace example.com/a => ./a v1.0.0
replace example.com/b => ../b v1.0.0
replace (
	example.com/c => /abs/c v1.0.0
	example.com/d v1.0.0 => C:\d
	example.com/e => D:/e v1.0.0
	example.com/f => some\dir v1.0.0
	example.com/ok => example.com/fork v1.2.3 // a module, not a path
)
replace example.com/g => dir/sub
replace example.com/ok2 v1.0.0 => example.com/fork2 v1.0.1
`
	fsys := fstest.MapFS{
		"go.mod":           {Data: []byte(gomod)},
		"go.work":          {Data: []byte("go 1.26\n")},
		"sub/go.work":      {Data: []byte("go 1.26\n")},
		"_ignored/go.work": {Data: []byte("go 1.26\n")}, // ignored by the go tool too
		"pkg/a.go":         {Data: []byte("package pkg\n")},
	}
	_, module, err := scanTree(fsys)
	if module != "warden.dev/warden" {
		t.Errorf("module path from a commented module line = %q, want warden.dev/warden", module)
	}
	var se *ScanError
	if !errors.As(err, &se) {
		t.Fatalf("scanTree returned %v, want a *ScanError", err)
	}
	var got []string
	for _, v := range se.Violations {
		got = append(got, fmt.Sprintf("%s %s:%d", v.Rule, v.Rel, v.Line))
	}
	sort.Strings(got)
	want := []string{
		"scan go.mod:10", "scan go.mod:11", "scan go.mod:14", // e, f, g
		"scan go.mod:5", "scan go.mod:6", "scan go.mod:8", "scan go.mod:9", // a, b, c, d
		"scan go.work:0", "scan sub/go.work:0",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("refused entries differ\n got: %v\nwant: %v", got, want)
	}

	for _, tc := range []struct {
		name, gomod, module string
		refused             bool
	}{
		{"quoted", "module \"warden.dev/warden\"\n", "warden.dev/warden", false},
		{"tab and comment", "module\twarden.dev/warden\t// x\n", "warden.dev/warden", false},
		{"no module line", "go 1.26\n", "", true},
		{"empty module", "module // nothing\n", "", true},
		// Final review: "replace(" with no space opened no block, so the
		// local replace inside it passed. And any "=>" the parser does not
		// recognise as a replace is refused, so the next unforeseen syntax
		// fails closed.
		{"replace( without space", "module warden.dev/warden\n\nreplace(\n\twarden.dev/hid => ./_hid\n)\n", "warden.dev/warden", true},
		{"=> outside a replace", "module warden.dev/warden\n\nrequire (\n\texample.com/x => ./x\n)\n", "warden.dev/warden", true},
		{"module replace in replace(", "module warden.dev/warden\n\nreplace(\n\texample.com/a => example.com/b v1.0.0\n)\n", "warden.dev/warden", false},
	} {
		_, module, err := scanTree(fstest.MapFS{"go.mod": {Data: []byte(tc.gomod)}})
		if module != tc.module {
			t.Errorf("%s: module = %q, want %q", tc.name, module, tc.module)
		}
		if refused := errors.As(err, &se); refused != tc.refused {
			t.Errorf("%s: refused = %v (%v), want %v", tc.name, refused, err, tc.refused)
		}
	}
	// No go.mod at all (the seeded fixture tree): the import check is off and
	// nothing is refused.
	if _, module, err := scanTree(fstest.MapFS{"pkg/a.go": {Data: []byte("package pkg\n")}}); module != "" || err != nil {
		t.Errorf("tree without go.mod: module %q, err %v; want \"\", nil", module, err)
	}
}
