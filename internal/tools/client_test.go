package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/model/providertest"
)

// hangGuard bounds how long a test waits for something that must happen.
// It is a deadlock guard, not a performance budget: every wait it bounds
// completes in microseconds when the code is right (CLAUDE.md §8.6).
const hangGuard = 20 * time.Second

// peer is the executor end of a net.Pipe, speaking raw frames, so these
// tests see exactly what the client puts on the wire. The real server is in
// internal/exec, which this package may not import (A02 §5 R4).
type peer struct {
	conn net.Conn
	r    *execproto.FrameReader
	w    *execproto.FrameWriter
}

func newPair(t *testing.T, onIO func(execproto.IONotification)) (*Client, *peer) {
	t.Helper()
	a, b := net.Pipe()
	c := New(context.Background(), a, onIO)
	p := &peer{conn: b, r: execproto.NewFrameReader(b), w: execproto.NewFrameWriter(b)}
	t.Cleanup(func() {
		_ = c.Close()
		_ = b.Close()
	})
	return c, p
}

// frame reads one raw frame.
func (p *peer) frame() ([]byte, error) { return p.r.ReadFrame() }

// request reads and decodes one request.
func (p *peer) request() (execproto.Message, error) {
	b, err := p.r.ReadFrame()
	if err != nil {
		return execproto.Message{}, err
	}
	m, err := execproto.DecodeMessage(b)
	if err != nil {
		return execproto.Message{}, err
	}
	if m.Kind() != execproto.KindRequest {
		return execproto.Message{}, fmt.Errorf("got message kind %v, want a request", m.Kind())
	}
	return m, nil
}

func (p *peer) send(m execproto.Message) error {
	m.JSONRPC = execproto.JSONRPCVersion
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return p.w.WriteFrame(b)
}

func (p *peer) reply(id json.RawMessage, result any) error {
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return p.send(execproto.Message{ID: id, Result: b})
}

func (p *peer) io(n execproto.IONotification) error {
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return p.send(execproto.Message{Method: execproto.MethodIO, Params: b})
}

// serve runs fn on its own goroutine and returns a func that reports its
// error, for use at the end of the test. On an error the peer closes its
// end, so the client call the test is blocked in fails instead of hanging
// and the peer's error is what the test reports.
func (p *peer) serve(fn func() error) func(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		err := fn()
		if err != nil {
			_ = p.conn.Close()
		}
		done <- err
	}()
	return func(t *testing.T) {
		t.Helper()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("peer: %v", err)
			}
		case <-time.After(hangGuard):
			t.Errorf("peer did not finish")
		}
	}
}

// TestClient_NotificationsInOrder guards the D-016 lesson for the executor
// client. jrpc2's client handed every inbound message to its own goroutine
// and delivered events 2, 4, 3, 5; the agent loop assembles a process's
// output from exec.proc.io chunks, so a reordered chunk is corrupted
// output. Many notifications for several handles are interleaved on the
// wire and must reach OnIO in wire order, all of them before the response
// that follows them returns from Call.
func TestClient_NotificationsInOrder(t *testing.T) {
	const handles, perHandle = 4, 500
	var mu sync.Mutex
	var got []execproto.IONotification
	c, p := newPair(t, func(n execproto.IONotification) {
		mu.Lock()
		got = append(got, n)
		mu.Unlock()
	})
	finish := p.serve(func() error {
		m, err := p.request()
		if err != nil {
			return err
		}
		for i := range handles * perHandle {
			h := i % handles
			if err := p.io(execproto.IONotification{
				Handle: "ph_" + strconv.Itoa(h), CallID: "call_" + strconv.Itoa(h), Seq: int64(i / handles),
				Stream: execproto.StreamStdout, Data: strconv.Itoa(i), Encoding: execproto.EncodingUTF8, Segment: execproto.SegmentHead,
			}); err != nil {
				return err
			}
		}
		code := 0
		return p.reply(m.ID, execproto.WaitResult{Handle: "ph_0", State: execproto.StateExited, ExitCode: &code, Reason: execproto.ReasonExit})
	})

	if _, err := c.Wait(context.Background(), execproto.WaitParams{Handle: "ph_0"}); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	mu.Lock()
	snapshot := append([]execproto.IONotification(nil), got...)
	mu.Unlock()
	finish(t)

	if len(snapshot) != handles*perHandle {
		t.Fatalf("when Wait returned, OnIO had seen %d of %d notifications", len(snapshot), handles*perHandle)
	}
	for i, n := range snapshot {
		if n.Data != strconv.Itoa(i) {
			t.Fatalf("notification %d carries %q: delivered out of wire order", i, n.Data)
		}
	}
}

