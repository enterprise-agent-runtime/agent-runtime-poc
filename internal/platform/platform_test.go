package platform

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tempHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv(EnvHome, h)
	return h
}

func TestHome_OverrideAndLayout(t *testing.T) {
	h := tempHome(t)
	got, err := Home()
	if err != nil || got != filepath.Clean(h) {
		t.Fatalf("Home = %q %v", got, err)
	}
	l := Layout{Home: got}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{l.Run(), l.DB(), l.Blobs(), l.Keys(), l.Exports(), l.Logs(), l.Cache()} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("%s missing", d)
		}
	}
	if filepath.Dir(l.Token()) != l.Run() || filepath.Base(l.Models()) != "models.yaml" {
		t.Error("layout paths")
	}
	t.Setenv(EnvHome, "relative/home")
	if _, err := Home(); err == nil {
		t.Fatal("relative WARDEN_HOME accepted")
	}
	t.Setenv(EnvHome, "")
	if def, err := Home(); err != nil || !(strings.HasSuffix(def, ".warden") || strings.HasSuffix(def, "Warden")) {
		t.Fatalf("default home = %q %v", def, err)
	}
}

// TestTransport_RoundTripAndSingleDaemon runs on every CI OS: the Unix
// socket on Linux/macOS, the named pipe on Windows (M1 "platform transport
// tests on 3 OSes").
func TestTransport_RoundTripAndSingleDaemon(t *testing.T) {
	h := tempHome(t)
	l, err := Listen(h)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan error, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		line, err := bufio.NewReader(c).ReadString('\n')
		if err == nil {
			_, err = c.Write([]byte("echo:" + line))
		}
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("hello\n"))
	reply, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || reply != "echo:hello\n" {
		t.Fatalf("reply = %q %v", reply, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// A second daemon for the same home is refused.
	if _, err := Listen(h); !errors.Is(err, ErrDaemonRunning) {
		t.Fatalf("second listen: %v", err)
	}
	if !strings.Contains(Endpoint(h), "warden") {
		t.Fatalf("endpoint = %s", Endpoint(h))
	}
}

func TestDial_NoDaemon(t *testing.T) {
	h := tempHome(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := Dial(ctx, h); err == nil {
		t.Fatal("dial without a daemon succeeded")
	}
}

// TestWriteOwnerOnly prints what was read back on failure: on Windows the
// SDDL of the DACL, so a CI failure is diagnosable from the log alone.
func TestWriteOwnerOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run", "token")
	if err := WriteOwnerOnly(p, []byte("tok")); err != nil {
		t.Fatal(err)
	}
	if err := WriteOwnerOnly(p, []byte("tok2")); err != nil { // replace
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	ok, err := OwnerOnly(p)
	if string(b) != "tok2" || err != nil || !ok {
		t.Fatalf("content %q owner-only %v %v; read back %s", b, ok, err, ownerOnlyDetail(p))
	}
}

// TestOwnerOnly_MissingFileIsError: a file that cannot be read is an error,
// never a quiet "not owner-only", so a caller cannot mistake a vanished or
// unreadable token for one with loose permissions (mutation audit of FX-3:
// on Windows ownerOnlyFor could swallow the read error with every test green).
func TestOwnerOnly_MissingFileIsError(t *testing.T) {
	ok, err := OwnerOnly(filepath.Join(t.TempDir(), "missing"))
	if err == nil || ok {
		t.Fatalf("OwnerOnly(missing) = %v, %v; want false and an error", ok, err)
	}
}

func TestReadSecretLine_NonTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString("s3cret-value\r\n")
	w.Close()
	got, err := ReadSecretLine(r)
	if err != nil || string(got) != "s3cret-value" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestUser(t *testing.T) {
	if u := Username(); u == "" || strings.ContainsAny(u, `\/`) {
		t.Fatalf("username %q", u)
	}
	if !strings.HasPrefix(LocalUser(), "local:") || OS() == "" {
		t.Fatal("local user / os")
	}
	if !Alive(os.Getpid()) || Alive(-1) {
		t.Fatal("Alive")
	}
}

// TestWriteOwnerOnly_OldTempNamePlanted guards WH-1+2 (a): the temp file was
// always path+".tmp", which another account able to write the directory
// could pre-create (owning it on Windows, so keeping WRITE_DAC). Something
// planted at the old name no longer affects the write; a directory makes
// the old code fail outright on every OS.
func TestWriteOwnerOnly_OldTempNamePlanted(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	if err := os.Mkdir(p+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteOwnerOnly(p, []byte("tok")); err != nil {
		t.Fatalf("WriteOwnerOnly with %s.tmp planted: %v", p, err)
	}
	if ok, err := OwnerOnly(p); err != nil || !ok {
		t.Fatalf("owner-only %v %v; read back %s", ok, err, ownerOnlyDetail(p))
	}
}

// TestWriteOwnerOnly_TempNameRandomAndExclusive guards WH-1+2 (a): the temp
// name is unpredictable (two writes use different names, never the old
// path+".tmp"), lives next to the target so the rename stays on one volume,
// is gone afterwards, and an existing file at the chosen name is refused
// (O_EXCL / CREATE_NEW), never truncated and reused.
func TestWriteOwnerOnly_TempNameRandomAndExclusive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	var names []string
	h := writeHooks{afterCreate: func(tmp string) error { names = append(names, tmp); return nil }}
	for i := 0; i < 2; i++ {
		if err := writeOwnerOnly(p, []byte("tok"), h); err != nil {
			t.Fatal(err)
		}
	}
	if len(names) != 2 || names[0] == names[1] {
		t.Fatalf("temp names %q: want two different names", names)
	}
	for _, n := range names {
		if n == p+".tmp" || filepath.Dir(n) != filepath.Dir(p) || !strings.HasPrefix(filepath.Base(n), "token.") {
			t.Errorf("temp name %s: want a random name next to %s", n, p)
		}
		if _, err := os.Lstat(n); !os.IsNotExist(err) {
			t.Errorf("temp file %s left behind (%v)", n, err)
		}
	}

	planted := filepath.Join(filepath.Dir(p), "planted")
	if err := os.WriteFile(planted, []byte("theirs"), 0o600); err != nil {
		t.Fatal(err)
	}
	q := filepath.Join(filepath.Dir(p), "other")
	if err := writeOwnerOnly(q, []byte("tok"), writeHooks{tempName: func(string) string { return planted }}); err == nil {
		t.Fatal("write through an existing temp file succeeded")
	}
	if b, _ := os.ReadFile(planted); string(b) != "theirs" {
		t.Errorf("planted file rewritten: %q", b)
	}
	if _, err := os.Lstat(q); !os.IsNotExist(err) {
		t.Errorf("%s created through a planted temp file (%v)", q, err)
	}
}

// TestEnsure_RefusesForeignDirectory guards the review of c8f81ca (WH-1+2):
// Ensure replaced the owner and DACL of whatever existing directory
// WARDEN_HOME named (D:\work, say). An existing directory that is not empty,
// not the default home and has no .warden-home marker is refused, and its
// permissions are left exactly as they were. Ensure leaves the marker,
// owner-only, in every home it creates or accepts.
func TestEnsure_RefusesForeignDirectory(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := ownerOnlyDetail(work)
	err := Layout{Home: work}.Ensure()
	if err == nil || !strings.Contains(err.Error(), "is not a Warden home") {
		t.Fatalf("Ensure on a foreign directory = %v; want a refusal", err)
	}
	if after := ownerOnlyDetail(work); after != before {
		t.Fatalf("foreign directory re-permissioned:\nbefore %s\nafter  %s", before, after)
	}
	if _, err := os.Lstat(filepath.Join(work, "run")); !os.IsNotExist(err) {
		t.Fatalf("run created in a refused home (%v)", err)
	}

	home := filepath.Join(t.TempDir(), "home")
	if err := (Layout{Home: home}).Ensure(); err != nil {
		t.Fatal(err)
	}
	if ok, err := OwnerOnly(filepath.Join(home, HomeMarker)); err != nil || !ok {
		t.Fatalf("marker owner-only %v %v", ok, err)
	}
	if err := (Layout{Home: home}).Ensure(); err != nil { // accepted again by its marker
		t.Fatalf("second Ensure: %v", err)
	}
}

// TestWriteOwnerOnly_FailedWriteLeavesNothing: when the write fails after the
// temp file exists, neither a temp file nor the target is left behind, so
// no half-written token file can be found and no *.tmp accumulates (WH-1+2
// review of c8f81ca).
func TestWriteOwnerOnly_FailedWriteLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "token")
	errHook := errors.New("injected")
	err := writeOwnerOnly(p, []byte("tok"), writeHooks{afterCreate: func(string) error { return errHook }})
	if !errors.Is(err, errHook) {
		t.Fatalf("writeOwnerOnly = %v; want the injected error", err)
	}
	left, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil || len(left) != 0 {
		t.Fatalf("left behind: %v %v", left, err)
	}
}

// TestEnsure_LooseMarkerIsNoProof guards WH-1+2 (re-review of 330a5ed): a
// .warden-home that others can write proves nothing, since anyone who can
// write the directory could have planted it. The marker counts only if it
// passes ReadOwnerOnly; otherwise the directory is refused like any other.
func TestEnsure_LooseMarkerIsNoProof(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, HomeMarker)
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(marker, 0o644); err != nil { // past the umask; on Windows the DACL is inherited
		t.Fatal(err)
	}
	before := ownerOnlyDetail(home)
	if err := (Layout{Home: home}).Ensure(); err == nil || !strings.Contains(err.Error(), "is not a Warden home") {
		t.Fatalf("Ensure with a loose marker = %v; want a refusal", err)
	}
	if after := ownerOnlyDetail(home); after != before {
		t.Fatalf("home re-permissioned:\nbefore %s\nafter  %s", before, after)
	}
}

// wantLinkRefusal is the wording ensureDir's link refusal must carry for
// any managed directory (WH-4).
const wantLinkRefusal = "Warden's directories must be real directories"
