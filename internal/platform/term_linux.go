//go:build linux

package platform

import (
	"os"
	"syscall"
	"unsafe"
)

func disableEcho(f *os.File) (func(), error) {
	fd := f.Fd()
	var t syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&t))); e != 0 {
		return nil, e
	}
	old := t
	t.Lflag &^= syscall.ECHO
	t.Lflag |= syscall.ICANON | syscall.ISIG
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&t))); e != 0 {
		return nil, e
	}
	return func() { syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&old))) }, nil
}
