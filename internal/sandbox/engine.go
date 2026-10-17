package sandbox

// The L2 engine gate (design A06 §15 as amended by CONFLICTS C-03; PROGRESS
// M1 follow-up "L2 sandbox creation must check the engine itself", CWE-636).
//
// Doctor reports; the gate refuses. The L2 manager (M2-21) calls
// CheckEngine immediately before it creates anything and passes the kind
// it returns to oci.Options.Engine, which oci.Build trusts (D-071). Doctor's
// sandbox.l2 check reports the same verdict through the same function
// (engineVerdict), so the two cannot disagree about what is acceptable.
//
// What is accepted, and why:
//
//   - Rootless Docker on a Linux host. The container's uid 0 is the host
//     user without capabilities, so oci.Build runs the sandbox as 0:0.
//   - Docker Desktop on any host. Its engine is rootful inside its VM, so a
//     container escape lands in the VM, not on the host; oci.Build runs the
//     sandbox as 65532:65532 (C-03, D-071).
//
// What is refused (sandbox_unavailable, with a code):
//
//   - engine_unreachable: nothing answers on the local endpoint.
//   - engine_os: the engine serves Windows containers (or says nothing
//     recognisable about its OS type); L2 images are Linux images.
//   - engine_rootful: a native engine that is not rootless. A container
//     escape there is root on the host itself.
//   - engine_version: Moby older than 26.0.0, other than 25.0.x from 25.0.5
//     and 23.0.x from 23.0.11. Before those releases the embedded DNS
//     resolver forwarded external names for containers on --internal
//     networks to the host's resolvers (CVE-2024-29018): a DNS exfiltration
//     channel out of the network that C-03 relies on having no way out.
//     Docker Desktop bundles Moby and is held to the same versions.
//   - engine_podman: Podman's Docker-compatible API. See podmanReason.
//   - engine_unknown: anything the gate cannot classify with certainty
//     (malformed or inconsistent answers, a rootless engine reached from a
//     non-Linux host, a version it cannot compare). Fail closed.
//
// Every field is chosen by whatever answers on the endpoint: a genuine
// engine, or a process squatting on the socket or pipe. The gate checks
// configuration, it does not authenticate the engine; only a version and
// API version that pass their shape checks are ever shown (CWE-150, M2-3).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"warden.dev/warden/internal/platform"
)

// EngineKind is the kind of Docker engine an L2 sandbox is built for;
// oci.Engine is this type.
type EngineKind string

// Engine kinds the gate accepts.
const (
	// EngineRootless is rootless Docker on a Linux host: the container's
	// uid 0 is the host user, without capabilities (A06 §15).
	EngineRootless EngineKind = "rootless"
	// EngineDesktop is Docker Desktop (Windows, macOS, or Linux with
	// Desktop): a rootful engine inside a VM, so the container runs as a
	// non-root user (C-03).
	EngineDesktop EngineKind = "desktop"
)

// Refusal codes of the gate, carried by EngineError and by doctor's
// sandbox.l2 check (Data["code"]).
const (
	EngineUnreachableCode = "engine_unreachable"
	EngineOSCode          = "engine_os"
	EngineRootfulCode     = "engine_rootful"
	EngineVersionCode     = "engine_version"
	EnginePodmanCode      = "engine_podman"
	EngineUnknownCode     = "engine_unknown"
)

// EngineFacts is what the engine said about itself in GET /version and
// GET /info, unvalidated.
type EngineFacts struct {
	// From GET /version.
	Version    string
	APIVersion string
	Os         string
	Platform   string   // Platform.Name
	Components []string // Components[].Name
	// From GET /info.
	OSType          string
	OperatingSystem string
	ServerVersion   string
	SecurityOptions []string
}

// EngineVerdict is the gate's decision. Exactly one of Kind and Code is
// set; Reason says what was seen, accepted or not. Version, APIVersion and OSType are set only once they passed their
// shape checks, so they are safe to show.
type EngineVerdict struct {
	Kind       EngineKind
	Code       string
	Reason     string
	Version    string
	APIVersion string
	OSType     string
}

