package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"warden.dev/warden/internal/api/schema"
	"warden.dev/warden/internal/config"
	"warden.dev/warden/internal/ids"
	"warden.dev/warden/internal/model"
	"warden.dev/warden/internal/model/providertest"
	"warden.dev/warden/internal/platform"
	"warden.dev/warden/internal/sandbox"
	"warden.dev/warden/internal/secrets"
	"warden.dev/warden/internal/store"
	"warden.dev/warden/internal/store/storetest"
)

const token = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ" // 43 characters, test only

type env struct {
	t      *testing.T
	s      *Server
	l      platform.Layout
	st     *store.Store
	broker *secrets.Broker
	fake   *providertest.FakeProvider
	file   *secrets.File
}

func newEnv(t *testing.T, mut func(*Deps)) *env {
	t.Helper()
	home := t.TempDir()
	t.Setenv(platform.EnvHome, home)
	l := platform.Layout{Home: home}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, l: l}
	var srv *Server
	e.broker = secrets.NewBroker(secrets.NewMemory(), nil, func(ctx context.Context, a secrets.Access) {
		if srv != nil {
			srv.SecretAccess(ctx, a)
		}
	})
	st, err := store.Open(context.Background(), store.Options{Dir: l.DB(), BlobsDir: l.Blobs(), Signer: storetest.NewSigner(""), Redactor: e.broker.Redactor(), RuntimeVersion: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.st = st
	e.fake = &providertest.FakeProvider{ProvID: "ollama", ProbeRes: model.ProbeResult{ProviderID: "ollama", OK: true, LatencyMS: 12,
		Models: []model.ModelProbe{{ModelID: "local/qwen-coder-32b", ToolCalling: "native", StructuredOutput: true, Streaming: true, MaxContext: 32768}}}}
	d := Deps{Layout: l, Store: st, Broker: e.broker, Catalog: &config.Catalog{}, CatalogPath: l.Models(), Cache: config.NewProbeCache(filepath.Join(l.Cache(), "providers")),
		NewProvider: func(model.ProviderConfig) (model.Provider, error) { return e.fake, nil },
		Token:       token, Version: "0.1.0", Commit: "abc1234", DefaultLevel: platform.DefaultSandboxLevel(),
		SignerKeyID: func() string { return storetest.NewSigner("").KeyID() },
		SandboxProbe: func(context.Context, sandbox.Options) []sandbox.Check {
			return []sandbox.Check{{ID: "sandbox.backend", Group: "sandbox", Status: "ok", Title: "fake", Detail: "fake backend", Blocking: true}}
		},
		GitVersion:   func(context.Context) (string, error) { return "2.47", nil },
		LocalServers: func(context.Context) []string { return []string{} },
	}
	if mut != nil {
		mut(&d)
	}
	e.file = d.File
	srv, err = New(d)
	if err != nil {
		t.Fatal(err)
	}
	e.s = srv
	ln, err := platform.Listen(home)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	if err := platform.WriteOwnerOnly(l.Token(), []byte(token)); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) dial(onNotify Notify) *Client {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, e.l, "warden-test", "0.1.0", onNotify)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { c.Close() })
	return c
}

// call invokes a method and validates its result against the A05 result
// schema: the contract test of every M1 method (CLAUDE.md §8.1 L2).
func call(t *testing.T, c *Client, method string, params any) map[string]any {
	t.Helper()
	var out map[string]any
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Call(ctx, method, params, &out); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	reg, _ := schema.Load()
	if err := reg.ValidateResult(method, out); err != nil {
		t.Fatalf("%s result violates its schema: %v\n%v", method, err, out)
	}
	return out
}

func callErr(c *Client, method string, params any) (int, ErrorData) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := c.Call(ctx, method, params, nil)
	code, d, _ := DataOf(err)
	return code, d
}

