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
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
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
	// Nothing wardend inherited reaches a child it starts (CWE-403).
	if err := platform.CloseOnExecInherited(); err != nil {
		fmt.Fprintln(os.Stderr, "wardend:", err)
		return 1
	}
	for _, k := range scrubbed {
		os.Unsetenv(k)
	}
	home, err := platform.Home()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wardend:", err)
		return 1
	}
	return serve(home, *tokenStdin, daemonDeps{
		lock:         platform.LockDaemon,
		backend:      func(ctx context.Context, home string) secrets.Backend { return secrets.Select(ctx, home, true) },
		runtimeStart: func(ctx context.Context, srv *api.Server) error { return srv.RuntimeStart(ctx) },
	})
}

// daemonDeps are the parts of serve a test replaces: the daemon lock, the
// secrets backend (tests use secrets.NewMemory, so no test touches the OS
// keychain, D-043) and the runtime.start append.
type daemonDeps struct {
	lock         func(home string) (unlock func(), err error)
	backend      func(ctx context.Context, home string) secrets.Backend
	runtimeStart func(ctx context.Context, srv *api.Server) error
}

// serve runs the daemon for home and returns its exit code (see the
// package comment).
func serve(home string, tokenStdin bool, deps daemonDeps) int {
	l := platform.Layout{Home: home}
	if err := l.Ensure(); err != nil {
		fmt.Fprintln(os.Stderr, "wardend:", err)
		return 1
	}
	// The daemon lock comes first: a second wardend for this home stops here
	// (exit 3), before it creates a checkpoint seed, writes keys/, migrates
	// db/ or touches run/ (WH-4 F2, CWE-362). It is held for the daemon's
	// whole lifetime and released last: this defer is registered before the
	// store's and the unpublish cleanup's, so it runs after both (F3), and no
	// other daemon can publish while this one is still removing its token.
	unlock, err := deps.lock(home)
	if errors.Is(err, platform.ErrDaemonRunning) {
		fmt.Fprintln(os.Stderr, "wardend: already running for", home)
		return 3
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "wardend: cannot take the daemon lock:", err)
		return 1
	}
	defer unlock()
	token, err := readToken(tokenStdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wardend:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Secrets backend: the OS keychain when it works, else the encrypted file.
	var srv *api.Server
	backend := deps.backend(ctx, home)
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
	// Checkpoint keys are trust anchors: a key file another account could
	// have written stops the start rather than being trusted or ignored
	// (WH-4; secrets.PublicKeys).
	pubKeys, err := secrets.PublicKeys(l.Keys())
	if err != nil {
		slog.Error("refusing to start: cannot load the checkpoint keys", "error", err.Error())
		return 1
	}
	opts := store.Options{Dir: l.DB(), BlobsDir: l.Blobs(), RuntimeVersion: buildinfo.Version, User: platform.LocalUser(),
		Redactor: broker.Redactor(), PublicKeys: pubKeys}
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
	// Bind, then write the token, under the lock taken above: a second
	// daemon fails before it can replace the live daemon's token (WH-1+2,
	// WH-4; this reverses A05 §3.1's token-then-bind order).
	ln, err := listenThenPublish(home, l.Token(), token, platform.Listen, platform.WriteOwnerOnly)
	if errors.Is(err, platform.ErrDaemonRunning) {
		fmt.Fprintln(os.Stderr, "wardend: already running for", home)
		return 3
	}
	if err != nil {
		slog.Error("refusing to serve", "endpoint", platform.Endpoint(home), "error", err.Error())
		return 1
	}
	// From here every exit path unpublishes, in the A05 §3.1 order: the
	// endpoint (Serve closes it too; a second Close is harmless), then the
	// token (only if it is still ours), then the pid file (WH-4 F4: a failed
	// runtime.start used to leave both behind). The store closes after this
	// and the lock is released last (deferred first, above).
	defer func() {
		removeOwnToken(l.Token(), token)
		os.Remove(l.PID())
	}()
	defer ln.Close()
	_ = platform.WriteOwnerOnly(l.PID(), []byte(strconv.Itoa(os.Getpid())))
	if err := deps.runtimeStart(ctx, srv); err != nil {
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
	// The deferred cleanup removes the token and pid file, then the store
	// closes, then the lock is released.
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

// writeToken writes the token file with write and verifies it before the
// daemon binds and serves: read back once through the same no-follow,
// owner-only check the client uses, and compared with the token. A token
// file another account can read or replace defeats the handshake, so a
// refused file is removed and wardend does not serve (WH-1+2; A05 §3.1).
// On a volume without ACLs (FAT, some network shares) the check fails and
// wardend refuses to start: fail closed.
func writeToken(path, token string, write func(string, []byte) error) error {
	if err := write(path, []byte(token)); err != nil {
		return fmt.Errorf("cannot write the token file: %w", err)
	}
	got, err := platform.ReadOwnerOnly(path, api.MaxTokenFile)
	if err != nil {
		os.Remove(path)
		return fmt.Errorf("the token file failed its owner-only check: %w", err)
	}
	if string(got) != token {
		os.Remove(path)
		return fmt.Errorf("the token file %s does not hold the token just written", path)
	}
	return nil
}

// removeOwnToken removes the token file at shutdown only if it still holds
// this daemon's token (A05 §3.1, WH-4, CWE-367). A daemon that started in
// the shutdown window has written its own token there, and removing it
// would lock that daemon's clients out. The file is read through the same
// no-follow, owner-only check the client uses; a file that fails it, or
// holds another value, is left alone. The daemon lock (platform.LockDaemon)
// is held until after this step, so no other daemon can publish between
// the read and the remove.
func removeOwnToken(path, token string) {
	got, err := platform.ReadOwnerOnly(path, api.MaxTokenFile)
	if err != nil || subtle.ConstantTimeCompare(got, []byte(token)) != 1 {
		return
	}
	os.Remove(path)
}

// listenThenPublish binds the daemon endpoint and only then writes and
// verifies the token file (writeToken). The caller holds the daemon lock
// (platform.LockDaemon) for its whole lifetime, which makes the sequence,
// Listen's stale-socket removal included, exclusive per home: after a
// crash two wardends could both remove the stale socket and bind, the
// second removing the first one's fresh socket (WH-4, CWE-367). Listen
// still refuses a live endpoint with ErrDaemonRunning, so a daemon of an
// older build that holds no lock keeps its token too (WH-1+2). Clients
// that connect between bind and token find no token, or a stale one, and
// retry: startDaemon polls, and a client sees ErrNotRunning.
func listenThenPublish(home, tokenPath, token string, listen func(string) (net.Listener, error), write func(string, []byte) error) (net.Listener, error) {
	ln, err := listen(home)
	if err != nil {
		return nil, err
	}
	if err := writeToken(tokenPath, token, write); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}
