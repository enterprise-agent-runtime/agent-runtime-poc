package seccomp

// Version is the filter version reported as "seccomp" in sandbox.create
// limits (A06 §13.2). Bump it when the rendered program or profile changes
// what it allows.
const Version = "v1"

// Kernel constants the table uses, each checked against golang.org/x/sys/unix
// by consts_linux.go on Linux. The values are the same on x86_64 and aarch64.
const (
	// include/uapi/asm-generic/errno-base.h, errno.h.
	errEPERM        = 1
	errENOSYS       = 38
	errEAFNOSUPPORT = 97

	// include/uapi/linux/sched.h.
	cloneNewNS     = 0x00020000
	cloneNewCgroup = 0x02000000
	cloneNewUTS    = 0x04000000
	cloneNewIPC    = 0x08000000
	cloneNewUser   = 0x10000000
	cloneNewPID    = 0x20000000
	cloneNewNet    = 0x40000000

	// include/linux/socket.h.
	afUnix    = 1
	afInet    = 2
	afInet6   = 10
	afNetlink = 16

	// include/uapi/asm-generic/ioctls.h (x86 and arm64 both use it).
	ioctlTIOCSTI   = 0x5412
	ioctlTIOCLINUX = 0x541c

	// include/uapi/linux/personality.h. Not in x/sys/unix.
	perLinux   = 0x0000
	perLinux32 = 0x0008
	perUname26 = 0x0020000
	perQuery   = 0xffffffff // personality(0xffffffff) only reads the persona
)

// nsFlags are the clone flags that create a namespace (A06 §11.5 NS_FLAGS).
// CLONE_NEWTIME (0x80) is absent on purpose: in clone() the low byte is the
// exit signal, so the bit cannot mean a time namespace there; unshare and
// clone3 are refused whole.
const nsFlags = cloneNewNS | cloneNewUTS | cloneNewIPC | cloneNewUser | cloneNewPID | cloneNewNet | cloneNewCgroup

// ActionKind says what the filter returns for a matching system call.
type ActionKind int

const (
	// KillProcess is SECCOMP_RET_KILL_PROCESS: the whole process dies with
	// SIGSYS (A06 §11.5, Linux >= 4.14).
	KillProcess ActionKind = iota + 1
	// Errno is SECCOMP_RET_ERRNO: the call fails with Action.Errno.
	Errno
)

// Action is the verdict for one table row.
type Action struct {
	Kind  ActionKind
	Errno uint16 // only for Errno
}

// MatchKind is how an ArgFilter decides that the row's action applies.
type MatchKind int

const (
	// AnyBit applies the action when arg & Mask != 0.
	AnyBit MatchKind = iota + 1
	// In applies the action when arg is one of Values.
	In
	// NotIn applies the action when arg is none of Values.
	NotIn
)

// ArgFilter limits a row to some argument values. It reads the low 32 bits of
// argument Arg (seccomp_data.args[Arg], little-endian), which is all the
// kernel uses for every filtered argument: clone flags (lower_32_bits),
// socket family (int), ioctl request (unsigned int) and personality
// (unsigned int).
type ArgFilter struct {
	Arg    int
	Kind   MatchKind
	Mask   uint32   // AnyBit
	Values []uint32 // In, NotIn
}

// Syscall is one system call of a row, with an optional argument filter.
// Without a filter the row's action applies to every call.
type Syscall struct {
	Name   string
	Filter *ArgFilter
}

// Row is one row of the A06 §11.5 table.
type Row struct {
	Syscalls []Syscall
	Action   Action
	Reason   string
}

func kill() Action       { return Action{Kind: KillProcess} }
func errno(e int) Action { return Action{Kind: Errno, Errno: uint16(e)} }
func plain(names ...string) []Syscall {
	out := make([]Syscall, len(names))
	for i, n := range names {
		out[i] = Syscall{Name: n}
	}
	return out
}

// Table returns the A06 §11.5 rows (except the first, the architecture check,
// which Program renders as its preamble). It is the single source of the BPF
// program and the Docker profile. Built per call, so callers may not change
// it for each other.
func Table() []Row {
	return []Row{
		{
			Syscalls: plain("kexec_load", "kexec_file_load", "init_module", "finit_module",
				"delete_module", "open_by_handle_at", "iopl", "ioperm"),
			Action: kill(),
			Reason: "Kernel code loading or known container-escape primitives; a build never calls them",
		},
		{
			Syscalls: plain("ptrace", "process_vm_readv", "process_vm_writev"),
			Action:   errno(errEPERM),
			Reason:   "No tracing (strace -p 1 fails cleanly)",
		},
		{
			Syscalls: plain("mount", "umount2", "pivot_root", "chroot", "move_mount", "open_tree",
				"fsopen", "fsconfig", "fsmount", "fspick", "mount_setattr"),
			Action: errno(errEPERM),
			Reason: "No filesystem view changes",
		},
		{
			Syscalls: append(plain("unshare", "setns"),
				Syscall{Name: "clone", Filter: &ArgFilter{Arg: 0, Kind: AnyBit, Mask: nsFlags}}),
			Action: errno(errEPERM),
			Reason: "unshare/setns after launch (WRD-16 §10.3); unshare -r fails",
		},
		{
			Syscalls: plain("clone3"),
			Action:   errno(errENOSYS),
			Reason:   "Flags live in a struct seccomp cannot inspect; ENOSYS makes glibc fall back to clone, which is filtered",
		},
		{
			Syscalls: plain("keyctl", "add_key", "request_key"),
			Action:   errno(errEPERM),
			Reason:   "No kernel keyring access",
		},
		{
			Syscalls: plain("bpf", "perf_event_open", "userfaultfd"),
			Action:   errno(errEPERM),
			Reason:   "Kernel attack surface",
		},
		{
			Syscalls: plain("io_uring_setup", "io_uring_enter", "io_uring_register"),
			Action:   errno(errENOSYS),
			Reason:   "Kernel attack surface; libuv and others fall back on ENOSYS",
		},
		{
			Syscalls: plain("reboot", "swapon", "swapoff", "acct", "quotactl", "syslog", "settimeofday",
				"clock_settime", "clock_adjtime", "sethostname", "setdomainname", "vhangup",
				"name_to_handle_at", "fanotify_init", "lookup_dcookie", "uselib", "nfsservctl"),
			Action: errno(errEPERM),
			Reason: "Host-level operations or information leaks",
		},
		{
			Syscalls: plain("modify_ldt", "_sysctl"),
			Action:   errno(errEPERM),
			Reason:   "Legacy attack surface",
		},
		{
			Syscalls: []Syscall{{Name: "ioctl", Filter: &ArgFilter{Arg: 1, Kind: In,
				Values: []uint32{ioctlTIOCSTI, ioctlTIOCLINUX}}}},
			Action: errno(errEPERM),
			Reason: "Terminal injection (also prevented by --new-session)",
		},
		{
			Syscalls: []Syscall{{Name: "socket", Filter: &ArgFilter{Arg: 0, Kind: NotIn,
				Values: []uint32{afUnix, afInet, afInet6, afNetlink}}}},
			Action: errno(errEAFNOSUPPORT),
			Reason: "Only Unix, IP (private namespace) and netlink (interface enumeration) are needed",
		},
		{
			Syscalls: []Syscall{{Name: "personality", Filter: &ArgFilter{Arg: 0, Kind: NotIn,
				Values: []uint32{perLinux, perLinux32, perUname26, perUname26 | perLinux32, perQuery}}}},
			Action: errno(errEPERM),
			Reason: "Blocks ADDR_NO_RANDOMIZE and similar",
		},
	}
}
