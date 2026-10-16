package sandbox

// The L1 per-task proxy listener lifecycle of design A07 §2 (M2-12a): open
// in Manager.Create before the sandbox process starts (the socket must
// exist to be bind-mounted at TargetProxySocket, A06 §13.2), serve the
// task's internal/proxy TaskProxy on it, and at Destroy (A06 §13.3 step 4)
// stop it, wait for its events, and remove the socket. The socket itself,
// owner-only from the first moment, the peer-uid check and the startup
// sweep are internal/platform's (TaskSocket), where the OS differences
// live; L1 exists only on Linux and macOS.

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"warden.dev/warden/internal/platform"
	"warden.dev/warden/internal/proxy"
)

// ProxyListener is the running proxy of one L1 sandbox.
type ProxyListener struct {
	sock   *platform.TaskSocket
	proxy  *proxy.TaskProxy
	cancel context.CancelFunc
	done   chan error

	once sync.Once
	err  error
}

// OpenProxy opens the Spec's proxy socket (S/scratch/px-<n>.sock, pinned
// by Validate, D-056) and starts serving a TaskProxy built from cfg on it.
// cfg is bound to the sandbox: an empty SandboxID becomes s.ID, any other
// is refused. A Spec that is not a valid L1 Spec is ErrInvalidSpec; a
// socket that cannot be opened owner-only (or L1 not existing on this OS,
// platform.ErrTaskSocketUnsupported) is ErrUnavailable. Nothing is created
// unless OpenProxy succeeds.
//
// ctx bounds the proxy's life; Close ends it earlier and must be called in
// every case, since only Close removes the socket.
func OpenProxy(ctx context.Context, s *Spec, cfg proxy.Config) (*ProxyListener, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if s.Level != LevelL1 {
		return nil, invalid("level %s has no per-task proxy socket (A07 §2 is L1; L2 uses the sidecar, C-03)", s.Level)
	}
	switch cfg.SandboxID {
	case "":
		cfg.SandboxID = s.ID
	case s.ID:
	default:
		return nil, invalid("proxy for sandbox %q opened for sandbox %q", cfg.SandboxID, s.ID)
	}
	p, err := proxy.New(cfg)
	if err != nil {
		return nil, err
	}
	sock, err := platform.ListenTaskSocket(s.WardenHome, s.Proxy.Socket)
	if err != nil {
		return nil, fmt.Errorf("%w: proxy socket: %w", ErrUnavailable, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	l := &ProxyListener{sock: sock, proxy: p, cancel: cancel, done: make(chan error, 1)}
	go func() { l.done <- p.Serve(ctx, sock) }()
	return l, nil
}

// Path is the socket's host path, the bind source for TargetProxySocket.
func (l *ProxyListener) Path() string { return l.sock.Path() }

// Proxy is the TaskProxy served on the socket.
func (l *ProxyListener) Proxy() *proxy.TaskProxy { return l.proxy }

// Close stops the proxy and returns once it has ended every connection and
// written its pending events (proxy.Serve), so all proxy events of the
// sandbox precede its sandbox.destroy (A07 §2). It then removes the socket,
// only if it is still the one opened (platform.TaskSocket.Remove).
// Further calls return the first result.
func (l *ProxyListener) Close() error {
	l.once.Do(func() {
		l.cancel()
		serveErr := <-l.done
		l.err = errors.Join(serveErr, l.sock.Remove())
	})
	return l.err
}

// SweepProxySockets removes the proxy sockets earlier runs left in the
// sessions of wardenHome (A07 §2 startup cleanup). wardend calls it once it
// holds its endpoint and before it creates any sandbox
// (platform.SweepTaskSockets).
func SweepProxySockets(wardenHome string) ([]string, error) {
	return platform.SweepTaskSockets(wardenHome)
}
