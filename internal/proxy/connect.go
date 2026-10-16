package proxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// connect handles "CONNECT host:port" (A07 §3.2). The request target is
// authoritative; the Host header is ignored.
func (p *TaskProxy) connect(ctx context.Context, c net.Conn, br *bufio.Reader, authority, callID string) {
	t, v, up, ip, ok := p.admit(ctx, c, authority, "CONNECT", callID)
	if !ok {
		return
	}
	defer up.Close()
	stop := context.AfterFunc(ctx, func() { _ = up.Close() })
	defer stop()
	start := p.now()
	ev := ConnectEvent{Host: t.Host, Port: t.Port, Method: "CONNECT", Rule: v.rule, DecisionID: v.decisionID,
		CallID: callID, IP: ip.String()}

	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		ev.DurationMS = p.now().Sub(start).Milliseconds()
		p.emitConnect(ctx, ev)
		return
	}

	// Step 6: the TLS check. Nothing is forwarded upstream before it passes.
	_ = c.SetReadDeadline(time.Now().Add(helloTimeout))
	rec, err := readTLSRecord(br)
	_ = c.SetReadDeadline(time.Time{})
	reason := Reason("")
	if err != nil {
		reason = ReasonNotTLS
	} else if sni, hasSNI, err := parseClientHelloSNI(rec); err != nil {
		reason = ReasonNotTLS
	} else if !t.IsIP() && (!hasSNI || !sniMatches(sni, t.Host)) {
		// A DNS target needs an SNI equal to it; an IP target may omit it
		// (A07 §3.2 step 6).
		reason = ReasonSNIMismatch
	}
	if reason != "" {
		p.emitDenied(ctx, DeniedEvent{Host: t.Host, Port: t.Port, Method: "CONNECT", Rule: v.rule,
			DecisionID: v.decisionID, CallID: callID, Reason: reason})
		return
	}

	// Step 7: the buffered ClientHello first, then both directions.
	n, err := up.Write(rec)
	ev.BytesUp = int64(n)
	if err == nil {
		up2, down, code := relay(c, br, up)
		ev.BytesUp += up2
		ev.BytesDown = down
		ev.Error = code
	} else {
		ev.Error = ConnErrUpstreamReset
	}
	ev.DurationMS = p.now().Sub(start).Milliseconds()
	p.emitConnect(ctx, ev)
}

// sniMatches compares a ClientHello server_name with the canonical target
// host, spelled the same way (case-insensitive, one trailing dot ignored).
// A name that does not normalise (non-ASCII, NUL, empty labels) never
// matches.
func sniMatches(sni, host string) bool {
	name, reason := normalizeName(sni)
	return reason == "" && name == host
}

// closeWriter is a connection that can half-close (TCP, Unix sockets).
type closeWriter interface{ CloseWrite() error }

// relay copies client→upstream (reading from cr, which holds bytes the
// client sent after the ClientHello) and upstream→client until both
// directions end. EOF in one direction is propagated as a half-close when
// the connection supports it, and closes both otherwise. A tunnel with no
// bytes in either direction for idleTimeout is closed (A07 §10). It
// returns the byte counts and the A07 §9 error value ("" for a normal end).
func relay(client net.Conn, cr io.Reader, up net.Conn) (bytesUp, bytesDown int64, code string) {
	var (
		last     atomic.Int64 // unix nanoseconds of the last byte either way
		closing  atomic.Bool  // we closed the connections; later errors are ours
		idle     atomic.Bool
		upstream atomic.Bool // upstream read failed other than EOF
		once     sync.Once
	)
	closeBoth := func() {
		once.Do(func() {
			closing.Store(true)
			_ = client.Close()
			_ = up.Close()
		})
	}
	touch := func() { last.Store(time.Now().UnixNano()) }
	touch()
	var watchdog *time.Timer
	watchdog = time.AfterFunc(idleTimeout, func() {
		if quiet := time.Since(time.Unix(0, last.Load())); quiet < idleTimeout {
			watchdog.Reset(idleTimeout - quiet)
			return
		}
		idle.Store(true)
		closeBoth()
	})
	defer watchdog.Stop()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = copyCounting(up, cr, &bytesUp, touch)
		halfClose(up, closeBoth)
	}()
	go func() {
		defer wg.Done()
		rerr, _ := copyCounting(client, up, &bytesDown, touch)
		if rerr != nil && !closing.Load() {
			upstream.Store(true)
		}
		halfClose(client, closeBoth)
	}()
	wg.Wait()
	closeBoth()
	switch {
	case idle.Load():
		code = ConnErrIdleTimeout
	case upstream.Load():
		code = ConnErrUpstreamReset
	}
	return bytesUp, bytesDown, code
}

func halfClose(c net.Conn, closeBoth func()) {
	if cw, ok := c.(closeWriter); ok {
		if cw.CloseWrite() == nil {
			return
		}
	}
	closeBoth()
}

// copyCounting copies src to dst, adding what was written to *n. It
// returns the read error (nil on EOF) and the write error separately, so
// the caller can tell an upstream failure from a client that went away.
func copyCounting(dst io.Writer, src io.Reader, n *int64, touch func()) (readErr, writeErr error) {
	buf := make([]byte, 32<<10)
	for {
		k, err := src.Read(buf)
		if k > 0 {
			touch()
			w, werr := dst.Write(buf[:k])
			*n += int64(w)
			if werr != nil {
				return nil, werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, nil
			}
			return err, nil
		}
	}
}
