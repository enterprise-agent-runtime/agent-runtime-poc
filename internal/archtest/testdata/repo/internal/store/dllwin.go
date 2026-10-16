package store

import "golang.org/x/sys/windows"

func dllWin() {
	_ = windows.NewLazySystemDLL("kernel32.dll")
	_, _ = windows.LoadLibraryEx("kernel32.dll", 0, 0)
}
