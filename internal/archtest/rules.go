package archtest

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// execPackages may create processes (CLAUDE.md §5 INV-H, §9 rule 1). Every
// file that does so must carry a "//sandboxed: <reason>" comment.
var execPackages = []string{"internal/sandbox", "internal/exec", "internal/worktree", "internal/harness"}

// spawnFiles may call os.StartProcess outside execPackages. The only entry is
// the daemon self-spawn used by "warden daemon start": a host-trusted start
// of wardend itself, never an agent-requested process (docs/DECISIONS-poc.md
// D-006). It may not import os/exec.
var spawnFiles = regexp.MustCompile(`^internal/platform/spawn(_[a-z]+)?\.go$`)

// listenDirs may open a TCP or UDP listener (CLAUDE.md §5 INV-J): the L2 proxy
// sidecar, which listens only inside the task's internal Docker network.
var listenDirs = []string{"cmd/warden-proxy"}

// osDirs may select behaviour by operating system (CLAUDE.md §3, §6).
var osDirs = []string{"internal/platform", "internal/sandbox", "internal/secrets"}

// processStarters are the calls that create a process without os/exec.
var processStarters = map[string]map[string]bool{
	"os":                       {"StartProcess": true},
	"syscall":                  {"ForkExec": true, "StartProcess": true, "Exec": true},
	"golang.org/x/sys/unix":    {"Exec": true, "ForkExec": true},
	"golang.org/x/sys/windows": {"CreateProcess": true, "CreateProcessAsUser": true},
}

// ExecRule implements INV-H statically: os/exec only in execPackages, with a
// //sandboxed justification; other process starters likewise, plus the
// documented daemon self-spawn. Test files are included ("including tests").
func ExecRule(files []File) []Violation {
	var out []Violation
	for _, f := range files {
		for _, imp := range f.Imports {
			if imp != "os/exec" {
				continue
			}
			if !underAny(f.Dir, execPackages) {
				out = append(out, Violation{f.Rel, 0, "INV-H", "os/exec imported outside " + strings.Join(execPackages, ", ")})
			} else if !f.Sandboxed {
				out = append(out, Violation{f.Rel, 0, "INV-H", "os/exec imported without a //sandboxed: <reason> comment"})
			}
		}
		for _, u := range f.Uses {
			if !processStarters[u.Pkg][u.Name] {
				continue
			}
			allowed := underAny(f.Dir, execPackages) || spawnFiles.MatchString(f.Rel)
			switch {
			case !allowed:
				out = append(out, Violation{f.Rel, u.Line, "INV-H", fmt.Sprintf("%s.%s outside the sandbox packages", u.Pkg, u.Name)})
			case !f.Sandboxed:
				out = append(out, Violation{f.Rel, u.Line, "INV-H", fmt.Sprintf("%s.%s without a //sandboxed: <reason> comment", u.Pkg, u.Name)})
			}
		}
	}
	return out
}

// ListenRule implements INV-J statically: no TCP or UDP listener outside
// listenDirs. net.Listen and net.ListenPacket are allowed only with a literal
// "unix", "unixpacket" or "unixgram" network. Test files are exempt (httptest
// servers bind 127.0.0.1 inside the test process).
func ListenRule(files []File) []Violation {
	var out []Violation
	for _, f := range files {
		if f.Test || underAny(f.Dir, listenDirs) {
			continue
		}
		for _, u := range f.Uses {
			bad := ""
			switch {
			case u.Pkg == "net" && (u.Name == "Listen" || u.Name == "ListenPacket"):
				if u.Arg0 != "unix" && u.Arg0 != "unixpacket" && u.Arg0 != "unixgram" {
					bad = fmt.Sprintf("net.%s(%q) is not a Unix socket", u.Name, u.Arg0)
				}
			case u.Pkg == "net" && (u.Name == "ListenTCP" || u.Name == "ListenUDP" || u.Name == "ListenIP" || u.Name == "ListenMulticastUDP" || u.Name == "ListenConfig"):
				bad = "net." + u.Name
			case u.Pkg == "net/http" && (u.Name == "ListenAndServe" || u.Name == "ListenAndServeTLS"):
				bad = "http." + u.Name
			}
			if bad != "" {
				out = append(out, Violation{f.Rel, u.Line, "INV-J", bad + " outside " + strings.Join(listenDirs, ", ")})
			}
		}
	}
	return out
}

