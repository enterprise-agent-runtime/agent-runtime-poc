package store

import "syscall"

func proc() {
	_ = syscall.CreateProcess(nil, nil, nil, nil, false, 0, nil, nil, nil, nil)
	_ = syscall.CreateProcessAsUser(0, nil, nil, nil, nil, false, 0, nil, nil, nil, nil)
}
