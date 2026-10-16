package platform

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"testing"
)

// TestPipeServerVerdict guards the client's check of who serves the
// Windows pipe (pipe squatting, security review of FX-3; CLAUDE.md §4 IPC).
// The client used to dial \\.\pipe\warden-<user> and send the session token
// in system.hello without asking who had created the pipe, so another local
// user who created that name first received the token. The decision is pure,
// so every case runs on every OS; the Windows tests feed it real descriptors.
func TestPipeServerVerdict(t *testing.T) {
	user := testSID(5, 21, 1111, 2222, 3333, 1001)
	other := testSID(5, 21, 1111, 2222, 3333, 1002)
	admins := testSID(5, 32, 544)
	ownerOnly := testSD(nil, seDACLPresent|seDACLProtected, testACE(0, 0, fileAllAccess, user))
	good := withOwner(ownerOnly, user)

	cases := []struct {
		name      string
		server    []byte
		user      []byte
		sd        []byte
		wantAllow bool
	}{
		{"served by the user, owner-only", user, user, good, true},
		{"served by another user", other, user, good, false},
		{"server SID unknown", nil, user, good, false},
		{"user SID unknown", user, nil, good, false},
		{"server SID prefix of the user's", user[:len(user)-4], user, good, false},
		{"owner is another user", user, user, withOwner(ownerOnly, other), false},
		{"owner is Administrators", user, user, withOwner(ownerOnly, admins), false},
		{"no owner", user, user, ownerOnly, false},
		{"DACL also grants Everyone", user, user, withOwner(testSD(nil, seDACLPresent|seDACLProtected, testACE(0, 0, fileAllAccess, user), testACE(0, 0, fileAllAccess, testSID(1, 0))), user), false},
		{"DACL not protected", user, user, withOwner(testSD(nil, seDACLPresent, testACE(0, 0, fileAllAccess, user)), user), false},
		{"NULL DACL", user, user, withOwner(nullDACLSD(nil), user), false},
		{"empty descriptor", user, user, nil, false},
	}
	for _, c := range cases {
		err := pipeServerVerdict(c.server, c.user, c.sd)
		if c.wantAllow && err != nil {
			t.Errorf("%s: refused: %v", c.name, err)
		}
		if !c.wantAllow && !errors.Is(err, ErrEndpointNotOwned) {
			t.Errorf("%s: err = %v, want ErrEndpointNotOwned", c.name, err)
		}
	}

	// The owner offset comes from the OS but is still bounds-checked:
	// lying offsets are refused, never a panic.
	for name, off := range map[string]uint32{
		"owner inside the header": 2,
		"owner past the end":      uint32(len(good)),
		"owner offset huge":       0xFFFFFFF0,
		"owner truncated":         uint32(len(good) - 4),
	} {
		b := append([]byte(nil), good...)
		binary.LittleEndian.PutUint32(b[4:], off)
		if err := pipeServerVerdict(user, user, b); !errors.Is(err, ErrEndpointNotOwned) {
			t.Errorf("%s: err = %v, want ErrEndpointNotOwned", name, err)
		}
	}
	for n := 0; n < len(good); n++ {
		if err := pipeServerVerdict(user, user, good[:n]); err == nil {
			t.Errorf("descriptor truncated to %d bytes: accepted", n)
		}
	}
}

// TestSocketVerdict guards the Unix half of the same check: before the
// token is sent, the socket must be a socket owned by the current user with
// mode 0600, in a directory owned by the user that nobody else can enter,
// and (where the OS reports it) the peer serving it must run as the user.
// Listen creates exactly that; anything else is somebody else's endpoint.
func TestSocketVerdict(t *testing.T) {
	const uid = 1000
	good := socketFacts{
		SockUID: uid, SockMode: fs.ModeSocket | 0o600,
		DirUID: uid, DirMode: fs.ModeDir | 0o700,
		PeerUID: uid, PeerKnown: true,
	}
	mut := func(f func(*socketFacts)) socketFacts { s := good; f(&s); return s }
	cases := []struct {
		name      string
		facts     socketFacts
		wantAllow bool
	}{
		{"as Listen creates it", good, true},
		{"peer not reported by the OS", mut(func(s *socketFacts) { s.PeerKnown = false; s.PeerUID = -1 }), true},
		{"socket mode 0644", mut(func(s *socketFacts) { s.SockMode = fs.ModeSocket | 0o644 }), false},
		{"socket mode 0660", mut(func(s *socketFacts) { s.SockMode = fs.ModeSocket | 0o660 }), false},
		{"socket mode 0666", mut(func(s *socketFacts) { s.SockMode = fs.ModeSocket | 0o666 }), false},
		{"socket owned by another user", mut(func(s *socketFacts) { s.SockUID = uid + 1 }), false},
		{"not a socket", mut(func(s *socketFacts) { s.SockMode = 0o600 }), false},
		{"socket is a symlink", mut(func(s *socketFacts) { s.SockMode = fs.ModeSymlink | 0o600 }), false},
		{"directory 0755", mut(func(s *socketFacts) { s.DirMode = fs.ModeDir | 0o755 }), false},
		{"directory 0710", mut(func(s *socketFacts) { s.DirMode = fs.ModeDir | 0o710 }), false},
		{"directory 0701", mut(func(s *socketFacts) { s.DirMode = fs.ModeDir | 0o701 }), false},
		{"directory owned by another user", mut(func(s *socketFacts) { s.DirUID = uid + 1 }), false},
		{"directory is a symlink", mut(func(s *socketFacts) { s.DirMode = fs.ModeSymlink | 0o700 }), false},
		{"peer is another user", mut(func(s *socketFacts) { s.PeerUID = uid + 1 }), false},
		{"peer is root", mut(func(s *socketFacts) { s.PeerUID = 0 }), false},
	}
	for _, c := range cases {
		err := socketVerdict(c.facts, uid)
		if c.wantAllow && err != nil {
			t.Errorf("%s: refused: %v", c.name, err)
		}
		if !c.wantAllow && !errors.Is(err, ErrEndpointNotOwned) {
			t.Errorf("%s: err = %v, want ErrEndpointNotOwned", c.name, err)
		}
	}
}

// withOwner returns a copy of sd with sid appended and OffsetOwner set to it.
func withOwner(sd, sid []byte) []byte {
	b := append([]byte(nil), sd...)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)))
	return append(b, sid...)
}
