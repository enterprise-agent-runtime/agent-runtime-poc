// Package exec is the in-sandbox executor of warden-exec: the server side
// of the executor protocol warden.exec/1 (WRD-04 §5; design A06 §4.1
// transport rules, §4.2 methods, §4.4 error codes; package layout per
// design A02 §4.15). It runs only inside the sandbox, linked only into
// cmd/warden-exec (A02 §5 R4), and imports from this module only
// internal/execproto and its own subpackages (R3).
//
// This file is the transport: a method registry and a serve loop over any
// io.ReadWriteCloser, so the same code runs over the inherited socketpair
// of L1 (--rpc-fd 3) and the container stdio of L2 (--rpc-stdio). The
// file methods are registered by RegisterFS (fsserver.go, fs_unix.go);
// proc and git come in later milestones.
//
// What the loop guarantees:
//
//   - exec.hello is the gate. Until one exec.hello has succeeded, every
//     other request fails with invalid_state; hello itself runs on the read
//     loop, so no request read after it can overtake it, and a second hello
//     is refused (A06 §4.1).
//   - Every other request runs on its own goroutine (A06 §4.1 "processed
//     concurrently"), with a context that ends when Serve does.
//   - Responses and notifications go through one FrameWriter, one whole
//     frame at a time. Notify returns only once its frame is written, so a
//     handler that calls Notify before it returns has its notifications on
//     the wire before its response. That is the transport's half of the
//     exec.proc.io-before-exec.proc.wait guarantee; the process table's half
//     is to have flushed a handle's output before its wait returns.
//   - Nothing is logged here, and handler errors that are not executor
//     errors reach the daemon as io_error with a fixed detail, because OS
//     error text quotes paths and arguments (A06 §4.4, INV-C).
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"warden.dev/warden/internal/execproto"
)

// Handler handles one method. params is the raw params member ({} when
// absent). It returns a value encoded with execproto.MarshalWire, or an
// error: an *execproto.Error or execproto.Code travels as itself, anything
// else as a sanitized io_error.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Server serves warden.exec/1 on one connection.
type Server struct {
	rwc io.ReadWriteCloser
	w   *execproto.FrameWriter

	mu       sync.Mutex
	handlers map[string]Handler
	serving  bool
}

// NewServer returns a server on rwc. Serve owns rwc and closes it.
func NewServer(rwc io.ReadWriteCloser) *Server {
	return &Server{rwc: rwc, w: execproto.NewFrameWriter(rwc), handlers: map[string]Handler{}}
}

// Register adds a handler. The method table is fixed once Serve starts:
// registering later is an error, which is what lets the read loop use the
// table without a lock.
func (s *Server) Register(method string, h Handler) error {
	if h == nil || method == "" {
		return errors.New("exec: Register needs a method name and a handler")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.serving {
		return fmt.Errorf("exec: Register %s after Serve started", method)
	}
	if _, dup := s.handlers[method]; dup {
		return fmt.Errorf("exec: %s registered twice", method)
	}
	s.handlers[method] = h
	return nil
}

// paramsError marks params that do not decode into the method's type: a
// JSON-RPC invalid-params error, not an executor error.
type paramsError struct{ err error }

func (e paramsError) Error() string { return "invalid params: " + e.err.Error() }

// Handle registers a typed handler: params are decoded strictly with
// execproto.Decode (unknown, case-variant and duplicate keys refused), and
// a failure is answered with JSON-RPC invalid params without calling fn.
func Handle[P, R any](s *Server, method string, fn func(context.Context, P) (R, error)) error {
	if fn == nil {
		return errors.New("exec: Handle needs a function")
	}
	return s.Register(method, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p P
		if err := execproto.Decode(raw, &p); err != nil {
			return nil, paramsError{err}
		}
		return fn(ctx, p)
	})
}

// Notify writes one notification and returns once it is on the wire (or
// failed). It is safe for concurrent use with responses and other
// notifications.
func (s *Server) Notify(method string, params any) error {
	p, err := execproto.MarshalWire(params)
	if err != nil {
		return fmt.Errorf("exec: notify %s: %w", method, err)
	}
	b, err := json.Marshal(execproto.Message{JSONRPC: execproto.JSONRPCVersion, Method: method, Params: p})
	if err != nil {
		return fmt.Errorf("exec: notify %s: %w", method, err)
	}
	return s.w.WriteFrame(b)
}

