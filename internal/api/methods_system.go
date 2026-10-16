package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/creachadair/jrpc2"

	"warden.dev/warden/internal/config"
	"warden.dev/warden/internal/platform"
	"warden.dev/warden/internal/sandbox"
	"warden.dev/warden/internal/secrets"
	"warden.dev/warden/internal/store"
	"warden.dev/warden/internal/worktree"
)

func (s *Server) version(context.Context, *conn, *jrpc2.Request, json.RawMessage) (any, error) {
	commit := s.d.Commit
	if len(commit) < 7 {
		commit = "0000000"
	}
	return map[string]any{"version": s.d.Version, "commit": commit, "go_version": runtime.Version(), "protocol": Protocol,
		"os": platform.OS(), "arch": runtime.GOARCH, "build_tags": []string{}, "schema_version": s.d.Store.SchemaVersion()}, nil
}

func (s *Server) shutdown(context.Context, *conn, *jrpc2.Request, json.RawMessage) (any, error) {
	if s.d.Shutdown != nil {
		go func() {
			time.Sleep(100 * time.Millisecond) // let the response go out first
			s.d.Shutdown()
		}()
	}
	return map[string]any{"ok": true, "interrupted_task_ids": []string{}}, nil
}

// unlock is secrets.unlock (NEW, DECISIONS-poc D-010): opens the
// encrypted-file secrets backend for the daemon's lifetime.
func (s *Server) unlock(ctx context.Context, _ *conn, _ *jrpc2.Request, raw json.RawMessage) (any, error) {
	var p struct {
		Passphrase string `json:"passphrase"`
	}
	_ = json.Unmarshal(raw, &p)
	if s.d.File == nil {
		return nil, Fail(CodeInvalidState, "not_file_backend", "the secrets backend is the OS keychain ("+s.d.Broker.Backend().Name()+"); there is nothing to unlock")
	}
	created := !s.d.File.Exists()
	if err := s.d.File.Unlock([]byte(p.Passphrase)); err != nil {
		if errors.Is(err, secrets.ErrLocked) {
			return nil, Fail(CodeInvalidState, "bad_passphrase", "wrong passphrase")
		}
		return nil, Fail(CodeInvalidParams, "passphrase", err.Error())
	}
	if s.d.OnUnlock != nil {
		if err := s.d.OnUnlock(ctx); err != nil {
			return nil, Fail(CodeInternal, "checkpoint_key", "unlocked, but the checkpoint key could not be loaded: "+err.Error())
		}
	}
	return map[string]any{"ok": true, "backend": "file", "created": created}, nil
}

func check(id, group, status, title, detail string, blocking bool, fix string) sandbox.Check {
	c := sandbox.Check{ID: id, Group: group, Status: status, Title: title, Detail: detail, Blocking: blocking}
	if fix != "" {
		c.FixHint = &fix
	}
	return c
}

