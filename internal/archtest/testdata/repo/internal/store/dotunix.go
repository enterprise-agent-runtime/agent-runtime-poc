package store

import . "golang.org/x/sys/unix"

func dotUnixExec() { _ = Exec("x", nil, nil) }
