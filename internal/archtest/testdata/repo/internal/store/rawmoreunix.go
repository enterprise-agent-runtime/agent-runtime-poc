package store

import "golang.org/x/sys/unix"

func rawMoreUnix() {
	unix.SyscallNoError(0, 0, 0, 0)
	unix.RawSyscallNoError(0, 0, 0, 0)
}
