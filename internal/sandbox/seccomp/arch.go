package seccomp

import "fmt"

// Arch is one of the two architectures A06 §11.5 supports.
type Arch int

const (
	// AMD64 is x86_64 (AUDIT_ARCH_X86_64). Its program also kills the x32 ABI.
	AMD64 Arch = iota + 1
	// ARM64 is aarch64 (AUDIT_ARCH_AARCH64).
	ARM64
)

// Arches lists the supported architectures in rendering order.
func Arches() []Arch { return []Arch{AMD64, ARM64} }

// String is the Go architecture name, which is also the name Docker's
// "includes.arches" uses.
func (a Arch) String() string {
	switch a {
	case AMD64:
		return "amd64"
	case ARM64:
		return "arm64"
	}
	return fmt.Sprintf("Arch(%d)", int(a))
}

// Kernel constants. Each is checked against golang.org/x/sys/unix by
// consts_linux.go at compile time on Linux.
const (
	// include/uapi/linux/audit.h: EM_X86_64 | __AUDIT_ARCH_64BIT | __AUDIT_ARCH_LE.
	auditArchX86_64 = 0xc000003e
	// include/uapi/linux/audit.h: EM_AARCH64 | __AUDIT_ARCH_64BIT | __AUDIT_ARCH_LE.
	auditArchAArch64 = 0xc00000b7
	// arch/x86/include/uapi/asm/unistd.h: __X32_SYSCALL_BIT. Not in x/sys/unix.
	x32SyscallBit = 0x40000000
)

// AuditArch is the value the kernel puts in seccomp_data.arch.
func (a Arch) AuditArch() uint32 {
	switch a {
	case AMD64:
		return auditArchX86_64
	case ARM64:
		return auditArchAArch64
	}
	return 0
}

// dockerArch is the libseccomp architecture name of the Docker profile.
func (a Arch) dockerArch() string {
	switch a {
	case AMD64:
		return "SCMP_ARCH_X86_64"
	case ARM64:
		return "SCMP_ARCH_AARCH64"
	}
	return ""
}

// Nr returns the system-call number of name on a, and false when the
// architecture has no such system call (the generator then skips it, A06
// §11.5 "Arch notes"). The numbers come from
// arch/x86/entry/syscalls/syscall_64.tbl (amd64) and
// include/uapi/asm-generic/unistd.h (arm64); consts_linux_<arch>.go checks
// every entry against golang.org/x/sys/unix SYS_* at compile time.
func (a Arch) Nr(name string) (uint32, bool) {
	var nr int
	var ok bool
	switch a {
	case AMD64:
		nr, ok = sysnumAMD64()[name]
	case ARM64:
		nr, ok = sysnumARM64()[name]
	}
	return uint32(nr), ok
}

