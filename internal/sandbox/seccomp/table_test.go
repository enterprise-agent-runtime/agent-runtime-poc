package seccomp

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"testing"
)

// The expectations below are copied from design A06 §11.5 by hand, not
// derived from Table(): the test is the document's check on the data, so a
// row dropped, an action changed or a syscall misspelled in table.go fails
// here (WRD-16 §10.3 "unshare/setns after launch", WRD-10 §5.2).

// a06Unconditional maps every syscall A06 §11.5 refuses outright to its
// verdict.
func a06Unconditional() map[string]verdict {
	m := map[string]verdict{}
	set := func(v verdict, names ...string) {
		for _, n := range names {
			m[n] = v
		}
	}
	set(vKill, "kexec_load", "kexec_file_load", "init_module", "finit_module", "delete_module",
		"open_by_handle_at", "iopl", "ioperm")
	set(vErrno(1), "ptrace", "process_vm_readv", "process_vm_writev")
	// Stricter than A06: the other ptrace-class calls. Review of M2-14 stole
	// an fd with pidfd_getfd while ptrace was EPERM (CWE-269). pidfd_open
	// itself stays allowed (a control below).
	set(vErrno(1), "pidfd_getfd", "process_madvise", "kcmp")
	set(vErrno(1), "mount", "umount2", "pivot_root", "chroot", "move_mount", "open_tree", "fsopen",
		"fsconfig", "fsmount", "fspick", "mount_setattr")
	set(vErrno(1), "unshare", "setns")
	set(vErrno(38), "clone3")
	set(vErrno(1), "keyctl", "add_key", "request_key")
	set(vErrno(1), "bpf", "perf_event_open", "userfaultfd")
	set(vErrno(38), "io_uring_setup", "io_uring_enter", "io_uring_register")
	set(vErrno(1), "reboot", "swapon", "swapoff", "acct", "quotactl", "syslog", "settimeofday",
		"clock_settime", "clock_adjtime", "sethostname", "setdomainname", "vhangup",
		"name_to_handle_at", "fanotify_init", "lookup_dcookie", "uselib", "nfsservctl")
	set(vErrno(1), "modify_ldt", "_sysctl")
	return m
}

// a06Filtered are the syscalls A06 §11.5 refuses only for some arguments.
var a06Filtered = []string{"clone", "socket", "ioctl", "personality"}

// a06AMD64Only are the syscalls the A06 "Arch notes" column marks amd64-only
// and that aarch64 really lacks. nfsservctl is marked too, but aarch64 has
// the number (42, sys_ni_syscall), so the generator keeps it there.
var a06AMD64Only = []string{"_sysctl", "ioperm", "iopl", "modify_ldt", "uselib"}

// argCase is one call with chosen arguments and the verdict A06 requires.
type argCase struct {
	name string
	args [6]uint64
	want verdict
}

