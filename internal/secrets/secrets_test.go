package secrets

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"warden.dev/warden/internal/model"
	"warden.dev/warden/internal/store"
)

var ctx = context.Background()

// synthetic builds the corpus values at test time. Literals are split so
// that no file in the repository looks like a real credential to a secret
// scanner (CLAUDE.md §10: never write a credential into any file); every
// value is random or a vendor-documented example.
func synthetic() map[string]string {
	r := mrand.New(mrand.NewPCG(42, 7))
	alnum := func(n int) string {
		const a = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
		b := make([]byte, n)
		for i := range b {
			b[i] = a[r.IntN(len(a))]
		}
		return string(b)
	}
	b64url := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	keyBody := base64.StdEncoding.EncodeToString([]byte(alnum(120)))
	return map[string]string{
		"AWS_KEY_ID":   "AK" + "IA" + "IOSFODNN7EXAMPLE",
		"AWS_SECRET":   "wJalrXUtnFEMI/K7MDENG/" + "bPxRfiCYEXAMPLEKEY",
		"GOOGLE_KEY":   "AI" + "za" + alnum(35),
		"GITHUB_PAT":   "gh" + "p_" + alnum(36),
		"GITHUB_FINE":  "github" + "_pat_" + alnum(30),
		"GITLAB":       "gl" + "pat-" + alnum(20),
		"NPM":          "np" + "m_" + alnum(36),
		"ANTHROPIC":    "sk-" + "ant-" + "api03-" + alnum(48),
		"OPENAI":       "sk-" + "proj-" + alnum(48),
		"JWT":          "ey" + "J" + b64url(`{"alg":"HS256","typ":"JWT"}`)[1:] + ".ey" + "J" + b64url(`{"sub":"123","name":"x"}`)[1:] + "." + alnum(43),
		"AZURE_KEY":    alnum(32),
		"PRIVATE_KEY":  "-----BEGIN " + "PRIVATE KEY-----\n" + keyBody + "\n-----END " + "PRIVATE KEY-----",
		"CONN_URL":     "postgres://app:" + alnum(20) + "@db.internal:5432/orders",
		"NET_PASSWORD": alnum(18),
		"GENERIC":      alnum(28),
		"SLACK":        "xo" + "xb-" + alnum(24),
		"STRIPE":       "sk" + "_test_" + alnum(24),
	}
}

// TestRedactionCorpus is INV-C (CLAUDE.md §5, §8.3): every corpus file
// redacts exactly the expected count per secret type, and no planted value
// survives. negatives.txt guards against false positives on lockfiles,
// hashes, identifiers, expressions and placeholders.
func TestRedactionCorpus(t *testing.T) {
	vals := synthetic()
	files, _ := filepath.Glob(filepath.Join("testdata", "redaction", "*.txt"))
	if len(files) < 6 {
		t.Fatalf("corpus has %d files", len(files))
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".txt")
		t.Run(name, func(t *testing.T) {
			raw, _ := os.ReadFile(f)
			text := strings.ReplaceAll(string(raw), "\r\n", "\n")
			var planted []string
			for k, v := range vals {
				if strings.Contains(text, "{{"+k+"}}") {
					text = strings.ReplaceAll(text, "{{"+k+"}}", v)
					planted = append(planted, v)
				}
			}
			var want map[string]int
			b, err := os.ReadFile(strings.TrimSuffix(f, ".txt") + ".expected.json")
			if err != nil || json.Unmarshal(b, &want) != nil {
				t.Fatalf("expected counts: %v", err)
			}
			out, counts := NewRedactor().RedactBytes([]byte(text))
			if len(counts) == 0 {
				counts = map[string]int{}
			}
			if !reflect.DeepEqual(counts, want) {
				t.Errorf("counts = %v, want %v\n%s", counts, want, out)
			}
			for _, v := range planted {
				if strings.Contains(string(out), v) {
					t.Errorf("value survived: %.12s…", v)
				}
			}
		})
	}
}

