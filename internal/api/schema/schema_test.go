package schema

import (
	"strings"
	"testing"
)

// TestSchema_AllCompile: every embedded schema compiles and every method
// has params and result schemas (CLAUDE.md §8.1 L0 "schema validity").
func TestSchema_AllCompile(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	ms := r.Methods()
	if len(ms) < 35 {
		t.Fatalf("only %d methods: %v", len(ms), ms)
	}
	for _, want := range []string{"system.hello", "system.doctor", "provider.test", "event.subscribe", "audit.verify", "workflow.resolveGate"} {
		if !r.Has(want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestSchema_StrictParamsOpenResults(t *testing.T) {
	r, _ := Load()
	tok := strings.Repeat("A", 43)
	if err := r.ValidateParams("system.hello", []byte(`{"token":"`+tok+`","client":{"name":"warden-cli","version":"0.1.0"},"protocol":"warden.poc/1"}`)); err != nil {
		t.Fatalf("valid hello: %v", err)
	}
	for name, raw := range map[string]string{
		"unknown param":   `{"token":"` + tok + `","client":{"name":"x","version":"1"},"protocol":"warden.poc/1","extra":1}`,
		"short token":     `{"token":"abc","client":{"name":"x","version":"1"},"protocol":"warden.poc/1"}`,
		"missing token":   `{"client":{"name":"x","version":"1"},"protocol":"warden.poc/1"}`,
		"positional args": `["x"]`,
	} {
		if err := r.ValidateParams("system.hello", []byte(raw)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := r.ValidateParams("system.version", nil); err != nil {
		t.Fatalf("omitted params: %v", err)
	}
	// Results may carry fields the schema does not know.
	if err := r.ValidateResult("system.version", map[string]any{"version": "0.1.0", "commit": "abc1234", "go_version": "go1.26", "os": "windows", "arch": "amd64", "schema_version": 1, "new_field": true}); err != nil {
		t.Fatalf("open result: %v", err)
	}
	if err := r.ValidateParams("audit.export", []byte(`{"session_id":"ses_01JAXR8Q7M2V9KTC3F6YH5N0PB","path":"C:\\Users\\a\\exports"}`)); err != nil {
		t.Fatalf("Windows path rejected (C-47): %v", err)
	}
	if err := r.ValidateParams("nope", nil); err == nil {
		t.Fatal("unknown method validated")
	}
	if err := r.ValidateNotification("event.gap", map[string]any{"subscription_id": "sub_01JAXR8Q7M2V9KTC3F6YH5N0PB", "last_seq": 3, "reason": "overflow"}); err != nil {
		t.Fatalf("event.gap: %v", err)
	}
	if err := r.ValidateEnvelope([]byte(`{}`)); err == nil {
		t.Fatal("empty envelope valid")
	}
}
