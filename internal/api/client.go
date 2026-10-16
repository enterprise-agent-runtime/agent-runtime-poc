package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/channel"

	"warden.dev/warden/internal/platform"
)

// Client is a runtime API client (the CLI; the desktop speaks the same
// protocol through its Rust bridge). It reads the token file, connects over
// the platform transport and performs system.hello.
//
// It is a small purpose-built JSON-RPC client rather than jrpc2.Client:
// jrpc2's client hands every inbound message to its own goroutine, which
// can reorder notifications, while A05 §2.3 guarantees event notifications
// in seq order. Here one read loop delivers notifications synchronously,
// in wire order.
type Client struct {
	ch    channel.Channel
	nc    net.Conn
	Hello map[string]any

	sendMu   sync.Mutex
	mu       sync.Mutex
	next     int64
	pending  map[string]chan reply
	onNotify Notify
	closed   chan struct{}
	err      error
}

// Notify receives server notifications (event, stream.delta, event.gap) in
// wire order, on the client's read goroutine; it must not block on Call.
type Notify func(method string, params json.RawMessage)

type reply struct {
	result json.RawMessage
	err    *jrpc2.Error
}

// Dial connects to the daemon of a Warden home.
func Dial(ctx context.Context, l platform.Layout, name, version string, onNotify Notify) (*Client, error) {
	tok, err := os.ReadFile(l.Token())
	if err != nil {
		return nil, fmt.Errorf("wardend is not running (no token file at %s)", l.Token())
	}
	nc, err := platform.Dial(ctx, l.Home)
	if err != nil {
		return nil, err
	}
	return Handshake(ctx, nc, strings.TrimSpace(string(tok)), name, version, onNotify)
}

// Handshake wraps an established connection and performs system.hello.
func Handshake(ctx context.Context, nc net.Conn, token, name, version string, onNotify Notify) (*Client, error) {
	return handshake(ctx, nc, token, Protocol, name, version, onNotify)
}

func handshake(ctx context.Context, nc net.Conn, token, protocol, name, version string, onNotify Notify) (*Client, error) {
	c := &Client{ch: channel.Header("")(nc, nc), nc: nc, pending: map[string]chan reply{}, onNotify: onNotify, closed: make(chan struct{})}
	go c.readLoop()
	err := c.Call(ctx, "system.hello", map[string]any{"token": token, "client": map[string]string{"name": name, "version": version}, "protocol": protocol}, &c.Hello)
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) readLoop() {
	defer close(c.closed)
	for {
		b, err := c.ch.Recv()
		if err != nil {
			c.mu.Lock()
			c.err = errors.New("connection to wardend closed")
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *jrpc2.Error    `json:"error"`
		}
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if m.Method != "" {
			if c.onNotify != nil {
				c.onNotify(m.Method, m.Params)
			}
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[string(m.ID)]
		delete(c.pending, string(m.ID))
		c.mu.Unlock()
		if ok {
			ch <- reply{m.Result, m.Error}
		}
	}
}

// Call invokes a method; result may be nil. Errors from the daemon are
// *jrpc2.Error (see DataOf).
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	if params == nil {
		params = map[string]any{}
	}
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
	}
	c.next++
	id := strconv.FormatInt(c.next, 10)
	ch := make(chan reply, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.next, "method": method, "params": json.RawMessage(p)})
	c.sendMu.Lock()
	err = c.ch.Send(msg)
	c.sendMu.Unlock()
	if err != nil {
		return err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return errors.New("connection to wardend closed")
		}
		if r.err != nil {
			return r.err
		}
		if result != nil {
			return json.Unmarshal(r.result, result)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		cancel, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "$/cancelRequest", "params": map[string]any{"id": json.RawMessage(id)}})
		c.sendMu.Lock()
		_ = c.ch.Send(cancel)
		c.sendMu.Unlock()
		return ctx.Err()
	}
}

// Close ends the connection.
func (c *Client) Close() error {
	err := c.nc.Close()
	<-c.closed
	return err
}
