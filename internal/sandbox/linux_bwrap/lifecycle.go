package linux_bwrap

// The pure parts of the L1 lifecycle of design A06 §13: the state file
// that lets a later daemon find an orphan (§13.2 step 4, §13.5), bwrap's
// info-fd report, the exec.hello check (§13.2 step 5), and the scope
// sweep (§11.6, §13.5). Nothing here starts or signals a process; the
// Linux manager (manager_linux.go) does, through these.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/platform"
	"warden.dev/warden/internal/sandbox"
)

// Backend is sandbox.create backend for this package.
const Backend = "bwrap"

// File names under S/sandbox/<sb_id>/ (A06 §2, §13.2, §13.3 step 5).
const (
	StateFile = "state.json" // removed at destroy
	ArgvFile  = "argv.json"  // kept until session retention
	EtcDir    = "etc"        // the generated /etc files; removed at destroy
)

// maxStateFile bounds a state.json read.
const maxStateFile = 4096

// State is S/sandbox/<sb_id>/state.json (A06 §13.2 step 4), written
// owner-only once bwrap reported its child: enough for a later daemon of
// the same home to recognise the sandbox's root process (pid and start
// time, so a reused pid is never killed) and its scope.
type State struct {
	SandboxID     string `json:"sandbox_id"`
	SessionID     string `json:"session_id"`
	HomeHash      string `json:"home_hash"` // platform.HomeHash of the Warden home (D-042)
	Backend       string `json:"backend"`
	RootPID       int    `json:"root_pid"`        // bwrap on the host
	RootStartTime uint64 `json:"root_start_time"` // /proc/<pid>/stat field 22
	ChildPID      int    `json:"child_pid"`       // warden-exec, as bwrap reported it (info fd)
	ScopeUnit     string `json:"scope_unit,omitempty"`
	// BootID is /proc/sys/kernel/random/boot_id when the root started: a
	// pid and start time are unique only within one boot, so a state file
	// from an earlier boot never gets a process killed (round 1, item 7).
	BootID string `json:"boot_id"`
}

// Check verifies that a state file read from
// <wardenHome>/sessions/<sessionDir>/sandbox/<dirID> describes a sandbox
// of this home, of this session and of this backend: a file copied from
// another home or session, or naming another sandbox's scope, is refused
// rather than acted on. Session directories are the session ulid, in
// either case (D-056).
func (s State) Check(wardenHome, sessionDir, dirID string) error {
	switch {
	case !strings.EqualFold("ses_"+sessionDir, s.SessionID) || !strings.HasPrefix(s.SessionID, "ses_"):
		return fmt.Errorf("state of session %q found in session directory %q", s.SessionID, sessionDir)
	case s.BootID == "" || len(s.BootID) > 64:
		return fmt.Errorf("state has no boot id")
	case s.SandboxID != dirID || !validSandboxID(s.SandboxID):
		return fmt.Errorf("state names sandbox %q in the directory of %q", s.SandboxID, dirID)
	case s.HomeHash != platform.HomeHash(wardenHome):
		return fmt.Errorf("state of home %q, not of this home", s.HomeHash)
	case s.Backend != Backend:
		return fmt.Errorf("state of backend %q", s.Backend)
	case s.RootPID <= 1 || s.RootStartTime == 0 || s.ChildPID < 0:
		return fmt.Errorf("state root pid %d start %d child %d", s.RootPID, s.RootStartTime, s.ChildPID)
	case s.ScopeUnit != "" && s.ScopeUnit != sandbox.ScopeUnit(wardenHome, s.SandboxID):
		return fmt.Errorf("state names scope %q, not this sandbox's", s.ScopeUnit)
	}
	return nil
}

// ParseState decodes a state file strictly.
func ParseState(b []byte) (State, error) {
	if len(b) > maxStateFile {
		return State{}, fmt.Errorf("state file is %d bytes", len(b))
	}
	var s State
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return State{}, fmt.Errorf("state file: %w", err)
	}
	return s, nil
}

func validSandboxID(id string) bool { return sandbox.ValidID(id) }

