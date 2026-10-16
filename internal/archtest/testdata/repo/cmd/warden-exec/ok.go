package main

import (
	_ "golang.org/x/sys/unix"
	_ "warden.dev/warden/internal/buildinfo"
	_ "warden.dev/warden/internal/exec"
	_ "warden.dev/warden/internal/exec/forward"
	_ "warden.dev/warden/internal/execproto"
)
