package secrets

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"warden.dev/warden/internal/model"
)

// Access is a secret.access event payload (core §5, A15 §3.5). It never
// carries the value, its length, a hash or a prefix.
type Access struct {
	Ref      string `json:"ref"`
	Consumer string `json:"consumer"`
	Purpose  string `json:"purpose"`
	Cache    string `json:"cache"`   // hit | miss
	Result   string `json:"result"`  // ok | not_found | locked | unavailable | timeout | not_permitted
	Backend  string `json:"backend"` // secret-service | keychain | wincred | file | memory
}

// Emitter records secret.access events (wired to the store by cmd/wardend).
type Emitter func(ctx context.Context, a Access)

// ProviderAuth tells the broker how a provider's credential is presented
// (design A11 §5.1). The daemon registers it from models.yaml.
type ProviderAuth struct {
	Protocol string
	Auth     model.AuthConfig
}

// Broker resolves secret:// references for host-side consumers.
type Broker struct {
	backend  Backend
	redactor *Redactor
	emit     Emitter
	now      func() time.Time
	ttl      time.Duration

	mu        sync.Mutex
	providers map[string]ProviderAuth
	cache     map[string]cached
	certs     map[string]*tls.Certificate
	seen      map[string]bool // ref|consumer pairs already reported
}

type cached struct {
	v    []byte
	used time.Time
}

// NewBroker returns a broker over a backend. emit may be nil.
func NewBroker(b Backend, r *Redactor, emit Emitter) *Broker {
	if r == nil {
		r = NewRedactor()
	}
	return &Broker{backend: b, redactor: r, emit: emit, now: time.Now, ttl: 10 * time.Minute,
		providers: map[string]ProviderAuth{}, cache: map[string]cached{}, certs: map[string]*tls.Certificate{}, seen: map[string]bool{}}
}

// Backend returns the active backend.
func (b *Broker) Backend() Backend { return b.backend }

// Redactor returns the redactor that knows every resolved value.
func (b *Broker) Redactor() *Redactor { return b.redactor }

// SetProvider registers how a provider presents its credential.
func (b *Broker) SetProvider(id string, pa ProviderAuth) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.providers[id] = pa
	b.flushLocked()
}

// Flush drops cached values (provider.add/remove, policy.reload, shutdown).
func (b *Broker) Flush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushLocked()
}

func (b *Broker) flushLocked() {
	for k, c := range b.cache {
		wipe(c.v)
		delete(b.cache, k)
	}
	b.certs = map[string]*tls.Certificate{}
}

func wipe(v []byte) {
	for i := range v {
		v[i] = 0
	}
}

func result(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrLocked):
		return "locked"
	case errors.Is(err, ErrTimeout):
		return "timeout"
	case errors.Is(err, ErrNotPermitted):
		return "not_permitted"
	}
	return "unavailable"
}

// resolve returns a copy of the value; the caller wipes it.
func (b *Broker) resolve(ctx context.Context, ref, consumer, purpose string) ([]byte, error) {
	r, err := Parse(ref)
	if err != nil {
		return nil, err
	}
	if err := Authorize(r, consumer); err != nil {
		// A refused binding is a programming error, not a keychain read:
		// it is logged, and no secret.access is recorded (A04 result enum).
		slog.Warn("secret reference refused", "ref", ref, "consumer", consumer)
		return nil, err
	}
	b.mu.Lock()
	c, hit := b.cache[r.Account]
	if hit && b.now().Sub(c.used) > b.ttl {
		wipe(c.v)
		delete(b.cache, r.Account)
		hit = false
	}
	b.mu.Unlock()
	if hit {
		b.mu.Lock()
		c.used = b.now()
		b.cache[r.Account] = c
		v := append([]byte(nil), c.v...)
		b.mu.Unlock()
		b.report(ctx, Access{Ref: ref, Consumer: consumer, Purpose: purpose, Cache: "hit", Result: "ok"}, false)
		return v, nil
	}
	v, err := b.backend.Get(ctx, r.Account)
	b.report(ctx, Access{Ref: ref, Consumer: consumer, Purpose: purpose, Cache: "miss", Result: result(err)}, true)
	if err != nil {
		return nil, err
	}
	b.redactor.AddKnown(v)
	b.mu.Lock()
	b.cache[r.Account] = cached{v: append([]byte(nil), v...), used: b.now()}
	b.mu.Unlock()
	return v, nil
}

// report emits secret.access once per keychain read and the first time a
// reference is served to a consumer from the cache (A15 §3.5).
func (b *Broker) report(ctx context.Context, a Access, always bool) {
	if b.emit == nil {
		return
	}
	a.Backend = b.backend.Name()
	key := a.Ref + "|" + a.Consumer
	b.mu.Lock()
	first := !b.seen[key]
	b.seen[key] = true
	b.mu.Unlock()
	if always || first {
		b.emit(ctx, a)
	}
}