func TestHello_TokenProtocolAndGating(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(nil)
	if c.Hello["protocol"] != Protocol || c.Hello["mode"] != "personal" {
		t.Fatalf("hello = %v", c.Hello)
	}
	if code, d := callErr(c, "system.hello", map[string]any{"token": token, "client": map[string]string{"name": "x", "version": "1"}, "protocol": Protocol}); code != CodeInvalidState || d.Reason != "already_authenticated" {
		t.Fatalf("second hello = %d %+v", code, d)
	}
	ctx := context.Background()
	for name, tc := range map[string]struct {
		tok, proto string
		code       int
	}{
		"bad token": {strings.Repeat("x", 43), Protocol, CodeUnauthorized},
		"protocol":  {token, "warden.poc/9", CodeProtocolMismatch},
	} {
		nc, err := platform.Dial(ctx, e.l.Home)
		if err != nil {
			t.Fatal(err)
		}
		_, err = handshake(ctx, nc, tc.tok, tc.proto, "warden-test", "1", nil)
		if code, _, _ := DataOf(err); code != tc.code {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// rawConn speaks the wire protocol directly, to test framing-level rules.
func rawConn(t *testing.T, e *env) (io.Writer, *bufio.Reader) {
	nc, err := platform.Dial(context.Background(), e.l.Home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	return nc, bufio.NewReader(nc)
}

func send(w io.Writer, body string) {
	fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

func readMsg(t *testing.T, r *bufio.Reader) (string, error) {
	n := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
			fmt.Sscanf(v, "%d", &n)
		}
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return string(b), err
}

// TestFraming_HelloFirstAndBatches: any method before hello is refused and
// the connection is closed; batches get one invalid_request (A05 §2.2, §3.2).
func TestFraming_HelloFirstAndBatches(t *testing.T) {
	e := newEnv(t, nil)
	w, r := rawConn(t, e)
	send(w, `{"jsonrpc":"2.0","id":1,"method":"system.version"}`)
	msg, err := readMsg(t, r)
	if err != nil || !strings.Contains(msg, `"code":-32001`) || !strings.Contains(msg, "hello_required") {
		t.Fatalf("reply = %s %v", msg, err)
	}
	if _, err := readMsg(t, r); err == nil {
		t.Fatal("connection stayed open after a refused first message")
	}
	w2, r2 := rawConn(t, e)
	send(w2, `[{"jsonrpc":"2.0","id":1,"method":"system.version"}]`)
	msg, err = readMsg(t, r2)
	if err != nil || !strings.Contains(msg, "batch_not_supported") {
		t.Fatalf("batch reply = %s %v", msg, err)
	}
}

func TestParams_StrictAndPositional(t *testing.T) {
	c := newEnv(t, nil).dial(nil)
	if code, d := callErr(c, "provider.test", map[string]any{"provider_id": "x", "extra": 1}); code != CodeInvalidParams || d.Reason != "schema" {
		t.Fatalf("unknown param = %d %+v", code, d)
	}
	if code, _ := callErr(c, "provider.test", []any{"x"}); code != CodeInvalidParams {
		t.Fatalf("positional = %d", code)
	}
	if code, _ := callErr(c, "no.such", nil); code != CodeMethodNotFound {
		t.Fatalf("unknown method = %d", code)
	}
}

func TestSystemVersionAndDoctor(t *testing.T) {
	c := newEnv(t, nil).dial(nil)
	v := call(t, c, "system.version", nil)
	if v["version"] != "0.1.0" || v["os"] != platform.OS() {
		t.Fatalf("version = %v", v)
	}
	d := call(t, c, "system.doctor", nil)
	ids := map[string]bool{}
	for _, ch := range d["checks"].([]any) {
		ids[ch.(map[string]any)["id"].(string)] = true
	}
	for _, want := range []string{"sandbox.backend", "exec.binary", "keychain", "checkpoint.key", "store", "disk", "catalog", "providers", "providers.local", "git"} {
		if !ids[want] {
			t.Errorf("doctor lacks %s", want)
		}
	}
	// The executor lands in M2, so doctor must not claim everything is fine.
	if d["status"] == "ok" {
		t.Fatalf("doctor status ok in M1: %v", d)
	}
}

// TestProviderAdd_StoresSecretOnlyInKeychain: provider.add writes the
// reference to models.yaml, the value to the keychain, and the value
// appears in no event and no file under the Warden home (INV-C).
func TestProviderAdd_StoresSecretOnlyInKeychain(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(nil)
	key := providertest.FakeKey
	res := call(t, c, "provider.add", map[string]any{"confirm": true, "test": false, "secret": map[string]string{"value": key},
		"spec": map[string]any{"id": "anthropic", "protocol": "anthropic-messages", "base_url": "https://api.anthropic.com", "auth": map[string]any{"mode": "api_key"}, "tier": "T3",
			"models": []any{map[string]any{"id": "anthropic/claude-sonnet", "provider": "anthropic", "model": "claude-sonnet-x",
				"capabilities": map[string]any{"tool_calling": "native", "structured_output": true, "streaming": true, "max_context": 200000},
				"pricing":      map[string]any{"input_per_mtok": 3, "output_per_mtok": 15, "currency": "USD"}, "quality_prior": map[string]any{"plan": 0.9, "implement": 0.9, "verify": 0.9, "summarize": 0.9}}}}})
	if res["provider_id"] != "anthropic" || res["test"] != nil {
		t.Fatalf("add = %v", res)
	}
	yml, _ := os.ReadFile(e.l.Models())
	if !strings.Contains(string(yml), "secret://providers/anthropic/api_key") || strings.Contains(string(yml), key) {
		t.Fatalf("models.yaml:\n%s", yml)
	}
	list := call(t, c, "provider.list", nil)
	pv := list["providers"].([]any)[0].(map[string]any)
	if pv["secret_present"] != true || pv["status"] != "untested" {
		t.Fatalf("view = %v", pv)
	}
	_ = filepath.Walk(e.l.Home, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), key) {
				t.Errorf("key found in %s", p)
			}
		}
		return nil
	})
	evs, _ := e.st.Events(context.Background(), store.SysChain, 0, 0, []string{"provider.configured", "secret.access"})
	if len(evs) < 2 {
		t.Fatalf("events = %d", len(evs))
	}
	reg, _ := schema.Load()
	for _, ev := range evs {
		if err := reg.ValidateEnvelope(ev); err != nil {
			t.Fatalf("stored envelope invalid: %v\n%s", err, ev)
		}
	}
	// Re-adding without a secret works now that the keychain has one; an
	// inline secret reference to another provider is refused.
	if code, _ := callErr(c, "provider.add", map[string]any{"confirm": true, "test": false,
		"spec": map[string]any{"id": "anthropic", "protocol": "anthropic-messages", "base_url": "https://api.anthropic.com", "auth": map[string]any{"mode": "api_key", "secret": "secret://providers/other/api_key"}, "tier": "T3"}}); code != CodeInvalidParams {
		t.Fatalf("foreign secret ref = %d", code)
	}
	if code, d := callErr(c, "provider.add", map[string]any{"confirm": true, "test": false,
		"spec": map[string]any{"id": "x", "protocol": "openai-compatible", "base_url": "https://x.example/v1", "auth": map[string]any{"mode": "cloud_iam"}, "tier": "T2"}}); code != CodeUnsupportedInPoC {
		t.Fatalf("cloud_iam = %d %+v", code, d)
	}
}

