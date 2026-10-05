// Package api implements the runtime API of WRD-02 §4, WRD-16 §12 and
// design A05: JSON-RPC 2.0 with LSP Content-Length framing over the
// platform transport (Unix socket or named pipe, never TCP: INV-J), the
// system.hello token handshake, strict params validated against the
// embedded A05 schemas, server notifications for live events, and the M1
// methods (system.*, provider.*, event.*, audit.*, secrets.unlock).
package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/channel"

	"warden.dev/warden/internal/api/schema"
	"warden.dev/warden/internal/config"
	"warden.dev/warden/internal/ids"
	"warden.dev/warden/internal/model"
	"warden.dev/warden/internal/platform"
	"warden.dev/warden/internal/sandbox"
	"warden.dev/warden/internal/secrets"
	"warden.dev/warden/internal/store"
)

// Protocol is the protocol string spoken by this daemon (A05 §4).
const Protocol = "warden.poc/1"

// Limits of A05 §2.5 enforced in M1.
const (
	MaxConnections   = 16
	MaxSubscriptions = 32
	MaxInflight      = 64
	HelloDeadline    = 10 * time.Second
	SubscriptionQ    = 1024
)

// Deps are the daemon's components, wired by cmd/wardend.
type Deps struct {
	Layout      platform.Layout
	Store       *store.Store
	Broker      *secrets.Broker
	File        *secrets.File // non-nil when the encrypted-file backend is active
	Catalog     *config.Catalog
	CatalogPath string
	Cache       *config.ProbeCache
	NewProvider func(model.ProviderConfig) (model.Provider, error)
	// Discover lists an openai-compatible endpoint's models when a provider
	// is added without any (D-015); nil disables discovery.
	Discover     func(context.Context, model.ProviderConfig) ([]string, error)
	Token        string
	Mode         string // personal | shared
	DefaultLevel string
	Version      string
	Commit       string
	Clock        func() time.Time
	Shutdown     func()
	OnUnlock     func(ctx context.Context) error // loads the checkpoint key after secrets.unlock
	SignerKeyID  func() string                   // "" when checkpoints are unsigned
	// Probes; nil uses the real implementations (tests inject fakes).
	SandboxProbe func(context.Context, sandbox.Options) []sandbox.Check
	GitVersion   func(context.Context) (string, error)
	LocalServers func(context.Context) []string
}

type handlerFunc func(ctx context.Context, c *conn, req *jrpc2.Request, raw json.RawMessage) (any, error)

// Server serves the runtime API.
type Server struct {
	d       Deps
	reg     *schema.Registry
	methods map[string]handlerFunc
	ids     *ids.Generator

	mu        sync.Mutex // append + publish + subscription registration (A05 §6.4)
	published int64
	bus       *bus

	catMu sync.Mutex // catalog and models.yaml
	nconn atomic.Int32
	wg    sync.WaitGroup

	connMu sync.Mutex
	conns  map[net.Conn]bool
}