// doctor is system.doctor (A05 §8.1): prerequisites reported honestly; a
// blocking failure means no task may start (ST-2), with no unsandboxed
// fallback.
func (s *Server) doctor(ctx context.Context, _ *conn, _ *jrpc2.Request, _ json.RawMessage) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	probe := s.d.SandboxProbe
	if probe == nil {
		probe = sandbox.Probe
	}
	checks := probe(ctx, sandbox.Options{DefaultLevel: s.d.DefaultLevel})
	checks = append(checks, check("exec.binary", "sandbox", "warn", "Sandbox executor (warden-exec)",
		"warden-exec and the sandbox launch land in milestone M2; no task can run yet", false, "Nothing to do in M1."))

	// keychain
	st := s.d.Broker.Status(ctx)
	needs := false
	s.catMu.Lock()
	for _, p := range s.d.Catalog.Providers {
		if p.Auth.Secret != "" {
			needs = true
		}
	}
	s.catMu.Unlock()
	kc := check("keychain", "keychain", "ok", "Secrets backend ("+st.Backend+")", st.Detail, needs, "")
	kc.Data = map[string]any{"backend": st.Backend, "locked": st.Locked}
	if !st.OK {
		kc.Status = "warn"
		if needs {
			kc.Status = "fail"
		}
		switch {
		case st.Locked && st.Backend == "file":
			kc.FixHint = strPtr("Run warden unlock and enter the secrets passphrase (it is never stored).")
		case st.Locked:
			kc.FixHint = strPtr("Unlock the OS keychain, then run warden doctor again.")
		default:
			kc.FixHint = strPtr("Linux: install and start GNOME Keyring (or use the encrypted file backend); macOS: unlock the login keychain.")
		}
	}
	checks = append(checks, kc)
	ck := check("checkpoint.key", "keychain", "ok", "Checkpoint signing key", "", false, "")
	if id := s.signerKeyID(); id != "" {
		ck.Detail = "key " + id + " loaded; checkpoints are signed"
	} else {
		ck.Status, ck.Detail = "warn", "no signing key loaded; checkpoints are written unsigned"
		ck.FixHint = strPtr("Unlock the secrets backend; the key is created on first use.")
	}
	checks = append(checks, ck)

	// store
	checks = append(checks, check("store", "store", "ok", "Event store", fmt.Sprintf("schema v%d, store %s, head seq %d", s.d.Store.SchemaVersion(), s.d.Store.StoreID(), s.d.Store.HeadSeq()), true, ""))

	// disk
	dk := check("disk", "disk", "ok", "Free disk space", "", true, "")
	if free, err := platform.FreeBytes(s.d.Layout.Home); err != nil {
		dk.Status, dk.Detail = "warn", "could not read free space: "+err.Error()
		dk.Blocking = false
	} else {
		dk.Detail = fmt.Sprintf("%d MiB free on the Warden home volume", free>>20)
		dk.Data = map[string]any{"free_bytes": free}
		switch {
		case free < 128<<20:
			dk.Status, dk.FixHint = "fail", strPtr("Free space, or remove old sessions.")
		case free < 512<<20:
			dk.Status, dk.Blocking, dk.FixHint = "warn", false, strPtr("Free space, or remove old sessions.")
		default:
			dk.Blocking = false
		}
	}
	checks = append(checks, dk)

	// catalog and providers
	cat := check("catalog", "providers", "ok", "Model catalog (models.yaml)", "", true, "")
	if _, err := config.LoadCatalog(s.d.CatalogPath); err != nil {
		cat.Status, cat.Detail, cat.FixHint = "fail", err.Error(), strPtr("Fix the entry or re-add the provider.")
	} else {
		cat.Detail = s.d.CatalogPath + " parses and validates"
	}
	checks = append(checks, cat)
	s.catMu.Lock()
	np, nh := len(s.d.Catalog.Providers), 0
	for _, h := range s.d.Catalog.Harnesses {
		if h.Enabled {
			nh++
		}
	}
	s.catMu.Unlock()
	pv := check("providers", "providers", "ok", "Configured model access", fmt.Sprintf("%d providers, %d enabled harnesses", np, nh), false, "")
	if np+nh == 0 {
		pv.Status, pv.FixHint = "warn", strPtr("Add one: warden provider add anthropic --api-key, warden provider add ollama, or a company endpoint.")
	}
	checks = append(checks, pv)
	local := s.d.LocalServers
	if local == nil {
		local = probeLocalServers
	}
	found := local(ctx)
	lc := check("providers.local", "providers", "ok", "Local model servers", "", false, "")
	if len(found) == 0 {
		lc.Status, lc.Detail = "warn", "no Ollama (127.0.0.1:11434) or LM Studio (127.0.0.1:1234) server answered"
		lc.FixHint = strPtr("Start Ollama or LM Studio; optional.")
	} else {
		lc.Detail = fmt.Sprintf("found %v", found)
	}
	lc.Data = map[string]any{"found": found}
	checks = append(checks, lc)

	// git
	gv := s.d.GitVersion
	if gv == nil {
		gv = worktree.GitVersion
	}
	g := check("git", "git", "ok", "Host git", "", true, "")
	if v, err := gv(ctx); err != nil {
		g.Status, g.Detail, g.FixHint = "fail", "git not found: "+err.Error(), strPtr("Install git "+worktree.MinGit+" or later.")
	} else if !worktree.AtLeast(v, worktree.MinGit) {
		g.Status, g.Detail, g.FixHint = "fail", "git "+v+" is older than "+worktree.MinGit, strPtr("Update git.")
	} else {
		g.Detail = "git " + v
	}
	checks = append(checks, g)

	worst, blocking := "ok", false
	rank := map[string]int{"ok": 0, "warn": 1, "fail": 2}
	for _, c := range checks {
		if rank[c.Status] > rank[worst] {
			worst = c.Status
		}
		if c.Status == "fail" && c.Blocking {
			blocking = true
		}
	}
	return map[string]any{"status": worst, "blocking": blocking, "checks": checks}, nil
}

func strPtr(s string) *string { return &s }

func (s *Server) signerKeyID() string {
	if s.d.SignerKeyID == nil {
		return ""
	}
	return s.d.SignerKeyID()
}

// probeLocalServers checks the default Ollama and LM Studio endpoints on
// loopback (A05 doctor check providers.local).
func probeLocalServers(ctx context.Context) []string {
	var found []string
	hc := &http.Client{Timeout: 2 * time.Second}
	for _, u := range []string{"http://127.0.0.1:11434/v1/models", "http://127.0.0.1:1234/v1/models"} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if resp, err := hc.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				found = append(found, u[:len(u)-len("/v1/models")])
			}
		}
	}
	if found == nil {
		found = []string{}
	}
	return found
}

// RuntimeStart appends runtime.start on the sys chain (A05 §3.4).
func (s *Server) RuntimeStart(ctx context.Context) error {
	var keyID any
	if id := s.signerKeyID(); id != "" {
		keyID = id
	}
	_, err := s.Append(ctx, store.Input{Type: "runtime.start", Chain: store.SysChain, Actor: s.runtimeActor(),
		Payload: map[string]any{"version": s.d.Version, "pid": os.Getpid(), "mode": s.d.Mode, "checkpoint_key_id": keyID,
			"secret_backend": s.d.Broker.Backend().Name(), "default_sandbox_level": s.d.DefaultLevel}})
	return err
}

// RuntimeStop appends runtime.stop on the sys chain.
func (s *Server) RuntimeStop(ctx context.Context, reason string) error {
	_, err := s.Append(ctx, store.Input{Type: "runtime.stop", Chain: store.SysChain, Actor: s.runtimeActor(),
		Payload: map[string]any{"version": s.d.Version, "pid": os.Getpid(), "mode": s.d.Mode, "reason": reason}})
	return err
}

// SecretAccess records a secret.access event (secrets.Emitter).
func (s *Server) SecretAccess(ctx context.Context, a secrets.Access) {
	_, _ = s.Append(ctx, store.Input{Type: "secret.access", Chain: store.SysChain, Actor: store.Runtime("secrets", s.d.Version), Payload: a})
}
