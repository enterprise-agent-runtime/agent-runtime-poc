package sandbox

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The samples in testdata/engine are GET /version and GET /info answers of
// the engines L2 meets, written from the Engine API reference and the
// output of `docker version` / `docker info` (and Podman's compat API) of
// the versions named in each file, reduced to the fields the gate reads
// plus their neighbours. They are not captures: no engine runs on the
// machine this was written on, and none may be started for a unit test.

type engineSample struct {
	Version json.RawMessage `json:"version"`
	Info    json.RawMessage `json:"info"`
}

func readSample(t *testing.T, name string) engineSample {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "engine", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var s engineSample
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return s
}

func loadSample(t *testing.T, name string) EngineFacts {
	t.Helper()
	s := readSample(t, name)
	f, err := parseEngineFacts(s.Version, s.Info)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return f
}

// TestDecideEngine_Samples is the gate's verdict for every engine L2 can
// meet (CWE-636: the gate, not doctor, refuses). Rootless Docker is
// accepted only on a Linux host, where container uid 0 is the host user
// (D-071); Docker Desktop is accepted on every host although its engine
// is rootful in its VM (C-03); a rootful native engine, an engine serving
// Windows containers and Podman are refused, each with its own code.
func TestDecideEngine_Samples(t *testing.T) {
	cases := []struct {
		sample, host string
		kind         EngineKind
		code         string
	}{
		{"rootless-linux", "linux", EngineRootless, ""},
		// A rootless engine reached from Windows or macOS runs in a VM we
		// cannot identify (Lima, Colima, a WSL distribution): its uid 0 is
		// not the host user, so the 0:0 user of D-071 would be wrong.
		{"rootless-linux", "windows", "", EngineUnknownCode},
		{"rootless-linux", "darwin", "", EngineUnknownCode},
		{"desktop-windows", "windows", EngineDesktop, ""},
		{"desktop-macos", "darwin", EngineDesktop, ""},
		{"desktop-macos", "linux", EngineDesktop, ""}, // Docker Desktop for Linux reports the same
		{"rootful-linux", "linux", "", EngineRootfulCode},
		{"rootful-linux", "windows", "", EngineRootfulCode},
		{"desktop-windows-containers", "windows", "", EngineOSCode},
		{"podman-rootless", "linux", "", EnginePodmanCode},
	}
	for _, c := range cases {
		t.Run(c.sample+"/"+c.host, func(t *testing.T) {
			v := decideEngine(c.host, loadSample(t, c.sample))
			if v.Kind != c.kind || v.Code != c.code {
				t.Fatalf("verdict = {kind %q code %q reason %q}, want {kind %q code %q}", v.Kind, v.Code, v.Reason, c.kind, c.code)
			}
			if c.code == "" {
				if v.Err() != nil {
					t.Errorf("accepted verdict has error %v", v.Err())
				}
				if v.Version == "" || v.APIVersion == "" || v.OSType != "linux" {
					t.Errorf("accepted verdict lacks what it saw: %+v", v)
				}
				return
			}
			err := v.Err()
			var ee *EngineError
			if !errors.Is(err, ErrUnavailable) || !errors.As(err, &ee) || ee.Code != c.code {
				t.Fatalf("Err() = %v, want sandbox_unavailable with code %s", err, c.code)
			}
			if !strings.Contains(err.Error(), "sandbox_unavailable: "+c.code+": ") {
				t.Errorf("error %q does not read sandbox_unavailable: %s: ...", err, c.code)
			}
			if v.Reason == "" {
				t.Error("refusal without a reason")
			}
		})
	}
}

// withVersion is a sample with the engine version replaced in both
// answers, as an engine of that version would report it.
func withVersion(f EngineFacts, v string) EngineFacts {
	f.Version, f.ServerVersion = v, v
	return f
}

