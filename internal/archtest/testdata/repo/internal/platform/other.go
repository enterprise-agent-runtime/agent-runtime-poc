package platform

import "syscall"

func other() { syscall.Syscall(0, 0, 0, 0) }