// System-call numbers: arch/x86/entry/syscalls/syscall_64.tbl (amd*) and
// include/uapi/asm-generic/unistd.h (arm*). consts_linux_<arch>.go checks
// each against golang.org/x/sys/unix SYS_* when building for that
// architecture. A name with no arm* constant does not exist on aarch64.
const (
	amdRead            = 0
	armRead            = 63
	amdWrite           = 1
	armWrite           = 64
	amdOpenat          = 257
	armOpenat          = 56
	amdExecve          = 59
	armExecve          = 221
	amdGetpid          = 39
	armGetpid          = 172
	amdPidfdOpen       = 434
	armPidfdOpen       = 434
	amdClone           = 56
	armClone           = 220
	amdSocket          = 41
	armSocket          = 198
	amdIoctl           = 16
	armIoctl           = 29
	amdPersonality     = 135
	armPersonality     = 92
	amdKexecLoad       = 246
	armKexecLoad       = 104
	amdKexecFileLoad   = 320
	armKexecFileLoad   = 294
	amdInitModule      = 175
	armInitModule      = 105
	amdFinitModule     = 313
	armFinitModule     = 273
	amdDeleteModule    = 176
	armDeleteModule    = 106
	amdOpenByHandleAt  = 304
	armOpenByHandleAt  = 265
	amdIopl            = 172
	amdIoperm          = 173
	amdPtrace          = 101
	armPtrace          = 117
	amdProcessVmReadv  = 310
	armProcessVmReadv  = 270
	amdProcessVmWritev = 311
	armProcessVmWritev = 271
	amdPidfdGetfd      = 438
	armPidfdGetfd      = 438
	amdProcessMadvise  = 440
	armProcessMadvise  = 440
	amdKcmp            = 312
	armKcmp            = 272
	amdMount           = 165
	armMount           = 40
	amdUmount2         = 166
	armUmount2         = 39
	amdPivotRoot       = 155
	armPivotRoot       = 41
	amdChroot          = 161
	armChroot          = 51
	amdMoveMount       = 429
	armMoveMount       = 429
	amdOpenTree        = 428
	armOpenTree        = 428
	amdFsopen          = 430
	armFsopen          = 430
	amdFsconfig        = 431
	armFsconfig        = 431
	amdFsmount         = 432
	armFsmount         = 432
	amdFspick          = 433
	armFspick          = 433
	amdMountSetattr    = 442
	armMountSetattr    = 442
	amdUnshare         = 272
	armUnshare         = 97
	amdSetns           = 308
	armSetns           = 268
	amdClone3          = 435
	armClone3          = 435
	amdKeyctl          = 250
	armKeyctl          = 219
	amdAddKey          = 248
	armAddKey          = 217
	amdRequestKey      = 249
	armRequestKey      = 218
	amdBpf             = 321
	armBpf             = 280
	amdPerfEventOpen   = 298
	armPerfEventOpen   = 241
	amdUserfaultfd     = 323
	armUserfaultfd     = 282
	amdIoUringSetup    = 425
	armIoUringSetup    = 425
	amdIoUringEnter    = 426
	armIoUringEnter    = 426
	amdIoUringRegister = 427
	armIoUringRegister = 427
	amdReboot          = 169
	armReboot          = 142
	amdSwapon          = 167
	armSwapon          = 224
	amdSwapoff         = 168
	armSwapoff         = 225
	amdAcct            = 163
	armAcct            = 89
	amdQuotactl        = 179
	armQuotactl        = 60
	amdSyslog          = 103
	armSyslog          = 116
	amdSettimeofday    = 164
	armSettimeofday    = 170
	amdClockSettime    = 227
	armClockSettime    = 112
	amdClockAdjtime    = 305
	armClockAdjtime    = 266
	amdSethostname     = 170
	armSethostname     = 161
	amdSetdomainname   = 171
	armSetdomainname   = 162
	amdVhangup         = 153
	armVhangup         = 58
	amdNameToHandleAt  = 303
	armNameToHandleAt  = 264
	amdFanotifyInit    = 300
	armFanotifyInit    = 262
	amdLookupDcookie   = 212
	armLookupDcookie   = 18
	amdUselib          = 134
	amdNfsservctl      = 180
	armNfsservctl      = 42
	amdModifyLdt       = 154
	amdSysctl          = 156
)

// sysnumAMD64 holds every system call the table names plus the ordinary
// controls the tests run (read, write, openat, execve, getpid, pidfd_open). Built per
// call: no mutable package state (CLAUDE.md §7).
func sysnumAMD64() map[string]int {
	return map[string]int{
		"read":              amdRead,
		"write":             amdWrite,
		"openat":            amdOpenat,
		"execve":            amdExecve,
		"getpid":            amdGetpid,
		"pidfd_open":        amdPidfdOpen,
		"clone":             amdClone,
		"socket":            amdSocket,
		"ioctl":             amdIoctl,
		"personality":       amdPersonality,
		"kexec_load":        amdKexecLoad,
		"kexec_file_load":   amdKexecFileLoad,
		"init_module":       amdInitModule,
		"finit_module":      amdFinitModule,
		"delete_module":     amdDeleteModule,
		"open_by_handle_at": amdOpenByHandleAt,
		"iopl":              amdIopl,
		"ioperm":            amdIoperm,
		"ptrace":            amdPtrace,
		"process_vm_readv":  amdProcessVmReadv,
		"process_vm_writev": amdProcessVmWritev,
		"pidfd_getfd":       amdPidfdGetfd,
		"process_madvise":   amdProcessMadvise,
		"kcmp":              amdKcmp,
		"mount":             amdMount,
		"umount2":           amdUmount2,
		"pivot_root":        amdPivotRoot,
		"chroot":            amdChroot,
		"move_mount":        amdMoveMount,
		"open_tree":         amdOpenTree,
		"fsopen":            amdFsopen,
		"fsconfig":          amdFsconfig,
		"fsmount":           amdFsmount,
		"fspick":            amdFspick,
		"mount_setattr":     amdMountSetattr,
		"unshare":           amdUnshare,
		"setns":             amdSetns,
		"clone3":            amdClone3,
		"keyctl":            amdKeyctl,
		"add_key":           amdAddKey,
		"request_key":       amdRequestKey,
		"bpf":               amdBpf,
		"perf_event_open":   amdPerfEventOpen,
		"userfaultfd":       amdUserfaultfd,
		"io_uring_setup":    amdIoUringSetup,
		"io_uring_enter":    amdIoUringEnter,
		"io_uring_register": amdIoUringRegister,
		"reboot":            amdReboot,
		"swapon":            amdSwapon,
		"swapoff":           amdSwapoff,
		"acct":              amdAcct,
		"quotactl":          amdQuotactl,
		"syslog":            amdSyslog,
		"settimeofday":      amdSettimeofday,
		"clock_settime":     amdClockSettime,
		"clock_adjtime":     amdClockAdjtime,
		"sethostname":       amdSethostname,
		"setdomainname":     amdSetdomainname,
		"vhangup":           amdVhangup,
		"name_to_handle_at": amdNameToHandleAt,
		"fanotify_init":     amdFanotifyInit,
		"lookup_dcookie":    amdLookupDcookie,
		"uselib":            amdUselib,
		"nfsservctl":        amdNfsservctl,
		"modify_ldt":        amdModifyLdt,
		"_sysctl":           amdSysctl,
	}
}

