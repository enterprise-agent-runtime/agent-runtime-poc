package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"warden.dev/warden/internal/model"
)

func TestProbeCache_StoreLoadLearnRemove(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "providers")
	c := NewProbeCache(dir)
	if r, err := c.Load("ollama"); r != nil || err != nil {
		t.Fatalf("empty cache = %v %v", r, err)
	}
	res := model.ProbeResult{ProviderID: "ollama", ProbedAt: time.Now().UTC(), OK: true,
		Models: []model.ModelProbe{{ModelID: "local/q", ToolCalling: "native", Streaming: true, MaxContext: 32768}}}
	if err := c.Store(res); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "ollama.json")); err != nil || (fi.Mode().Perm()&0o077 != 0 && fi.Mode().Perm() != 0o666) {
		t.Fatalf("cache file %v %v", fi, err)
	}
	if err := c.LearnQuirk("ollama", "local/q", "stream_usage", false); err != nil {
		t.Fatal(err)
	}
	r, _ := c.Load("ollama")
	if !r.OK || r.Models[0].Quirks["stream_usage"] != false {
		t.Fatalf("loaded = %+v", r)
	}
	// A quirk for an unprobed model must not create a synthetic record that
	// would lower the model's effective capabilities.
	if err := c.LearnQuirk("ollama", "local/unprobed", "tools", false); err != nil {
		t.Fatal(err)
	}
	if r, _ := c.Load("ollama"); len(r.Models) != 1 {
		t.Fatalf("synthetic record created: %+v", r.Models)
	}
	if err := c.Store(model.ProbeResult{ProviderID: "../escape"}); err == nil {
		t.Fatal("path traversal in provider id accepted")
	}
	c.Remove("ollama")
	if r, _ := c.Load("ollama"); r != nil {
		t.Fatal("not removed")
	}
	_ = os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0o600)
	if _, err := c.Load("bad"); err == nil {
		t.Fatal("corrupt cache accepted")
	}
}
