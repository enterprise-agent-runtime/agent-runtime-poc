package store

import "golang.org/x/sys/windows"

func rawWindows() { windows.SyscallN(0) }
