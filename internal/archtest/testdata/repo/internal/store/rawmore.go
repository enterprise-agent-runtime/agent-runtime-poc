package store

import "syscall"

func rawMore() {
	syscall.Syscall9(0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	syscall.Syscall12(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	syscall.Syscall15(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	syscall.Syscall18(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
}
