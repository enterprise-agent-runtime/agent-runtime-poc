package api

import "golang.org/x/sys/windows"

func winListen() { _ = windows.Listen(0, 1) }
