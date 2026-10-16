package platform

import "syscall"

var procOther = syscall.NewLazyDLL("kernel32.dll").NewProc("CreateProcessW")
