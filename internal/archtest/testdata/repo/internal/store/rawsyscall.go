package store

import "syscall"

func raw() {
	syscall.Syscall(0, 0, 0, 0)
	syscall.Syscall6(0, 0, 0, 0, 0, 0, 0)
	syscall.RawSyscall(0, 0, 0, 0)
	syscall.RawSyscall6(0, 0, 0, 0, 0, 0, 0)
	syscall.SyscallN(0)
}
