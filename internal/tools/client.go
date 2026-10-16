// Package tools is the daemon side of the executor protocol warden.exec/1:
// the client that drives warden-exec over an inherited socketpair (L1) or
// the sandbox container's stdio (L2). It implements WRD-04 §5 and design
// A06 §4.1 (transport rules, ordering) and §4.2 (methods) on the client
// side, with the API sketched in design A02 §4.8. The tool descriptors of
// A02 §4.8 arrive with the agent loop (M3).
//
// The client is purpose-built rather than jrpc2.Client, for the reason
// recorded in docs/DECISIONS-poc.md D-016: jrpc2's client hands every
// inbound message to its own goroutine and reordered notifications. Here one
// read loop handles frames in wire order and calls OnIO synchronously, so
// exec.proc.io chunks arrive in order and every chunk the executor wrote
// before a response has been delivered before that response's call returns.
// Together with the executor's guarantee that all exec.proc.io for a handle
// are written before the exec.proc.wait response reporting its exit (A06
// §4.1), a returned Wait means the whole output is in.
//
// Params are encoded with execproto.MarshalWire into a json.RawMessage
// before the envelope is built: exec.proc.spawn carries secret_env, which
// json.Marshal deliberately redacts (D-046). The client logs nothing; no
// frame, and no error text built from one, reaches a log (INV-C).
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"warden.dev/warden/internal/execproto"
)

// Error is a client error. It is a constant type, so ErrClosed is not
// global mutable state (CLAUDE.md §7).
type Error string

func (e Error) Error() string { return string(e) }

// ErrClosed is the error of every call that cannot complete because the
// connection is closed: by Close, by the client's context, by the executor
// (EOF) or after a protocol violation. Errors wrap it together with the
// cause, so errors.Is(err, ErrClosed) and errors.Is(err, io.EOF) both work.
const ErrClosed Error = "tools: executor connection closed"

// IOHandler receives exec.proc.io notifications in wire order, on the
// client's read goroutine. It must return promptly and must not wait for a
// call on the same client: the read loop that would deliver that call's
// response is the one running the handler.
type IOHandler func(execproto.IONotification)

// Client is a warden.exec/1 client on one connection. Its methods are safe
// for concurrent use; calls are correlated with their responses by id.
type Client struct {
	rwc   io.Closer
	w     *execproto.FrameWriter
	wlock chan struct{} // capacity 1: the writer, held for one whole frame; waiting on it honours ctx
	onIO  IOHandler

	mu      sync.Mutex
	next    int64
	pending map[int64]chan reply
	err     error // non-nil once closed; wraps ErrClosed

	done chan struct{} // closed when the read loop has returned

	// afterWrite, when set (tests only, before any call), runs after a
	// request's write and its context watch have ended.
	afterWrite func()
	// beforeWrite, likewise, runs just before a request waits for the writer.
	beforeWrite func()
}

type reply struct {
	result json.RawMessage
	err    *execproto.RPCError
}

// New starts a client on rwc, which it owns from now on. onIO may be nil.
// The client closes when ctx ends, when Close is called, when the executor
// closes the channel, or on the first message it cannot accept.
func New(ctx context.Context, rwc io.ReadWriteCloser, onIO IOHandler) *Client {
	c := &Client{rwc: rwc, w: execproto.NewFrameWriter(rwc), wlock: make(chan struct{}, 1), onIO: onIO, pending: map[int64]chan reply{}, done: make(chan struct{})}
	go c.readLoop(execproto.NewFrameReader(rwc))
	go func() {
		select {
		case <-ctx.Done():
			c.fail(ctx.Err())
		case <-c.done:
		}
	}()
	return c
}

// Done is closed once the client is closed and its read loop has returned.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close closes the connection and fails every pending call with ErrClosed.
// It waits for the read loop, so no IOHandler call is running after it
// returns. Calling it again is a no-op.
func (c *Client) Close() error {
	c.fail(nil)
	<-c.done
	return nil
}

