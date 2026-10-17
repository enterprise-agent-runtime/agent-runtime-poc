//go:build darwin

package platform

func closeOnExecInherited() error { return markDirFds("/dev/fd") }