// Err is nil for an accepted engine and an *EngineError otherwise.
func (v EngineVerdict) Err() error {
	if v.Code == "" {
		return nil
	}
	return &EngineError{Code: v.Code, Reason: v.Reason}
}

// EngineError is a refusal of the gate. It is ErrUnavailable
// (sandbox_unavailable, -32006) and reads like the other L2 refusals of
// internal/sandbox/oci: "sandbox_unavailable: <code>: <reason>".
type EngineError struct {
	Code   string
	Reason string
}

func (e *EngineError) Error() string {
	return fmt.Sprintf("%s: %s: %s", ErrUnavailable.Error(), e.Code, e.Reason)
}

// Unwrap makes errors.Is(err, ErrUnavailable) hold.
func (e *EngineError) Unwrap() error { return ErrUnavailable }

// CheckEngine is the L2 engine gate: it asks the local Docker engine (Unix
// socket or named pipe, never TCP) what it is and returns the kind for
// oci.Options.Engine, or an *EngineError. Call it immediately before
// creating anything, every time: the engine behind the endpoint can change
// between doctor and a task.
func CheckEngine(ctx context.Context) (EngineKind, error) {
	return checkEngineVia(ctx, platform.OS(), dialDocker)
}

func checkEngineVia(ctx context.Context, hostOS string, dial func(context.Context) (net.Conn, error)) (EngineKind, error) {
	v := engineVerdictVia(ctx, hostOS, dial)
	return v.Kind, v.Err()
}

func engineVerdictVia(ctx context.Context, hostOS string, dial func(context.Context) (net.Conn, error)) EngineVerdict {
	return engineVerdict(ctx, hostOS, func(ctx context.Context) (EngineFacts, error) { return fetchEngineVia(ctx, dial) })
}

// engineVerdict is the one function the gate and doctor share: fetch, then
// decide. An endpoint that answered, but not like an engine, was reached,
// so it is engine_unknown rather than engine_unreachable.
func engineVerdict(ctx context.Context, hostOS string, fetch func(context.Context) (EngineFacts, error)) EngineVerdict {
	f, err := fetch(ctx)
	if err != nil {
		var ae *engineAnswerError
		if errors.As(err, &ae) {
			return EngineVerdict{Code: EngineUnknownCode, Reason: ae.msg + "; the endpoint may not be a Docker engine"}
		}
		return EngineVerdict{Code: EngineUnreachableCode, Reason: capRunes("Docker engine not reachable: "+err.Error(), maxReason)}
	}
	return decideEngine(hostOS, f)
}

// maxReason bounds a reason built from an error text.
const maxReason = 300

// engineAnswerError is an endpoint that answered, but not with an engine's
// answer. Its message never contains what the endpoint sent.
type engineAnswerError struct{ msg string }

func (e *engineAnswerError) Error() string { return e.msg }

// maxEngineAnswer bounds each answer read from the endpoint.
const maxEngineAnswer = 1 << 20

// fetchEngineVia asks for GET /version and GET /info over an injected
// dialer, so a test can stand in for whatever answers on the endpoint.
func fetchEngineVia(ctx context.Context, dial func(context.Context) (net.Conn, error)) (EngineFacts, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tr := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dial(ctx)
	}}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr}
	get := func(path string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			// Only the numeric code: the reason phrase is chosen by whatever
			// answers on the endpoint, unbounded and unescaped (CWE-150, M2-3).
			return nil, &engineAnswerError{fmt.Sprintf("engine answered HTTP %d to GET %s", resp.StatusCode, path)}
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxEngineAnswer+1))
		if err != nil {
			return nil, err
		}
		if len(b) > maxEngineAnswer {
			return nil, &engineAnswerError{fmt.Sprintf("GET %s answered more than 1 MiB, not a Docker engine answer", path)}
		}
		return b, nil
	}
	v, err := get("/version")
	if err != nil {
		return EngineFacts{}, err
	}
	i, err := get("/info")
	if err != nil {
		return EngineFacts{}, err
	}
	return parseEngineFacts(v, i)
}

