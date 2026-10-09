// Package goldentest compares test output with golden files under
// testdata/golden (CLAUDE.md §8.1 layer L2). Run the tests with
// UPDATE_GOLDEN=1 to rewrite the files, then review the diff like code.
package goldentest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Update reports whether golden files should be rewritten.
func Update() bool { return os.Getenv("UPDATE_GOLDEN") == "1" }

// JSON compares got (any JSON-marshalable value or raw JSON bytes) with the
// golden file at path, after indenting both, so key order inside raw bytes
// produced by encoding/json is stable and diffs are readable.
func JSON(t *testing.T, path string, got any) {
	t.Helper()
	var raw []byte
	switch v := got.(type) {
	case []byte:
		raw = v
	case json.RawMessage:
		raw = v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		raw = b
	}
	var norm any
	if err := json.Unmarshal(raw, &norm); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, raw)
	}
	pretty, _ := json.MarshalIndent(norm, "", "  ")
	pretty = append(pretty, '\n')
	if Update() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, pretty, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with UPDATE_GOLDEN=1): %v", path, err)
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(pretty, want) {
		t.Errorf("%s differs from golden\n--- got\n%s\n--- want\n%s", path, pretty, want)
	}
}

// Read returns a testdata file with CRLF normalized only when asked.
func Read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
