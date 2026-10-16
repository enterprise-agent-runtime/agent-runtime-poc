package store

import "golang.org/x/sys/unix"

func rawUnix() { unix.Syscall(0, 0, 0, 0) }
