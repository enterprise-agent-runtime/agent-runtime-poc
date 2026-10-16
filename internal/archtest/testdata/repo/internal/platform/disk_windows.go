package platform

import "syscall"

var procFree = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")
