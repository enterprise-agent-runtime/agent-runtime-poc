package store

import "golang.org/x/sys/unix"

// Typed execveat/fexecve wrappers (should x/sys add them) start a process
// as surely as unix.Exec does (M2-8 review round 2).
func g() {
	_ = unix.Execveat(3, "", nil, nil, 0)
	_ = unix.Fexecve(3, nil, nil)
}
