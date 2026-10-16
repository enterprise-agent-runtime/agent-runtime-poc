package platform

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
)

// ErrEndpointNotOwned means the daemon endpoint is served by, or open to,
// someone other than the current user. Dial refuses such an endpoint before
// anything is written to it, so the session token in system.hello never
// reaches another local user who created the pipe or socket first (pipe
// squatting; CLAUDE.md §4 IPC, design A05 §2.1 and §3.3).
var ErrEndpointNotOwned = errors.New("endpoint not served by the current user")

// pipeServerVerdict decides whether the Windows pipe may receive the token.
// serverSID is the token user of the process serving the pipe, userSID the
// client's own, sd the pipe's self-relative security descriptor (owner and
// DACL). The pipe must be served by the user, owned by the user, and carry
// the owner-only DACL Listen sets (ownerOnlyDACL; D-011, D-027). Pure, so
// every case is tested on every OS.
func pipeServerVerdict(serverSID, userSID, sd []byte) error {
	if !validSID(userSID) || !validSID(serverSID) || !bytes.Equal(serverSID, userSID) {
		return fmt.Errorf("%w: the pipe server runs as another user", ErrEndpointNotOwned)
	}
	if owner := sdOwner(sd); owner == nil || !bytes.Equal(owner, userSID) {
		return fmt.Errorf("%w: the pipe is owned by another user", ErrEndpointNotOwned)
	}
	if !ownerOnlyDACL(sd, userSID) {
		return fmt.Errorf("%w: the pipe's DACL is not owner-only", ErrEndpointNotOwned)
	}
	return nil
}

// sdOwner returns the owner SID of a self-relative security descriptor, or
// nil when it has none or the offset or SID does not fit in sd.
func sdOwner(sd []byte) []byte {
	if len(sd) < sdHeaderSize || binary.LittleEndian.Uint16(sd[2:])&seSelfRelative == 0 {
		return nil
	}
	off := uint64(binary.LittleEndian.Uint32(sd[4:]))
	if off < sdHeaderSize || off >= uint64(len(sd)) {
		return nil
	}
	s := sd[off:]
	if !validSID(s) {
		return nil
	}
	return s[:sidLen(s)]
}

// socketFacts is what the client learns about a Unix daemon socket before it
// writes to it: Lstat of the socket and of its directory, and the uid of the
// peer serving it when the OS reports one (SO_PEERCRED, LOCAL_PEERCRED).
type socketFacts struct {
	SockUID, DirUID   int
	SockMode, DirMode fs.FileMode
	PeerUID           int
	PeerKnown         bool
}

// socketVerdict decides whether the socket may receive the token: exactly
// what Listen creates, a socket of mode 0600 owned by uid in a directory
// owned by uid that nobody else may enter, served by uid where the OS says
// who serves it. Pure, so every case is tested on every OS.
func socketVerdict(f socketFacts, uid int) error {
	switch {
	case f.SockMode.Type() != fs.ModeSocket:
		return fmt.Errorf("%w: the endpoint is not a socket (%v)", ErrEndpointNotOwned, f.SockMode)
	case f.SockUID != uid:
		return fmt.Errorf("%w: the socket is owned by uid %d", ErrEndpointNotOwned, f.SockUID)
	case f.SockMode.Perm() != 0o600:
		return fmt.Errorf("%w: the socket has mode %#o, not 0600", ErrEndpointNotOwned, f.SockMode.Perm())
	case f.DirMode.Type() != fs.ModeDir:
		return fmt.Errorf("%w: the socket's directory is not a directory (%v)", ErrEndpointNotOwned, f.DirMode)
	case f.DirUID != uid:
		return fmt.Errorf("%w: the socket's directory is owned by uid %d", ErrEndpointNotOwned, f.DirUID)
	case f.DirMode.Perm()&0o077 != 0:
		return fmt.Errorf("%w: the socket's directory has mode %#o, not owner-only", ErrEndpointNotOwned, f.DirMode.Perm())
	case f.PeerKnown && f.PeerUID != uid:
		return fmt.Errorf("%w: the socket is served by uid %d", ErrEndpointNotOwned, f.PeerUID)
	}
	return nil
}
