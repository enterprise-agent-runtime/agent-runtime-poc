//go:build linux

package seccomp

import "golang.org/x/sys/unix"

// Compile-time checks that the architecture-independent kernel constants
// defined in this package equal golang.org/x/sys/unix (doc.go). Indexing a
// one-element array with a constant other than 0 does not compile. Errnos
// are typed (syscall.Errno), so they are converted to untyped arithmetic
// through int.
var _ = [...]int{
	[1]int{}[auditArchX86_64-unix.AUDIT_ARCH_X86_64],
	[1]int{}[auditArchAArch64-unix.AUDIT_ARCH_AARCH64],

	[1]int{}[seccompRetKillProcess-unix.SECCOMP_RET_KILL_PROCESS],
	[1]int{}[seccompRetErrno-unix.SECCOMP_RET_ERRNO],
	[1]int{}[seccompRetAllow-unix.SECCOMP_RET_ALLOW],
	[1]int{}[seccompRetData-unix.SECCOMP_RET_DATA],
	[1]int{}[maxInstructions-unix.BPF_MAXINSNS],

	[1]int{}[bpfLD-unix.BPF_LD],
	[1]int{}[bpfW-unix.BPF_W],
	[1]int{}[bpfABS-unix.BPF_ABS],
	[1]int{}[bpfJMP-unix.BPF_JMP],
	[1]int{}[bpfJA-unix.BPF_JA],
	[1]int{}[bpfJEQ-unix.BPF_JEQ],
	[1]int{}[bpfJGE-unix.BPF_JGE],
	[1]int{}[bpfJSET-unix.BPF_JSET],
	[1]int{}[bpfK-unix.BPF_K],
	[1]int{}[bpfRET-unix.BPF_RET],

	[1]int{}[errEPERM-int(unix.EPERM)],
	[1]int{}[errENOSYS-int(unix.ENOSYS)],
	[1]int{}[errEAFNOSUPPORT-int(unix.EAFNOSUPPORT)],

	[1]int{}[cloneNewNS-unix.CLONE_NEWNS],
	[1]int{}[cloneNewCgroup-unix.CLONE_NEWCGROUP],
	[1]int{}[cloneNewUTS-unix.CLONE_NEWUTS],
	[1]int{}[cloneNewIPC-unix.CLONE_NEWIPC],
	[1]int{}[cloneNewUser-unix.CLONE_NEWUSER],
	[1]int{}[cloneNewPID-unix.CLONE_NEWPID],
	[1]int{}[cloneNewNet-unix.CLONE_NEWNET],

	[1]int{}[afUnix-unix.AF_UNIX],
	[1]int{}[afInet-unix.AF_INET],
	[1]int{}[afInet6-unix.AF_INET6],
	[1]int{}[afNetlink-unix.AF_NETLINK],

	[1]int{}[ioctlTIOCSTI-unix.TIOCSTI],
	[1]int{}[ioctlTIOCLINUX-unix.TIOCLINUX],
}