// TestClient_PendingCallsFailOnClose: closing the client fails every call
// still waiting for its response, with ErrClosed, instead of leaving the
// callers blocked forever (CLAUDE.md §7: every goroutine has a way to stop).
// The executor never answers here; it only reads the requests.
func TestClient_PendingCallsFailOnClose(t *testing.T) {
	const calls = 10
	c, p := newPair(t, nil)
	received := make(chan struct{}, calls)
	go func() {
		for {
			if _, err := p.request(); err != nil {
				return
			}
			received <- struct{}{}
		}
	}()

	errs := make(chan error, calls)
	for range calls {
		go func() {
			_, err := c.Wait(context.Background(), execproto.WaitParams{Handle: "ph_1"})
			errs <- err
		}()
	}
	for range calls {
		select {
		case <-received:
		case <-time.After(hangGuard):
			t.Fatal("the peer did not receive every request")
		}
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for range calls {
		select {
		case err := <-errs:
			if !errors.Is(err, ErrClosed) {
				t.Errorf("pending call after Close: %v, want ErrClosed", err)
			}
		case <-time.After(hangGuard):
			t.Fatal("a pending call was still blocked after Close")
		}
	}
	if _, err := c.Hello(context.Background(), execproto.HelloParams{}); !errors.Is(err, ErrClosed) {
		t.Errorf("call after Close: %v, want ErrClosed", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestClient_PendingCallsFailOnPeerClose: the executor exiting (EOF on the
// channel) fails pending calls the same way, and says why.
func TestClient_PendingCallsFailOnPeerClose(t *testing.T) {
	c, p := newPair(t, nil)
	finish := p.serve(func() error {
		if _, err := p.request(); err != nil {
			return err
		}
		return p.conn.Close()
	})
	_, err := c.Wait(context.Background(), execproto.WaitParams{Handle: "ph_1"})
	finish(t)
	if !errors.Is(err, ErrClosed) || !errors.Is(err, io.EOF) {
		t.Fatalf("Wait when the executor closed the channel: %v, want ErrClosed wrapping io.EOF", err)
	}
}

// TestClient_ContextClosesClient: cancelling the context New was given
// closes the client; this is the stop path of its read goroutine.
func TestClient_ContextClosesClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a, b := net.Pipe()
	defer b.Close()
	c := New(ctx, a, nil)
	go func() { _, _ = io.Copy(io.Discard, b) }()
	errc := make(chan error, 1)
	go func() {
		_, err := c.Wait(context.Background(), execproto.WaitParams{Handle: "ph_1"})
		errc <- err
	}()
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrClosed) || !errors.Is(err, context.Canceled) {
			t.Errorf("pending call after the client's context ended: %v, want ErrClosed wrapping context.Canceled", err)
		}
	case <-time.After(hangGuard):
		t.Fatal("pending call still blocked after the client's context ended")
	}
	select {
	case <-c.Done():
	case <-time.After(hangGuard):
		t.Fatal("read loop still running after the client's context ended")
	}
}

// TestClient_CallContextCancel: a call whose own context ends returns
// ctx.Err() and leaves the connection usable; its late response is dropped.
func TestClient_CallContextCancel(t *testing.T) {
	c, p := newPair(t, nil)
	// Cancel only once the request is fully written and its write watch has
	// ended: cancelling during the write tears the connection down
	// (TestClient_WriteHonoursContext), which is not what this test is about.
	written := make(chan struct{}, 2)
	c.afterWrite = func() { written <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	gotFirst := make(chan struct{})
	finish := p.serve(func() error {
		m1, err := p.request()
		if err != nil {
			return err
		}
		close(gotFirst)
		m2, err := p.request()
		if err != nil {
			return err
		}
		// The late answer to the cancelled call comes first and must be
		// dropped, not handed to the next caller.
		if err := p.reply(m1.ID, execproto.WaitResult{Handle: "ph_1", State: execproto.StateRunning}); err != nil {
			return err
		}
		return p.reply(m2.ID, execproto.WaitResult{Handle: "ph_2", State: execproto.StateRunning})
	})
	errc := make(chan error, 1)
	go func() {
		_, err := c.Wait(ctx, execproto.WaitParams{Handle: "ph_1"})
		errc <- err
	}()
	<-gotFirst
	<-written
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call: %v, want context.Canceled", err)
	}
	res, err := c.Wait(context.Background(), execproto.WaitParams{Handle: "ph_2"})
	finish(t)
	if err != nil || res.Handle != "ph_2" {
		t.Fatalf("call after a cancelled one = %+v, %v; want the ph_2 result", res, err)
	}
}

// TestClient_ExecErrors: an executor error arrives as an *execproto.Error,
// so the daemon can test it with errors.Is and read its violation flag.
func TestClient_ExecErrors(t *testing.T) {
	c, p := newPair(t, nil)
	finish := p.serve(func() error {
		m, err := p.request()
		if err != nil {
			return err
		}
		return p.send(execproto.Message{ID: m.ID, Error: execproto.RPCErrorOf(execproto.NewError(execproto.ErrInvalidState, "exec.hello first"))})
	})
	var out execproto.ReadResult
	err := c.FS(context.Background(), execproto.MethodFSRead, execproto.ReadParams{Path: "a"}, &out)
	finish(t)
	var e *execproto.Error
	if !errors.Is(err, execproto.ErrInvalidState) || !errors.As(err, &e) || e.Data.Code != "invalid_state" {
		t.Fatalf("FS error = %v, want *execproto.Error invalid_state", err)
	}
}

// TestClient_ProtocolViolationCloses: the executor is the side next to
// agent-controlled processes. A message the client cannot attribute or
// does not understand is not skipped: a skipped exec.proc.io chunk would
// silently lose output that the ordering guarantee says has arrived. The
// client closes, and the pending call learns why. That includes a response
// to an id the client never issued (not a positive integer, or above the
// last id sent): an executor answering calls nobody made is not one to
// trust with the rest. A late response to an id the client did issue and
// then forgot after a cancel is dropped quietly instead
// (TestClient_CallContextCancel). The one call here has id 1.
func TestClient_ProtocolViolationCloses(t *testing.T) {
	cases := map[string]string{
		"not JSON":              `{`,
		"unknown notification":  `{"jsonrpc":"2.0","method":"exec.surprise","params":{}}`,
		"malformed io":          `{"jsonrpc":"2.0","method":"exec.proc.io","params":{"handle":1}}`,
		"io with unknown field": `{"jsonrpc":"2.0","method":"exec.proc.io","params":{"handle":"ph_1","extra":1}}`,
		"request from executor": `{"jsonrpc":"2.0","id":1,"method":"exec.hello","params":{}}`,
		"id never issued":       `{"jsonrpc":"2.0","id":2,"result":{}}`,
		"id zero":               `{"jsonrpc":"2.0","id":0,"result":{}}`,
		"negative id":           `{"jsonrpc":"2.0","id":-1,"result":{}}`,
		"string id":             `{"jsonrpc":"2.0","id":"1","result":{}}`,
		"fractional id":         `{"jsonrpc":"2.0","id":1.5,"result":{}}`,
		"null id":               `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"x"}}`,
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			c, p := newPair(t, func(execproto.IONotification) {})
			finish := p.serve(func() error {
				if _, err := p.request(); err != nil {
					return err
				}
				return p.w.WriteFrame([]byte(msg))
			})
			ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
			defer cancel()
			_, err := c.Wait(ctx, execproto.WaitParams{Handle: "ph_1"})
			finish(t)
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("Wait after %s: %v, want ErrClosed", name, err)
			}
		})
	}
}

