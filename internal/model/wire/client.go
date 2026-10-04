package wire

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"warden.dev/warden/internal/model"
)

// AuthTransport adds the provider credential to a clone of each outgoing
// request (design A11 §5.3). The credential is resolved per request through
// the CredentialSource (which emits secret.access), wiped after use and
// never stored, logged or put into an error.
type AuthTransport struct {
	Base       http.RoundTripper
	ProviderID string
	Ref        string // secret://providers/<id>/<name>; "" for auth mode none
	Creds      model.CredentialSource
}

// RoundTrip implements http.RoundTripper.
func (t *AuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.Ref == "" || t.Creds == nil {
		return t.Base.RoundTrip(r)
	}
	c, err := t.Creds.Credential(r.Context(), t.Ref, "adapter:"+t.ProviderID)
	if err != nil {
		e := model.NewError(model.ErrAuthFailed, "credential unavailable (keychain locked or entry missing)")
		e.Details = DetailsJSON(Details{Note: "keychain"})
		return nil, e
	}
	defer c.Wipe()
	if c.Header == "" { // gateway mTLS: the TLS layer presents the certificate
		return t.Base.RoundTrip(r)
	}
	r2 := r.Clone(r.Context())
	v := string(c.Value)
	if c.Scheme != "" {
		v = c.Scheme + " " + v
	}
	r2.Header.Set(c.Header, v)
	return t.Base.RoundTrip(r2)
}