// Serve reads requests until the connection ends or ctx does, then closes
// the connection, waits for every running handler (whose context it has
// cancelled) and returns. It returns nil when the daemon closed the channel
// (EOF), ctx.Err() when ctx ended, and the framing or I/O error otherwise;
// a frame over MaxFrameBytes closes the channel (A06 §4.1) and returns
// execproto.ErrFrameTooLarge.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	if s.serving {
		s.mu.Unlock()
		return errors.New("exec: Serve called twice")
	}
	s.serving = true
	s.mu.Unlock()

	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Closing the connection is what unblocks the read loop when ctx ends.
	go func() {
		<-hctx.Done()
		_ = s.rwc.Close()
	}()

	var wg sync.WaitGroup
	err := s.readLoop(hctx, &wg)
	cancel()
	_ = s.rwc.Close()
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func (s *Server) readLoop(ctx context.Context, wg *sync.WaitGroup) error {
	r := execproto.NewFrameReader(s.rwc)
	helloDone := false
	for {
		b, err := r.ReadFrame()
		if err != nil {
			return err
		}
		m, err := execproto.DecodeMessage(b)
		if err != nil {
			code := execproto.RPCInvalidRequest
			if !json.Valid(b) {
				code = execproto.RPCParseError
			}
			if err := s.respond(nil, nil, &execproto.RPCError{Code: code, Message: "invalid request"}); err != nil {
				return err
			}
			continue
		}
		if m.Kind() != execproto.KindRequest {
			// The daemon sends only requests (A06 §4.1). A notification
			// needs no answer and a response has no caller here.
			continue
		}
		switch {
		case m.Method == execproto.MethodHello && helloDone:
			err = s.respond(m.ID, nil, execproto.RPCErrorOf(execproto.NewError(execproto.ErrInvalidState, "exec.hello already completed")))
		case m.Method == execproto.MethodHello:
			// On the read loop: nothing read after hello can run before it.
			var ok bool
			ok, err = s.call(ctx, m)
			helloDone = ok
		case !helloDone:
			err = s.respond(m.ID, nil, execproto.RPCErrorOf(execproto.NewError(execproto.ErrInvalidState, "exec.hello must be the first call")))
		default:
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = s.call(ctx, m) // a write error also ends the read loop: the connection is closed below
			}()
		}
		if err != nil {
			return err
		}
	}
}

// call runs the handler of m and writes its response. ok reports a
// successful result; err is a write failure, after which the connection is
// unusable and is closed.
func (s *Server) call(ctx context.Context, m execproto.Message) (ok bool, err error) {
	h := s.handlers[m.Method] // fixed since Serve started
	if h == nil {
		return false, s.respond(m.ID, nil, &execproto.RPCError{Code: execproto.RPCMethodNotFound, Message: "method not found"})
	}
	params := m.Params
	if params == nil {
		params = json.RawMessage(`{}`)
	}
	res, herr := h(ctx, params)
	if herr != nil {
		return false, s.respond(m.ID, nil, rpcErrorFor(herr))
	}
	b, merr := execproto.MarshalWire(res)
	if merr != nil {
		return false, s.respond(m.ID, nil, execproto.RPCErrorOf(execproto.NewError(execproto.ErrIOError, "result could not be encoded")))
	}
	return true, s.respond(m.ID, b, nil)
}

// rpcErrorFor maps a handler error to what the daemon sees.
func rpcErrorFor(err error) *execproto.RPCError {
	var pe paramsError
	var ee *execproto.Error
	var code execproto.Code
	switch {
	case errors.As(err, &pe):
		// Fixed text: the decoder's message names keys from the input.
		return &execproto.RPCError{Code: execproto.RPCInvalidParams, Message: "invalid params"}
	case errors.As(err, &ee):
		return execproto.RPCErrorOf(ee)
	case errors.As(err, &code) && code.Name() != "":
		return execproto.RPCErrorOf(execproto.NewError(code, ""))
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return execproto.RPCErrorOf(execproto.NewError(execproto.ErrShuttingDown, ""))
	}
	return execproto.RPCErrorOf(execproto.NewError(execproto.ErrIOError, "internal error"))
}

// respond writes one response: result when rerr is nil. A nil id is sent
// as null (the request could not be read). A result too large for one
// frame is replaced by too_large (A06 §4.4); any other write error closes
// the connection, because a partial frame cannot be taken back.
func (s *Server) respond(id, result json.RawMessage, rerr *execproto.RPCError) error {
	if id == nil {
		id = json.RawMessage(`null`)
	}
	m := execproto.Message{JSONRPC: execproto.JSONRPCVersion, ID: id, Result: result, Error: rerr}
	if rerr == nil && result == nil {
		m.Result = json.RawMessage(`null`)
	}
	b, err := json.Marshal(m)
	if err == nil {
		err = s.w.WriteFrame(b)
	}
	if errors.Is(err, execproto.ErrFrameTooLarge) && rerr == nil {
		return s.respond(id, nil, execproto.RPCErrorOf(execproto.NewError(execproto.ErrTooLarge, "result larger than the 8 MiB frame")))
	}
	if err != nil {
		_ = s.rwc.Close()
	}
	return err
}
