package ok

import "syscall"

var procSandbox = syscall.NewLazyDLL("kernel32.dll")
