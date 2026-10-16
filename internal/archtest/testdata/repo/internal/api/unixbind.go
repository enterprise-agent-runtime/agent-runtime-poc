package api

import "golang.org/x/sys/unix"

func unixBind() { _ = unix.Bind(0, nil) }