func a06ArgCases() []argCase {
	var cs []argCase
	add := func(name string, idx int, val uint64, want verdict) {
		var a [6]uint64
		a[idx] = val
		cs = append(cs, argCase{name, a, want})
	}
	// clone: refused only when a namespace flag is set. The flags a thread
	// or fork uses, CLONE_NEWTIME (low byte = exit signal in clone) and bits
	// above 32 (the kernel takes lower_32_bits) pass.
	const threadFlags = 0x00000100 | 0x00000200 | 0x00000400 | 0x00000800 | 0x00010000 | 0x00040000 | 0x00080000 | 0x00100000 | 0x00200000
	add("clone", 0, 0, vAllow)
	add("clone", 0, 17, vAllow) // SIGCHLD: fork()
	add("clone", 0, threadFlags, vAllow)
	add("clone", 0, 0x80, vAllow)
	add("clone", 0, 0xffffffff_00000000|threadFlags, vAllow)
	for _, f := range []uint64{0x00020000, 0x02000000, 0x04000000, 0x08000000, 0x10000000, 0x20000000, 0x40000000} {
		add("clone", 0, f, vErrno(1))
		add("clone", 0, f|threadFlags|17, vErrno(1))
	}
	// socket: Unix, IPv4, IPv6 and netlink only.
	for _, af := range []uint64{1, 2, 10, 16} {
		add("socket", 0, af, vAllow)
	}
	for _, af := range []uint64{0, 3, 9, 11, 15, 17 /* AF_PACKET */, 31 /* AF_BLUETOOTH */, 38 /* AF_ALG */, 40 /* AF_VSOCK */, 0xffffffff} {
		add("socket", 0, af, vErrno(97))
	}
	add("socket", 0, 0xffffffff_00000011, vErrno(97)) // AF_PACKET with junk upper bits
	// ioctl: only the request TIOCSTI or TIOCLINUX in args[1].
	add("ioctl", 1, 0x5412, vErrno(1))
	add("ioctl", 1, 0x541c, vErrno(1))
	add("ioctl", 1, 0xffffffff_00005412, vErrno(1)) // the kernel truncates the request to 32 bits
	for _, req := range []uint64{0, 0x5401 /* TCGETS */, 0x541b /* FIONREAD */, 0x5411, 0x5413} {
		add("ioctl", 1, req, vAllow)
	}
	add("ioctl", 0, 0x5412, vAllow) // TIOCSTI as the fd, not the request
	// personality: the plain personas and the query pass, anything else fails.
	for _, p := range []uint64{0, 0x8, 0x20000, 0x20008, 0xffffffff} {
		add("personality", 0, p, vAllow)
	}
	for _, p := range []uint64{0x0040000 /* ADDR_NO_RANDOMIZE */, 0x0040008, 1, 0x20000 | 0x0040000, 0xfffffffe} {
		add("personality", 0, p, vErrno(1))
	}
	return cs
}

func callNr(t *testing.T, a Arch, name string) uint32 {
	t.Helper()
	nr, ok := a.Nr(name)
	if !ok {
		t.Fatalf("%s: no number for %s", a, name)
	}
	return nr
}

func runFor(t *testing.T, p []Instruction, a Arch, nr uint32, args [6]uint64) verdict {
	t.Helper()
	r, err := run(p, seccompData{nr: nr, arch: a.AuditArch(), ip: 0x401000, args: args})
	if err != nil {
		t.Fatalf("%s nr %d: %v", a, nr, err)
	}
	return decodeRet(r)
}

func programFor(t *testing.T, a Arch) []Instruction {
	t.Helper()
	raw, err := Render(a)
	if err != nil {
		t.Fatalf("Render(%s): %v", a, err)
	}
	p, err := decode(raw)
	if err != nil {
		t.Fatalf("Render(%s): %v", a, err)
	}
	if err := check(p); err != nil {
		t.Fatalf("Render(%s) would be refused by the kernel: %v", a, err)
	}
	return p
}

