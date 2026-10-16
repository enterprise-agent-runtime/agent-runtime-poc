package ok

import "syscall"

func raw() { syscall.Syscall(0, 0, 0, 0) }
