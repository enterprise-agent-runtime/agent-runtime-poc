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
		t.Fatalf("content %q owner-only %v %v", b, ok, err)
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