// sysnumARM64 is sysnumAMD64 for aarch64. iopl, ioperm, uselib, modify_ldt
// and _sysctl do not exist there. nfsservctl does: asm-generic keeps number
// 42 (wired to sys_ni_syscall), so the filter covers it too.
func sysnumARM64() map[string]int {
	return map[string]int{
		"read":              armRead,
		"write":             armWrite,
		"openat":            armOpenat,
		"execve":            armExecve,
		"getpid":            armGetpid,
		"pidfd_open":        armPidfdOpen,
		"clone":             armClone,
		"socket":            armSocket,
		"ioctl":             armIoctl,
		"personality":       armPersonality,
		"kexec_load":        armKexecLoad,
		"kexec_file_load":   armKexecFileLoad,
		"init_module":       armInitModule,
		"finit_module":      armFinitModule,
		"delete_module":     armDeleteModule,
		"open_by_handle_at": armOpenByHandleAt,
		"ptrace":            armPtrace,
		"process_vm_readv":  armProcessVmReadv,
		"process_vm_writev": armProcessVmWritev,
		"pidfd_getfd":       armPidfdGetfd,
		"process_madvise":   armProcessMadvise,
		"kcmp":              armKcmp,
		"mount":             armMount,
		"umount2":           armUmount2,
		"pivot_root":        armPivotRoot,
		"chroot":            armChroot,
		"move_mount":        armMoveMount,
		"open_tree":         armOpenTree,
		"fsopen":            armFsopen,
		"fsconfig":          armFsconfig,
		"fsmount":           armFsmount,
		"fspick":            armFspick,
		"mount_setattr":     armMountSetattr,
		"unshare":           armUnshare,
		"setns":             armSetns,
		"clone3":            armClone3,
		"keyctl":            armKeyctl,
		"add_key":           armAddKey,
		"request_key":       armRequestKey,
		"bpf":               armBpf,
		"perf_event_open":   armPerfEventOpen,
		"userfaultfd":       armUserfaultfd,
		"io_uring_setup":    armIoUringSetup,
		"io_uring_enter":    armIoUringEnter,
		"io_uring_register": armIoUringRegister,
		"reboot":            armReboot,
		"swapon":            armSwapon,
		"swapoff":           armSwapoff,
		"acct":              armAcct,
		"quotactl":          armQuotactl,
		"syslog":            armSyslog,
		"settimeofday":      armSettimeofday,
		"clock_settime":     armClockSettime,
		"clock_adjtime":     armClockAdjtime,
		"sethostname":       armSethostname,
		"setdomainname":     armSetdomainname,
		"vhangup":           armVhangup,
		"name_to_handle_at": armNameToHandleAt,
		"fanotify_init":     armFanotifyInit,
		"lookup_dcookie":    armLookupDcookie,
		"nfsservctl":        armNfsservctl,
	}
}
