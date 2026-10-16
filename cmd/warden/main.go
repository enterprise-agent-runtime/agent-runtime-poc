// Command warden is the Warden CLI (WRD-16 §14). It is a client of the
// runtime API only (INV-F): every command maps to JSON-RPC methods of
// wardend, which it starts when absent (WRD-16 §5.1).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"warden.dev/warden/internal/api"
	"warden.dev/warden/internal/buildinfo"
	"warden.dev/warden/internal/platform"
	"warden.dev/warden/internal/termsafe"
)

// exitError carries a process exit code.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

var asJSON bool

func main() {
	root := &cobra.Command{Use: "warden", Short: "Warden: run AI agents inside a sandbox, under policy, with an audit trail", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "print raw JSON results")
	root.AddCommand(versionCmd(), doctorCmd(), daemonCmd(), unlockCmd(), providerCmd(), harnessCmd(), modelsCmd(), auditCmd(), eventsCmd())
	if err := root.Execute(); err != nil {
		var ee exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "warden:", describe(err))
		os.Exit(2)
	}
}

// describe renders API errors with their reason, sanitized for the
// terminal: an error message can carry a model endpoint's or the Docker
// engine's text (CWE-150, M2-3).
func describe(err error) string { return termsafe.SanitizeDetail(describeRaw(err)) }

func describeRaw(err error) string {
	if code, d, ok := api.DataOf(err); ok {
		msg := err.Error()
		if i := strings.Index(msg, "] "); i >= 0 {
			msg = msg[i+2:]
		}
		if d.Reason != "" {
			return fmt.Sprintf("%s (%s/%s, %d)", msg, d.Code, d.Reason, code)
		}
		return fmt.Sprintf("%s (%s, %d)", msg, d.Code, code)
	}
	return err.Error()
}

func layout() (platform.Layout, error) {
	h, err := platform.Home()
	return platform.Layout{Home: h}, err
}

// connect dials the daemon, starting it first when autostart is set.
func connect(ctx context.Context, autostart bool) (*api.Client, error) {
	l, err := layout()
	if err != nil {
		return nil, err
	}
	return connectTo(ctx, l, autostart, startDaemon)
}

// connectTo is connect for a layout, with the daemon starter injected.
func connectTo(ctx context.Context, l platform.Layout, autostart bool, start func(context.Context, platform.Layout) error) (*api.Client, error) {
	return connectWith(ctx, l, autostart, dialCLI, start)
}

func dialCLI(ctx context.Context, l platform.Layout) (*api.Client, error) {
	return api.Dial(ctx, l, "warden-cli", buildinfo.Version, nil)
}

// connectWith is connectTo with the dialer injected too.
func connectWith(ctx context.Context, l platform.Layout, autostart bool, dial func(context.Context, platform.Layout) (*api.Client, error), start func(context.Context, platform.Layout) error) (*api.Client, error) {
	c, err := dial(ctx, l)
	// Start a daemon only when none answers: any other error (a refused
	// token file, an endpoint served by another user) must reach the user
	// unchanged, and starting a second wardend would replace the running
	// one's token (WH-1+2).
	if err == nil || !autostart || !errors.Is(err, api.ErrNotRunning) {
		return c, err
	}
	if err := start(ctx, l); err != nil {
		return nil, err
	}
	return dial(ctx, l)
}

// wardendPath finds wardend next to this executable.
func wardendPath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	p := filepath.Join(filepath.Dir(self), platform.ExeName("wardend"))
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("wardend not found next to warden (%s); build both with make build", p)
	}
	return p, nil
}