// TestClient_OversizedRequestRefused: a request over the frame cap is
// refused before anything is written (the executor would close the channel
// on it), and the connection stays usable.
func TestClient_OversizedRequestRefused(t *testing.T) {
	c, p := newPair(t, nil)
	big := execproto.WriteParams{Path: "a", Content: strings.Repeat("x", execproto.MaxFrameBytes)}
	if err := c.FS(context.Background(), execproto.MethodFSWrite, big, nil); !errors.Is(err, execproto.ErrFrameTooLarge) {
		t.Fatalf("oversized write: %v, want ErrFrameTooLarge", err)
	}
	finish := p.serve(func() error {
		m, err := p.request()
		if err != nil {
			return err
		}
		return p.reply(m.ID, execproto.WaitResult{Handle: "ph_1", State: execproto.StateRunning})
	})
	_, err := c.Wait(context.Background(), execproto.WaitParams{Handle: "ph_1"})
	finish(t)
	if err != nil {
		t.Fatalf("call after a refused oversized one: %v", err)
	}
}

// TestClient_SpawnSendsSecretOnWireOnly guards CLAUDE.md §9 rule 3 / INV-C
// on the one path that must carry a secret value: exec.proc.spawn
// secret_env (HX-1b, D-046). SpawnParams redacts the value under
// json.Marshal by design, so a client that built the envelope with
// json.Marshal would send "[REDACTED:secret_env]" and the harness engine
// would start without its token; the frame must hold the real value.
// Equally, the frame is the only place it may appear: the client logs no
// frame and no error quotes one, so the default slog handler and the log
// package see nothing at any level.
func TestClient_SpawnSendsSecretOnWireOnly(t *testing.T) {
	var logged bytes.Buffer
	prevSlog := slog.Default()
	prevLog := log.Writer()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug - 8})))
	log.SetOutput(&logged)
	t.Cleanup(func() {
		slog.SetDefault(prevSlog)
		log.SetOutput(prevLog)
	})

	c, p := newPair(t, nil)
	params := execproto.SpawnParams{
		CallID: "call_1", Argv: []string{"copilot", "--headless"}, TimeoutMS: 60000,
		SecretEnv: execproto.NewSecretEnv(map[string]string{"COPILOT_TOKEN": providertest.FakeKey}),
		Authz:     execproto.Authz{DecisionID: "dec_1", Kind: execproto.AuthzHarness, Approved: true},
	}
	frames := make(chan []byte, 2)
	finish := p.serve(func() error {
		for i := range 2 {
			b, err := p.frame()
			if err != nil {
				return err
			}
			frames <- b
			m, err := execproto.DecodeMessage(b)
			if err != nil {
				return err
			}
			if m.Method != execproto.MethodSpawn {
				return fmt.Errorf("method %q, want %q", m.Method, execproto.MethodSpawn)
			}
			var got execproto.SpawnParams
			if err := execproto.Decode(m.Params, &got); err != nil {
				return err
			}
			if v, _ := got.SecretEnv.Value("COPILOT_TOKEN"); v != providertest.FakeKey {
				return errors.New("the executor decoded a secret_env value other than the one sent")
			}
			// The first spawn succeeds, the second fails: both paths are
			// checked for leaks.
			if i == 0 {
				if err := p.reply(m.ID, execproto.SpawnResult{Handle: "ph_1", PID: 7, PGID: 7, Executable: "/usr/bin/copilot"}); err != nil {
					return err
				}
				continue
			}
			if err := p.send(execproto.Message{ID: m.ID, Error: execproto.RPCErrorOf(execproto.NewError(execproto.ErrNotAllowed, "argv matches no profile"))}); err != nil {
				return err
			}
		}
		return nil
	})

	res, err := c.Spawn(context.Background(), params)
	_, errSpawn := c.Spawn(context.Background(), params)
	finish(t)
	close(frames) // the peer has returned: no more sends
	if err != nil || res.Handle != "ph_1" {
		t.Errorf("first Spawn = %+v, %v", res, err)
	}

	n := 0
	for b := range frames {
		if !bytes.Contains(b, []byte(providertest.FakeKey)) || bytes.Contains(b, []byte(execproto.RedactedValue)) {
			t.Errorf("spawn frame %d does not carry the real secret_env value: %s", n, bytes.ReplaceAll(b, []byte(providertest.FakeKey), []byte("<secret>")))
		}
		n++
	}
	if n != 2 {
		t.Errorf("the executor received %d spawn frames, want 2", n)
	}
	if errSpawn == nil {
		t.Fatal("second Spawn succeeded, want ErrNotAllowed")
	}
	if !errors.Is(errSpawn, execproto.ErrNotAllowed) {
		t.Errorf("second Spawn: %v, want ErrNotAllowed", errSpawn)
	}
	if strings.Contains(errSpawn.Error(), providertest.FakeKey) {
		t.Error("the Spawn error quotes the secret value")
	}
	if logged.Len() != 0 {
		t.Errorf("the client logged %d bytes; it must log no frame: %q", logged.Len(), strings.ReplaceAll(logged.String(), providertest.FakeKey, "<secret>"))
	}
}

