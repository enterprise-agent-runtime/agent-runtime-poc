//go:build !linux && !darwin

package platform

import "net"

// TaskSocket is the L1 per-task proxy listener; it is never created here.
type TaskSocket struct{}

// ListenTaskSocket creates nothing and returns ErrTaskSocketUnsupported.
func ListenTaskSocket(root, path string) (*TaskSocket, error) {
	return nil, ErrTaskSocketUnsupported
}

// SweepTaskSockets has nothing to sweep where L1 does not exist.
func SweepTaskSockets(root string) ([]string, error) { return nil, nil }

// Path, Addr, Accept, Close and Remove complete the net.Listener of the
// Unix version; they are unreachable because no TaskSocket is returned.
func (s *TaskSocket) Path() string              { return "" }
func (s *TaskSocket) Addr() net.Addr            { return nil }
func (s *TaskSocket) Accept() (net.Conn, error) { return nil, ErrTaskSocketUnsupported }
func (s *TaskSocket) Close() error              { return nil }
func (s *TaskSocket) Remove() error             { return nil }
