package platform

import "syscall"

func disableEcho() { syscall.Syscall(syscall.SYS_IOCTL, 0, 0, 0) }
