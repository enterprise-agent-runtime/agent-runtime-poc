//go:build !windows && !linux && !darwin

package platform

import "errors"

// errNoHostFS: L2 runs only on Linux, Windows and macOS hosts.
var errNoHostFS = errors.New("host file system queries are not supported on this OS")

func (hostFS) CaseSensitiveDir(string) (bool, error)  { return false, errNoHostFS }
func (hostFS) ShortNameCreation(string) (bool, error) { return true, errNoHostFS }
func (hostFS) HasShortAlias(string) (bool, error)     { return true, errNoHostFS }
func (hostFS) FinalPath(string) (string, error)       { return "", errNoHostFS }