// ParseInfo reads bwrap's --info-fd report, one JSON object such as
// {"child-pid": 12345, "cgroup-namespace": ...}, and returns the child pid
// (warden-exec on the host). It reads only that object, so a writer that
// keeps the pipe open does not block it.
func ParseInfo(r io.Reader) (int, error) {
	var v struct {
		ChildPID *int `json:"child-pid"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 4096)).Decode(&v); err != nil {
		return 0, fmt.Errorf("%w: bwrap info fd: %w", sandbox.ErrUnavailable, err)
	}
	if v.ChildPID == nil || *v.ChildPID <= 1 {
		return 0, fmt.Errorf("%w: bwrap info fd has no child pid", sandbox.ErrUnavailable)
	}
	return *v.ChildPID, nil
}

// ParseStartTime returns field 22 (starttime) of a /proc/<pid>/stat line.
// The command name (field 2) is in parentheses and may contain spaces and
// parentheses, so fields are counted after the last ")".
func ParseStartTime(stat string) (uint64, error) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, errors.New("stat: no command name")
	}
	f := strings.Fields(stat[i+1:])
	// f[0] is field 3 (state); starttime is field 22, f[19].
	if len(f) < 20 {
		return 0, fmt.Errorf("stat: %d fields after the name", len(f))
	}
	return strconv.ParseUint(f[19], 10, 64)
}

// ParseProcCgroup returns the cgroup v2 path ("0::<path>") of a
// /proc/<pid>/cgroup file, or "" when there is none.
func ParseProcCgroup(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			return p
		}
	}
	return ""
}

// ExpectedRoots are the roots warden-exec must report in exec.hello for
// a tool sandbox: the --roots of the argv (A06 §3.1, §11.1).
func ExpectedRoots() []execproto.HelloRoot {
	return []execproto.HelloRoot{
		{Name: string(execproto.RootWorktree), Path: sandbox.TargetWorktree, Mode: execproto.RootRW},
		{Name: string(execproto.RootScratch), Path: sandbox.TargetScratch, Mode: execproto.RootRW},
		{Name: string(execproto.RootCache), Path: sandbox.TargetCache, Mode: execproto.RootRW},
		{Name: string(execproto.RootGit), Path: sandbox.TargetGit, Mode: execproto.RootRW},
	}
}

// ErrHelloMismatch is an exec.hello that does not match the launch
// (A06 §13.1 launching -> failed; sandbox.destroy reason hello_mismatch).
var ErrHelloMismatch = errors.New("exec.hello does not match the launch")

// CheckHello compares exec.hello with the Spec (A06 §13.2 step 5):
// protocol, mode, roots (in any order) and the profiles digest.
func CheckHello(s *sandbox.Spec, r execproto.HelloResult) error {
	fail := func(what string, got, want any) error {
		return fmt.Errorf("%w: %w: %s %v, want %v", sandbox.ErrUnavailable, ErrHelloMismatch, what, got, want)
	}
	if r.Protocol != execproto.Protocol {
		return fail("protocol", r.Protocol, execproto.Protocol)
	}
	if r.Mode != s.Mode {
		return fail("mode", r.Mode, s.Mode)
	}
	if r.ProfilesDigest != s.ProfilesDigest {
		return fail("profiles digest", r.ProfilesDigest, s.ProfilesDigest)
	}
	got := slices.Clone(r.Roots)
	want := ExpectedRoots()
	key := func(a, b execproto.HelloRoot) int { return strings.Compare(a.Name, b.Name) }
	slices.SortFunc(got, key)
	slices.SortFunc(want, key)
	if !slices.Equal(got, want) {
		return fail("roots", got, want)
	}
	return nil
}

// maxScopeDepth bounds the scope search below the user manager's cgroup:
// systemd puts a user scope in app.slice (or a nested slice), never deep.
const maxScopeDepth = 3

// FindScopes returns the cgroup directories below serviceDir (the user
// manager's cgroup, sandbox.UserServiceCgroup) whose name is a sandbox
// scope of wardenHome, keyed by sandbox id. Only this home's scopes match
// (sandbox.ScopeSandbox); symbolic links are not followed. A missing
// serviceDir is no scopes.
func FindScopes(serviceDir, wardenHome string) (map[string]string, error) {
	out := map[string]string{}
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		ents, err := os.ReadDir(dir)
		if err != nil {
			if depth == 0 && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		for _, e := range ents {
			if !e.IsDir() || e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if id, ok := sandbox.ScopeSandbox(wardenHome, e.Name()); ok {
				out[id] = p
				continue
			}
			if depth+1 < maxScopeDepth && strings.HasSuffix(e.Name(), ".slice") {
				if err := walk(p, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return out, walk(serviceDir, 0)
}

// KillCgroup kills every process in the cgroup dir: it writes "1" to
// cgroup.kill (Linux >= 5.14, A06 §11.6), or, where that file does not
// exist, kills each pid of cgroup.procs with kill. It never creates a
// file. It returns how many processes were in the cgroup; a cgroup that
// is already gone (the scope was collected) has none.
func KillCgroup(dir string, kill func(pid int) error) (int, error) {
	pids, err := cgroupProcs(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "cgroup.kill"), os.O_WRONLY, 0)
	if err == nil {
		_, werr := f.WriteString("1")
		cerr := f.Close()
		return len(pids), errors.Join(werr, cerr)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return len(pids), err
	}
	var errs []error
	for _, p := range pids {
		errs = append(errs, kill(p))
	}
	return len(pids), errors.Join(errs...)
}

func cgroupProcs(dir string) ([]int, error) {
	f, err := os.Open(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, l := range strings.Fields(string(b)) {
		n, err := strconv.Atoi(l)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("cgroup.procs: bad pid %q", l)
		}
		pids = append(pids, n)
	}
	return pids, nil
}

// CgroupCounters are the scope's event counters A06 §7.4 reads: oom_kill
// of memory.events and max of pids.events.
type CgroupCounters struct {
	OOMKill uint64
	PidsMax uint64
}

// ReadCgroupCounters reads them from a scope's cgroup directory. A file
// that does not exist (no delegation, or the scope was collected) counts
// zero; a malformed one is an error.
func ReadCgroupCounters(dir string) (CgroupCounters, error) {
	var c CgroupCounters
	var err error
	if c.OOMKill, err = readEventsKey(filepath.Join(dir, "memory.events"), "oom_kill"); err != nil {
		return c, err
	}
	c.PidsMax, err = readEventsKey(filepath.Join(dir, "pids.events"), "max")
	return c, err
}

func readEventsKey(p, key string) (uint64, error) {
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, " ")
		if !ok || k != key {
			continue
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s: %s %q", p, key, v)
		}
		return n, nil
	}
	return 0, nil
}

// RootExit is how the sandbox root (bwrap) ended: Exited with Code, or
// killed by Signal ("SIGSYS", ...).
type RootExit struct {
	Exited bool
	Code   int
	Signal string
}

// sigsysCode is bwrap's exit status when its child died of SIGSYS: as PID
// 1 it exits 128 + the signal (bubblewrap.c propagate_exit_status).
const sigsysCode = 128 + 31

// End is what the daemon adds to sandbox.destroy about how the sandbox
// ended (A06 §7.4).
type End struct {
	Resource    *execproto.WaitResource
	SeccompKill bool
}

// ClassifyEnd maps the daemon's observations to the A06 §7.4 rows it
// owns: memory when the scope's oom_kill rose since before, pids when its
// pids max rose, disk when the free-space watchdog fired (that order when
// several hold), and seccomp_kill when bwrap exited 159 or died of SIGSYS
// (root nil: still running). tmp_full is the executor's to report (M2-10):
// A06 §7.4's tmp_full row is per process exit, which the executor sees as
// it happens, and a statfs of /tmp is trivial for it there. (The daemon
// can reach that tmpfs too, through /proc/<pid>/root/tmp, since it owns
// the sandbox's user namespace; that is not why the row is split.)
func ClassifyEnd(before, after CgroupCounters, root *RootExit, diskLow bool) End {
	var e End
	switch {
	case after.OOMKill > before.OOMKill:
		e.Resource = &execproto.WaitResource{Kind: execproto.ResourceMemory, Detail: "cgroup oom_kill"}
	case after.PidsMax > before.PidsMax:
		e.Resource = &execproto.WaitResource{Kind: execproto.ResourcePids, Detail: "cgroup pids.max reached"}
	case diskLow:
		e.Resource = &execproto.WaitResource{Kind: execproto.ResourceDisk, Detail: "free space below the disk floor"}
	}
	if root != nil {
		e.SeccompKill = (root.Exited && root.Code == sigsysCode) || (!root.Exited && root.Signal == "SIGSYS")
	}
	return e
}

// DiskLow reports free bytes below the floor (A06 §7.3 disk row; a zero
// floor disables the watchdog).
func DiskLow(free uint64, floorMB int) bool {
	return floorMB > 0 && free < uint64(floorMB)<<20
}