func TestRedactor_KnownSecretsAndBase64(t *testing.T) {
	r := NewRedactor()
	shapeless := "WRDN-CANARY-1f2e3d4c5b6a-bearer"
	r.AddKnown([]byte(shapeless))
	r.AddKnown([]byte("short")) // under 8 bytes: ignored
	r.AddKnown([]byte(shapeless))
	in := "token " + shapeless + " and b64 " + base64.StdEncoding.EncodeToString([]byte(shapeless)) + " short"
	out, types := r.Redact(in)
	if strings.Contains(out, "CANARY") || len(types) != 2 || types[0] != "known_secret" || !strings.HasSuffix(out, "short") {
		t.Fatalf("out = %q types = %v", out, types)
	}
	if out2, types2 := r.Redact("nothing here"); out2 != "nothing here" || types2 != nil {
		t.Fatal("clean text changed")
	}
}

func TestRedactor_PriorityOnOverlap(t *testing.T) {
	jwt := synthetic()["JWT"]
	out, types := NewRedactor().Redact("Authorization: Bearer " + jwt)
	if out != "Authorization: Bearer [REDACTED:jwt]" || len(types) != 1 {
		t.Fatalf("out = %q %v", out, types)
	}
}

func TestParseAndAuthorize(t *testing.T) {
	good := map[string]Ref{
		"secret://providers/anthropic/api_key":  {Account: "providers/anthropic/api_key", Kind: "provider", ID: "anthropic", Name: "api_key"},
		"secret://providers/company-vllm/token": {Account: "providers/company-vllm/token", Kind: "provider", ID: "company-vllm", Name: "token"},
		"secret://harnesses/copilot/token":      {Account: "harnesses/copilot/token", Kind: "harness", ID: "copilot", Name: "token"},
		"secret://keys/checkpoint/ed25519":      {Account: "keys/checkpoint/ed25519", Kind: "checkpoint_key"},
	}
	for s, want := range good {
		got, err := Parse(s)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v %v", s, got, err)
		}
	}
	for _, s := range []string{"providers/x/api_key", "secret://providers/X/api_key", "secret://providers/x/password", "secret://providers/x/api_key/",
		"secret://providers/../api_key", "secret://providers/x/api_key?y=1", "secret://providers/x%2Fy/api_key", "secret://keys/other"} {
		if _, err := Parse(s); !errors.Is(err, ErrBadRef) {
			t.Errorf("Parse(%q) accepted", s)
		}
	}
	r, _ := Parse("secret://providers/anthropic/api_key")
	if Authorize(r, "adapter:anthropic") != nil {
		t.Error("own adapter refused")
	}
	// A catalog entry for ollama cannot borrow the anthropic key (A15 §3.2).
	for _, c := range []string{"adapter:ollama", "proxy", "delivery", "checkpoint", "harness:anthropic"} {
		if !errors.Is(Authorize(r, c), ErrNotPermitted) {
			t.Errorf("%s may resolve the anthropic key", c)
		}
	}
	k, _ := Parse(CheckpointRef)
	if Authorize(k, "checkpoint") != nil || Authorize(k, "adapter:anthropic") == nil {
		t.Error("checkpoint key binding wrong")
	}
}

func TestFileBackend_RoundTripAndWrongPassphrase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.enc")
	f := NewFile(path)
	if !f.Locked() || f.Exists() {
		t.Fatal("new file backend not locked/absent")
	}
	if _, err := f.Get(ctx, "a"); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked get: %v", err)
	}
	if err := f.Unlock([]byte("short")); err == nil {
		t.Fatal("short passphrase accepted on creation")
	}
	pass := []byte("correct horse battery staple")
	if err := f.Unlock(pass); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(ctx, "providers/anthropic/api_key", []byte("value-1")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "value-1") || strings.Contains(string(raw), "anthropic") {
		t.Fatal("plaintext in the encrypted file")
	}
	g := NewFile(path)
	if err := g.Unlock([]byte("wrong passphrase!!")); !errors.Is(err, ErrLocked) || !g.Locked() {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if err := g.Unlock(pass); err != nil {
		t.Fatal(err)
	}
	if v, err := g.Get(ctx, "providers/anthropic/api_key"); err != nil || string(v) != "value-1" {
		t.Fatalf("get = %q %v", v, err)
	}
	if err := g.Delete(ctx, "providers/anthropic/api_key"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Get(ctx, "providers/anthropic/api_key"); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted value still there")
	}
	if err := g.Delete(ctx, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete of a missing value")
	}
	_ = os.WriteFile(path, []byte("{}"), 0o600)
	if err := NewFile(path).Unlock(pass); err == nil {
		t.Fatal("garbage file accepted")
	}
}

