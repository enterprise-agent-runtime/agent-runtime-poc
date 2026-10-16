//go:build windows

package platform

import (
	"os"
	"syscall"
)

var procSetConsoleMode = syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleMode")

const enableEchoInput = 0x0004

func disableEcho(f *os.File) (func(), error) {
	h := syscall.Handle(f.Fd())
	var mode uint32
	if err := syscall.GetConsoleMode(h, &mode); err != nil {
		return nil, err
	}
	if r, _, e := procSetConsoleMode.Call(uintptr(h), uintptr(mode&^enableEchoInput)); r == 0 {
		return nil, e
	}
	return func() { procSetConsoleMode.Call(uintptr(h), uintptr(mode)) }, nil
}