// Credential implements model.CredentialSource for the adapters.
func (b *Broker) Credential(ctx context.Context, ref, consumer string) (*model.Credential, error) {
	r, err := Parse(ref)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	pa, ok := b.providers[r.ID]
	cert := b.certs[r.ID]
	b.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("provider %s is not registered with the secrets broker", r.ID)
	}
	if pa.Auth.Mode == model.AuthGateway && pa.Auth.Kind == model.KindMTLS {
		if cert != nil {
			return &model.Credential{Cert: cert}, nil
		}
		return b.clientCert(ctx, r, consumer, pa)
	}
	v, err := b.resolve(ctx, ref, consumer, "model_call")
	if err != nil {
		return nil, err
	}
	c := &model.Credential{Value: v}
	switch {
	case pa.Protocol == model.ProtocolAnthropic:
		c.Header = "x-api-key"
	case pa.Auth.Header == "api-key":
		c.Header = "api-key"
	default:
		c.Header, c.Scheme = "Authorization", "Bearer"
	}
	return c, nil
}

// clientCert builds the mTLS certificate from the keychain key and the
// keychain chain (or auth.cert_file, certificate only); the key never
// touches disk (A11 §5.4, A15 §5).
func (b *Broker) clientCert(ctx context.Context, r Ref, consumer string, pa ProviderAuth) (*model.Credential, error) {
	key, err := b.resolve(ctx, ProviderRef(r.ID, "client_key"), consumer, "model_call")
	if err != nil {
		return nil, err
	}
	defer wipe(key)
	var chain []byte
	if pa.Auth.CertFile != "" {
		chain, err = os.ReadFile(pa.Auth.CertFile)
	} else {
		chain, err = b.resolve(ctx, ProviderRef(r.ID, "client_cert"), consumer, "model_call")
	}
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(chain, key)
	if err != nil {
		return nil, errors.New("client certificate and key do not match")
	}
	b.mu.Lock()
	b.certs[r.ID] = &cert
	b.mu.Unlock()
	return &model.Credential{Cert: &cert}, nil
}

// Put stores a value written by provider.add (never echoed), then reads it
// back to confirm. It validates tokens and refuses encrypted private keys.
func (b *Broker) Put(ctx context.Context, ref string, value []byte) error {
	r, err := Parse(ref)
	if err != nil {
		return err
	}
	if len(value) == 0 {
		return errors.New("empty secret")
	}
	switch r.Name {
	case "api_key", "token":
		if strings.ContainsAny(string(value), "\x00\r\n") {
			return errors.New("the value contains a newline or NUL")
		}
	case "client_key":
		if strings.Contains(string(value), "ENCRYPTED") {
			return errors.New("encrypted private keys are not supported; provide an unencrypted PEM key")
		}
	}
	if err := b.backend.Set(ctx, r.Account, value); err != nil {
		return err
	}
	b.Flush()
	consumer := "adapter:" + r.ID
	if r.Kind == "harness" {
		consumer = "harness:" + r.ID
	}
	back, err := b.resolve(ctx, ref, consumer, "provider_add_verify")
	if err != nil {
		return fmt.Errorf("stored secret could not be read back: %w", err)
	}
	defer wipe(back)
	if string(back) != string(value) {
		return errors.New("stored secret differs on read-back")
	}
	return nil
}

// Delete removes a value (provider.remove).
func (b *Broker) Delete(ctx context.Context, ref string) error {
	r, err := Parse(ref)
	if err != nil {
		return err
	}
	b.Flush()
	if err := b.backend.Delete(ctx, r.Account); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// Has reports whether a value exists, without resolving it for a consumer.
func (b *Broker) Has(ctx context.Context, ref string) bool {
	r, err := Parse(ref)
	if err != nil {
		return false
	}
	v, err := b.backend.Get(ctx, r.Account)
	wipe(v)
	return err == nil
}

// Status is the doctor view of the backend.
type Status struct {
	Backend string `json:"backend"`
	OK      bool   `json:"ok"`
	Locked  bool   `json:"locked"`
	Detail  string `json:"detail"`
}

// Status probes the backend with a set/get/delete round trip of
// doctor/probe (doctor check "keychain").
func (b *Broker) Status(ctx context.Context) Status {
	s := Status{Backend: b.backend.Name()}
	if f, ok := b.backend.(*File); ok && f.Locked() {
		s.Locked = true
		s.Detail = "encrypted file " + map[bool]string{true: "exists", false: "not created yet"}[f.Exists()] + "; locked (run warden unlock)"
		return s
	}
	if err := b.backend.Set(ctx, "doctor/probe", []byte("probe")); err != nil {
		s.Locked = errors.Is(err, ErrLocked)
		s.Detail = err.Error()
		return s
	}
	v, err := b.backend.Get(ctx, "doctor/probe")
	_ = b.backend.Delete(ctx, "doctor/probe")
	if err != nil || string(v) != "probe" {
		s.Detail = "round trip failed"
		return s
	}
	s.OK, s.Detail = true, "round trip ok"
	return s
}

// LogValue keeps the broker out of logs.
func (b *Broker) LogValue() slog.Value { return slog.StringValue("[secrets broker]") }