func TestMemoryBackend(t *testing.T) {
	m := NewMemory()
	if _, err := m.Get(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing value")
	}
	_ = m.Set(ctx, "a", []byte("1"))
	if v, _ := m.Get(ctx, "a"); string(v) != "1" {
		t.Fatal("round trip")
	}
	if m.Delete(ctx, "a") != nil || !errors.Is(m.Delete(ctx, "a"), ErrNotFound) {
		t.Fatal("delete")
	}
}

type events struct {
	mu sync.Mutex
	l  []Access
}

func (e *events) emit(_ context.Context, a Access) { e.mu.Lock(); e.l = append(e.l, a); e.mu.Unlock() }

func TestBroker_HeadersCacheAndEvents(t *testing.T) {
	ev := &events{}
	b := NewBroker(NewMemory(), nil, ev.emit)
	key := "sk-" + "ant-" + "api03-" + strings.Repeat("Ab1", 12)
	if err := b.Put(ctx, "secret://providers/anthropic/api_key", []byte(key)); err != nil {
		t.Fatal(err)
	}
	_ = b.Put(ctx, "secret://providers/azure-openai/api_key", []byte("azure-key-123456"))
	_ = b.Put(ctx, "secret://providers/company-vllm/token", []byte("gateway-token-123"))
	b.SetProvider("anthropic", ProviderAuth{Protocol: model.ProtocolAnthropic, Auth: model.AuthConfig{Mode: model.AuthAPIKey}})
	b.SetProvider("azure-openai", ProviderAuth{Protocol: model.ProtocolOpenAICompat, Auth: model.AuthConfig{Mode: model.AuthAPIKey, Header: "api-key"}})
	b.SetProvider("company-vllm", ProviderAuth{Protocol: model.ProtocolOpenAICompat, Auth: model.AuthConfig{Mode: model.AuthGateway, Kind: model.KindBearer}})
	cases := []struct{ ref, consumer, header, scheme string }{
		{"secret://providers/anthropic/api_key", "adapter:anthropic", "x-api-key", ""},
		{"secret://providers/azure-openai/api_key", "adapter:azure-openai", "api-key", ""},
		{"secret://providers/company-vllm/token", "adapter:company-vllm", "Authorization", "Bearer"},
	}
	for _, c := range cases {
		cr, err := b.Credential(ctx, c.ref, c.consumer)
		if err != nil || cr.Header != c.header || cr.Scheme != c.scheme {
			t.Fatalf("%s: %+v %v", c.ref, cr, err)
		}
		cr.Wipe()
	}
	// Second resolution is a cache hit and is not reported again.
	n := len(ev.l)
	cr, _ := b.Credential(ctx, "secret://providers/anthropic/api_key", "adapter:anthropic")
	if string(cr.Value) != key || len(ev.l) != n {
		t.Fatalf("cache: %d events after hit", len(ev.l)-n)
	}
	// The resolved value is now known to the redactor.
	if out, _ := b.Redactor().Redact("leak " + key); strings.Contains(out, key) {
		t.Fatal("resolved key not redacted")
	}
	// Borrowing another provider's key is refused and reported.
	if _, err := b.Credential(ctx, "secret://providers/anthropic/api_key", "adapter:ollama"); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("borrow: %v", err)
	}
	if last := ev.l[len(ev.l)-1]; last.Result != "not_permitted" || last.Backend != "memory" {
		t.Fatalf("last event = %+v", last)
	}
	for _, a := range ev.l {
		b, _ := json.Marshal(a)
		if strings.Contains(string(b), key) || strings.Contains(string(b), "gateway-token") {
			t.Fatal("value in a secret.access event")
		}
	}
	if _, err := b.Credential(ctx, "secret://providers/unknown/api_key", "adapter:unknown"); err == nil {
		t.Fatal("unregistered provider resolved")
	}
	// Missing value.
	b.SetProvider("openai", ProviderAuth{Protocol: model.ProtocolOpenAICompat, Auth: model.AuthConfig{Mode: model.AuthAPIKey}})
	if _, err := b.Credential(ctx, "secret://providers/openai/api_key", "adapter:openai"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestBroker_PutValidation(t *testing.T) {
	b := NewBroker(NewMemory(), nil, nil)
	for v, ok := range map[string]bool{"": false, "line\nbreak": false, "nul\x00": false, "fine-token-1234": true} {
		if err := b.Put(ctx, "secret://providers/p/token", []byte(v)); (err == nil) != ok {
			t.Errorf("Put(%q) = %v", v, err)
		}
	}
	if err := b.Put(ctx, "secret://providers/p/client_key", []byte("-----BEGIN ENCRYPTED PRIVATE KEY-----")); err == nil {
		t.Error("encrypted key accepted")
	}
	if err := b.Put(ctx, "nope", []byte("x")); err == nil {
		t.Error("bad ref accepted")
	}
	if !b.Has(ctx, "secret://providers/p/token") || b.Has(ctx, "secret://providers/q/token") {
		t.Error("Has")
	}
	if err := b.Delete(ctx, "secret://providers/p/token"); err != nil || b.Has(ctx, "secret://providers/p/token") {
		t.Error("Delete")
	}
	if err := b.Delete(ctx, "secret://providers/p/token"); err != nil {
		t.Error("deleting a missing value is not an error")
	}
}

func TestBroker_MTLSClientCertificate(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "warden"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	kder, _ := x509.MarshalPKCS8PrivateKey(key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})
	b := NewBroker(NewMemory(), nil, nil)
	_ = b.Put(ctx, "secret://providers/vps/client_key", keyPEM)
	_ = b.Put(ctx, "secret://providers/vps/client_cert", certPEM)
	b.SetProvider("vps", ProviderAuth{Protocol: model.ProtocolOpenAICompat, Auth: model.AuthConfig{Mode: model.AuthGateway, Kind: model.KindMTLS}})
	c, err := b.Credential(ctx, "secret://providers/vps/client_key", "adapter:vps")
	if err != nil || c.Cert == nil || c.Header != "" {
		t.Fatalf("cred = %+v %v", c, err)
	}
	c2, _ := b.Credential(ctx, "secret://providers/vps/client_key", "adapter:vps")
	if c2.Cert != c.Cert {
		t.Fatal("certificate not kept for the provider instance")
	}
	// Certificate from cert_file; mismatched key is refused.
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	oder, _ := x509.MarshalPKCS8PrivateKey(other)
	_ = b.Put(ctx, "secret://providers/vps2/client_key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: oder}))
	certFile := filepath.Join(t.TempDir(), "client.crt")
	_ = os.WriteFile(certFile, certPEM, 0o600)
	b.SetProvider("vps2", ProviderAuth{Protocol: model.ProtocolOpenAICompat, Auth: model.AuthConfig{Mode: model.AuthGateway, Kind: model.KindMTLS, CertFile: certFile}})
	if _, err := b.Credential(ctx, "secret://providers/vps2/client_key", "adapter:vps2"); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("mismatched key: %v", err)
	}
}