// TestDecideEngine_VersionBoundaries guards CVE-2024-29018: before Moby
// 26.0.0, 25.0.5 and 23.0.11, the embedded DNS resolver of a container on
// an --internal network forwarded external names to the host's resolvers,
// a DNS exfiltration channel out of the network that is meant to have no
// way out (C-03). Each boundary is tested on both sides, so a boundary off
// by one is caught; pre-releases of a fixed version are older than it.
// Docker Desktop bundles Moby and is held to the same versions.
func TestDecideEngine_VersionBoundaries(t *testing.T) {
	cases := []struct {
		version string
		code    string
	}{
		{"26.0.0", ""},
		{"26.0.1", ""},
		{"26.1.4", ""},
		{"27.3.1", ""},
		{"28.0.0-rc.1", ""},
		{"100.0.0", ""},
		{"27.0.1+dfsg-1ubuntu1", ""}, // build metadata is not a pre-release
		{"26.0.0+dfsg1", ""},
		{"26.0.0-rc.1", EngineVersionCode},
		{"26.0.0-beta.2", EngineVersionCode},
		{"25.0.5", ""},
		{"25.0.6", ""},
		{"25.0.4", EngineVersionCode},
		{"25.0.5-rc.1", EngineVersionCode},
		{"25.0.0", EngineVersionCode},
		// No 25.1 was released; anything on 25 but the patched 25.0 line is
		// unknown to the advisory and refused.
		{"25.1.0", EngineVersionCode},
		{"24.0.9", EngineVersionCode}, // 24.0 never received the fix
		{"24.0.7-0ubuntu4", EngineVersionCode},
		{"23.0.11", ""},
		{"23.0.12", ""},
		{"23.0.10", EngineVersionCode},
		{"23.0.11-rc.1", EngineVersionCode},
		{"23.1.0", EngineVersionCode},
		{"20.10.24", EngineVersionCode},
		{"19.03.15", EngineVersionCode},
		{"0.0.0", EngineVersionCode},
		// Not a version the gate can compare: refused as unclassifiable.
		{"26", EngineUnknownCode},
		{"26.0", EngineUnknownCode},
		{"v26.0.0", EngineUnknownCode},
		{"26.0.0.1", EngineUnknownCode},
		{"26.x.0", EngineUnknownCode},
		{"26..0", EngineUnknownCode},
		{"+26.0.0", EngineUnknownCode},
		{"-26.0.0", EngineUnknownCode},
		{"99999999999999999999.0.0", EngineUnknownCode},
		{"", EngineUnknownCode},
	}
	for _, sample := range []struct{ name, host string }{{"rootless-linux", "linux"}, {"desktop-windows", "windows"}} {
		base := loadSample(t, sample.name)
		for _, c := range cases {
			t.Run(sample.name+"/"+c.version, func(t *testing.T) {
				v := decideEngine(sample.host, withVersion(base, c.version))
				if v.Code != c.code {
					t.Fatalf("code = %q (reason %q), want %q", v.Code, v.Reason, c.code)
				}
				if c.code == "" && v.Kind == "" {
					t.Fatal("accepted without a kind")
				}
				if c.code != "" && v.Kind != "" {
					t.Fatalf("refused with kind %q", v.Kind)
				}
				if c.code == EngineVersionCode && !strings.Contains(v.Reason, "CVE-2024-29018") {
					t.Errorf("reason %q does not name CVE-2024-29018", v.Reason)
				}
			})
		}
	}
}