// New builds the server.
func New(d Deps) (*Server, error) {
	reg, err := schema.Load()
	if err != nil {
		return nil, err
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.Mode == "" {
		d.Mode = "personal"
	}
	if d.Catalog == nil {
		d.Catalog = &config.Catalog{}
	}
	s := &Server{d: d, reg: reg, ids: ids.New(), bus: newBus(), conns: map[net.Conn]bool{}}
	s.published = d.Store.HeadSeq()
	s.methods = map[string]handlerFunc{
		"system.hello":      s.hello,
		"system.version":    s.version,
		"system.doctor":     s.doctor,
		"system.shutdown":   s.shutdown,
		"secrets.unlock":    s.unlock,
		"provider.list":     s.providerList,
		"provider.add":      s.providerAdd,
		"provider.remove":   s.providerRemove,
		"provider.enable":   s.providerEnable,
		"provider.test":     s.providerTest,
		"provider.models":   s.providerModels,
		"event.subscribe":   s.eventSubscribe,
		"event.unsubscribe": s.eventUnsubscribe,
		"event.query":       s.eventQuery,
		"audit.export":      s.auditExport,
		"audit.verify":      s.auditVerify,
	}
	for _, p := range d.Catalog.Providers {
		d.Broker.SetProvider(p.ID, secrets.ProviderAuth{Protocol: p.Protocol, Auth: authOf(p)})
	}
	return s, nil
}

// Methods lists the implemented methods.
func (s *Server) Methods() []string {
	var out []string
	for m := range s.methods {
		out = append(out, m)
	}
	return out
}

// Append writes an event and publishes every newly committed event
// (including periodic checkpoints) to subscribers, in seq order.
func (s *Server) Append(ctx context.Context, in store.Input) (store.Envelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.d.Store.Append(ctx, in)
	if err != nil {
		return e, err
	}
	s.publishNewLocked(ctx)
	return e, nil
}

// AppendGroup writes events in one transaction and publishes them.
func (s *Server) AppendGroup(ctx context.Context, ins []store.Input) ([]store.Envelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	evs, err := s.d.Store.AppendGroup(ctx, ins)
	if err != nil {
		return evs, err
	}
	s.publishNewLocked(ctx)
	return evs, nil
}

// Publish forwards events committed by other components.
func (s *Server) Publish(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishNewLocked(ctx)
}

func (s *Server) publishNewLocked(ctx context.Context) {
	for {
		evs, err := s.d.Store.AllEvents(ctx, s.published, 500)
		if err != nil || len(evs) == 0 {
			return
		}
		for _, raw := range evs {
			var h struct {
				Seq   int64  `json:"seq"`
				Type  string `json:"type"`
				Chain string `json:"chain"`
			}
			_ = json.Unmarshal(raw, &h)
			s.bus.publish(h.Seq, h.Chain, h.Type, raw)
			s.published = h.Seq
		}
	}
}

// Serve accepts connections until ctx ends or l is closed.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	go func() {
		<-ctx.Done()
		l.Close()
		// Shutdown ends every client connection, so Serve can return.
		s.connMu.Lock()
		for nc := range s.conns {
			nc.Close()
		}
		s.connMu.Unlock()
	}()
	for {
		nc, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				s.wg.Wait()
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			s.wg.Wait()
			return err
		}
		if s.nconn.Add(1) > MaxConnections {
			s.nconn.Add(-1)
			nc.Close()
			continue
		}
		s.connMu.Lock()
		if ctx.Err() != nil {
			s.connMu.Unlock()
			nc.Close()
			s.nconn.Add(-1)
			continue
		}
		s.conns[nc] = true
		s.connMu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.nconn.Add(-1)
			defer func() { s.connMu.Lock(); delete(s.conns, nc); s.connMu.Unlock() }()
			s.ServeConn(ctx, nc)
		}()
	}
}

// conn is one client connection.
type conn struct {
	s          *Server
	nc         net.Conn
	srv        *jrpc2.Server
	id         string
	authed     atomic.Bool
	closeAfter atomic.Bool

	mu      sync.Mutex
	subs    map[string]*sub
	waiters map[string]chan struct{} // request id → closed after its response is sent
}

// ServeConn serves one connection until it closes.
func (s *Server) ServeConn(ctx context.Context, nc net.Conn) {
	c := &conn{s: s, nc: nc, id: "conn_" + s.ids.ULID(), subs: map[string]*sub{}, waiters: map[string]chan struct{}{}}
	ch := &guardChannel{Channel: channel.Header("")(nc, nc), c: c}
	c.srv = jrpc2.NewServer(c, &jrpc2.ServerOptions{AllowPush: true, DisableBuiltin: true, Concurrency: MaxInflight,
		NewContext: func() context.Context { return ctx }})
	timer := time.AfterFunc(HelloDeadline, func() {
		if !c.authed.Load() {
			nc.Close()
		}
	})
	c.srv.Start(ch)
	_ = c.srv.Wait()
	timer.Stop()
	c.mu.Lock()
	for _, sb := range c.subs {
		s.bus.remove(sb)
	}
	c.mu.Unlock()
	nc.Close()
}

// Assign implements jrpc2.Assigner: hello gating, positional-param
// rejection and strict schema validation before every handler.
func (c *conn) Assign(_ context.Context, name string) jrpc2.Handler {
	return func(ctx context.Context, req *jrpc2.Request) (any, error) {
		if req.IsNotification() {
			if name == "$/cancelRequest" && c.authed.Load() {
				var p struct {
					ID json.RawMessage `json:"id"`
				}
				if req.UnmarshalParams(&p) == nil {
					c.srv.CancelRequest(string(p.ID))
				}
			} else {
				slog.Debug("ignored client notification", "method", name)
			}
			return nil, nil
		}
		if !c.authed.Load() && name != "system.hello" {
			c.closeAfter.Store(true)
			return nil, Fail(CodeUnauthorized, "hello_required", "the first call on a connection must be system.hello")
		}
		h, ok := c.s.methods[name]
		if !ok {
			return nil, Fail(CodeMethodNotFound, "", "unknown method "+name)
		}
		raw := json.RawMessage(req.ParamString())
		if t := bytes.TrimSpace(raw); len(t) > 0 && t[0] == '[' {
			return nil, Fail(CodeInvalidParams, "positional_params", "params must be an object")
		}
		if c.s.reg.Has(name) {
			if err := c.s.reg.ValidateParams(name, raw); err != nil {
				return nil, FailData(CodeInvalidParams, "invalid params", ErrorData{Reason: "schema", Detail: trim(err.Error())})
			}
		}
		return h(ctx, c, req, raw)
	}
}