func startDaemon(ctx context.Context, l platform.Layout) error {
	exe, err := wardendPath()
	if err != nil {
		return err
	}
	if err := l.Ensure(); err != nil {
		return err
	}
	_ = os.Remove(l.Token())
	pid, err := platform.SpawnDaemon(exe, nil, filepath.Join(l.Logs(), "wardend.log"), nil)
	if err != nil {
		return fmt.Errorf("start wardend: %w", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := api.Dial(ctx, l, "warden-cli", buildinfo.Version, nil); err == nil {
			c.Close()
			fmt.Fprintf(os.Stderr, "started wardend (pid %d)\n", pid)
			return nil
		}
		if !platform.Alive(pid) {
			return fmt.Errorf("wardend exited during start; see %s", filepath.Join(l.Logs(), "wardend.log"))
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("wardend did not come up within 15 s; see %s", filepath.Join(l.Logs(), "wardend.log"))
}

// do connects (starting the daemon if needed), calls one method and
// prints the result as JSON when --json is set.
func do(cmd *cobra.Command, method string, params any, out any) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 6*time.Minute)
	defer cancel()
	c, err := connect(ctx, true)
	if err != nil {
		return err
	}
	defer c.Close()
	var raw json.RawMessage
	if err := c.Call(ctx, method, params, &raw); err != nil {
		return err
	}
	if asJSON {
		printJSON(os.Stdout, raw)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// printJSON is the --json output of every command. encoding/json escapes C0
// controls but writes DEL, C1 controls and bidi characters raw, and results
// carry text the daemon did not write (Docker engine answers, provider
// errors, tool output), so the bytes pass termsafe.EscapeJSON before they
// reach the terminal (CWE-150, M2-3 review).
func printJSON(w io.Writer, raw json.RawMessage) {
	var v any
	_ = json.Unmarshal(raw, &v)
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(w, string(termsafe.EscapeJSON(b)))
}

func versionCmd() *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Show CLI and daemon versions", RunE: func(cmd *cobra.Command, _ []string) error {
		fmt.Printf("warden %s (%s) %s %s/%s\n", buildinfo.Version, buildinfo.Commit, buildinfo.GoVersion(), platform.OS(), platform.Arch())
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		c, err := connect(ctx, false)
		if err != nil {
			fmt.Println("wardend: not running")
			return nil
		}
		defer c.Close()
		var v map[string]any
		if err := c.Call(ctx, "system.version", nil, &v); err != nil {
			return err
		}
		fmt.Printf("wardend %v (%v) protocol %v\n", v["version"], v["commit"], v["protocol"])
		return nil
	}}
}

type doctorCheck struct {
	ID       string  `json:"id"`
	Status   string  `json:"status"`
	Title    string  `json:"title"`
	Detail   string  `json:"detail"`
	FixHint  *string `json:"fix_hint"`
	Blocking bool    `json:"blocking"`
}

type doctorResult struct {
	Status   string        `json:"status"`
	Blocking bool          `json:"blocking"`
	Checks   []doctorCheck `json:"checks"`
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{Use: "doctor", Short: "Check sandbox, keychain, store, providers and git (exit 1 on a blocking failure)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var r doctorResult
			if err := do(cmd, "system.doctor", nil, &r); err != nil {
				return err
			}
			if !asJSON {
				renderDoctor(os.Stdout, r)
			}
			if r.Blocking {
				return exitError{1}
			}
			return nil
		}}
}

// renderDoctor prints the human form of system.doctor. Detail and FixHint
// carry text the daemon did not write (Docker engine fields, error text from
// whatever answers on the Docker endpoint), so they are sanitized before
// reaching the terminal (CWE-150, M2-3).
func renderDoctor(w io.Writer, r doctorResult) {
	for _, c := range r.Checks {
		mark := map[string]string{"ok": "ok  ", "warn": "WARN", "fail": "FAIL"}[c.Status]
		block := ""
		if c.Blocking && c.Status == "fail" {
			block = " [blocking]"
		}
		fmt.Fprintf(w, "%s  %-17s %s: %s%s\n", mark, c.ID, c.Title, termsafe.SanitizeDetail(c.Detail), block)
		if c.FixHint != nil && c.Status != "ok" {
			fmt.Fprintf(w, "      fix: %s\n", termsafe.SanitizeDetail(*c.FixHint))
		}
	}
	fmt.Fprintf(w, "\noverall: %s%s\n", r.Status, map[bool]string{true: " (blocking: no task can start)", false: ""}[r.Blocking])
}

// daemonStopTimeout bounds how long "warden daemon stop" waits for wardend
// to exit after system.shutdown (M2-1). wardend gives its server 5 s to
// drain, so this leaves room for the store and log to close.
const daemonStopTimeout = 10 * time.Second

