package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"warden.dev/warden/internal/exec"
	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/tools"
)

// hangGuard bounds waits for things that must happen. It is a deadlock
// guard, not a performance budget (CLAUDE.md §8.6).
const hangGuard = 20 * time.Second

// startServer serves s on one end of a net.Pipe and returns the other end
// and a func that stops the server and returns Serve's error.
func startServer(t *testing.T, s *exec.Server, conn net.Conn) func() error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Serve(ctx) }()
	var once sync.Once
	var serveErr error
	stop := func() error {
		once.Do(func() {
			cancel()
			select {
			case serveErr = <-errc:
			case <-time.After(hangGuard):
				serveErr = errors.New("Serve did not return after its context ended")
			}
		})
		return serveErr
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

func helloResult() execproto.HelloResult {
	return execproto.HelloResult{Protocol: execproto.Protocol, ExecutorVersion: "test", OS: "linux", Arch: "amd64", Mode: execproto.ModeTool, ProfilesDigest: "sha256:" + strings.Repeat("0", 64)}
}

func helloParams() execproto.HelloParams {
	return execproto.HelloParams{Protocol: execproto.Protocol, SandboxID: "sb_01JAXR8Q7M2V9KTC3F6YH5N0PB", SessionID: "ses_01JAXR8Q7M2V9KTC3F6YH5N0PB", DaemonVersion: "test"}
}

// newServer returns a server with exec.hello (which refuses a wrong
// protocol, so a failed hello can be tested) and a trivial exec.fs.stat.
func newServer(t *testing.T, rwc io.ReadWriteCloser) *exec.Server {
	t.Helper()
	s := exec.NewServer(rwc)
	must(t, exec.Handle(s, execproto.MethodHello, func(_ context.Context, p execproto.HelloParams) (execproto.HelloResult, error) {
		if p.Protocol != execproto.Protocol {
			return execproto.HelloResult{}, execproto.NewError(execproto.ErrInvalidArgument, "protocol mismatch")
		}
		return helloResult(), nil
	}))
	must(t, exec.Handle(s, execproto.MethodFSStat, func(_ context.Context, p execproto.StatParams) (execproto.StatResult, error) {
		return execproto.StatResult{Exists: true}, nil
	}))
	return s
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestExecProtocol_HelloFirst guards A06 §4.1: "exec.hello must be the
// first request; any other method before it fails with invalid_state".
// Hello is where the daemon checks the executor's protocol, roots, mode and
// profile digest against its launch spec; a method that ran before it would
// run against an executor nobody had checked. A failed hello does not open
// the gate, and a second hello after a successful one is refused too, so
// the checked state cannot be replaced.
func TestExecProtocol_HelloFirst(t *testing.T) {
	a, b := net.Pipe()
	startServer(t, newServer(t, a), a)
	c := tools.New(context.Background(), b, nil)
	defer c.Close()
	ctx := context.Background()

	stat := func() error {
		return c.FS(ctx, execproto.MethodFSStat, execproto.StatParams{Path: "a"}, &execproto.StatResult{})
	}
	before := map[string]func() error{
		"exec.fs.stat":   stat,
		"exec.proc.wait": func() error { _, err := c.Wait(ctx, execproto.WaitParams{Handle: "ph_1"}); return err },
		"unregistered":   func() error { return c.Call(ctx, "exec.nonexistent", nil, nil) },
	}
	for name, call := range before {
		if err := call(); !errors.Is(err, execproto.ErrInvalidState) {
			t.Errorf("%s before exec.hello: %v, want invalid_state", name, err)
		}
	}

	bad := helloParams()
	bad.Protocol = "warden.exec/0"
	if _, err := c.Hello(ctx, bad); !errors.Is(err, execproto.ErrInvalidArgument) {
		t.Fatalf("hello with the wrong protocol: %v, want invalid_argument", err)
	}
	if err := stat(); !errors.Is(err, execproto.ErrInvalidState) {
		t.Errorf("exec.fs.stat after a failed exec.hello: %v, want invalid_state", err)
	}

	res, err := c.Hello(ctx, helloParams())
	if err != nil || res.Protocol != execproto.Protocol {
		t.Fatalf("hello = %+v, %v", res, err)
	}
	if err := stat(); err != nil {
		t.Errorf("exec.fs.stat after exec.hello: %v", err)
	}
	if _, err := c.Hello(ctx, helloParams()); !errors.Is(err, execproto.ErrInvalidState) {
		t.Errorf("second exec.hello: %v, want invalid_state", err)
	}
	var re *execproto.RPCError
	if err := c.Call(ctx, "exec.nonexistent", nil, nil); !errors.As(err, &re) || re.Code != execproto.RPCMethodNotFound {
		t.Errorf("unknown method after exec.hello: %v, want -32601", err)
	}
}

// procs is a stand-in for the process table that M2-later builds: spawn
// starts a goroutine that emits chunks through Server.Notify, and wait
// returns once that goroutine has finished, which is the rule the real
// table follows. What the test checks is the transport's half of A06 §4.1:
// a notification whose Notify returned before a response was written
// reaches the caller before that response.
type procs struct {
	s      *exec.Server
	chunks int
	mu     sync.Mutex
	next   int
	done   map[string]chan struct{}
}

func (p *procs) spawn(ctx context.Context, sp execproto.SpawnParams) (execproto.SpawnResult, error) {
	p.mu.Lock()
	p.next++
	pid := p.next
	h := "ph_" + strconv.Itoa(pid)
	done := make(chan struct{})
	p.done[h] = done
	p.mu.Unlock()
	go func() {
		defer close(done)
		for i := range p.chunks {
			if ctx.Err() != nil {
				return
			}
			n := execproto.IONotification{Handle: h, CallID: sp.CallID, Seq: int64(i), Stream: execproto.StreamStdout, Data: strconv.Itoa(i), Encoding: execproto.EncodingUTF8, Segment: execproto.SegmentHead}
			if err := p.s.Notify(execproto.MethodIO, n); err != nil {
				return
			}
		}
	}()
	return execproto.SpawnResult{Handle: h, PID: pid, PGID: pid, Executable: "/bin/true"}, nil
}

func (p *procs) wait(ctx context.Context, wp execproto.WaitParams) (execproto.WaitResult, error) {
	p.mu.Lock()
	done, ok := p.done[wp.Handle]
	p.mu.Unlock()
	if !ok {
		return execproto.WaitResult{}, execproto.NewError(execproto.ErrInvalidState, "unknown handle")
	}
	select {
	case <-done:
	case <-ctx.Done():
		return execproto.WaitResult{}, ctx.Err()
	}
	code := 0
	return execproto.WaitResult{Handle: wp.Handle, State: execproto.StateExited, ExitCode: &code, Reason: execproto.ReasonExit}, nil
}

// TestExecProtocol_IOBeforeWait guards the ordering guarantee of A06 §4.1:
// "All exec.proc.io notifications for a handle are written before the
// response to the exec.proc.wait that reports its exit (ordering guarantee
// the agent loop relies on)". The agent loop treats a returned wait as the
// end of the output; a chunk that arrives after it is lost or, worse,
// attributed to the next call. Several processes stream at once and are
// waited for concurrently; when each Wait returns, the client must already
// hold every chunk of that handle, in seq order. Two independent faults
// break this and both are covered: a server that queued notifications
// behind a separate writer, and a client that delivered them on goroutines
// of their own (D-016).
func TestExecProtocol_IOBeforeWait(t *testing.T) {
	const handles, chunks = 8, 300
	a, b := net.Pipe()
	s := newServer(t, a)
	p := &procs{s: s, chunks: chunks, done: map[string]chan struct{}{}}
	must(t, exec.Handle(s, execproto.MethodSpawn, p.spawn))
	must(t, exec.Handle(s, execproto.MethodWait, p.wait))
	startServer(t, s, a)

	var mu sync.Mutex
	got := map[string][]execproto.IONotification{}
	c := tools.New(context.Background(), b, func(n execproto.IONotification) {
		mu.Lock()
		got[n.Handle] = append(got[n.Handle], n)
		mu.Unlock()
	})
	defer c.Close()
	ctx := context.Background()
	if _, err := c.Hello(ctx, helloParams()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, handles)
	for i := range handles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sp, err := c.Spawn(ctx, execproto.SpawnParams{CallID: "call_" + strconv.Itoa(i), Argv: []string{"true"}, TimeoutMS: 1000, Authz: execproto.Authz{DecisionID: "dec_1", Kind: execproto.AuthzTool}})
			if err != nil {
				errs <- err
				return
			}
			wr, err := c.Wait(ctx, execproto.WaitParams{Handle: sp.Handle})
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			seen := append([]execproto.IONotification(nil), got[sp.Handle]...)
			mu.Unlock()
			if wr.State != execproto.StateExited || len(seen) != chunks {
				errs <- fmt.Errorf("%s: Wait returned %s with %d of %d chunks delivered", sp.Handle, wr.State, len(seen), chunks)
				return
			}
			for j, n := range seen {
				if n.Seq != int64(j) {
					errs <- fmt.Errorf("%s: chunk %d has seq %d: out of order", sp.Handle, j, n.Seq)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// rawPeer talks to the server in raw frames, to send what the real client
// never would.
type rawPeer struct {
	r *execproto.FrameReader
	w *execproto.FrameWriter
}

func (p rawPeer) roundTrip(t *testing.T, frame string) execproto.Message {
	t.Helper()
	if err := p.w.WriteFrame([]byte(frame)); err != nil {
		t.Fatal(err)
	}
	b, err := p.r.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	m, err := execproto.DecodeMessage(b)
	if err != nil {
		t.Fatalf("server sent an invalid message %s: %v", b, err)
	}
	return m
}

// TestServer_InvalidMessages: a frame that is not a valid request gets the
// JSON-RPC error for it (id null when no id can be trusted), and the
// server goes on serving; framing is intact, so nothing is lost.
func TestServer_InvalidMessages(t *testing.T) {
	a, b := net.Pipe()
	startServer(t, newServer(t, a), a)
	p := rawPeer{execproto.NewFrameReader(b), execproto.NewFrameWriter(b)}

	if m := p.roundTrip(t, `{"jsonrpc":"2.0","id":1,"method":"exec.hello","params":{"protocol":"warden.exec/1","sandbox_id":"x","session_id":"y","daemon_version":"z"}}`); m.Error != nil {
		t.Fatalf("hello: %+v", m.Error)
	}
	cases := []struct {
		name, frame string
		code        int
		id          string
	}{
		{"not JSON", `{"jsonrpc":`, execproto.RPCParseError, "null"},
		{"not a request", `{"jsonrpc":"2.0","id":2}`, execproto.RPCInvalidRequest, "null"},
		{"duplicate method", `{"jsonrpc":"2.0","id":3,"method":"exec.fs.stat","method":"exec.hello"}`, execproto.RPCInvalidRequest, "null"},
		{"unknown method", `{"jsonrpc":"2.0","id":4,"method":"exec.nope","params":{}}`, execproto.RPCMethodNotFound, "4"},
		{"unknown param", `{"jsonrpc":"2.0","id":5,"method":"exec.fs.stat","params":{"path":"a","PATH":"b"}}`, execproto.RPCInvalidParams, "5"},
		{"params not an object", `{"jsonrpc":"2.0","id":"s","method":"exec.fs.stat","params":[1]}`, execproto.RPCInvalidParams, `"s"`},
	}
	for _, c := range cases {
		m := p.roundTrip(t, c.frame)
		if m.Error == nil || m.Error.Code != c.code || string(m.ID) != c.id {
			t.Errorf("%s: response id %s error %+v, want id %s code %d", c.name, m.ID, m.Error, c.id, c.code)
		}
	}
	if m := p.roundTrip(t, `{"jsonrpc":"2.0","id":9,"method":"exec.fs.stat","params":{"path":"a"}}`); m.Error != nil || string(m.ID) != "9" {
		t.Errorf("valid call after the invalid ones: %+v", m)
	}
}

// TestServer_OversizedFrameClosesChannel: A06 §4.1 "larger frames close the
// channel". Serve returns ErrFrameTooLarge and the connection is closed, so
// the daemon sees the executor go away rather than a desynchronised stream.
func TestServer_OversizedFrameClosesChannel(t *testing.T) {
	a, b := net.Pipe()
	s := newServer(t, a)
	errc := make(chan error, 1)
	go func() { errc <- s.Serve(context.Background()) }()
	if _, err := fmt.Fprintf(b, "Content-Length: %d\r\n\r\n", 9<<20); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		if !errors.Is(err, execproto.ErrFrameTooLarge) {
			t.Fatalf("Serve = %v, want ErrFrameTooLarge", err)
		}
	case <-time.After(hangGuard):
		t.Fatal("Serve still running after an oversized frame")
	}
	if _, err := b.Read(make([]byte, 1)); err == nil {
		t.Error("the connection is still open after an oversized frame")
	}
}

// TestServer_StopPaths: Serve ends on EOF (the daemon went away; A06 §3.1
// "die with daemon") with nil, and on its context with the context's
// error; in both cases it returns only after every handler has returned,
// and handlers see their context end (CLAUDE.md §7).
func TestServer_StopPaths(t *testing.T) {
	for _, viaContext := range []bool{false, true} {
		t.Run(fmt.Sprintf("context=%v", viaContext), func(t *testing.T) {
			a, b := net.Pipe()
			s := newServer(t, a)
			entered := make(chan struct{})
			returned := make(chan struct{})
			must(t, exec.Handle(s, execproto.MethodWait, func(ctx context.Context, _ execproto.WaitParams) (execproto.WaitResult, error) {
				close(entered)
				defer close(returned)
				<-ctx.Done()
				return execproto.WaitResult{}, ctx.Err()
			}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			errc := make(chan error, 1)
			go func() { errc <- s.Serve(ctx) }()

			c := tools.New(context.Background(), b, nil)
			defer c.Close()
			if _, err := c.Hello(context.Background(), helloParams()); err != nil {
				t.Fatal(err)
			}
			go func() { _, _ = c.Wait(context.Background(), execproto.WaitParams{Handle: "ph_1"}) }()
			<-entered
			if viaContext {
				cancel()
			} else {
				_ = c.Close()
			}
			select {
			case err := <-errc:
				select {
				case <-returned:
				default:
					t.Error("Serve returned before its handler did")
				}
				want := error(nil)
				if viaContext {
					want = context.Canceled
				}
				if !errors.Is(err, want) && !(want == nil && err == nil) {
					t.Errorf("Serve = %v, want %v", err, want)
				}
			case <-time.After(hangGuard):
				t.Fatal("Serve did not return")
			}
		})
	}
}

// TestServer_ErrorsSanitized: a handler error that is not an executor error
// reaches the daemon as io_error with a fixed detail. OS error text quotes
// paths and arguments, and A06 §4.4 requires io_error messages sanitized
// (INV-C): the detail goes into events and into the model's tool result.
func TestServer_ErrorsSanitized(t *testing.T) {
	a, b := net.Pipe()
	s := newServer(t, a)
	must(t, exec.Handle(s, execproto.MethodFSRead, func(context.Context, execproto.ReadParams) (execproto.ReadResult, error) {
		return execproto.ReadResult{}, fmt.Errorf("open /work/.secret-token-abc: permission denied")
	}))
	must(t, exec.Handle(s, execproto.MethodSignal, func(context.Context, execproto.SignalParams) (execproto.SignalResult, error) {
		return execproto.SignalResult{}, execproto.ErrBusy // a bare Code is an executor error too
	}))
	startServer(t, s, a)
	c := tools.New(context.Background(), b, nil)
	defer c.Close()
	ctx := context.Background()
	if _, err := c.Hello(ctx, helloParams()); err != nil {
		t.Fatal(err)
	}
	err := c.FS(ctx, execproto.MethodFSRead, execproto.ReadParams{Path: "a"}, nil)
	if !errors.Is(err, execproto.ErrIOError) || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "permission") {
		t.Errorf("handler OS error reached the daemon as %v, want a sanitized io_error", err)
	}
	if _, err := c.Signal(ctx, execproto.SignalParams{Handle: "ph_1", Signal: execproto.SignalTERM}); !errors.Is(err, execproto.ErrBusy) {
		t.Errorf("handler Code error: %v, want busy", err)
	}
}

// TestServer_OversizedResponse: a result too large for one frame is replaced
// by a too_large error (A06 §4.4 "frame 8 MiB"); the channel stays open.
func TestServer_OversizedResponse(t *testing.T) {
	a, b := net.Pipe()
	s := newServer(t, a)
	must(t, exec.Handle(s, execproto.MethodFSRead, func(context.Context, execproto.ReadParams) (execproto.ReadResult, error) {
		return execproto.ReadResult{Content: strings.Repeat("x", execproto.MaxFrameBytes)}, nil
	}))
	startServer(t, s, a)
	c := tools.New(context.Background(), b, nil)
	defer c.Close()
	ctx := context.Background()
	if _, err := c.Hello(ctx, helloParams()); err != nil {
		t.Fatal(err)
	}
	if err := c.FS(ctx, execproto.MethodFSRead, execproto.ReadParams{Path: "a"}, nil); !errors.Is(err, execproto.ErrTooLarge) {
		t.Errorf("oversized result: %v, want too_large", err)
	}
	if err := c.FS(ctx, execproto.MethodFSStat, execproto.StatParams{Path: "a"}, nil); err != nil {
		t.Errorf("call after an oversized result: %v", err)
	}
}

// TestServer_RegisterAfterServe: the method table is fixed once Serve
// starts, so the read loop can use it without a lock.
func TestServer_RegisterAfterServe(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	s := newServer(t, a)
	stop := startServer(t, s, a)
	// Serve marks itself started before reading; a call proves it has.
	c := tools.New(context.Background(), b, nil)
	defer c.Close()
	if _, err := c.Hello(context.Background(), helloParams()); err != nil {
		t.Fatal(err)
	}
	if err := s.Register("exec.late", func(context.Context, json.RawMessage) (any, error) { return nil, nil }); err == nil {
		t.Error("Register after Serve succeeded")
	}
	if err := s.Register(execproto.MethodFSStat, nil); err == nil {
		t.Error("Register of a nil handler succeeded")
	}
	_ = stop()
}

// TestServer_RegistrationRules: a method has one handler, registered before
// Serve, and a server is served once. A second handler for a method would
// make which one runs depend on registration order.
func TestServer_RegistrationRules(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	s := newServer(t, a)
	if err := exec.Handle(s, execproto.MethodHello, func(context.Context, execproto.HelloParams) (execproto.HelloResult, error) {
		return execproto.HelloResult{}, nil
	}); err == nil {
		t.Error("a second exec.hello handler was accepted")
	}
	if err := exec.Handle[execproto.WaitParams, execproto.WaitResult](s, execproto.MethodWait, nil); err == nil {
		t.Error("Handle with a nil function succeeded")
	}
	stop := startServer(t, s, a)
	c := tools.New(context.Background(), b, nil)
	defer c.Close()
	if _, err := c.Hello(context.Background(), helloParams()); err != nil {
		t.Fatal(err)
	}
	if err := s.Serve(context.Background()); err == nil {
		t.Error("a second Serve on the same server succeeded")
	}
	_ = stop()
}

// TestServer_NotifyAfterCloseFails: once the channel is gone, Notify
// reports it, so an output pump stops instead of writing into nothing.
func TestServer_NotifyAfterCloseFails(t *testing.T) {
	a, b := net.Pipe()
	s := newServer(t, a)
	stop := startServer(t, s, a)
	_ = b.Close()
	if err := stop(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve = %v", err)
	}
	if err := s.Notify(execproto.MethodIO, execproto.IONotification{Handle: "ph_1"}); err == nil {
		t.Error("Notify after the channel closed succeeded")
	}
	if err := s.Notify(execproto.MethodIO, make(chan int)); err == nil {
		t.Error("Notify of an unencodable value succeeded")
	}
}

// TestServer_UnencodableResult: a result that cannot be encoded is an
// io_error for that call, not a broken channel.
func TestServer_UnencodableResult(t *testing.T) {
	a, b := net.Pipe()
	s := newServer(t, a)
	must(t, s.Register(execproto.MethodFSRead, func(context.Context, json.RawMessage) (any, error) { return make(chan int), nil }))
	startServer(t, s, a)
	c := tools.New(context.Background(), b, nil)
	defer c.Close()
	ctx := context.Background()
	if _, err := c.Hello(ctx, helloParams()); err != nil {
		t.Fatal(err)
	}
	if err := c.FS(ctx, execproto.MethodFSRead, execproto.ReadParams{Path: "a"}, nil); !errors.Is(err, execproto.ErrIOError) {
		t.Errorf("unencodable result: %v, want io_error", err)
	}
	if err := c.FS(ctx, execproto.MethodFSStat, execproto.StatParams{Path: "a"}, nil); err != nil {
		t.Errorf("call after an unencodable result: %v", err)
	}
}