func trim(s string) string {
	if len(s) > 1000 {
		return s[:1000]
	}
	return s
}

// guardChannel rejects batches (A05 §2.2 rule 5), closes the connection
// after a failed hello is answered (§3.2), and releases subscription
// pumps once the subscribe response is on the wire (§2.3 ordering a).
type guardChannel struct {
	channel.Channel
	c  *conn
	mu sync.Mutex
}

var batchError = []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"batch requests are not supported","data":{"code":"invalid_request","reason":"batch_not_supported"}}}`)

func (g *guardChannel) Recv() ([]byte, error) {
	for {
		msg, err := g.Channel.Recv()
		if err != nil {
			return msg, err
		}
		if t := bytes.TrimLeft(msg, " \t\r\n"); len(t) > 0 && t[0] == '[' {
			if err := g.Send(batchError); err != nil {
				return nil, err
			}
			continue
		}
		return msg, nil
	}
}

func (g *guardChannel) Send(msg []byte) error {
	g.mu.Lock()
	err := g.Channel.Send(msg)
	g.mu.Unlock()
	if g.c.closeAfter.Load() {
		g.c.nc.Close()
		return err
	}
	g.c.mu.Lock()
	if len(g.c.waiters) > 0 {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(msg, &m) == nil && m.Method == "" && len(m.ID) > 0 {
			if w, ok := g.c.waiters[string(m.ID)]; ok {
				close(w)
				delete(g.c.waiters, string(m.ID))
			}
		}
	}
	g.c.mu.Unlock()
	return err
}

// afterResponse returns a channel closed once the response to req is sent.
func (c *conn) afterResponse(req *jrpc2.Request) <-chan struct{} {
	w := make(chan struct{})
	c.mu.Lock()
	c.waiters[req.ID()] = w
	c.mu.Unlock()
	return w
}

// hello is system.hello (A05 §3).
func (s *Server) hello(_ context.Context, c *conn, _ *jrpc2.Request, raw json.RawMessage) (any, error) {
	if c.authed.Load() {
		return nil, Fail(CodeInvalidState, "already_authenticated", "this connection is already authenticated")
	}
	var p struct {
		Token  string `json:"token"`
		Client struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"client"`
		Protocol string `json:"protocol"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		c.closeAfter.Store(true)
		return nil, Fail(CodeInvalidParams, "", "invalid hello")
	}
	if p.Protocol != Protocol {
		c.closeAfter.Store(true)
		return nil, FailData(CodeProtocolMismatch, "unsupported protocol "+p.Protocol, ErrorData{Supported: []string{Protocol}})
	}
	if subtle.ConstantTimeCompare([]byte(p.Token), []byte(s.d.Token)) != 1 {
		c.closeAfter.Store(true)
		return nil, Fail(CodeUnauthorized, "bad_token", "the token does not match; re-read the token file")
	}
	c.authed.Store(true)
	slog.Info("client connected", "client", p.Client.Name, "version", p.Client.Version, "connection", c.id)
	return map[string]any{
		"daemon_version": s.d.Version, "protocol": Protocol, "mode": s.d.Mode,
		"features": []string{"event.gap", "cancel_request"}, "connection_id": c.id, "user": platform.LocalUser(),
		"server_time": s.d.Clock().UTC().Format("2006-01-02T15:04:05.000Z"),
		"limits":      map[string]int{"max_message_bytes": 16 << 20, "max_inflight_requests": MaxInflight, "max_subscriptions": MaxSubscriptions, "max_query_limit": 1000},
	}, nil
}

func (s *Server) runtimeActor() store.Actor { return store.Runtime("api", s.d.Version) }

func authOf(p config.Provider) model.AuthConfig {
	return model.AuthConfig{Mode: p.Auth.Mode, Kind: p.Auth.Kind, Header: p.Auth.Header, Secret: p.Auth.Secret, CertFile: p.Auth.CertFile, CAFile: p.Auth.CAFile}
}
