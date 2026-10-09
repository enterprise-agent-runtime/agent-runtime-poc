package secrets

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestLogHandler_RedactsMessagesAndAttrs: a key that reaches a log call is
// replaced before the record is written (INV-C, "logs").
func TestLogHandler_RedactsMessagesAndAttrs(t *testing.T) {
	r := NewRedactor()
	known := "WRDN-CANARY-LOG-0123456789"
	r.AddKnown([]byte(known))
	var buf bytes.Buffer
	lg := slog.New(NewLogHandler(slog.NewTextHandler(&buf, nil), r))
	ghp := "gh" + "p_" + strings.Repeat("a1B2", 9)
	lg.With("ctx", known).WithGroup("g").Info("token "+ghp, "auth", "Authorization: Bearer "+known, "n", 3, "nested", slog.GroupValue(slog.String("k", known)), "err", errString(known))
	out := buf.String()
	if strings.Contains(out, known) || strings.Contains(out, ghp) {
		t.Fatalf("secret in log: %s", out)
	}
	if !strings.Contains(out, "[REDACTED:") || !strings.Contains(out, "n=3") {
		t.Fatalf("log = %s", out)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