// parseEngineFacts decodes the two answers.
func parseEngineFacts(version, info []byte) (EngineFacts, error) {
	var v struct {
		Version    string `json:"Version"`
		APIVersion string `json:"ApiVersion"`
		Os         string `json:"Os"`
		Platform   struct {
			Name string `json:"Name"`
		} `json:"Platform"`
		Components []struct {
			Name string `json:"Name"`
		} `json:"Components"`
	}
	if err := json.Unmarshal(version, &v); err != nil {
		return EngineFacts{}, &engineAnswerError{"GET /version is not a Docker engine answer"}
	}
	var i struct {
		OSType          string   `json:"OSType"`
		OperatingSystem string   `json:"OperatingSystem"`
		ServerVersion   string   `json:"ServerVersion"`
		SecurityOptions []string `json:"SecurityOptions"`
	}
	if err := json.Unmarshal(info, &i); err != nil {
		return EngineFacts{}, &engineAnswerError{"GET /info is not a Docker engine answer"}
	}
	f := EngineFacts{
		Version: v.Version, APIVersion: v.APIVersion, Os: v.Os, Platform: v.Platform.Name,
		OSType: i.OSType, OperatingSystem: i.OperatingSystem, ServerVersion: i.ServerVersion, SecurityOptions: i.SecurityOptions,
	}
	for _, c := range v.Components {
		f.Components = append(f.Components, c.Name)
	}
	return f, nil
}

// apiVersion is the shape of an Engine API version ("1.47").
var apiVersion = regexp.MustCompile(`^1\.[0-9]{1,3}$`)

// mobyVersion is a comparable engine version: major.minor.patch, an
// optional pre-release and optional build metadata ("27.0.1+dfsg-1ubuntu1"
// is 27.0.1; "26.0.0-rc.1" is older than 26.0.0).
var mobyVersion = regexp.MustCompile(`^([0-9]{1,6})\.([0-9]{1,6})\.([0-9]{1,6})(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.+-]+)?$`)

// desktopOS is what Docker Desktop reports as OperatingSystem in GET /info,
// on Windows, macOS and Linux alike.
const desktopOS = "Docker Desktop"

// podmanReason records why Podman is refused rather than accepted.
//
// Podman's DNS server on netavark networks is aardvark-dns. Whether it
// forwards external names for containers on an --internal network, the
// leak CVE-2024-29018 closed in Moby, depends on the aardvark-dns and
// netavark versions: from memory (not verified, no network access when
// this was written; PLAUSIBLE), older aardvark-dns forwarded every name it
// was not authoritative for to the host's resolvers whatever the network's
// internal flag, and a later release changed that. The gate cannot check
// the aardvark-dns version through the compat API, the L2 backend
// (M2-21) is written and tested against Moby's Engine API only, and the
// 0:0 user of D-071 assumes a rootless Docker user namespace. So Podman is
// identified and refused, not passed through as rootless Docker.
const podmanReason = "Podman is not supported for L2: whether its aardvark-dns resolver forwards external names " +
	"from --internal networks (the leak of CVE-2024-29018) cannot be checked, and the L2 backend is built for Docker's Engine API"

