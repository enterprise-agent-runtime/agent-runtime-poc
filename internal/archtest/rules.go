package archtest

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
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

// listenFiles may open a TCP or UDP listener although they lie outside
// listenDirs. Each entry is one exact file, never a package or a directory, so
// a listener added anywhere else, including beside the entry, still fails
// INV-J. The only entry is the in-sandbox proxy forwarder, "warden-exec proxy"
// (CLAUDE.md §4 "Network"; docs/CONFLICTS.md C-46, C-26):
//   - Linux L1 and L2: it listens on 127.0.0.1:3128 inside the sandbox's
//     network namespace, which is not a host port.
//   - macOS L1 (Seatbelt): there is no network namespace, so it listens on an
//     ephemeral port on the shared host loopback. Any local process can reach
//     it while the task runs, and it reaches only that task's allowlist; this
//     is a risk accepted in design A06 §9 and WRD-10 T-23, not something this
//     check removes.
//
// INV-J's subject is the daemon on the host. The exemption is safe only
// together with ImportRules R4: no daemon package can import internal/exec,
// so the daemon cannot link this file and call its listener on the host.
var listenFiles = map[string]bool{"internal/exec/forward/listen.go": true}

// listenAllowed ends every INV-J message, so the reader learns where a
// listener may live without reading this package.
var listenAllowed = "; allowed only in " + strings.Join(listenDirs, ", ") + " and " + strings.Join(sortedKeys(listenFiles), ", ")

// dotWatchedExec and dotWatchedListen are the packages whose identifiers the rules look for. A
// dot-import makes them unqualified, so Scan cannot resolve their uses and
// the rules would be blind; such an import is therefore a violation itself.
var dotWatchedExec = []string{"os/exec", "golang.org/x/sys/execabs", "net/http/cgi", "go/build", "go/importer", "plugin", "os", "syscall", "golang.org/x/sys/unix", "golang.org/x/sys/windows"}
var dotWatchedListen = []string{"net", "net/http", "crypto/tls"}

// rawSocketPkgs expose Bind and Listen on raw sockets, bypassing package net,
// and the numbered system-call entry points in rawSyscalls.
var rawSocketPkgs = map[string]bool{"syscall": true, "golang.org/x/sys/unix": true, "golang.org/x/sys/windows": true}

// rawSyscalls call a system call by number, so they can fork, exec, bind or
// listen past every named check here. They are allowed in execPackages and
// in rawSyscallFiles only.
var rawSyscalls = map[string]bool{
	"Syscall": true, "Syscall6": true, "Syscall9": true, "Syscall12": true, "Syscall15": true, "Syscall18": true,
	"RawSyscall": true, "RawSyscall6": true, "SyscallN": true, "SyscallNoError": true, "RawSyscallNoError": true,
}

// rawSyscallFiles may make raw system calls outside execPackages. Each entry
// is one exact file. The entries are the host terminal helpers behind the
// CLI's passphrase prompt: ioctl TCGETS/TCSETS (TIOCGETA/TIOCSETA on macOS)
// to switch echo off and back on. They neither create a process nor open a
// socket.
var rawSyscallFiles = map[string]bool{"internal/platform/term_linux.go": true, "internal/platform/term_darwin.go": true}

// dllLoaders load a Windows DLL or resolve a function in one, or name the
// DLL and proc types. A resolved proc is called by name with arbitrary
// arguments (CreateProcessW, bind), past every named check here, so they are
// allowed only in execPackages and in dllFiles.
var dllLoaders = map[string]bool{
	"NewLazyDLL": true, "NewLazySystemDLL": true, "LoadDLL": true, "MustLoadDLL": true,
	"LoadLibrary": true, "LoadLibraryEx": true, "GetProcAddress": true,
	// The types, because a struct literal (&syscall.LazyDLL{Name: ...}) builds
	// a loader without calling any function above.
	"LazyDLL": true, "DLL": true, "LazyProc": true, "Proc": true,
}

// dllFiles may load DLLs outside execPackages. Each entry is one exact file,
// and each calls only the Windows functions named here, none of which creates
// a process or opens a socket:
//   - disk_windows.go: kernel32 GetDiskFreeSpaceExW (free-space check);
//   - files_windows.go: advapi32 SetFileSecurityW / GetFileSecurityW (the
//     owner-only DACL on files under WARDEN_HOME);
//   - term_windows.go: kernel32 SetConsoleMode (echo off for the passphrase
//     prompt).
var dllFiles = map[string]bool{
	"internal/platform/disk_windows.go":  true,
	"internal/platform/files_windows.go": true,
	"internal/platform/term_windows.go":  true,
}

// linknameFiles may carry a //go:linkname directive, which reaches
// unexported runtime and syscall functions past every check here. Empty: no
// file needs one, and adding an entry needs its own justification.
var linknameFiles = map[string]bool{}

