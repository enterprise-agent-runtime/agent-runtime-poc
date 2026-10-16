// Command wardend is the Warden runtime daemon (WRD-02 §3, WRD-16 §5,
// design A05 §3.4). It wires the components together and serves the
// JSON-RPC API on the platform endpoint: a Unix socket on Linux/macOS, a
// named pipe on Windows, never a TCP port (INV-J).
//
// Exit codes: 0 normal shutdown, 1 start failure, 2 bad --token-stdin,
// 3 another daemon is already running for this home.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"warden.dev/warden/internal/api"
	"warden.dev/warden/internal/buildinfo"
	"warden.dev/warden/internal/config"
	"warden.dev/warden/internal/model"
	"warden.dev/warden/internal/platform"
	"warden.dev/warden/internal/providers/anthropic"
	"warden.dev/warden/internal/providers/openaicompat"
	"warden.dev/warden/internal/secrets"
	"warden.dev/warden/internal/store"
)

// scrubbed are environment variables SDKs read implicitly (A15 §3.4);
// credentials reach adapters only from the keychain.
var scrubbed = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "OPENAI_API_KEY", "OPENAI_BASE_URL",
	"OPENAI_ORG_ID", "AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT"}

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func main() { os.Exit(run()) }

func run() int {
	tokenStdin := flag.Bool("token-stdin", false, "read the API token from stdin (desktop sidecar start)")
	flag.Bool("foreground", true, "run in the foreground (the default; warden daemon start detaches)")
	flag.Parse()
	for _, k := range scrubbed {
		os.Unsetenv(k)
	}
	home, err := platform.Home()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wardend:", err)
		return 1
	}
	l := platform.Layout{Home: home}
	if err := l.Ensure(); err != nil {
		fmt.Fprintln(os.Stderr, "wardend:", err)
		return 1
	}
	token, err := readToken(*tokenStdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wardend:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Secrets backend: the OS keychain when it works, else the encrypted file.
	var srv *api.Server
	backend := secrets.Select(ctx, home, true)
	broker := secrets.NewBroker(backend, nil, func(ctx context.Context, a secrets.Access) {
		if srv != nil {
			srv.SecretAccess(ctx, a)
		}
	})
	slog.SetDefault(slog.New(secrets.NewLogHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}), broker.Redactor())))
	file, _ := backend.(*secrets.File)

	var signer *secrets.Signer
	unsigned := "key_missing"
	if file != nil && file.Locked() {
		unsigned = "keychain_locked"
		slog.Warn("secrets backend is the encrypted file and is locked; run warden unlock")
	} else if signer, err = secrets.LoadSigner(ctx, broker, l.Keys()); err != nil {
		unsigned = "keychain_locked"
		slog.Warn("checkpoint key unavailable; checkpoints will be unsigned", "error", err.Error())
	}
	opts := store.Options{Dir: l.DB(), BlobsDir: l.Blobs(), RuntimeVersion: buildinfo.Version, User: platform.LocalUser(),
		Redactor: broker.Redactor(), PublicKeys: secrets.PublicKeys(l.Keys())}
	if signer != nil {
		opts.Signer = signer
	}
	st, err := store.Open(ctx, opts)
	if err != nil {
		slog.Error("store unavailable", "error", err.Error())
		return 1
	}
	defer st.Close()
	if signer == nil {
		st.SetSigner(nil, unsigned)
	}
	cat, err := config.LoadCatalog(l.Models())
	if err != nil {
		slog.Error("models.yaml is invalid; fix it or remove the entry", "error", err.Error())
		cat = &config.Catalog{}
	}
	cache := config.NewProbeCache(filepath.Join(l.Cache(), "providers"))
	shutdown := make(chan struct{})
	srv, err = api.New(api.Deps{
		Layout: l, Store: st, Broker: broker, File: file, Catalog: cat, CatalogPath: l.Models(), Cache: cache,
		NewProvider: func(pc model.ProviderConfig) (model.Provider, error) { return newProvider(pc, broker, cache) },
		Discover: func(ctx context.Context, pc model.ProviderConfig) ([]string, error) {
			return openaicompat.ListModels(ctx, pc, broker)
		},
		Token: token, Mode: "personal", DefaultLevel: platform.DefaultSandboxLevel(), Version: buildinfo.Version, Commit: buildinfo.Commit,
		Shutdown: func() { close(shutdown) },
		OnUnlock: func(ctx context.Context) error {
			sg, err := secrets.LoadSigner(ctx, broker, l.Keys())
			if err != nil {
				return err
			}
			signer = sg
			st.SetSigner(sg, "")
			return nil
		},
		SignerKeyID: func() string {
			if signer == nil {
				return ""
			}
			return signer.KeyID()
		},
	})
	if err != nil {
		slog.Error("api", "error", err.Error())
		return 1
	}
	// Token file first, then bind, so any client that can connect can read
	// the token (A05 §3.1).
	if err := platform.WriteOwnerOnly(l.Token(), []byte(token)); err != nil {
		slog.Error("cannot write the token file", "error", err.Error())
		return 1
	}
	ln, err := platform.Listen(home)
	if errors.Is(err, platform.ErrDaemonRunning) {
		fmt.Fprintln(os.Stderr, "wardend: already running for", home)
		return 3
	}
	if err != nil {
		slog.Error("cannot listen", "endpoint", platform.Endpoint(home), "error", err.Error())
		return 1
	}
	_ = platform.WriteOwnerOnly(l.PID(), []byte(strconv.Itoa(os.Getpid())))
	if err := srv.RuntimeStart(ctx); err != nil {
		slog.Error("cannot record runtime.start; the runtime fails closed", "error", err.Error())
		return 1
	}
	slog.Info("wardend listening", "endpoint", platform.Endpoint(home), "version", buildinfo.Version, "secrets_backend", backend.Name(), "sandbox_default", platform.DefaultSandboxLevel())
	serveCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(serveCtx, ln) }()
	reason := "signal"
	select {
	case <-ctx.Done():
	case <-shutdown:
		reason = "shutdown_request"
	case err := <-done:
		slog.Error("server stopped", "error", fmt.Sprint(err))
		reason = "fatal_error"
	}
	_ = srv.RuntimeStop(context.Background(), reason)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	broker.Flush()
	// Shutdown order: endpoint (closed by Serve), token, pid (A05 §3.1).
	os.Remove(l.Token())
	os.Remove(l.PID())
	slog.Info("wardend stopped", "reason", reason)
	return 0
}