func TestSigner_GenerateLoadAndArchive(t *testing.T) {
	dir := t.TempDir()
	mem := NewMemory()
	b := NewBroker(mem, nil, nil)
	s1, err := LoadSigner(ctx, b, dir)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := LoadSigner(ctx, NewBroker(mem, nil, nil), dir)
	if err != nil || s2.KeyID() != s1.KeyID() {
		t.Fatalf("reload: %v %s %s", err, s2.KeyID(), s1.KeyID())
	}
	sig, _ := s1.Sign(ctx, []byte("m"))
	// The store's verifier accepts this signer's key from checkpoint.pub.
	keys := PublicKeys(dir)
	if pub, ok := keys[s1.KeyID()]; !ok || !pub.Equal(s1.PublicKey()) || len(sig) != 64 {
		t.Fatalf("keys = %v", keys)
	}
	// A different keychain key archives the old public key.
	s3, err := LoadSigner(ctx, NewBroker(NewMemory(), nil, nil), dir)
	if err != nil || s3.KeyID() == s1.KeyID() {
		t.Fatal("new key expected")
	}
	if keys := PublicKeys(dir); len(keys) != 2 {
		t.Fatalf("archived keys = %d", len(keys))
	}
	_ = mem.Set(ctx, "keys/checkpoint/ed25519", []byte("not base64!"))
	if _, err := LoadSigner(ctx, NewBroker(mem, nil, nil), dir); err == nil {
		t.Fatal("malformed seed accepted")
	}
}