// osDirs may select behaviour by operating system (CLAUDE.md §3, §6). The
// executor is included because its semantics are per OS (openat2, process
// groups) and it must still compile on Windows (docs/CONFLICTS.md C-61).
var osDirs = []string{"internal/platform", "internal/sandbox", "internal/secrets", "internal/exec", "cmd/warden-exec"}

// execImports start processes and are allowed, like os/exec itself, only in
// execPackages with a //sandboxed justification: golang.org/x/sys/execabs is
// a drop-in for os/exec (its Cmd is exec.Cmd), and net/http/cgi runs its
// handler programs through os/exec; go/build and go/importer run go list and
// pkg-config through os/exec; plugin loads and runs code at run time.
var execImports = []string{"os/exec", "golang.org/x/sys/execabs", "net/http/cgi", "go/build", "go/importer", "plugin"}

// processStarters are the calls that create a process without os/exec.
// Execveat and Fexecve are named for x/sys/unix although v0.48 has neither:
// the executor runs the checked file by descriptor through a raw
// SYS_EXECVEAT (M2-8), and a typed wrapper added later must be covered
// the day it appears (syscall has neither).
var processStarters = map[string]map[string]bool{
	"os":                       {"StartProcess": true},
	"syscall":                  {"ForkExec": true, "StartProcess": true, "Exec": true, "CreateProcess": true, "CreateProcessAsUser": true},
	"golang.org/x/sys/unix":    {"Exec": true, "ForkExec": true, "Execveat": true, "Fexecve": true},
	"golang.org/x/sys/windows": {"CreateProcess": true, "CreateProcessAsUser": true, "ShellExecute": true, "ShellExecuteEx": true},
}

