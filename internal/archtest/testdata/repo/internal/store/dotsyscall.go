package store

import . "syscall"

func dotFork() { _, _ = ForkExec("x", nil, nil) }