// readToken reads the desktop-generated token from stdin within 2 s, or
// generates 32 random bytes (A05 §3.1).
func readToken(fromStdin bool) (string, error) {
	if !fromStdin {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(b), nil
	}
	ch := make(chan string, 1)
	go func() {
		r := bufio.NewReader(os.Stdin)
		line, _ := r.ReadString('\n')
		ch <- line
	}()
	select {
	case line := <-ch:
		if len(line) > 64 {
			return "", errors.New("token line too long")
		}
		t := line
		for len(t) > 0 && (t[len(t)-1] == '\n' || t[len(t)-1] == '\r') {
			t = t[:len(t)-1]
		}
		if !tokenPattern.MatchString(t) {
			return "", errors.New("malformed token on stdin")
		}
		return t, nil
	case <-time.After(2 * time.Second):
		return "", errors.New("no token on stdin within 2 s")
	}
}

// newProvider is the only place that constructs adapters (design A02 R2).
func newProvider(pc model.ProviderConfig, creds model.CredentialSource, cache model.ProbeCache) (model.Provider, error) {
	switch pc.Protocol {
	case model.ProtocolAnthropic:
		return anthropic.New(pc, creds, cache)
	case model.ProtocolOpenAICompat:
		return openaicompat.New(pc, creds, cache)
	}
	return nil, fmt.Errorf("protocol %q is not supported in the PoC", pc.Protocol)
}