// ExecRule implements INV-H statically: os/exec and the other execImports
// only in execPackages, with a //sandboxed justification; other process
// starters likewise, plus the documented daemon self-spawn. Raw numbered
// system calls only in execPackages and rawSyscallFiles; DLL loading only in
// execPackages and dllFiles; //go:linkname only in linknameFiles (none).
// Test files are included ("including tests").
func ExecRule(files []File) []Violation {
	var out []Violation
	for _, f := range files {
		for _, imp := range f.DotImports {
			if slices.Contains(dotWatchedExec, imp) {
				out = append(out, Violation{f.Rel, 0, "INV-H", fmt.Sprintf("dot-import of %q hides process creation from this check", imp)})
			}
		}
		for _, imp := range f.Imports {
			if !slices.Contains(execImports, imp) {
				continue
			}
			if !underAny(f.Dir, execPackages) {
				out = append(out, Violation{f.Rel, 0, "INV-H", imp + " imported outside " + strings.Join(execPackages, ", ")})
			} else if !f.Sandboxed {
				out = append(out, Violation{f.Rel, 0, "INV-H", imp + " imported without a //sandboxed: <reason> comment"})
			}
		}
		if !linknameFiles[f.Rel] {
			for _, line := range f.Linknames {
				out = append(out, Violation{f.Rel, line, "INV-H", "//go:linkname reaches unexported runtime and syscall code past this check"})
			}
		}
		for _, u := range f.Uses {
			if rawSocketPkgs[u.Pkg] && rawSyscalls[u.Name] && !underAny(f.Dir, execPackages) && !rawSyscallFiles[f.Rel] {
				out = append(out, Violation{f.Rel, u.Line, "INV-H", fmt.Sprintf("raw system call %s.%s outside the sandbox packages", path.Base(u.Pkg), u.Name)})
			}
			if (u.Pkg == "syscall" || u.Pkg == "golang.org/x/sys/windows") && dllLoaders[u.Name] && !underAny(f.Dir, execPackages) && !dllFiles[f.Rel] {
				out = append(out, Violation{f.Rel, u.Line, "INV-H", fmt.Sprintf("DLL loading %s.%s outside the sandbox packages", path.Base(u.Pkg), u.Name)})
			}
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
// listenDirs and listenFiles. net.Listen and net.ListenPacket are allowed only
// with a literal "unix", "unixpacket" or "unixgram" network. The net/http
// Server type is flagged wherever it is named, because its methods
// (ListenAndServe, Serve) are called on a value and are invisible to a check
// of package-qualified calls. Dot-imports of net and net/http are flagged for
// the same reason, as are dot-imports of crypto/tls. Also flagged:
// tls.Listen, tls.NewListener, net.FileListener, raw Listen and Bind from
// syscall and golang.org/x/sys/{unix,windows}, and importing net/http/httptest.
// Test files are exempt (httptest servers bind 127.0.0.1 inside the test
// process).
func ListenRule(files []File) []Violation {
	var out []Violation
	for _, f := range files {
		if f.Test || underAny(f.Dir, listenDirs) || listenFiles[f.Rel] {
			continue
		}
		for _, imp := range f.DotImports {
			if slices.Contains(dotWatchedListen, imp) {
				out = append(out, Violation{f.Rel, 0, "INV-J", fmt.Sprintf("dot-import of %q hides listeners from this check", imp) + listenAllowed})
			}
		}
		if slices.Contains(f.Imports, "net/http/httptest") {
			// httptest.NewServer listens on 127.0.0.1; it belongs in tests.
			out = append(out, Violation{f.Rel, 0, "INV-J", "net/http/httptest imported outside a test file" + listenAllowed})
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
			case u.Pkg == "net/http" && u.Name == "Server":
				bad = "http.Server (its ListenAndServe and Serve methods open or serve a listener)"
			case u.Pkg == "crypto/tls" && (u.Name == "Listen" || u.Name == "NewListener"):
				bad = "tls." + u.Name + " (a TLS listener is a TCP listener)"
			case u.Pkg == "net" && u.Name == "FileListener":
				bad = "net.FileListener (a listener from an inherited or raw socket)"
			case rawSocketPkgs[u.Pkg] && (u.Name == "Listen" || u.Name == "Bind"):
				// The address family is not visible statically, so a raw Bind
				// or Listen is flagged whatever it binds.
				bad = fmt.Sprintf("raw socket %s.%s", path.Base(u.Pkg), u.Name)
			}
			if bad != "" {
				out = append(out, Violation{f.Rel, u.Line, "INV-J", bad + listenAllowed})
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

// executorDirs hold the in-sandbox executor: the only places that may import
// internal/exec (design A02 §5 R4), and bound by R3 themselves.
var executorDirs = []string{"internal/exec", "cmd/warden-exec"}

// ImportRules implements the import rules of CLAUDE.md §6 (design A02 import
// matrix). module is the module path ("warden.dev/warden").
func ImportRules(module string, files []File) []Violation {
	var out []Violation
	for _, f := range files {
		if under(f.Dir, "apps") {
			out = append(out, Violation{f.Rel, 1, "imports", "apps/ contains no Go"})
			continue
		}
		if under(f.Dir, "cmd/warden-exec") && f.Package != "main" && f.Package != "main_test" {
			// The executor binary is wiring only; its code lives in
			// internal/exec, which R4 guards. A library package here would be
			// importable executor code outside R4's reach.
			out = append(out, Violation{f.Rel, 1, "imports", "only package main under cmd/warden-exec (found package " + f.Package + ")"})
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
			case underAny(f.Dir, executorDirs) && !f.Test:
				// Design A02 §5 R3: the executor shares only the wire types with
				// the daemon. R10: buildinfo reaches internal/exec through
				// exec.Config, so only the binary imports it. Third-party: only
				// golang.org/x/sys (CLAUDE.md §4, D-041). Test files are exempt,
				// as in A02's depguard rule ("!$test"); R4 still binds them.
				binary := under(f.Dir, "cmd/warden-exec")
				if isInternal && !under(internal, "internal/exec") && !under(internal, "internal/execproto") && !(binary && internal == "internal/buildinfo") {
					bad = "R3: the executor imports only internal/exec/..., internal/execproto and (cmd/warden-exec only) internal/buildinfo"
				} else if !isInternal && !isStdlib(imp) && !under(imp, "golang.org/x/sys") {
					bad = "R3: the executor imports no third-party module except golang.org/x/sys"
				}
			case under(f.Dir, "internal/execproto") && !f.Test:
				// Design A02 §5 R6: execproto is a stdlib-only leaf. The wire
				// types and the deny-list (internal/execproto/denylist, D-014,
				// design A06 §3.1/§17) are linked into the daemon, the policy
				// engine, the sandbox mount layer and the executor (INV-I), so
				// anything it imports would cross R3 into the executor. Test
				// files are exempt (the golden tests use jsonschema).
				if isInternal || !isStdlib(imp) {
					bad = "R6: internal/execproto/... imports only the standard library"
				}
			case under(f.Dir, "internal/policy"):
				if isInternal && (under(internal, "internal/providers") || under(internal, "internal/harness")) {
					bad = "internal/policy imports no provider or harness package"
				}
			}
			if isInternal && under(internal, "internal/exec") && !underAny(f.Dir, executorDirs) {
				// Design A02 §5 R4: executor code is never linked into the
				// daemon. It is also what keeps listenFiles safe: a daemon
				// package cannot import the forwarder and listen on the host.
				// Test files are included.
				bad = "R4: only internal/exec/... and cmd/warden-exec may import internal/exec/..."
			}
			if isInternal && under(internal, "cmd") {
				// cmd/ holds binaries; nothing imports them. Without this a
				// library under cmd/warden-exec could carry the forwarder into
				// the daemon around R4. Test files are included.
				bad = "nothing imports a package under cmd/"
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

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
