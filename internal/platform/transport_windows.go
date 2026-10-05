//go:build windows

package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Microsoft/go-winio"
)

// Endpoint returns the named pipe \\.\pipe\warden-<user>[-<home hash>]
// (CLAUDE.md §4 IPC; docs/DECISIONS-poc.md D-011).
func Endpoint(home string) string {
	return `\\.\pipe\warden-` + sanitize.Replace(Username()) + homeSuffix(home)
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

// Listen creates the pipe with an owner-only DACL. go-winio creates the
// first instance exclusively, so a second daemon fails here.
func Listen(home string) (net.Listener, error) {
	sddl, err := ownerSDDL()
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

// Dial connects to the daemon pipe.
func Dial(ctx context.Context, home string) (net.Conn, error) {
	c, err := winio.DialPipeContext(ctx, Endpoint(home))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, fmt.Errorf("wardend is not running (%s): %w", Endpoint(home), err)
	}
	return c, nil
}

// errorPipeBusy is ERROR_PIPE_BUSY (231), absent from package syscall.
const errorPipeBusy = syscall.Errno(231)

// homeSuffix distinguishes endpoints of overridden homes, so a test daemon
// never collides with the user's real one.
func homeSuffix(home string) string {
	if os.Getenv(EnvHome) == "" {
		return ""
	}
	h := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(home))))
	return "-" + hex.EncodeToString(h[:])[:8]
}

var sanitize = strings.NewReplacer(" ", "_", "\\", "_", "/", "_", ":", "_")