// TestClient_TypedMethods: each typed method sends its own A06 §4.2 method
// name and decodes the result into its own type. A wrapper wired to the
// wrong name would, for example, send a signal as a wait.
func TestClient_TypedMethods(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		method string
		result any
		call   func(c *Client) error
	}{
		{execproto.MethodHello, execproto.HelloResult{Protocol: execproto.Protocol}, func(c *Client) error {
			r, err := c.Hello(ctx, execproto.HelloParams{})
			if err == nil && r.Protocol != execproto.Protocol {
				err = fmt.Errorf("result %+v", r)
			}
			return err
		}},
		{execproto.MethodSignal, execproto.SignalResult{Delivered: true, PGID: 9}, func(c *Client) error {
			r, err := c.Signal(ctx, execproto.SignalParams{Handle: "ph_1", Signal: execproto.SignalTERM})
			if err == nil && r.PGID != 9 {
				err = fmt.Errorf("result %+v", r)
			}
			return err
		}},
		{execproto.MethodGitRun, execproto.GitRunResult{}, func(c *Client) error {
			_, err := c.Git(ctx, execproto.GitRunParams{Op: execproto.GitOp("status")})
			return err
		}},
		{execproto.MethodShutdown, execproto.ShutdownResult{KilledPIDs: 3}, func(c *Client) error {
			r, err := c.Shutdown(ctx, execproto.ShutdownParams{})
			if err == nil && r.KilledPIDs != 3 {
				err = fmt.Errorf("result %+v", r)
			}
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			c, p := newPair(t, nil)
			finish := p.serve(func() error {
				m, err := p.request()
				if err != nil {
					return err
				}
				if m.Method != tc.method {
					return fmt.Errorf("method %q, want %q", m.Method, tc.method)
				}
				return p.reply(m.ID, tc.result)
			})
			err := tc.call(c)
			finish(t)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	c, _ := newPair(t, nil)
	if err := c.FS(ctx, execproto.MethodSpawn, execproto.SpawnParams{}, nil); err == nil {
		t.Error("FS sent a method outside exec.fs.*")
	}
}

// TestClient_WriteHonoursContext guards the review finding on bcfa1fc
// (CWE-1088): an executor that stops reading left Call blocked in
// WriteFrame past its deadline, and every other call queued on the writer
// behind it, so Wait, Signal and Shutdown could not meet theirs either. A
// context that ends during a write now tears the connection down: a
// partially written frame cannot be taken back, so this is the same rule
// as a write error. The peer reads only the first bytes of the first frame
// (so that call provably holds the writer, mid-frame) and then nothing.
func TestClient_WriteHonoursContext(t *testing.T) {
	a, b := net.Pipe()
	c := New(context.Background(), a, nil)
	t.Cleanup(func() { _ = c.Close(); _ = b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	errc := make(chan error, 1)
	go func() { errc <- c.Call(ctx, execproto.MethodWait, execproto.WaitParams{Handle: "ph_1"}, nil) }()
	if _, err := bufio.NewReader(b).Peek(1); err != nil { // the call above holds the writer
		t.Fatal(err)
	}
	const queued = 3
	qerrs := make(chan error, queued)
	for range queued {
		go func() {
			_, err := c.Signal(context.Background(), execproto.SignalParams{Handle: "ph_1", Signal: execproto.SignalKILL})
			qerrs <- err
		}()
	}

	select {
	case err := <-errc:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Call blocked in a write: %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(hangGuard):
		t.Fatal("Call still blocked in a write after its deadline")
	}
	select {
	case <-c.Done():
	case <-time.After(hangGuard):
		t.Fatal("the client is still open after a write was abandoned")
	}
	for range queued {
		select {
		case err := <-qerrs:
			if !errors.Is(err, ErrClosed) {
				t.Errorf("call queued behind the blocked write: %v, want ErrClosed", err)
			}
		case <-time.After(hangGuard):
			t.Fatal("a call queued behind the blocked write is still blocked")
		}
	}
}

// TestClient_CancelWhileWaitingForWriter guards the re-review finding on
// c37e6d7: the write watch was armed before the call held the writer, so a
// call that was only queued behind another call's write (nothing of its
// own on the wire) closed the connection when its context ended, and the
// innocent call holding the writer failed with it. A call cancelled before
// it holds the writer has written nothing and may simply return.
// A writes 4 MiB that the executor reads only after B is cancelled.
func TestClient_CancelWhileWaitingForWriter(t *testing.T) {
	a, b := net.Pipe()
	c := New(context.Background(), a, nil)
	t.Cleanup(func() { _ = c.Close(); _ = b.Close() })
	waiting := make(chan struct{}, 2)
	c.beforeWrite = func() { waiting <- struct{}{} }

	br := bufio.NewReader(b) // NewFrameReader reuses it, so peeked bytes are kept
	p := &peer{conn: b, r: execproto.NewFrameReader(br), w: execproto.NewFrameWriter(b)}
	release := make(chan struct{})
	finish := p.serve(func() error {
		<-release
		m, err := p.request()
		if err != nil {
			return err
		}
		return p.reply(m.ID, execproto.WriteResult{BytesWritten: 4 << 20})
	})

	errA := make(chan error, 1)
	go func() {
		errA <- c.FS(context.Background(), execproto.MethodFSWrite, execproto.WriteParams{Path: "a", Content: strings.Repeat("x", 4<<20)}, nil)
	}()
	<-waiting
	if _, err := br.Peek(1); err != nil { // A holds the writer and is mid-frame
		t.Fatal(err)
	}

	ctxB, cancelB := context.WithCancel(context.Background())
	errB := make(chan error, 1)
	go func() {
		_, err := c.Signal(ctxB, execproto.SignalParams{Handle: "ph_1", Signal: execproto.SignalTERM})
		errB <- err
	}()
	<-waiting
	cancelB()
	select {
	case err := <-errB:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("B, cancelled while waiting for the writer: %v, want context.Canceled", err)
		}
	case <-time.After(hangGuard):
		t.Fatal("B still blocked after its context ended")
	}
	close(release)
	if err := <-errA; err != nil {
		t.Errorf("A, which held the writer: %v; B's cancel must not touch it", err)
	}
	finish(t)
	select {
	case <-c.Done():
		t.Fatal("the connection closed although nothing was abandoned mid-frame")
	default:
	}
}
