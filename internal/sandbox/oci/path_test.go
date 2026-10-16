package oci

import (
	"strings"
	"testing"
)

// The bind-source mapping is pure and runs on every OS: a Windows-host
// mistake must fail on the Linux and macOS CI runners too. Every row that
// is refused names a way a bind could reach something other than the
// directory the Spec checks examined (CWE-22, CWE-41, CWE-59), or a
// spelling Docker Desktop could read differently from us.
func TestMapHostPath_Windows(t *testing.T) {
	ok := []struct{ in, want string }{
		{`C:\Users\ana\AppData\Local\Warden\sessions\01JAXR\worktree`, `C:\Users\ana\AppData\Local\Warden\sessions\01JAXR\worktree`},
		// Case is kept exactly: on a case-sensitive worktree it is part of
		// the name.
		{`D:\x\MiXeD\cAsE`, `D:\x\MiXeD\cAsE`},
		// Spaces are fine in a structured Engine API mount.
		{`C:\Program Files\Warden\linux-amd64\warden-exec`, `C:\Program Files\Warden\linux-amd64\warden-exec`},
		{`C:\Users\ana\my project\.git\objects`, `C:\Users\ana\my project\.git\objects`},
		{`C:\a`, `C:\a`},
		{`C:\` + strings.Repeat("a", 256), `C:\` + strings.Repeat("a", 256)}, // 259 characters, the MAX_PATH limit
	}
	for _, c := range ok {
		got, err := MapHostPath(HostWindows, c.in)
		if err != nil || got != c.want {
			t.Errorf("MapHostPath(windows, %q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	refused := []struct{ in, why string }{
		{``, "empty"},
		{`\\server\share\worktree`, "UNC path (network share)"},
		{`\\?\C:\Users\ana\worktree`, `long-path prefix: refused, not stripped; HostFS.FinalPath already returns the plain drive form`},
		{`\\?\UNC\server\share`, "long-path UNC"},
		{`\\.\C:\Users`, "device namespace"},
		{`\??\C:\Users`, "NT object namespace"},
		{`//server/share`, "UNC with forward slashes"},
		{`C:/Users/ana`, "forward slashes are not the clean form"},
		{`/c/Users/ana`, "MSYS form"},
		{`Users\ana`, "relative"},
		{`\Users\ana`, "rooted without a drive"},
		{`C:Users\ana`, "drive-relative"},
		{`C:`, "drive only"},
		{`C:\`, "a drive root is never a bind source"},
		{`C:\Users\ana\`, "trailing separator"},
		{`C:\Users\\ana`, "empty element"},
		{`C:\Users\.\ana`, "dot element"},
		{`C:\Users\..\ana`, "dot-dot element"},
		{`C:\Users\ana.`, "trailing dot: Win32 strips it, an alias"},
		{`C:\Users\ana `, "trailing space: Win32 strips it, an alias"},
		{`C:\Users\RUNNER~1\x`, "8.3 short-name element: the caller passes long-form paths (D-055 amendment)"},
		{`C:\Users\ana\ENV~1`, "8.3 short-name element"},
		{`C:\Users\ana\file.txt:secret`, "alternate data stream"},
		{`C:\Users\a*na`, "wildcard"},
		{`C:\Users\a?na`, "wildcard"},
		{`C:\Users\a"na`, "quote"},
		{`C:\Users\a<b`, "invalid character"},
		{`C:\Users\a|b`, "invalid character"},
		{`C:\Users\a,b`, "comma, which splits a --mount option list"},
		{"C:\\Users\\a\x00b", "NUL"},
		{"C:\\Users\\a\nb", "control character"},
		{`C:\Users\CON`, "reserved device name"},
		{`C:\Users\nul.txt`, "reserved device name with an extension"},
		{`C:\Users\com1`, "reserved device name"},
		{`C:\Users\LPT9.log`, "reserved device name"},
		{`C:\Users\COM` + string(rune(0xB9)), "reserved device name with a superscript digit"},
		{`C:\` + strings.Repeat("a", 257), "longer than MAX_PATH - 1 (long paths refused)"},
		{`1:\Users`, "not a drive letter"},
		// One spelling: the drive letter is upper case, as FinalPath returns
		// it, so a lower-case one would fail Build's FinalPath comparison
		// anyway; refusing it here keeps the two checks consistent (M2-18
		// review round 1: the old upper-casing was dead code).
		{`c:\Users\ana\Src`, "lower-case drive letter"},
	}
	for _, c := range refused {
		if got, err := MapHostPath(HostWindows, c.in); err == nil {
			t.Errorf("MapHostPath(windows, %q) = %q, want refusal (%s)", c.in, got, c.why)
		}
	}
}

func TestMapHostPath_Slash(t *testing.T) {
	for _, host := range []string{HostLinux, HostDarwin} {
		for _, p := range []string{"/home/ana/.warden/sessions/01JAXR/worktree", "/Users/ana/my project/x", "/opt/warden-exec"} {
			if got, err := MapHostPath(host, p); err != nil || got != p {
				t.Errorf("MapHostPath(%s, %q) = %q, %v; want it unchanged", host, p, got, err)
			}
		}
		for _, p := range []string{
			"", "relative/x", "/", "/a/../b", "/a/./b", "/a//b", "/a/", "/a\x00b", "/a\nb",
			`C:\Users\ana`, "/a:b", "/a,b",
		} {
			if got, err := MapHostPath(host, p); err == nil {
				t.Errorf("MapHostPath(%s, %q) = %q, want refusal", host, p, got)
			}
		}
	}
	if _, err := MapHostPath("plan9", "/a"); err == nil {
		t.Errorf("MapHostPath on an unknown host OS succeeded")
	}
}

func TestCheckImageRef(t *testing.T) {
	const d = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, ref := range []string{
		"warden/sandbox-go@" + d,
		"docker.io/library/node@" + d,
		"ghcr.io/acme/warden/proxy@" + d,
		"localhost:5000/warden/proxy@" + d,
		"node@" + d,
	} {
		if err := CheckImageRef(ref); err != nil {
			t.Errorf("CheckImageRef(%q) = %v, want nil", ref, err)
		}
	}
	for _, ref := range []string{
		"", "node", "node:22-bookworm-slim", "node:latest",
		"node:22@" + d, // a tag beside the digest: one spelling only
		"node@sha256:0123", "node@sha256:" + strings.ToUpper(d[7:]), "node@sha512:" + d[7:],
		"Node@" + d, "node@" + d + "\n", " node@" + d, "node@" + d + "x",
	} {
		if err := CheckImageRef(ref); err == nil {
			t.Errorf("CheckImageRef(%q) = nil, want refusal", ref)
		}
	}
}
