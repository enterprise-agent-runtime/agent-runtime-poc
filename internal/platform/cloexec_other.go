//go:build !linux && !darwin

package platform

func closeOnExecInherited() error { return nil }