// TestDecideEngine_FailsClosed: every answer the gate cannot classify with
// certainty is refused, never taken for the nearest kind. The look-alikes
// are what a misconfigured endpoint or a squatter produces; a case-folded
// or padded marker is not the marker.
func TestDecideEngine_FailsClosed(t *testing.T) {
	rootless := loadSample(t, "rootless-linux")
	desktop := loadSample(t, "desktop-windows")
	podman := loadSample(t, "podman-rootless")
	mod := func(f EngineFacts, m func(*EngineFacts)) EngineFacts {
		f.SecurityOptions = append([]string(nil), f.SecurityOptions...)
		f.Components = append([]string(nil), f.Components...)
		m(&f)
		return f
	}
	cases := []struct {
		name  string
		host  string
		facts EngineFacts
		code  string
	}{
		{"rootless marker capitalised", "linux", mod(rootless, func(f *EngineFacts) { f.SecurityOptions = []string{"name=seccomp,profile=builtin", "name=Rootless"} }), EngineRootfulCode},
		{"rootless marker with a suffix", "linux", mod(rootless, func(f *EngineFacts) { f.SecurityOptions = []string{"name=rootlessx"} }), EngineRootfulCode},
		{"rootless as a later key", "linux", mod(rootless, func(f *EngineFacts) { f.SecurityOptions = []string{"name=seccomp,rootless"} }), EngineRootfulCode},
		{"rootless as a profile value", "linux", mod(rootless, func(f *EngineFacts) { f.SecurityOptions = []string{"name=seccomp,profile=rootless"} }), EngineRootfulCode},
		{"rootless padded", "linux", mod(rootless, func(f *EngineFacts) { f.SecurityOptions = []string{" name=rootless"} }), EngineRootfulCode},
		{"no security options", "linux", mod(rootless, func(f *EngineFacts) { f.SecurityOptions = nil }), EngineRootfulCode},
		{"Desktop name lower-case", "windows", mod(desktop, func(f *EngineFacts) { f.OperatingSystem = "docker desktop" }), EngineRootfulCode},
		{"Desktop name padded", "windows", mod(desktop, func(f *EngineFacts) { f.OperatingSystem = "Docker Desktop " }), EngineRootfulCode},
		{"Desktop only in the platform name", "windows", mod(desktop, func(f *EngineFacts) { f.OperatingSystem = "Ubuntu 24.04.1 LTS" }), EngineRootfulCode},
		{"versions disagree", "linux", mod(rootless, func(f *EngineFacts) { f.ServerVersion = "26.0.0" }), EngineUnknownCode},
		{"info without a server version", "linux", mod(rootless, func(f *EngineFacts) { f.ServerVersion = "" }), EngineUnknownCode},
		{"API version missing", "linux", mod(rootless, func(f *EngineFacts) { f.APIVersion = "" }), EngineUnknownCode},
		{"API version malformed", "linux", mod(rootless, func(f *EngineFacts) { f.APIVersion = "1.4x" }), EngineUnknownCode},
		{"API version with escape", "linux", mod(rootless, func(f *EngineFacts) { f.APIVersion = "1.47\x1b[2J" }), EngineUnknownCode},
		{"version with escape", "linux", withVersion(rootless, "27.3.1\x1b]0;x\x07"), EngineUnknownCode},
		{"version of 65 characters", "linux", withVersion(rootless, "27.3.1+"+strings.Repeat("a", 58)), EngineUnknownCode},
		{"OS types disagree", "linux", mod(rootless, func(f *EngineFacts) { f.OSType = "windows" }), EngineOSCode},
		{"OS type missing from info", "linux", mod(rootless, func(f *EngineFacts) { f.OSType = "" }), EngineOSCode},
		{"OS capitalised", "linux", mod(rootless, func(f *EngineFacts) { f.Os, f.OSType = "Linux", "Linux" }), EngineOSCode},
		{"Podman by its component only", "linux", mod(podman, func(f *EngineFacts) { f.Platform, f.OperatingSystem = "", "" }), EnginePodmanCode},
		{"Podman by its platform only", "linux", mod(podman, func(f *EngineFacts) { f.Components = nil; f.Platform = "Podman Desktop" }), EnginePodmanCode},
		{"Podman claiming to be Desktop", "linux", mod(podman, func(f *EngineFacts) { f.OperatingSystem = "Docker Desktop" }), EnginePodmanCode},
		{"nothing at all", "linux", EngineFacts{}, EngineUnknownCode},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := decideEngine(c.host, c.facts)
			if v.Code != c.code || v.Kind != "" {
				t.Fatalf("verdict = {kind %q code %q reason %q}, want code %q", v.Kind, v.Code, v.Reason, c.code)
			}
			for _, r := range v.Reason {
				if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
					t.Fatalf("reason %q contains control character %U", v.Reason, r)
				}
			}
		})
	}
	// The values that must keep the accepted kind, so the look-alikes above
	// are refused for being look-alikes and not for something else.
	if v := decideEngine("linux", rootless); v.Kind != EngineRootless {
		t.Fatalf("baseline rootless: %+v", v)
	}
	if v := decideEngine("windows", desktop); v.Kind != EngineDesktop {
		t.Fatalf("baseline desktop: %+v", v)
	}
	if v := decideEngine("linux", withVersion(rootless, "27.3.1+"+strings.Repeat("a", 57))); v.Kind != EngineRootless {
		t.Fatalf("64-character version: %+v", v)
	}
}

