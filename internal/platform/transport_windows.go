//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/user"
	"strings"
	"syscall"

	"github.com/Microsoft/go-winio"
)

// Endpoint returns the named pipe \\.\pipe\warden-<user>[-<home hash>]
// (CLAUDE.md §4 IPC; docs/DECISIONS-poc.md D-011).
func Endpoint(home string) string {
	return `\\.\pipe\warden-` + sanitize.Replace(Username()) + HomeSuffix(home)
}

// ownerSDDL grants the current user full access and nobody else; "P"
// protects the DACL from inheritance.
func ownerSDDL() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return "D:P(A;;GA;;;" + u.Uid + ")", nil
}

// pipeSDDL is ownerSDDL with the owner set to the current user as well.
// Without "O:" the owner is the token's default owner, which for an elevated
// administrator is BUILTIN\Administrators; the client requires owner ==
// user (verifyPipeServer), so the daemon states it.
func pipeSDDL() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	d, err := ownerSDDL()
	if err != nil {
		return "", err
	}
	return "O:" + u.Uid + d, nil
}

// Listen creates the pipe owned by the current user with an owner-only DACL
// (pipeSDDL). go-winio creates the first instance exclusively, so a second
// daemon fails here.
func Listen(home string) (net.Listener, error) {
	sddl, err := pipeSDDL()
	if err != nil {
		return nil, err
	}
	l, err := winio.ListenPipe(Endpoint(home), &winio.PipeConfig{SecurityDescriptor: sddl, InputBufferSize: 64 << 10, OutputBufferSize: 64 << 10})
	if err != nil {
		// The first instance is created exclusively: an existing pipe of
		// another daemon makes creation fail with access denied.
		if errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, errorPipeBusy) {
			return nil, ErrDaemonRunning
		}
		return nil, err
	}
	return l, nil
}

// Dial connects to the daemon pipe and, before returning the connection,
// verifies that the current user serves it (verifyPipeServer). Another
// local user can create the pipe name first; nothing, in particular not the
// token, may be written to a pipe that fails the check.
func Dial(ctx context.Context, home string) (net.Conn, error) {
	c, err := winio.DialPipeContext(ctx, Endpoint(home))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, fmt.Errorf("wardend is not running (%s): %w", Endpoint(home), err)
	}
	me, err := currentUserSID()
	if err == nil {
		err = verifyPipeServer(c, me)
	}
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("refusing %s: %w", Endpoint(home), err)
	}
	return c, nil
}

// errorPipeBusy is ERROR_PIPE_BUSY (231), absent from package syscall.
const errorPipeBusy = syscall.Errno(231)

var sanitize = strings.NewReplacer(" ", "_", "\\", "_", "/", "_", ":", "_")