// fail closes the client once, recording why. A nil cause is Close.
func (c *Client) fail(cause error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	if cause == nil {
		c.err = ErrClosed
	} else {
		c.err = fmt.Errorf("%w: %w", ErrClosed, cause)
	}
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	_ = c.rwc.Close() // unblocks the read loop and any blocked write
}

func (c *Client) closedErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// readLoop is the only reader. It returns on the first read error, which
// Close and the context watcher cause by closing the connection.
func (c *Client) readLoop(r *execproto.FrameReader) {
	defer close(c.done)
	for {
		b, err := r.ReadFrame()
		if err != nil {
			c.fail(err)
			return
		}
		if err := c.handle(b); err != nil {
			c.fail(err)
			return
		}
	}
}

// handle processes one frame. An error is a protocol violation and closes
// the client. Its text is fixed or comes from Decode, which names keys and
// positions but never quotes values.
func (c *Client) handle(b []byte) error {
	m, err := execproto.DecodeMessage(b)
	if err != nil {
		return fmt.Errorf("tools: invalid message from the executor: %w", err)
	}
	switch m.Kind() {
	case execproto.KindNotification:
		if m.Method != execproto.MethodIO {
			return errors.New("tools: unknown notification from the executor")
		}
		var n execproto.IONotification
		if err := execproto.Decode(m.Params, &n); err != nil {
			return fmt.Errorf("tools: invalid exec.proc.io: %w", err)
		}
		if c.onIO != nil {
			c.onIO(n)
		}
	case execproto.KindResponse:
		id, err := strconv.ParseInt(string(m.ID), 10, 64)
		c.mu.Lock()
		issued := c.next // incremented before the request is written
		ch, ok := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if err != nil || id <= 0 || id > issued {
			// Ids are 1, 2, 3 … as sent; anything else answers a call
			// nobody made.
			return errors.New("tools: response to an id this client never issued")
		}
		if ok { // absent: the caller gave up (context) and the answer is late
			ch <- reply{result: m.Result, err: m.Error}
		}
	default:
		return errors.New("tools: the executor sent a request; it never may (A06 §4.1)")
	}
	return nil
}