// TestDecideEngine_ReasonNeverEchoesMalformed: whoever answers on the
// endpoint chooses every field; only a version and API version that passed
// their shape checks are shown or kept (CWE-150, M2-3).
func TestDecideEngine_ReasonNeverEchoesMalformed(t *testing.T) {
	rootless := loadSample(t, "rootless-linux")
	for _, bad := range []string{"27\x1b]0;pwn\x07", "27.3.1 pwn", strings.Repeat("9", 70)} {
		v := decideEngine("linux", withVersion(rootless, bad))
		if strings.Contains(v.Reason, "pwn") || strings.Contains(v.Reason, bad) || v.Version != "" {
			t.Errorf("version %q echoed: reason %q version %q", bad, v.Reason, v.Version)
		}
	}
	f := rootless
	f.Os, f.OSType = "linux\x1b[2Jpwn", "linux\x1b[2Jpwn"
	if v := decideEngine("linux", f); strings.Contains(v.Reason, "pwn") || strings.Contains(v.OSType, "pwn") {
		t.Errorf("OS type echoed: %+v", v)
	}
}

// fakeEngineRoutes answers each request on an in-memory connection with
// the raw HTTP response for its path (404 otherwise), the way whatever
// owns the Docker socket or pipe would. No listener, no network (INV-J).
func fakeEngineRoutes(t *testing.T, routes map[string]string) (func(context.Context) (net.Conn, error), *[]string) {
	var seen []string
	return func(ctx context.Context) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			br := bufio.NewReader(server)
			for {
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				seen = append(seen, req.Method+" "+req.URL.Path)
				resp, ok := routes[req.URL.Path]
				if !ok {
					resp = "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n"
				}
				if _, err := server.Write([]byte(resp)); err != nil {
					return
				}
			}
		}()
		return client, nil
	}, &seen
}

func okJSON(body []byte) string {
	return fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
}

// TestFetchEngine_ReadsVersionAndInfo: the facts fetched over the injected
// dialer are those of the two answers, and the gate asks for both.
func TestFetchEngine_ReadsVersionAndInfo(t *testing.T) {
	for _, name := range []string{"rootless-linux", "desktop-windows", "podman-rootless"} {
		t.Run(name, func(t *testing.T) {
			s := readSample(t, name)
			dial, seen := fakeEngineRoutes(t, map[string]string{"/version": okJSON(s.Version), "/info": okJSON(s.Info)})
			got, err := fetchEngineVia(context.Background(), dial)
			if err != nil {
				t.Fatal(err)
			}
			want := loadSample(t, name)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("facts = %+v\nwant    %+v", got, want)
			}
			if fmt.Sprint(*seen) != "[GET /version GET /info]" {
				t.Errorf("requests = %v", *seen)
			}
		})
	}
}

