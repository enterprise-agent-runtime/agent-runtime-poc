package platform

import "errors"

// ErrTaskSocketRefused wraps every refusal to open a per-task proxy socket
// (design A07 §2): an unsafe directory, an existing entry at the path, a
// path outside the session layout, or a result that is not an owner-only
// socket (tasksock_unix.go).
var ErrTaskSocketRefused = errors.New("per-task proxy socket refused")

// ErrTaskSocketUnsupported: the per-task proxy socket belongs to L1, which
// exists only on Linux and macOS. Windows runs L2 only, where the proxy is
// a sidecar container on the task's internal network (CLAUDE.md §4).
var ErrTaskSocketUnsupported = errors.New("per-task proxy socket: L1 exists only on Linux and macOS")
