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
		"INV-J internal/exec/forward/other.go",   // net.ListenTCP beside the forwarder file
		"INV-J internal/exec/other.go",           // net.Listen("tcp", ...) elsewhere in the executor
		"imports apps/desktop/x.go",              // Go under apps/
		"imports internal/exec/bad.go",           // exec importing the daemon
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
