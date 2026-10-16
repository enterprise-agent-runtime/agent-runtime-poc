//go:build windows

package platform

import (
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// verifyPipeServer decides, on a connected client end and before anything
// is written to it, whether the pipe belongs to the user whose binary SID is
// user: the process serving it must run as that user (its token user), and
// the pipe's own descriptor must be the owner-only one Listen sets (owner
// and single FILE_ALL_ACCESS grant both that user, DACL protected). Every
// failure to find out is a refusal.
func verifyPipeServer(c net.Conn, user []byte) error {
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return fmt.Errorf("%w: %T exposes no pipe handle", ErrEndpointNotOwned, c)
	}
	h := windows.Handle(f.Fd())
	server, err := pipeServerSID(h)
	if err != nil {
		return fmt.Errorf("%w: cannot identify the pipe server: %v", ErrEndpointNotOwned, err)
	}
	sd, err := pipeSecurity(h)
	if err != nil {
		return fmt.Errorf("%w: cannot read the pipe's security descriptor: %v", ErrEndpointNotOwned, err)
	}
	return pipeServerVerdict(server, user, sd)
}

// pipeServerSID returns the token user of the process serving the pipe.
func pipeServerSID(h windows.Handle) ([]byte, error) {
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(h, &pid); err != nil {
		return nil, err
	}
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(p)
	var tok windows.Token
	if err := windows.OpenProcessToken(p, windows.TOKEN_QUERY, &tok); err != nil {
		return nil, err
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return sidBytes(tu.User.Sid), nil
}

// pipeSecurity reads the pipe's owner and DACL as a self-relative
// security descriptor.
func pipeSecurity(h windows.Handle) ([]byte, error) {
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(sd)), sd.Length())...), nil
}

// sidBytes copies a SID out of the buffer it points into.
func sidBytes(s *windows.SID) []byte {
	return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(s)), windows.GetLengthSid(s))...)
}