func TestStatus(t *testing.T) {
	if s := NewBroker(NewMemory(), nil, nil).Status(ctx); !s.OK || s.Backend != "memory" {
		t.Fatalf("memory status = %+v", s)
	}
	f := NewFile(filepath.Join(t.TempDir(), "secrets.enc"))
	if s := NewBroker(f, nil, nil).Status(ctx); s.OK || !s.Locked || !strings.Contains(s.Detail, "warden unlock") {
		t.Fatalf("locked file status = %+v", s)
	}
	_ = f.Unlock([]byte("a long enough passphrase"))
	if s := NewBroker(f, nil, nil).Status(ctx); !s.OK {
		t.Fatalf("unlocked file status = %+v", s)
	}
}

// TestNoSecretsInEvents (M1 slice of the CLAUDE.md §8.3 test; the full T1
// e2e version lands with M3): a known fake key resolved through the broker
// and then echoed in tool output is redacted by the store's final pass and
// appears nowhere in the SQLite files, including the WAL.
func TestNoSecretsInEvents(t *testing.T) {
	key := "sk-" + "ant-" + "api03-" + "FAKEKEYFORTESTSONLY0123456789abcdefghijkl"
	ev := &events{}
	b := NewBroker(NewMemory(), nil, ev.emit)
	_ = b.Put(ctx, "secret://providers/anthropic/api_key", []byte(key))
	b.SetProvider("anthropic", ProviderAuth{Protocol: model.ProtocolAnthropic, Auth: model.AuthConfig{Mode: model.AuthAPIKey}})
	c, _ := b.Credential(ctx, "secret://providers/anthropic/api_key", "adapter:anthropic")
	c.Wipe()
	// A shapeless gateway token matches no pattern; only the known-value set
	// (filled when the broker resolves it) can catch it.
	shapeless := "WRDN-CANARY-7d3f9a1c2e4b-FAKEKEYFORTESTSONLY"
	_ = b.Put(ctx, "secret://providers/company-vllm/token", []byte(shapeless))
	b.SetProvider("company-vllm", ProviderAuth{Protocol: model.ProtocolOpenAICompat, Auth: model.AuthConfig{Mode: model.AuthGateway, Kind: model.KindBearer}})
	c, _ = b.Credential(ctx, "secret://providers/company-vllm/token", "adapter:company-vllm")
	c.Wipe()
	dir := t.TempDir()
	st, err := store.Open(ctx, store.Options{Dir: filepath.Join(dir, "db"), Redactor: b.Redactor()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, a := range ev.l {
		if _, err := st.Append(ctx, store.Input{Type: "secret.access", Chain: store.SysChain, Actor: store.Runtime("secrets", "0"), Payload: a}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Append(ctx, store.Input{Type: "redaction", Chain: store.SysChain, Actor: store.Runtime("secrets", "0"),
		Payload: map[string]any{"source": "log", "echo": "printed " + key + " and " + shapeless}}); err != nil {
		t.Fatal(err)
	}
	var found []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if raw, _ := os.ReadFile(p); strings.Contains(string(raw), "FAKEKEYFORTESTSONLY") {
				found = append(found, filepath.Base(p))
			}
		}
		return nil
	})
	sort.Strings(found)
	if len(found) > 0 {
		t.Fatalf("fake key found in %v", found)
	}
}
