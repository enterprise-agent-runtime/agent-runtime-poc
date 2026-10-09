package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
)

// bus fans committed events out to subscriptions. Publishing never blocks
// the store: a full subscription queue ends that subscription with
// event.gap instead (A05 §6.5).
type bus struct {
	mu   sync.Mutex
	subs map[*sub]bool
}

func newBus() *bus { return &bus{subs: map[*sub]bool{}} }

type sub struct {
	id      string
	session string // ses_… | "*"
	types   []string
	head    int64
	q       chan busEvent
	c       *conn
	stop    chan struct{}
	once    sync.Once
	gapped  bool
}

type busEvent struct {
	seq int64
	raw json.RawMessage
}

func (s *sub) matches(chain, typ string) bool {
	if s.session != "*" && s.session != chain {
		return false
	}
	return typeMatch(s.types, typ)
}

// typeMatch applies the exact or "family.*" filters (A05 §6.6).
func typeMatch(filters []string, typ string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, f := range filters {
		if f == typ {
			return true
		}
		if fam, ok := strings.CutSuffix(f, ".*"); ok && strings.HasPrefix(typ, fam+".") {
			return true
		}
	}
	return false
}

func (b *bus) add(s *sub) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[s] = true
}

func (b *bus) remove(s *sub) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
	s.once.Do(func() { close(s.stop) })
}

func (b *bus) publish(seq int64, chain, typ string, raw json.RawMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		if !s.matches(chain, typ) {
			continue
		}
		select {
		case s.q <- busEvent{seq, raw}:
		default:
			s.gapped = true
			delete(b.subs, s)
			s.once.Do(func() { close(s.stop) })
		}
	}
}

// withSubscription adds subscription_id to an envelope (A05 §6.1).
func withSubscription(raw json.RawMessage, id string) json.RawMessage {
	t := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(t, "{") {
		return raw
	}
	return json.RawMessage(`{"subscription_id":"` + id + `",` + t[1:])
}

// pump delivers replayed then live events in seq order, after the
// subscribe response has been written.
func (s *Server) pump(ctx context.Context, sb *sub, after *int64, ready <-chan struct{}) {
	select {
	case <-ready:
	case <-sb.stop:
		return
	}
	last := sb.head
	if after != nil {
		last = *after
		for last < sb.head {
			var page []json.RawMessage
			var err error
			if sb.session == "*" {
				page, err = s.d.Store.AllEvents(ctx, last, 500)
			} else {
				page, err = s.d.Store.Events(ctx, sb.session, last, 500, nil)
			}
			if err != nil || len(page) == 0 {
				break
			}
			for _, raw := range page {
				var h struct {
					Seq  int64  `json:"seq"`
					Type string `json:"type"`
				}
				_ = json.Unmarshal(raw, &h)
				if h.Seq > sb.head {
					last = sb.head
					break
				}
				last = h.Seq
				if typeMatch(sb.types, h.Type) {
					if err := sb.c.srv.Notify(ctx, "event", withSubscription(raw, sb.id)); err != nil {
						return
					}
				}
			}
		}
		last = sb.head
	}
	for {
		select {
		case ev := <-sb.q:
			if ev.seq <= last {
				continue // already replayed
			}
			last = ev.seq
			if err := sb.c.srv.Notify(ctx, "event", withSubscription(ev.raw, sb.id)); err != nil {
				return
			}
		case <-sb.stop:
			if sb.gapped {
				// Deliver what is queued, then report the gap and end.
				for {
					select {
					case ev := <-sb.q:
						if ev.seq > last {
							last = ev.seq
							_ = sb.c.srv.Notify(ctx, "event", withSubscription(ev.raw, sb.id))
						}
						continue
					default:
					}
					break
				}
				_ = sb.c.srv.Notify(ctx, "event.gap", map[string]any{"subscription_id": sb.id, "last_seq": last, "reason": "overflow"})
				sb.c.mu.Lock()
				delete(sb.c.subs, sb.id)
				sb.c.mu.Unlock()
			}
			return
		}
	}
}