// IsLoopbackHost reports whether host (without port) is a loopback name or
// address: localhost, 127.0.0.0/8, ::1.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// NewHTTPClient builds the client for one provider (design A11 §5.3–§5.5,
// §7.2): connect timeout, TLS 1.2+ with system roots plus ca_file, the mTLS
// client certificate from the credential source, the system proxy only for
// non-loopback hosts, no redirects, and AuthTransport on top. Plain http is
// accepted only for loopback hosts (OQ-22).
func NewHTTPClient(cfg model.ProviderConfig, creds model.CredentialSource) (*http.Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("provider %s: invalid base_url", cfg.ID)
	}
	if u.User != nil {
		return nil, fmt.Errorf("provider %s: base_url must not contain credentials", cfg.ID)
	}
	loopback := IsLoopbackHost(u.Hostname())
	switch u.Scheme {
	case "https":
	case "http":
		if !loopback {
			return nil, fmt.Errorf("provider %s: http:// is allowed only for loopback hosts; use https", cfg.ID)
		}
	default:
		return nil, fmt.Errorf("provider %s: unsupported scheme %q", cfg.ID, u.Scheme)
	}
	connect, _, _ := Timeouts(string(cfg.Tier), cfg.Timeouts.ConnectMS, 0, 0)
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.Auth.CAFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pem, err := os.ReadFile(cfg.Auth.CAFile)
		if err != nil {
			return nil, fmt.Errorf("provider %s: read ca_file: %w", cfg.ID, err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("provider %s: ca_file contains no PEM certificate", cfg.ID)
		}
		tlsCfg.RootCAs = pool
	}
	if cfg.Auth.Mode == model.AuthGateway && cfg.Auth.Kind == model.KindMTLS {
		cc := &clientCert{ref: cfg.Auth.Secret, consumer: "adapter:" + cfg.ID, creds: creds}
		tlsCfg.GetClientCertificate = cc.get
	}
	dialer := &net.Dialer{Timeout: connect, KeepAlive: 30 * time.Second}
	base := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   connect,
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	if !loopback {
		base.Proxy = http.ProxyFromEnvironment
	}
	ref := cfg.Auth.Secret
	if cfg.Auth.Mode == model.AuthNone || (cfg.Auth.Mode == model.AuthGateway && cfg.Auth.Kind == model.KindMTLS) {
		ref = "" // mode none ignores any configured secret (A15 §5); mTLS uses no header
	}
	return &http.Client{
		Transport: &AuthTransport{Base: base, ProviderID: cfg.ID, Ref: ref, Creds: creds},
		// Redirects are never followed: Go strips Authorization across hosts
		// but not x-api-key or api-key (A15 §5).
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// clientCert resolves the mTLS certificate once and keeps it for the
// provider instance's lifetime (A11 §5.5).
type clientCert struct {
	ref, consumer string
	creds         model.CredentialSource
	once          sync.Mutex
	cert          *tls.Certificate
}

func (c *clientCert) get(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	c.once.Lock()
	defer c.once.Unlock()
	if c.cert != nil {
		return c.cert, nil
	}
	if c.creds == nil {
		return nil, errors.New("no credential source for mTLS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cred, err := c.creds.Credential(ctx, c.ref, c.consumer)
	if err != nil {
		return nil, err
	}
	defer cred.Wipe()
	if cred.Cert == nil {
		return nil, errors.New("credential has no client certificate")
	}
	c.cert = cred.Cert
	return c.cert, nil
}

// Details is the diagnostic payload of model.Error.Details (A11 §8).
type Details struct {
	Status            int    `json:"status,omitempty"`
	ProviderErrorType string `json:"provider_error_type,omitempty"`
	ProviderErrorCode string `json:"provider_error_code,omitempty"`
	RequestID         string `json:"request_id,omitempty"`
	Body              string `json:"body,omitempty"` // ≤ 8 KiB; the host redactor runs before persistence
	Note              string `json:"note,omitempty"`
	TLS               string `json:"tls,omitempty"`
}

// MaxDetailBody bounds Details.Body.
const MaxDetailBody = 8 << 10

// DetailsJSON marshals d, truncating the body.
func DetailsJSON(d Details) json.RawMessage {
	if len(d.Body) > MaxDetailBody {
		d.Body = d.Body[:MaxDetailBody]
	}
	b, _ := json.Marshal(d)
	return b
}

// ReadErrorBody reads at most 64 KiB of a response body (A11 §6.2).
func ReadErrorBody(r io.Reader) []byte {
	b, _ := io.ReadAll(io.LimitReader(r, 64<<10))
	return b
}

// TransportError normalizes an error from http.Client.Do or a body read
// (A11 §8 rows 1–5, §7.2): caller cancellation, watchdog timeouts,
// connection failures, untrusted server certificates and rejected client
// certificates. callerCtx is the context the caller passed in; wd may be nil.
func TransportError(err error, callerCtx context.Context, wd *Watchdog) *model.Error {
	var me *model.Error
	if errors.As(err, &me) {
		return me
	}
	switch {
	case callerCtx.Err() != nil:
		return &model.Error{Code: model.ErrCancelled, Message: "request cancelled"}
	case wd != nil && wd.Fired():
		return model.NewError(model.ErrTimeout, "provider did not respond in time")
	}
	var uerr x509.UnknownAuthorityError
	var herr x509.HostnameError
	var cerr x509.CertificateInvalidError
	if errors.As(err, &uerr) || errors.As(err, &herr) || errors.As(err, &cerr) {
		e := &model.Error{Code: model.ErrProviderUnavailable, Message: "server certificate is not trusted; set ca_file", Retryable: false}
		e.Details = DetailsJSON(Details{TLS: "server_certificate_untrusted"})
		return e
	}
	msg := err.Error()
	for _, alert := range []string{"bad certificate", "certificate required", "unknown certificate authority", "certificate unknown"} {
		if strings.Contains(msg, "tls: "+alert) {
			e := model.NewError(model.ErrAuthFailed, "the server rejected the client certificate")
			e.Details = DetailsJSON(Details{TLS: strings.ReplaceAll(alert, " ", "_")})
			return e
		}
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return model.NewError(model.ErrProviderUnavailable, "connection timed out")
	}
	return model.NewError(model.ErrProviderUnavailable, "provider unreachable")
}