// Call invokes method with params (nil sends {}) and decodes the result
// strictly into result (nil discards it). Executor errors come back as
// *execproto.Error, wrapped with the method name.
//
// Once the request has been sent, an error other than an executor error
// (context end, close) leaves the outcome unknown: warden.exec/1 has no
// cancel, so the executor may have done it. Callers re-read the state they
// depend on (exec.fs.stat, exec.proc.wait) before acting on either outcome.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("tools: %s: %w", method, err)
	}
	p := json.RawMessage(`{}`)
	if params != nil {
		b, err := execproto.MarshalWire(params)
		if err != nil {
			return fmt.Errorf("tools: %s: encode params: %w", method, err)
		}
		p = b
	}

	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return fmt.Errorf("tools: %s: %w", method, err)
	}
	c.next++
	id := c.next
	ch := make(chan reply, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	msg, err := json.Marshal(execproto.Message{JSONRPC: execproto.JSONRPCVersion, ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method, Params: p})
	if err == nil {
		// A write blocks while the executor does not read, and every other
		// call queues behind it on the writer. If ctx ends meanwhile, the
		// connection is torn down: a partly written frame cannot be taken
		// back, the same rule as a write error (CWE-1088).
		// A ctx that ends just as a write completes may still tear down:
		// conservative, never a partial frame.
		//
		// The watch is armed only once this call holds the writer. A call
		// still waiting for it has written nothing, so its cancel just
		// returns; tearing down there failed the call holding the writer.
		if c.beforeWrite != nil {
			c.beforeWrite()
		}
		select {
		case c.wlock <- struct{}{}:
		case <-ctx.Done():
			c.forget(id)
			return fmt.Errorf("tools: %s: %w", method, ctx.Err())
		}
		if cerr := ctx.Err(); cerr != nil { // both were ready and the lock won
			<-c.wlock
			c.forget(id)
			return fmt.Errorf("tools: %s: %w", method, cerr)
		}
		stop := context.AfterFunc(ctx, func() { c.fail(ctx.Err()) })
		err = c.w.WriteFrame(msg)
		stop()
		<-c.wlock
		if c.afterWrite != nil {
			c.afterWrite()
		}
	}
	if err != nil {
		c.forget(id)
		if !errors.Is(err, execproto.ErrFrameTooLarge) && !errors.Is(err, execproto.ErrFrameHeader) {
			// A failed write leaves a partial frame: the stream is unusable.
			c.fail(err)
			if cerr := c.closedErr(); cerr != nil {
				err = cerr
			}
		}
		return fmt.Errorf("tools: %s: %w", method, err)
	}

	select {
	case r, ok := <-ch:
		if !ok {
			return fmt.Errorf("tools: %s: %w", method, c.closedErr())
		}
		if r.err != nil {
			return fmt.Errorf("tools: %s: %w", method, r.err.Err())
		}
		if result != nil {
			if err := execproto.Decode(r.result, result); err != nil {
				return fmt.Errorf("tools: %s: result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		// warden.exec/1 has no cancel message (A06 §4.2): the daemon stops a
		// process with exec.proc.signal. The late response is dropped.
		c.forget(id)
		return fmt.Errorf("tools: %s: %w", method, ctx.Err())
	}
}

func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Hello calls exec.hello, which must be the first call (A06 §4.1).
func (c *Client) Hello(ctx context.Context, p execproto.HelloParams) (execproto.HelloResult, error) {
	var r execproto.HelloResult
	err := c.Call(ctx, execproto.MethodHello, p, &r)
	return r, err
}

// FS calls one of the exec.fs.* methods; out may be nil.
func (c *Client) FS(ctx context.Context, method string, p, out any) error {
	if !strings.HasPrefix(method, "exec.fs.") {
		return fmt.Errorf("tools: FS: %q is not an exec.fs method", method)
	}
	return c.Call(ctx, method, p, out)
}

// Spawn calls exec.proc.spawn. Its params carry secret_env, which reaches
// the wire through MarshalWire only.
func (c *Client) Spawn(ctx context.Context, p execproto.SpawnParams) (execproto.SpawnResult, error) {
	var r execproto.SpawnResult
	err := c.Call(ctx, execproto.MethodSpawn, p, &r)
	return r, err
}

// Wait calls exec.proc.wait. When it returns a result with state "exited",
// every exec.proc.io notification of that handle has already been passed
// to the IOHandler (A06 §4.1).
func (c *Client) Wait(ctx context.Context, p execproto.WaitParams) (execproto.WaitResult, error) {
	var r execproto.WaitResult
	err := c.Call(ctx, execproto.MethodWait, p, &r)
	return r, err
}

// Signal calls exec.proc.signal.
func (c *Client) Signal(ctx context.Context, p execproto.SignalParams) (execproto.SignalResult, error) {
	var r execproto.SignalResult
	err := c.Call(ctx, execproto.MethodSignal, p, &r)
	return r, err
}

// Git calls exec.git.run.
func (c *Client) Git(ctx context.Context, p execproto.GitRunParams) (execproto.GitRunResult, error) {
	var r execproto.GitRunResult
	err := c.Call(ctx, execproto.MethodGitRun, p, &r)
	return r, err
}

// Shutdown calls exec.shutdown.
func (c *Client) Shutdown(ctx context.Context, p execproto.ShutdownParams) (execproto.ShutdownResult, error) {
	var r execproto.ShutdownResult
	err := c.Call(ctx, execproto.MethodShutdown, p, &r)
	return r, err
}