// TestProviderTestAndModels: provider.test runs the probe and caches it;
// provider.models greys out T3/T4 for confidential data (INV-G preview).
func TestProviderTestAndModels(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(nil)
	call(t, c, "provider.add", map[string]any{"confirm": true, "test": false,
		"spec": map[string]any{"id": "ollama", "protocol": "openai-compatible", "base_url": "http://127.0.0.1:11434/v1", "auth": map[string]any{"mode": "none"}, "tier": "T0",
			"models": []any{map[string]any{"id": "local/qwen-coder-32b", "provider": "ollama", "model": "qwen2.5-coder:32b",
				"capabilities": map[string]any{"tool_calling": "native", "structured_output": true, "streaming": true, "max_context": 32768},
				"pricing":      nil, "quality_prior": map[string]any{"plan": 0.6, "implement": 0.55, "verify": 0.7, "summarize": 0.8}}}}})
	call(t, c, "provider.add", map[string]any{"confirm": true, "test": false, "secret": map[string]string{"value": "sk-test-value-1234567890"},
		"spec": map[string]any{"id": "openai", "protocol": "openai-compatible", "base_url": "https://api.openai.com/v1", "auth": map[string]any{"mode": "api_key"}, "tier": "T3",
			"models": []any{map[string]any{"id": "openai/gpt", "provider": "openai", "model": "gpt-x",
				"capabilities": map[string]any{"tool_calling": "native", "structured_output": true, "streaming": true, "max_context": 128000},
				"pricing":      nil, "quality_prior": map[string]any{"plan": 0.9, "implement": 0.9, "verify": 0.9, "summarize": 0.9}}}}})
	call(t, c, "provider.add", map[string]any{"confirm": true, "spec": map[string]any{"id": "copilot", "kind": "copilot-sdk", "billing": "subscription", "vendor_terms": "permitted", "tier": "T4", "enabled": true}})
	tr := call(t, c, "provider.test", map[string]any{"provider_id": "ollama"})
	if tr["ok"] != true || len(tr["models"].([]any)) != 1 {
		t.Fatalf("test = %v", tr)
	}
	if v := call(t, c, "provider.list", nil)["providers"].([]any)[0].(map[string]any); v["status"] != "ok" || v["last_test"] == nil {
		t.Fatalf("after test = %v", v)
	}
	ms := call(t, c, "provider.models", map[string]any{"classification": "confidential"})
	got := map[string]any{}
	for _, m := range ms["models"].([]any) {
		mv := m.(map[string]any)
		got[mv["model_id"].(string)] = mv["reason_code"]
		if mv["model_id"] == "local/qwen-coder-32b" && mv["admissible"] != true {
			t.Errorf("T0 not admitted for confidential: %v", mv)
		}
	}
	if got["openai/gpt"] != "tier_not_admitted" || got["copilot"] != "tier_not_admitted" {
		t.Fatalf("confidential admission = %v", got)
	}
	ms = call(t, c, "provider.models", map[string]any{"classification": "internal"})
	for _, m := range ms["models"].([]any) {
		if mv := m.(map[string]any); mv["admissible"] != true {
			t.Fatalf("internal should admit all configured models: %v", mv)
		}
	}
	if code, _ := callErr(c, "provider.test", map[string]any{"provider_id": "copilot"}); code != CodeUnsupportedInPoC {
		t.Fatalf("harness test = %d", code)
	}
	if code, _ := callErr(c, "provider.test", map[string]any{"provider_id": "nope"}); code != CodeNotFound {
		t.Fatalf("unknown = %d", code)
	}
	// Enable/disable and remove.
	call(t, c, "provider.enable", map[string]any{"provider_id": "ollama", "enabled": false})
	if code, d := callErr(c, "provider.enable", map[string]any{"provider_id": "nope", "enabled": true}); code != CodeNotFound {
		t.Fatalf("enable unknown = %d %+v", code, d)
	}
	rm := call(t, c, "provider.remove", map[string]any{"provider_id": "openai", "confirm": true})
	if len(rm["removed_models"].([]any)) != 1 || e.broker.Has(context.Background(), "secret://providers/openai/api_key") {
		t.Fatalf("remove = %v", rm)
	}
}

