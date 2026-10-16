package secrets

import (
	"context"
	"log/slog"
)

// LogHandler redacts every log record (message and string attributes)
// before it reaches the underlying handler (CLAUDE.md §7: "redaction
// applied before logging"; A15 §6.3 source "log").
type LogHandler struct {
	next slog.Handler
	r    *Redactor
}

// NewLogHandler wraps next.
func NewLogHandler(next slog.Handler, r *Redactor) *LogHandler { return &LogHandler{next: next, r: r} }

// Enabled implements slog.Handler.
func (h *LogHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

// Handle implements slog.Handler.
func (h *LogHandler) Handle(ctx context.Context, rec slog.Record) error {
	msg, _ := h.r.Redact(rec.Message)
	out := slog.NewRecord(rec.Time, rec.Level, msg, rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *LogHandler) attr(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		s, _ := h.r.Redact(a.Value.String())
		return slog.String(a.Key, s)
	case slog.KindGroup:
		var as []any
		for _, g := range a.Value.Group() {
			as = append(as, h.attr(g))
		}
		return slog.Group(a.Key, as...)
	case slog.KindAny, slog.KindLogValuer:
		s, _ := h.r.Redact(a.Value.Resolve().String())
		return slog.String(a.Key, s)
	}
	return a
}

// WithAttrs implements slog.Handler.
func (h *LogHandler) WithAttrs(as []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(as))
	for i, a := range as {
		red[i] = h.attr(a)
	}
	return &LogHandler{next: h.next.WithAttrs(red), r: h.r}
}

// WithGroup implements slog.Handler.
func (h *LogHandler) WithGroup(name string) slog.Handler {
	return &LogHandler{next: h.next.WithGroup(name), r: h.r}
}