// decideEngine is the gate's pure decision for an engine as fetched. It is
// shared by CheckEngine and doctor (through engineVerdict). hostOS is
// platform.OS().
func decideEngine(hostOS string, f EngineFacts) EngineVerdict {
	refuse := func(v EngineVerdict, code, reason string) EngineVerdict {
		v.Kind, v.Code, v.Reason = "", code, reason
		return v
	}
	var v EngineVerdict
	if !engineVersion.MatchString(f.Version) {
		return refuse(v, EngineUnknownCode, "engine answered with a malformed version (not shown); the endpoint may not be a Docker engine")
	}
	if f.ServerVersion != f.Version {
		return refuse(v, EngineUnknownCode, "GET /version and GET /info report different engine versions; the endpoint may not be a Docker engine")
	}
	v.Version = f.Version
	if !apiVersion.MatchString(f.APIVersion) {
		return refuse(v, EngineUnknownCode, fmt.Sprintf("engine %s answered with a malformed API version (not shown)", v.Version))
	}
	v.APIVersion = f.APIVersion
	if f.Os != "linux" || f.OSType != "linux" {
		what := "an OS type that is not recognised (not shown)"
		if f.Os == "windows" && f.OSType == "windows" {
			what, v.OSType = "Windows containers", "windows"
		}
		return refuse(v, EngineOSCode, fmt.Sprintf("engine %s serves %s; Linux containers are required", v.Version, what))
	}
	v.OSType = "linux"
	if isPodman(f) {
		return refuse(v, EnginePodmanCode, podmanReason)
	}
	switch {
	case f.OperatingSystem == desktopOS:
		v.Kind = EngineDesktop
	case hasSecurityOption(f.SecurityOptions, "rootless") && hostOS == "linux":
		v.Kind = EngineRootless
	case hasSecurityOption(f.SecurityOptions, "rootless"):
		return refuse(v, EngineUnknownCode, fmt.Sprintf("a rootless engine reached from a %s host runs in a VM the gate cannot identify, so its uid 0 is not the host user; only rootless Docker on Linux or Docker Desktop are accepted", hostOS))
	default:
		return refuse(v, EngineRootfulCode, fmt.Sprintf("engine %s is a rootful native engine (no rootless security option and not Docker Desktop); a container escape there would be root on the host", v.Version))
	}
	patched, ok := mobyPatched(f.Version)
	if !ok {
		return refuse(v, EngineUnknownCode, fmt.Sprintf("engine version %s is not major.minor.patch, so it cannot be checked against CVE-2024-29018", v.Version))
	}
	if !patched {
		return refuse(v, EngineVersionCode, fmt.Sprintf("engine %s is older than 26.0.0 (or 25.0.5, 23.0.11): its embedded DNS resolver forwards external names from --internal networks (CVE-2024-29018)", v.Version))
	}
	name := "rootless Docker"
	if v.Kind == EngineDesktop {
		name = "Docker Desktop"
	}
	v.Reason = fmt.Sprintf("%s, engine %s (API %s), Linux containers", name, v.Version, v.APIVersion)
	return v
}

// isPodman reports any sign of Podman: its engine component, or the name
// in the platform, a component or the operating system. Over-matching is
// safe here, since Podman is refused.
func isPodman(f EngineFacts) bool {
	for _, s := range append([]string{f.Platform, f.OperatingSystem}, f.Components...) {
		if strings.Contains(strings.ToLower(s), "podman") {
			return true
		}
	}
	return false
}

// hasSecurityOption reports whether one of GET /info's SecurityOptions
// entries ("name=seccomp,profile=builtin", "name=rootless") names the
// option, exactly: no case folding, no padding, the name key first.
func hasSecurityOption(opts []string, name string) bool {
	for _, o := range opts {
		if first, _, _ := strings.Cut(o, ","); first == "name="+name {
			return true
		}
	}
	return false
}

// mobyPatched reports whether a Moby version has the CVE-2024-29018 fix:
// 26.0.0 or later, 25.0.x from 25.0.5, 23.0.x from 23.0.11. A pre-release
// of a version is older than that version. ok is false for a version that
// cannot be compared.
func mobyPatched(ver string) (patched, ok bool) {
	m := mobyVersion.FindStringSubmatch(ver)
	if m == nil {
		return false, false
	}
	var n [3]int
	for i := range n {
		x, err := strconv.Atoi(m[i+1])
		if err != nil {
			return false, false
		}
		n[i] = x
	}
	pre := m[4] != ""
	atLeast := func(maj, min, patch int) bool {
		got, want := [3]int{n[0], n[1], n[2]}, [3]int{maj, min, patch}
		for i := range got {
			if got[i] != want[i] {
				return got[i] > want[i]
			}
		}
		return !pre // equal: a pre-release is older
	}
	switch {
	case n[0] >= 26:
		return atLeast(26, 0, 0), true
	case n[0] == 25 && n[1] == 0:
		return atLeast(25, 0, 5), true
	case n[0] == 23 && n[1] == 0:
		return atLeast(23, 0, 11), true
	}
	return false, true
}