func TestProviderEnable_VendorTerms(t *testing.T) {
	c := newEnv(t, func(d *Deps) { d.Mode = "shared" }).dial(nil)
	call(t, c, "provider.add", map[string]any{"confirm": true, "spec": map[string]any{"id": "codex", "kind": "codex-app-server", "billing": "chatgpt_login", "vendor_terms": "tolerated", "tier": "T4"}})
	call(t, c, "provider.add", map[string]any{"confirm": true, "spec": map[string]any{"id": "claude-code", "kind": "claude-code-cli", "billing": "subscription_personal", "vendor_terms": "personal_use_only", "tier": "T4"}})
	if code, _ := callErr(c, "provider.enable", map[string]any{"provider_id": "codex", "enabled": true}); code != CodeConfirmationRequired {
		t.Fatalf("tolerated without ack = %d", code)
	}
	call(t, c, "provider.enable", map[string]any{"provider_id": "codex", "enabled": true, "acknowledge_terms": true})
	if code, d := callErr(c, "provider.enable", map[string]any{"provider_id": "claude-code", "enabled": true, "acknowledge_terms": true}); code != CodeVendorTerms || d.Reason != "harness_locked_shared_mode" {
		t.Fatalf("personal mode lock = %d %+v", code, d)
	}
	hs := call(t, c, "provider.list", nil)["harnesses"].([]any)
	for _, h := range hs {
		hv := h.(map[string]any)
		if hv["harness_id"] == "claude-code" && hv["locked_reason"] != "harness_locked_shared_mode" {
			t.Fatalf("view = %v", hv)
		}
	}
}

