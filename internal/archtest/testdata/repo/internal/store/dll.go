package store

import "syscall"

func dll() {
	_ = syscall.NewLazyDLL("kernel32.dll")
	_, _ = syscall.LoadDLL("kernel32.dll")
	_ = syscall.MustLoadDLL("kernel32.dll")
	_, _ = syscall.LoadLibrary("kernel32.dll")
	_, _ = syscall.GetProcAddress(0, "x")
}