var osToken = regexp.MustCompile(`\b(linux|windows|darwin|unix|freebsd|openbsd|netbsd|android|ios|plan9|solaris|illumos|aix|js|wasip1)\b`)
var osSuffix = regexp.MustCompile(`_(linux|windows|darwin|freebsd|openbsd|netbsd|android|ios|plan9|solaris|illumos|aix|js|wasip1)(_[a-z0-9]+)?(_test)?\.go$`)

// OSRule confines OS selection (runtime.GOOS, OS build constraints, OS file
// suffixes) to osDirs (CLAUDE.md §3 "Nothing above that package may contain
// runtime.GOOS checks", §6). Test files are exempt: OS-specific tests use
// build tags wherever the code under test lives (CLAUDE.md §8.6). spikes/ is
// exempt (throwaway programs outside the product).
func OSRule(files []File) []Violation {
	var out []Violation
	for _, f := range files {
		if f.Test || underAny(f.Dir, osDirs) || under(f.Dir, "spikes") {
			continue
		}
		for _, u := range f.Uses {
			if u.Pkg == "runtime" && u.Name == "GOOS" {
				out = append(out, Violation{f.Rel, u.Line, "os-selection", "runtime.GOOS outside " + strings.Join(osDirs, ", ")})
			}
		}
		if osToken.MatchString(f.Constraint) {
			out = append(out, Violation{f.Rel, 1, "os-selection", "OS build constraint " + f.Constraint + " outside " + strings.Join(osDirs, ", ")})
		}
		if osSuffix.MatchString(path.Base(f.Rel)) {
			out = append(out, Violation{f.Rel, 1, "os-selection", "OS file-name suffix outside " + strings.Join(osDirs, ", ")})
		}
	}
	return out
}

// ImportRules implements the import rules of CLAUDE.md §6 (design A02 import
// matrix). module is the module path ("warden.dev/warden").
func ImportRules(module string, files []File) []Violation {
	var out []Violation
	for _, f := range files {
		if under(f.Dir, "apps") {
			out = append(out, Violation{f.Rel, 1, "imports", "apps/ contains no Go"})
			continue
		}
		for _, imp := range f.Imports {
			internal, isInternal := strings.CutPrefix(imp, module+"/")
			if imp == module {
				internal, isInternal = "", true
			}
			bad := ""
			switch {
			case under(f.Dir, "internal/providers"):
				// Providers see only the canonical model types and the stdlib.
				if isInternal && !under(internal, "internal/model") {
					bad = "providers import only internal/model"
				} else if !isInternal && !isStdlib(imp) && !f.Test {
					bad = "providers import no third-party packages"
				}
			case under(f.Dir, "internal/harness"):
				// Harnesses see internal/model, the stdlib and their vendor SDK.
				if isInternal && !under(internal, "internal/model") {
					bad = "harnesses import only internal/model"
				} else if !isInternal && !isStdlib(imp) && !f.Test && !strings.HasPrefix(imp, "github.com/github/copilot-sdk/") {
					bad = "harnesses import only their vendor SDK"
				}
			case under(f.Dir, "internal/exec"):
				if isInternal && !under(internal, "internal/exec") {
					bad = "internal/exec imports nothing from the daemon"
				}
			case under(f.Dir, "internal/policy"):
				if isInternal && (under(internal, "internal/providers") || under(internal, "internal/harness")) {
					bad = "internal/policy imports no provider or harness package"
				}
			}
			if isInternal && under(internal, "spikes") {
				bad = "nothing imports spikes/"
			}
			if bad != "" {
				out = append(out, Violation{f.Rel, 0, "imports", bad + " (imports " + imp + ")"})
			}
		}
	}
	return out
}