// TestEngineVerdict_OverDialer: what the gate and doctor share. A dial
// failure is engine_unreachable; an endpoint that answers, but not like an
// engine, is engine_unknown (it was reached); the reason keeps only the
// numeric status, never the reason phrase or the body.
func TestEngineVerdict_OverDialer(t *testing.T) {
	rootless := readSample(t, "rootless-linux")
	huge := `{"Version":"` + strings.Repeat("9", 2<<20) + `"}`
	cases := []struct {
		name   string
		dial   func(context.Context) (net.Conn, error)
		code   string
		detail string
	}{
		{"dial fails", func(context.Context) (net.Conn, error) { return nil, errors.New("connect: no such file or directory") }, EngineUnreachableCode, "no such file or directory"},
		{"version 500", first(fakeEngineRoutes(t, map[string]string{"/version": "HTTP/1.1 500 pwn\r\nContent-Length: 3\r\n\r\npwn"})), EngineUnknownCode, "HTTP 500"},
		{"info missing", first(fakeEngineRoutes(t, map[string]string{"/version": okJSON(rootless.Version)})), EngineUnknownCode, "HTTP 404"},
		{"version not JSON", first(fakeEngineRoutes(t, map[string]string{"/version": okJSON([]byte("<html>pwn</html>")), "/info": okJSON(rootless.Info)})), EngineUnknownCode, "not a Docker engine answer"},
		{"version a JSON array", first(fakeEngineRoutes(t, map[string]string{"/version": okJSON([]byte(`["pwn"]`)), "/info": okJSON(rootless.Info)})), EngineUnknownCode, "not a Docker engine answer"},
		{"info empty object", first(fakeEngineRoutes(t, map[string]string{"/version": okJSON(rootless.Version), "/info": okJSON([]byte(`{}`))})), EngineUnknownCode, ""},
		{"version over 1 MiB", first(fakeEngineRoutes(t, map[string]string{"/version": okJSON([]byte(huge)), "/info": okJSON(rootless.Info)})), EngineUnknownCode, "more than 1 MiB"},
		// A valid answer padded past the bound is refused for its size, not
		// parsed from its first MiB.
		{"valid version padded past 1 MiB", first(fakeEngineRoutes(t, map[string]string{"/version": okJSON(append(append([]byte{}, rootless.Version...), []byte(strings.Repeat(" ", 1<<20))...)), "/info": okJSON(rootless.Info)})), EngineUnknownCode, "more than 1 MiB"},
		{"accepted", first(fakeEngineRoutes(t, map[string]string{"/version": okJSON(rootless.Version), "/info": okJSON(rootless.Info)})), "", "27.3.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := engineVerdictVia(context.Background(), "linux", c.dial)
			if v.Code != c.code {
				t.Fatalf("code = %q (reason %.200q), want %q", v.Code, v.Reason, c.code)
			}
			if strings.Contains(v.Reason, "pwn") || len(v.Reason) > 500 {
				t.Errorf("reason carries what the endpoint chose: %.200q", v.Reason)
			}
			if !strings.Contains(v.Reason, c.detail) {
				t.Errorf("reason %q lacks %q", v.Reason, c.detail)
			}
		})
	}
}

func first[A, B any](a A, _ B) A { return a }

// TestCheckEngine_GateRefusesWithCode: the gate M2-21 calls before creating
// anything returns the kind for oci.Options.Engine, or an error that is
// sandbox_unavailable and carries the code (CWE-636: doctor reports, the
// gate refuses).
func TestCheckEngine_GateRefusesWithCode(t *testing.T) {
	serve := func(name string) func(context.Context) (net.Conn, error) {
		s := readSample(t, name)
		d, _ := fakeEngineRoutes(t, map[string]string{"/version": okJSON(s.Version), "/info": okJSON(s.Info)})
		return d
	}
	kind, err := checkEngineVia(context.Background(), "linux", serve("rootless-linux"))
	if err != nil || kind != EngineRootless {
		t.Fatalf("rootless: kind %q err %v", kind, err)
	}
	kind, err = checkEngineVia(context.Background(), "windows", serve("desktop-windows"))
	if err != nil || kind != EngineDesktop {
		t.Fatalf("desktop: kind %q err %v", kind, err)
	}
	for name, code := range map[string]string{"rootful-linux": EngineRootfulCode, "podman-rootless": EnginePodmanCode, "desktop-windows-containers": EngineOSCode} {
		kind, err := checkEngineVia(context.Background(), "linux", serve(name))
		var ee *EngineError
		if kind != "" || !errors.Is(err, ErrUnavailable) || !errors.As(err, &ee) || ee.Code != code {
			t.Errorf("%s: kind %q err %v, want sandbox_unavailable %s", name, kind, err, code)
		}
	}
	kind, err = checkEngineVia(context.Background(), "linux", func(context.Context) (net.Conn, error) { return nil, errors.New("refused") })
	var ee *EngineError
	if kind != "" || !errors.As(err, &ee) || ee.Code != EngineUnreachableCode {
		t.Errorf("unreachable: kind %q err %v", kind, err)
	}
}
