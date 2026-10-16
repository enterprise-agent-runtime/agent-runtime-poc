//go:build windows

package platform

import (
	"context"
	"errors"
	"io"
	"net"
	"os/user"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// TestListen_PipeIsOwnerOnly is the M1 transport test that was missing
// (CLAUDE.md §8.7): the pipe platform.Listen creates must carry the
// owner-only descriptor of D-011 as Windows reads it back, owned by the
// current user and with one FILE_ALL_ACCESS grant to that user in a
// protected DACL, because that is exactly what the client now demands
// before it sends the token.
func TestListen_PipeIsOwnerOnly(t *testing.T) {
	h := tempHome(t)
	l, err := Listen(h)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go acceptAndDrain(l, nil)
	c, err := winio.DialPipeContext(testCtx(t), Endpoint(h))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sd, err := pipeSecurity(pipeHandle(t, c))
	if err != nil {
		t.Fatal(err)
	}
	me, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeServerVerdict(me, me, sd); err != nil {
		t.Fatalf("Listen's pipe is not owner-only: %v; descriptor %s", err, sddlOf(sd))
	}
}

// TestDial_RefusesPipeOpenToOthers reproduces the squat: a pipe at the
// daemon's name whose descriptor lets someone else in. Created by the test
// itself, so the serving process is the current user; the descriptor alone
// must make Dial refuse, and nothing may be written to the pipe.
func TestDial_RefusesPipeOpenToOthers(t *testing.T) {
	uid := currentUID(t)
	for name, sddl := range map[string]string{
		"also grants Everyone":   "O:" + uid + "D:P(A;;GA;;;" + uid + ")(A;;GA;;;WD)",
		"grants Everyone only":   "O:" + uid + "D:P(A;;GA;;;WD)",
		"DACL not protected":     "O:" + uid + "D:(A;;GA;;;" + uid + ")",
		"also grants Users read": "O:" + uid + "D:P(A;;GA;;;" + uid + ")(A;;GR;;;BU)",
	} {
		t.Run(name, func(t *testing.T) {
			h := tempHome(t)
			l, err := winio.ListenPipe(Endpoint(h), &winio.PipeConfig{SecurityDescriptor: sddl})
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			got := make(chan int, 1)
			go acceptAndDrain(l, got)
			c, err := Dial(testCtx(t), h)
			if err == nil {
				c.Write([]byte("token"))
				c.Close()
				t.Fatalf("Dial accepted a pipe with descriptor %s", sddl)
			}
			if !errors.Is(err, ErrEndpointNotOwned) {
				t.Fatalf("err = %v, want ErrEndpointNotOwned", err)
			}
			// Refused by the DACL verdict, not by failing to read the
			// descriptor: reading it with SE_FILE_OBJECT failed ("parameter
			// is incorrect") on the unprotected DACL, which refused for the
			// wrong reason and would have hidden a verdict that accepts it.
			if !strings.Contains(err.Error(), "DACL is not owner-only") {
				t.Fatalf("refused for another reason: %v", err)
			}
			// Close the listener before waiting for the count. Dial
			// connects, reads the descriptor, refuses and closes at once;
			// if all of that happens before go-winio's server calls
			// ConnectNamedPipe, Windows reports the instance as closed
			// and go-winio waits for a next client that never comes, so
			// Accept would block forever (a 10-minute hang on the CI
			// runner, 2026-10-16). Closing the listener makes Accept
			// return; an accepted connection still drains to EOF.
			l.Close()
			if n := <-got; n != 0 {
				t.Fatalf("%d bytes reached the squatted pipe", n)
			}
		})
	}
}

// TestDial_AcceptsOwnerOnlyPipe: the same in-process pipe with the
// descriptor Listen sets passes the check (the test is the current user).
func TestDial_AcceptsOwnerOnlyPipe(t *testing.T) {
	h := tempHome(t)
	sddl, err := pipeSDDL()
	if err != nil {
		t.Fatal(err)
	}
	l, err := winio.ListenPipe(Endpoint(h), &winio.PipeConfig{SecurityDescriptor: sddl})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go acceptAndDrain(l, nil)
	c, err := Dial(testCtx(t), h)
	if err != nil {
		t.Fatalf("Dial refused an owner-only pipe served by the current user: %v", err)
	}
	c.Close()
}

// TestVerifyPipeServer_ServerIsAnotherUser drives the real server-identity
// path (GetNamedPipeServerProcessId, the server's process token) with a
// "current user" the server is not: BUILTIN\Administrators stands in for
// the user, so the server, running as the test user, is someone else.
func TestVerifyPipeServer_ServerIsAnotherUser(t *testing.T) {
	h := tempHome(t)
	l, err := Listen(h)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go acceptAndDrain(l, nil)
	c, err := winio.DialPipeContext(testCtx(t), Endpoint(h))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPipeServer(c, sidBytes(admins)); !errors.Is(err, ErrEndpointNotOwned) {
		t.Fatalf("err = %v, want ErrEndpointNotOwned", err)
	}
	me, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPipeServer(c, me); err != nil {
		t.Fatalf("own server refused: %v", err)
	}
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func currentUID(t *testing.T) string {
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return u.Uid
}

// acceptAndDrain accepts one connection and reports how many bytes the
// client wrote before closing.
func acceptAndDrain(l net.Listener, got chan<- int) {
	c, err := l.Accept()
	if err != nil {
		if got != nil {
			got <- 0
		}
		return
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _ := io.Copy(io.Discard, c)
	if got != nil {
		got <- int(n)
	}
}

func pipeHandle(t *testing.T, c net.Conn) windows.Handle {
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		t.Fatalf("%T has no handle", c)
	}
	return windows.Handle(f.Fd())
}

func sddlOf(sd []byte) string {
	s, err := winio.SecurityDescriptorToSddl(sd)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return s
}

// TestPipeSDDL_StatesOwner: without "O:" the pipe's owner is the token's
// default owner, the current user here but BUILTIN\Administrators for an
// elevated administrator (GitHub's windows-latest runner), whose pipe the
// client would then refuse. TestListen_PipeIsOwnerOnly cannot see that on a
// non-elevated machine, so the descriptor Listen passes is checked directly.
func TestPipeSDDL_StatesOwner(t *testing.T) {
	s, err := pipeSDDL()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString(s)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		t.Fatalf("pipe SDDL %s states no owner (%v)", s, err)
	}
	me, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if got := sidBytes(owner); string(got) != string(me) {
		t.Fatalf("pipe SDDL owner %s is not the current user", owner)
	}
}