// procWatch is what stop needs from platform.Watch.
type procWatch interface {
	PID() int
	Wait(time.Duration) error
	Close()
}

// stopAndWait is the decision of "warden daemon stop" after it has
// connected (M2-1). It opens the watch first, because wardend removes its
// pid file while shutting down and on Windows the handle taken now is immune
// to pid reuse. A watch that cannot be opened is a warning: the daemon is
// still shut down and stop exits 0 without waiting. A daemon that does not
// exit within timeout is exit 1, because it may still hold files under home.
func stopAndWait(open func() (procWatch, error), shutdown func() error, timeout time.Duration, stdout, stderr io.Writer, home string) error {
	w, err := open()
	if err != nil {
		fmt.Fprintf(stderr, "warden: %v; not waiting for wardend to exit\n", err)
		w = nil
	} else {
		defer w.Close()
	}
	if err := shutdown(); err != nil {
		return err
	}
	if w == nil {
		fmt.Fprintln(stdout, "wardend stopping")
		return nil
	}
	if err := w.Wait(timeout); err != nil {
		fmt.Fprintf(stderr, "warden: wardend (pid %d) did not exit within %s of system.shutdown; it may still hold files under %s\n", w.PID(), timeout, home)
		return exitError{1}
	}
	fmt.Fprintln(stdout, "wardend stopped")
	return nil
}

func daemonCmd() *cobra.Command {
	d := &cobra.Command{Use: "daemon", Short: "Start, stop or inspect wardend"}
	d.AddCommand(&cobra.Command{Use: "start", Short: "Start wardend in the background", RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
		defer cancel()
		if c, err := connect(ctx, false); err == nil {
			c.Close()
			fmt.Println("wardend is already running")
			return nil
		}
		l, err := layout()
		if err != nil {
			return err
		}
		return startDaemon(ctx, l)
	}})
	d.AddCommand(&cobra.Command{Use: "stop", Short: "Stop wardend (system.shutdown) and wait until it has exited", RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()
		c, err := connect(ctx, false)
		if err != nil {
			fmt.Println("wardend is not running")
			return nil
		}
		defer c.Close()
		l, _ := layout()
		open := func() (procWatch, error) {
			pid, err := l.DaemonPID()
			if err != nil {
				return nil, fmt.Errorf("cannot read the wardend pid: %w", err)
			}
			w, err := platform.WatchProcess(pid)
			if err != nil {
				return nil, fmt.Errorf("cannot watch wardend: %w", err)
			}
			return w, nil
		}
		shutdown := func() error {
			if err := c.Call(ctx, "system.shutdown", map[string]any{"confirm": true}, nil); err != nil {
				return err
			}
			return c.Close() // do not hold a connection open while the server drains
		}
		return stopAndWait(open, shutdown, daemonStopTimeout, os.Stdout, os.Stderr, l.Home)
	}})
	d.AddCommand(&cobra.Command{Use: "status", Short: "Report whether wardend is running (exit 1 if not)", RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		l, _ := layout()
		c, err := connect(ctx, false)
		if err != nil {
			fmt.Printf("wardend is not running (%s)\n", platform.Endpoint(l.Home))
			return exitError{1}
		}
		defer c.Close()
		fmt.Printf("wardend %v running on %s (mode %v, features %v)\n", c.Hello["daemon_version"], platform.Endpoint(l.Home), c.Hello["mode"], c.Hello["features"])
		return nil
	}})
	return d
}

func unlockCmd() *cobra.Command {
	return &cobra.Command{Use: "unlock", Short: "Unlock the encrypted-file secrets backend for this daemon run", RunE: func(cmd *cobra.Command, _ []string) error {
		fmt.Fprint(os.Stderr, "Secrets passphrase (not echoed; at least 12 characters when creating): ")
		pass, err := platform.ReadSecretLine(os.Stdin)
		if err != nil {
			return err
		}
		var r map[string]any
		if err := do(cmd, "secrets.unlock", map[string]any{"passphrase": string(pass)}, &r); err != nil {
			return err
		}
		if !asJSON {
			if r["created"] == true {
				fmt.Println("created and unlocked the encrypted secrets file")
			} else {
				fmt.Println("unlocked")
			}
		}
		return nil
	}}
}