// TestEvents_SubscribeReplayLiveAndQuery: replay then live delivery in seq
// order without gaps, after the subscribe response (A05 §6.4).
func TestEvents_SubscribeReplayLiveAndQuery(t *testing.T) {
	e := newEnv(t, nil)
	ses := ids.NewID(ids.Session)
	ctx := context.Background()
	if _, err := e.s.Append(ctx, store.Input{Type: "session.open", Chain: ses, NewChain: true, Actor: e.s.runtimeActor(), Payload: map[string]any{"workspace_root": "/r"}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_, _ = e.s.Append(ctx, store.Input{Type: "task.state", Chain: ses, Actor: e.s.runtimeActor(), Payload: map[string]any{"i": i}})
	}
	var mu sync.Mutex
	var seqs []int64
	got := make(chan struct{}, 100)
	c := e.dial(func(method string, params json.RawMessage) {
		if method != "event" {
			return
		}
		var ev struct {
			Seq            int64  `json:"seq"`
			SubscriptionID string `json:"subscription_id"`
		}
		_ = json.Unmarshal(params, &ev)
		mu.Lock()
		seqs = append(seqs, ev.Seq)
		mu.Unlock()
		got <- struct{}{}
	})
	sub := call(t, c, "event.subscribe", map[string]any{"session_id": ses, "after_seq": 1})
	if sub["head_seq"].(float64) != 4 {
		t.Fatalf("subscribe = %v", sub)
	}
	_, _ = e.s.Append(ctx, store.Input{Type: "task.state", Chain: ses, Actor: e.s.runtimeActor(), Payload: map[string]any{"i": 3}})
	_, _ = e.s.Append(ctx, store.Input{Type: "runtime.stop", Chain: store.SysChain, Actor: e.s.runtimeActor(), Payload: map[string]any{"version": "0", "pid": 1, "mode": "personal", "reason": "signal"}})
	for i := 0; i < 4; i++ {
		select {
		case <-got:
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out; seqs = %v", seqs)
		}
	}
	mu.Lock()
	if fmt.Sprint(seqs) != "[2 3 4 5]" {
		t.Fatalf("seqs = %v (replay 2..4, live 5; sys event 6 filtered)", seqs)
	}
	mu.Unlock()
	q := call(t, c, "event.query", map[string]any{"session_id": ses, "after_seq": 0, "limit": 2, "types": []string{"task.*"}})
	if len(q["events"].([]any)) != 2 || q["has_more"] != true {
		t.Fatalf("query = %v", q)
	}
	all := call(t, c, "event.query", map[string]any{"session_id": "sys", "after_seq": 0})
	if len(all["events"].([]any)) == 0 {
		t.Fatal("sys chain query empty")
	}
	un := call(t, c, "event.unsubscribe", map[string]any{"subscription_id": sub["subscription_id"]})
	if un["existed"] != true {
		t.Fatalf("unsubscribe = %v", un)
	}
	if code, _ := callErr(c, "event.subscribe", map[string]any{"session_id": ids.NewID(ids.Session)}); code != CodeNotFound {
		t.Fatalf("unknown session = %d", code)
	}
}

func TestAudit_ExportVerifyRoundTrip(t *testing.T) {
	e := newEnv(t, nil)
	ses := ids.NewID(ids.Session)
	ctx := context.Background()
	_, _ = e.s.Append(ctx, store.Input{Type: "session.open", Chain: ses, NewChain: true, Actor: e.s.runtimeActor(), Payload: map[string]any{}})
	_, _ = e.s.Append(ctx, store.Input{Type: "policy.decision", Chain: ses, Actor: e.s.runtimeActor(), Payload: map[string]any{"decision_id": "dec_1", "call_id": "call_1", "effect": "allow"}})
	_, _ = e.s.Append(ctx, store.Input{Type: "tool.exec.start", Chain: ses, Actor: e.s.runtimeActor(), Payload: map[string]any{"call_id": "call_1", "decision_id": "dec_1", "tool": "fs.read"}})
	c := e.dial(nil)
	v := call(t, c, "audit.verify", map[string]any{"session_id": ses, "strict": true})
	if v["ok"] != true || v["strict_ok"] != true || v["tool_calls_checked"].(float64) != 1 {
		t.Fatalf("verify = %v", v)
	}
	x := call(t, c, "audit.export", map[string]any{"session_id": ses})
	path := x["path"].(string)
	if !strings.HasPrefix(path, e.l.Exports()) {
		t.Fatalf("export path %s", path)
	}
	fv := call(t, c, "audit.verify", map[string]any{"file": path, "strict": true})
	if fv["ok"] != true || fv["events"].(float64) != 4 {
		t.Fatalf("file verify = %v", fv)
	}
	if code, d := callErr(c, "audit.export", map[string]any{"session_id": ses, "path": e.l.DB()}); code != CodeInvalidParams || d.Reason != "path_not_allowed" {
		t.Fatalf("export into db = %d %+v", code, d)
	}
	if code, _ := callErr(c, "audit.verify", map[string]any{"session_id": ids.NewID(ids.Session)}); code != CodeNotFound {
		t.Fatalf("unknown session = %d", code)
	}
	if code, _ := callErr(c, "audit.verify", map[string]any{"file": filepath.Join(e.l.Home, "missing.jsonl")}); code != CodeNotFound {
		t.Fatalf("missing file = %d", code)
	}
}

func TestSecretsUnlock_FileBackend(t *testing.T) {
	var unlocked bool
	e := newEnv(t, func(d *Deps) {
		f := secrets.NewFile(filepath.Join(d.Layout.Home, "secrets.enc"))
		d.File = f
		d.Broker = secrets.NewBroker(f, nil, nil)
		d.OnUnlock = func(context.Context) error { unlocked = true; return nil }
	})
	c := e.dial(nil)
	if code, _ := callErr(c, "provider.add", map[string]any{"confirm": true, "test": false, "secret": map[string]string{"value": "v-1234567890"},
		"spec": map[string]any{"id": "openai", "protocol": "openai-compatible", "base_url": "https://api.openai.com/v1", "auth": map[string]any{"mode": "api_key"}, "tier": "T3"}}); code != CodeInvalidState {
		t.Fatalf("add while locked = %d", code)
	}
	if code, d := callErr(c, "secrets.unlock", map[string]any{"passphrase": "short"}); code != CodeInvalidParams {
		t.Fatalf("short passphrase = %d %+v", code, d)
	}
	u := call(t, c, "secrets.unlock", map[string]any{"passphrase": "a long enough passphrase"})
	if u["created"] != true || !unlocked {
		t.Fatalf("unlock = %v", u)
	}
	call(t, c, "provider.add", map[string]any{"confirm": true, "test": false, "secret": map[string]string{"value": "v-1234567890"},
		"spec": map[string]any{"id": "openai", "protocol": "openai-compatible", "base_url": "https://api.openai.com/v1", "auth": map[string]any{"mode": "api_key"}, "tier": "T3"}})
	c2 := newEnv(t, nil).dial(nil)
	if code, d := callErr(c2, "secrets.unlock", map[string]any{"passphrase": "whatever passphrase"}); code != CodeInvalidState || d.Reason != "not_file_backend" {
		t.Fatalf("keyring unlock = %d %+v", code, d)
	}
}

func TestShutdown(t *testing.T) {
	stopped := make(chan struct{})
	c := newEnv(t, func(d *Deps) { d.Shutdown = func() { close(stopped) } }).dial(nil)
	if code, _ := callErr(c, "system.shutdown", map[string]any{}); code != CodeInvalidParams {
		t.Fatalf("shutdown without confirm = %d", code)
	}
	call(t, c, "system.shutdown", map[string]any{"confirm": true})
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown not requested")
	}
}

// TestProviderAdd_DiscoversLocalModels: "warden provider add ollama"
// lists the server's models through the daemon (D-015).
func TestProviderAdd_DiscoversLocalModels(t *testing.T) {
	e := newEnv(t, func(d *Deps) {
		d.Discover = func(context.Context, model.ProviderConfig) ([]string, error) {
			return []string{"qwen2.5-coder:32b", "qwen2.5-coder:7b", "bad name with spaces"}, nil
		}
	})
	c := e.dial(nil)
	call(t, c, "provider.add", map[string]any{"confirm": true, "test": false,
		"spec": map[string]any{"id": "ollama", "protocol": "openai-compatible", "base_url": "http://127.0.0.1:11434/v1", "auth": map[string]any{"mode": "none"}, "tier": "T0"}})
	pv := call(t, c, "provider.list", nil)["providers"].([]any)[0].(map[string]any)
	if fmt.Sprint(pv["models"]) != "[ollama/qwen2.5-coder:32b ollama/qwen2.5-coder:7b]" {
		t.Fatalf("models = %v", pv["models"])
	}
}
