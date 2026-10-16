package api

import "syscall"

func sysListen() { _ = syscall.Listen(0, 1) }
