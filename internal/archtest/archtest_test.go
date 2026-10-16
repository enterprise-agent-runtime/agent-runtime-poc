package archtest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
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
		"imports internal/policy/bad.go",          // policy importing a provider
		"imports internal/providers/bad/bad.go",   // third-party import in a provider
		"imports internal/providers/bad/bad.go",   // provider importing internal/store
		"os-selection internal/api/os.go",         // //go:build linux
		"os-selection internal/api/os.go",         // runtime.GOOS
		"os-selection internal/api/x_windows.go",  // file-name suffix
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
