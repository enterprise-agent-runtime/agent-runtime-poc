package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// cleanHeader returns a copy of h without the headers A07 §3.3 removes in
// both directions: every header named in Connection, and the hop-by-hop
// list below. Transfer-Encoding is re-framed by net/http.
func cleanHeader(h http.Header) http.Header {
	hopByHop := [...]string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	}
	out := h.Clone()
	if out == nil {
		out = http.Header{}
	}
	for _, v := range h["Connection"] {
		for _, f := range strings.Split(v, ",") {
			if f = textproto.TrimString(f); f != "" {
				out.Del(f)
			}
		}
	}
	for _, k := range hopByHop {
		out.Del(k)
	}
	return out
}

// plain forwards one absolute-form plain HTTP request (A07 §3.3): the
// evaluated target is host:port from the URI, the outbound request is in
// origin form with Host from the URI, hop-by-hop headers go in both
// directions, nothing is added (no Via, X-Forwarded-For, User-Agent or
// Accept-Encoding), and redirects are passed back, never followed. The
// connection carries one request and is then closed.
func (p *TaskProxy) plain(ctx context.Context, c net.Conn, req *http.Request, callID string) {
	u := req.URL
	port := u.Port()
	if port == "" {
		port = "80"
	}
	t, v, up, ip, release, ok := p.admit(ctx, c, net.JoinHostPort(u.Hostname(), port), req.Method, callID)
	if !ok {
		return
	}
	defer release()
	start := p.now()
	ev := ConnectEvent{Host: t.Host, Port: t.Port, Method: req.Method, Rule: v.rule, DecisionID: v.decisionID,
		CallID: callID, IP: ip.String()}
	defer func() {
		ev.DurationMS = p.now().Sub(start).Milliseconds()
		p.emitConnect(ctx, ev)
	}()

	hostHeader := t.Host
	if t.IP.Is6() {
		hostHeader = "[" + t.Host + "]"
	}
	if t.Port != 80 {
		hostHeader += ":" + strconv.Itoa(int(t.Port))
	}
	h := cleanHeader(req.Header)
	if _, set := h["User-Agent"]; !set {
		h["User-Agent"] = []string{""} // an empty value stops net/http adding its own
	}
	if p.injector.Match(t) {
		if err := p.injector.Inject(ctx, t, h); err != nil {
			_ = up.Close()
			p.writeStatus(c, http.StatusBadGateway, "warden: egress to "+t.Key()+" failed\n")
			ev.Error = ConnErrUpstreamReset
			return
		}
	}
	upBody := &countingReader{r: req.Body, conn: c, idle: p.limits.IdleTimeout}
	out := &http.Request{
		Method:        req.Method,
		URL:           &url.URL{Scheme: "http", Host: t.Key(), Path: u.Path, RawPath: u.RawPath, RawQuery: u.RawQuery},
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Host:          hostHeader,
		ContentLength: req.ContentLength,
		Body:          upBody,
	}
	if req.ContentLength == 0 {
		out.Body = http.NoBody
	}

	// The transport may use exactly the connection admit dialed to the
	// checked address; it can never dial by name.
	var used atomic.Bool
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			if used.Swap(true) {
				return nil, errors.New("proxy: one upstream connection per request")
			}
			return up, nil
		},
		ResponseHeaderTimeout:  responseHeaderTimeout,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: headerLimit,
	}
	defer tr.CloseIdleConnections()
	resp, err := tr.RoundTrip(out.WithContext(ctx))
	ev.BytesUp = upBody.n.Load()
	if err != nil {
		if !used.Load() {
			_ = up.Close()
		}
		p.log.Warn("proxy upstream request failed", "target", t.Key(), "err", err)
		ev.Error = ConnErrUpstreamReset
		if upBody.timedOut.Load() {
			ev.Error = ConnErrIdleTimeout // the client stopped sending its body
		}
		p.writeStatus(c, http.StatusBadGateway, "warden: egress to "+t.Key()+" failed: "+ev.Error+"\n")
		return
	}
	defer resp.Body.Close()
	down := &countingReader{r: resp.Body, conn: up, idle: p.limits.IdleTimeout}
	resp.Header = cleanHeader(resp.Header)
	resp.Trailer = nil
	resp.Body = down
	resp.Close = true
	dw := &deadlineWriter{c: c, idle: p.limits.IdleTimeout}
	werr := resp.Write(dw)
	ev.BytesDown = down.n.Load()
	switch {
	case down.timedOut.Load() || dw.timedOut:
		ev.Error = ConnErrIdleTimeout // upstream stopped sending, or the client stopped reading
	case werr != nil && down.err != nil && !errors.Is(down.err, io.EOF):
		ev.Error = ConnErrUpstreamReset
	}
}

// countingReader counts bytes read and remembers the last read error.
// With conn set, every Read first moves conn's read deadline to idle from
// now, so a peer that stops sending mid-body is cut off after idle without
// progress (CWE-400); timedOut records that this is why the read failed.
type countingReader struct {
	r        io.Reader
	conn     net.Conn
	idle     time.Duration
	n        atomic.Int64
	err      error
	timedOut atomic.Bool
}

func (c *countingReader) Read(b []byte) (int, error) {
	if c.r == nil {
		return 0, io.EOF
	}
	if c.conn != nil {
		_ = c.conn.SetReadDeadline(time.Now().Add(c.idle))
	}
	k, err := c.r.Read(b)
	c.n.Add(int64(k))
	if err != nil {
		c.err = err
		if isTimeout(err) {
			c.timedOut.Store(true)
		}
	}
	return k, err
}

func (c *countingReader) Close() error {
	if rc, ok := c.r.(io.Closer); ok {
		return rc.Close()
	}
	return nil
}

// deadlineWriter moves the connection's write deadline to idle from now
// before every Write, so a client that stops reading the response cannot
// hold the connection and its slot forever.
type deadlineWriter struct {
	c        net.Conn
	idle     time.Duration
	timedOut bool
}

func (w *deadlineWriter) Write(b []byte) (int, error) {
	_ = w.c.SetWriteDeadline(time.Now().Add(w.idle))
	n, err := w.c.Write(b)
	if isTimeout(err) {
		w.timedOut = true
	}
	return n, err
}

// isTimeout reports a deadline expiry, whether it arrives as
// os.ErrDeadlineExceeded or, re-wrapped by net/http, as a net.Error whose
// Timeout is true.
func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