// TestSeccompTable_A06 runs the rendered program, for both architectures,
// over every A06 §11.5 row and the controls: each listed syscall gets exactly
// its action, argument-filtered syscalls are refused only for the bad
// arguments, a foreign architecture and the x32 ABI are killed, and ordinary
// syscalls are allowed.
func TestSeccompTable_A06(t *testing.T) {
	for _, a := range Arches() {
		t.Run(a.String(), func(t *testing.T) {
			p := programFor(t, a)
			t.Logf("%s program: %d instructions", a, len(p))
			junk := [6]uint64{0xdeadbeef, 0x5412, 0x40000, 17, 0xffffffff_ffffffff, 1}

			for name, want := range a06Unconditional() {
				nr, ok := a.Nr(name)
				if !ok {
					if a == ARM64 && slices.Contains(a06AMD64Only, name) {
						continue
					}
					t.Fatalf("%s: A06 lists %s but it has no number", a, name)
				}
				for _, args := range [][6]uint64{{}, junk} {
					if got := runFor(t, p, a, nr, args); got != want {
						t.Errorf("%s(%v) = %v, A06 §11.5 wants %v", name, args, got, want)
					}
				}
			}

			for _, c := range a06ArgCases() {
				if got := runFor(t, p, a, callNr(t, a, c.name), c.args); got != c.want {
					t.Errorf("%s(%#x) = %v, A06 §11.5 wants %v", c.name, c.args, got, c.want)
				}
			}

			for _, name := range []string{"read", "write", "openat", "execve", "getpid", "pidfd_open"} {
				for _, args := range [][6]uint64{{}, junk} {
					if got := runFor(t, p, a, callNr(t, a, name), args); got != vAllow {
						t.Errorf("control %s = %v, want allow", name, got)
					}
				}
			}

			// Every other number up to well past the end of both tables is
			// allowed: the filter is a deny-list (A06 §11.5 last line).
			listed := map[uint32]bool{}
			for _, n := range append(slices.Collect(maps.Keys(a06Unconditional())), a06Filtered...) {
				if nr, ok := a.Nr(n); ok {
					listed[nr] = true
				}
			}
			for nr := uint32(0); nr < 600; nr++ {
				if listed[nr] {
					continue
				}
				if got := runFor(t, p, a, nr, junk); got != vAllow {
					t.Errorf("unlisted nr %d = %v, want allow", nr, got)
				}
			}

			// Foreign architectures die whatever the number: i386 and x32
			// table confusion (A06 §11.5 first row).
			others := []uint32{0, 0x40000003 /* AUDIT_ARCH_I386 */, 0x40000028 /* ARM */}
			for _, o := range Arches() {
				if o != a {
					others = append(others, o.AuditArch())
				}
			}
			for _, arch := range others {
				for _, nr := range []uint32{callNr(t, a, "read"), callNr(t, a, "getpid")} {
					r, err := run(p, seccompData{nr: nr, arch: arch})
					if err != nil {
						t.Fatal(err)
					}
					if got := decodeRet(r); got != vKill {
						t.Errorf("arch %#x nr %d = %v, want kill", arch, nr, got)
					}
				}
			}
			if a == AMD64 {
				for _, nr := range []uint32{0x40000000, 0x40000000 + 39, 0x40000000 + 101, 0xffffffff} {
					if got := runFor(t, p, a, nr, [6]uint64{}); got != vKill {
						t.Errorf("x32 nr %#x = %v, want kill", nr, got)
					}
				}
			}
		})
	}
}

// TestTable_MatchesA06Names keeps table.go and the A06 list above in step:
// the same syscall names, each in exactly one row, and the same
// architecture gaps (a misspelled name has no number on either arch).
func TestTable_MatchesA06Names(t *testing.T) {
	want := slices.Collect(maps.Keys(a06Unconditional()))
	want = append(want, a06Filtered...)
	sort.Strings(want)

	var got []string
	seen := map[string]bool{}
	for _, r := range Table() {
		for _, s := range r.Syscalls {
			if seen[s.Name] {
				t.Errorf("%s appears in more than one row", s.Name)
			}
			seen[s.Name] = true
			got = append(got, s.Name)
		}
	}
	sort.Strings(got)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("table syscalls\n got %v\nwant %v", got, want)
	}

	for _, a := range Arches() {
		var missing []string
		for _, n := range got {
			if _, ok := a.Nr(n); !ok {
				missing = append(missing, n)
			}
		}
		var wantMissing []string
		if a == ARM64 {
			wantMissing = a06AMD64Only
		}
		if fmt.Sprint(missing) != fmt.Sprint(wantMissing) {
			t.Errorf("%s lacks %v, want %v", a, missing, wantMissing)
		}
	}
}

// TestVersion pins the limits value A06 §13.2 shows.
func TestVersion(t *testing.T) {
	if Version != "v1" {
		t.Errorf("Version = %q, want v1", Version)
	}
}
