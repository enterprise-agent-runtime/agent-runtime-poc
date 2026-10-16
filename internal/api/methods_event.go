package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/creachadair/jrpc2"

	"warden.dev/warden/internal/ids"
	"warden.dev/warden/internal/secrets"
	"warden.dev/warden/internal/store"
)

func (s *Server) eventSubscribe(ctx context.Context, c *conn, req *jrpc2.Request, raw json.RawMessage) (any, error) {
	var p struct {
		SessionID string   `json:"session_id"`
		AfterSeq  *int64   `json:"after_seq"`
		Types     []string `json:"types"`
	}
	_ = json.Unmarshal(raw, &p)
	if p.SessionID != "*" {
		if _, err := s.d.Store.Chain(ctx, p.SessionID); err != nil {
			return nil, Fail(CodeNotFound, "session", "unknown session "+p.SessionID)
		}
	}
	c.mu.Lock()
	n := len(c.subs)
	c.mu.Unlock()
	if n >= MaxSubscriptions {
		return nil, Fail(CodeInvalidState, "subscription_limit", "too many subscriptions on this connection")
	}
	sb := &sub{id: s.ids.New(ids.Subscription), session: p.SessionID, types: p.Types, q: make(chan busEvent, SubscriptionQ), c: c, stop: make(chan struct{})}
	// Registration and head_seq under the append lock: the cut between
	// replay and live delivery is exact (A05 §6.4).
	s.mu.Lock()
	sb.head = s.published
	s.bus.add(sb)
	s.mu.Unlock()
	c.mu.Lock()
	c.subs[sb.id] = sb
	c.mu.Unlock()
	ready := c.afterResponse(req)
	go s.pump(context.WithoutCancel(ctx), sb, p.AfterSeq, ready)
	return map[string]any{"subscription_id": sb.id, "head_seq": sb.head}, nil
}

func (s *Server) eventUnsubscribe(_ context.Context, c *conn, _ *jrpc2.Request, raw json.RawMessage) (any, error) {
	var p struct {
		SubscriptionID string `json:"subscription_id"`
	}
	_ = json.Unmarshal(raw, &p)
	c.mu.Lock()
	sb, ok := c.subs[p.SubscriptionID]
	delete(c.subs, p.SubscriptionID)
	c.mu.Unlock()
	if ok {
		s.bus.remove(sb)
	}
	return map[string]any{"ok": true, "existed": ok}, nil
}

// eventQuery is event.query: paged history of a session chain, the sys
// chain or all chains, with type filters (A05 §8.7).
func (s *Server) eventQuery(ctx context.Context, _ *conn, _ *jrpc2.Request, raw json.RawMessage) (any, error) {
	var p struct {
		SessionID string   `json:"session_id"`
		AfterSeq  int64    `json:"after_seq"`
		Limit     int      `json:"limit"`
		Types     []string `json:"types"`
	}
	_ = json.Unmarshal(raw, &p)
	if p.Limit == 0 {
		p.Limit = 200
	}
	if p.SessionID != "*" && p.SessionID != store.SysChain {
		if _, err := s.d.Store.Chain(ctx, p.SessionID); err != nil {
			return nil, Fail(CodeNotFound, "session", "unknown session "+p.SessionID)
		}
	}
	out := []json.RawMessage{}
	next, size := p.AfterSeq, 0
	hasMore := false
	for len(out) < p.Limit {
		var page []json.RawMessage
		var err error
		if p.SessionID == "*" {
			page, err = s.d.Store.AllEvents(ctx, next, 500)
		} else {
			page, err = s.d.Store.Events(ctx, p.SessionID, next, 500, nil)
		}
		if err != nil {
			return nil, Fail(CodeStoreUnavailable, "", err.Error())
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			var h struct {
				Seq  int64  `json:"seq"`
				Type string `json:"type"`
			}
			_ = json.Unmarshal(e, &h)
			if len(out) >= p.Limit || size+len(e) > 12<<20 {
				hasMore = true
				break
			}
			next = h.Seq
			if typeMatch(p.Types, h.Type) {
				out = append(out, e)
				size += len(e)
			}
		}
		if hasMore {
			break
		}
	}
	return map[string]any{"events": out, "next_seq": next, "head_seq": s.d.Store.HeadSeq(), "has_more": hasMore}, nil
}

// exportDir checks an export target (A05 §8.10): inside the user's home
// and not inside the Warden home except its exports directory.
func (s *Server) exportDir(path string) (string, error) {
	if path == "" {
		return s.d.Layout.Exports(), nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	under := func(p, root string) bool {
		r, err := filepath.Rel(root, p)
		return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
	}
	if under(abs, s.d.Layout.Home) && !under(abs, s.d.Layout.Exports()) {
		return "", errors.New("exports inside the Warden home must go to its exports directory")
	}
	if home, err := os.UserHomeDir(); err == nil && !under(abs, home) && !under(abs, s.d.Layout.Home) && !under(abs, os.TempDir()) {
		return "", errors.New("the export directory must be inside your home directory")
	}
	return abs, nil
}

func (s *Server) auditExport(ctx context.Context, _ *conn, _ *jrpc2.Request, raw json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		WithBlobs bool   `json:"with_blobs"`
		Path      string `json:"path"`
	}
	_ = json.Unmarshal(raw, &p)
	if p.WithBlobs {
		return nil, Fail(CodeUnsupportedInPoC, "with_blobs", "exports with blobs land with artifacts (M4)")
	}
	dir, err := s.exportDir(p.Path)
	if err != nil {
		return nil, Fail(CodeInvalidParams, "path_not_allowed", err.Error())
	}
	res, err := s.d.Store.Export(ctx, p.SessionID, dir)
	s.Publish(ctx) // the export checkpoint, if one was written
	if errors.Is(err, store.ErrNotFound) {
		return nil, Fail(CodeNotFound, "session", "unknown session "+p.SessionID)
	}
	if err != nil {
		return nil, Fail(CodeStoreUnavailable, "export", err.Error())
	}
	return res, nil
}

// auditVerify is audit.verify for a session in the store or an export file;
// a failed verification is a result with ok=false, not an error.
func (s *Server) auditVerify(ctx context.Context, _ *conn, _ *jrpc2.Request, raw json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		File      string `json:"file"`
		Strict    bool   `json:"strict"`
	}
	_ = json.Unmarshal(raw, &p)
	var rep store.Report
	var err error
	if p.File != "" {
		rep, err = store.VerifyFile(p.File, secrets.PublicKeys(s.d.Layout.Keys()), p.Strict)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, Fail(CodeNotFound, "file", "no such export file")
			}
			return nil, Fail(CodeInvalidParams, "malformed_file", err.Error())
		}
	} else {
		rep, err = s.d.Store.Verify(ctx, p.SessionID, p.Strict)
		if errors.Is(err, store.ErrNotFound) {
			return nil, Fail(CodeNotFound, "session", "unknown session "+p.SessionID)
		}
		if err != nil {
			return nil, Fail(CodeStoreUnavailable, "verify", err.Error())
		}
	}
	b, _ := json.Marshal(rep)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if out["session_id"] == "" {
		delete(out, "session_id")
	}
	out["verified_at"] = s.d.Clock().UTC().Format(ts3)
	return out, nil
}
