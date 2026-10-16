package store

import _ "unsafe"

//go:linknamestd nanotime2 runtime.nanotime
func nanotime2() int64
