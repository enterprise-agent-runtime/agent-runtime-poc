package store

import (
	"syscall"

	"golang.org/x/sys/windows"
)

var (
	_ = &syscall.LazyDLL{Name: "kernel32.dll"}
	_ = &syscall.DLL{Name: "kernel32.dll"}
	_ = &syscall.LazyProc{Name: "CreateProcessW"}
	_ = &syscall.Proc{Name: "CreateProcessW"}
	_ = &windows.LazyDLL{Name: "kernel32.dll"}
)
