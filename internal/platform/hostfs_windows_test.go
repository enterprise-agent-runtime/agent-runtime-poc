//go:build windows

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// These tests run against the real NTFS of the machine (CLAUDE.md §8.6:
// each OS-specific code path has a test on its OS). They exist because the
// L2 worktree masks are bypassed on a case-insensitive directory (".ENV")
// and through 8.3 names ("ENV~1"), PROGRESS M2-12 "must fix before the L2
// backend merges"; the spec builder refuses on what these functions say,
// so what they say is checked here against what the file system does.

// setCaseSensitive sets the per-directory case-sensitivity flag, the API
// behind `fsutil file setCaseSensitiveInfo <dir> enable`. It needs no
// elevation, only FILE_DELETE_CHILD-level rights on the directory (full
// control, which the owner of a new temp directory has); a machine where it
// still fails skips the test that needs it.
func setCaseSensitive(t *testing.T, dir string) {
	t.Helper()
	u, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(u, windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Skipf("cannot open %s to set case sensitivity: %v", dir, err)
	}
	defer windows.CloseHandle(h)
	flags := uint32(windows.FILE_CS_FLAG_CASE_SENSITIVE_DIR)
	var iosb windows.IO_STATUS_BLOCK
	if err := windows.NtSetInformationFile(h, &iosb, (*byte)(unsafe.Pointer(&flags)), 4, windows.FileCaseSensitiveInformation); err != nil {
		t.Skipf("setting the case-sensitivity flag on a new temp directory failed (%v); not requesting elevation", err)
	}
}

// observedShortName is the 8.3 name NTFS gave p, or "" when it has none.
func observedShortName(t *testing.T, p string) string {
	t.Helper()
	u, _ := windows.UTF16PtrFromString(p)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(u, &buf[0], uint32(len(buf)))
	if err != nil {
		t.Fatal(err)
	}
	if s := filepath.Base(windows.UTF16ToString(buf[:n])); s != filepath.Base(p) {
		return s
	}
	return ""
}

// longTempDir is t.TempDir() in its final long form: GitHub's runners
// have TEMP = C:\Users\RUNNER~1\..., which FinalPath would rewrite.
func longTempDir(t *testing.T) string {
	t.Helper()
	d, err := NewHostFS().FinalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A new directory is case-insensitive unless the flag was set (Windows
// default), and the answer matches what a lookup of another case does.
func TestHostFS_TempDirIsCaseInsensitive(t *testing.T) {
	dir := longTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".ENV")); err != nil {
		t.Fatalf("precondition: .ENV should open .env in a default directory: %v", err)
	}
	cs, err := NewHostFS().CaseSensitiveDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cs {
		t.Fatalf("CaseSensitiveDir(%s) = true, but .ENV opens .env there", dir)
	}
}

func TestHostFS_CaseSensitiveDirAfterFlag(t *testing.T) {
	dir := filepath.Join(longTempDir(t), "cs")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	setCaseSensitive(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".ENV")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("precondition: .ENV must not open .env in a case-sensitive directory: %v", err)
	}
	cs, err := NewHostFS().CaseSensitiveDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cs {
		t.Fatalf("CaseSensitiveDir(%s) = false after the flag was set", dir)
	}
	// The flag is inherited by directories created inside afterwards, which
	// is why setting it on S/worktree before checkout covers the tree.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if cs, err := NewHostFS().CaseSensitiveDir(sub); err != nil || !cs {
		t.Fatalf("CaseSensitiveDir(sub) = %v, %v; want inherited true", cs, err)
	}
}

// ShortNameCreation must agree with what NTFS does on the same volume: a
// new long name gets an 8.3 alias exactly when creation is on. The
// case-sensitivity flag does not change this (measured, see
// TestHostFS_CaseSensitiveDirStillGetsShortNames).
func TestHostFS_ShortNameCreationMatchesVolume(t *testing.T) {
	dir := longTempDir(t)
	f := filepath.Join(dir, "a long file name.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	observed := observedShortName(t, f) != ""
	got, err := NewHostFS().ShortNameCreation(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != observed {
		t.Fatalf("ShortNameCreation(%s) = %v, but NTFS created a short name: %v", dir, got, observed)
	}
	t.Logf("8.3 name creation on the temp volume: %v", got)
}

func TestHostFS_HasShortAlias(t *testing.T) {
	dir := longTempDir(t)
	env := filepath.Join(dir, ".env")
	plain := filepath.Join(dir, "src")
	if err := os.WriteFile(env, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	h := NewHostFS()
	for _, p := range []string{env, plain} {
		got, err := h.HasShortAlias(p)
		if err != nil {
			t.Fatal(err)
		}
		if want := observedShortName(t, p) != ""; got != want {
			t.Errorf("HasShortAlias(%s) = %v, want %v", p, got, want)
		}
	}
	if got, _ := h.HasShortAlias(plain); got {
		t.Errorf("HasShortAlias(src) = true; a valid 8.3 name has no separate alias")
	}
}

// Measured on this machine for M2-18: the case-sensitivity flag does not
// stop 8.3 aliasing, so both checks are needed. Skipped when the volume
// does not create short names.
func TestHostFS_CaseSensitiveDirStillGetsShortNames(t *testing.T) {
	dir := filepath.Join(longTempDir(t), "cs")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if on, err := NewHostFS().ShortNameCreation(dir); err != nil || !on {
		t.Skipf("8.3 creation is off on this volume (%v, %v)", on, err)
	}
	setCaseSensitive(t, dir)
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := observedShortName(t, env)
	if alias == "" {
		t.Fatalf("no short name for .env in a case-sensitive directory; the case flag would then cover 8.3 too")
	}
	if _, err := os.Stat(filepath.Join(dir, alias)); err != nil {
		t.Fatalf("%s does not open .env: %v", alias, err)
	}
	if got, _ := NewHostFS().HasShortAlias(env); !got {
		t.Fatalf("HasShortAlias(.env) = false, but %s opens it", alias)
	}
}

func TestHostFS_FinalPath(t *testing.T) {
	h := NewHostFS()
	dir := longTempDir(t)
	if strings.HasPrefix(dir, `\\`) || len(dir) < 3 || dir[1] != ':' {
		t.Fatalf("FinalPath gave %q, want a plain drive path without the \\\\?\\ prefix", dir)
	}
	target := filepath.Join(dir, "Target Dir")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := h.FinalPath(target); err != nil || got != target {
		t.Errorf("FinalPath(%s) = %q, %v; want it unchanged", target, got, err)
	}
	// Another case of the same directory comes back in the on-disk case.
	if got, err := h.FinalPath(filepath.Join(dir, "TARGET DIR")); err != nil || got != target {
		t.Errorf("FinalPath(TARGET DIR) = %q, %v; want %q", got, err, target)
	}
	link := filepath.Join(dir, "link")
	makeJunction(t, link, target)
	if got, err := h.FinalPath(link); err != nil || got != target {
		t.Errorf("FinalPath(junction) = %q, %v; want its target %q", got, err, target)
	}
	if alias := observedShortName(t, target); alias != "" {
		if got, err := h.FinalPath(filepath.Join(dir, alias)); err != nil || got != target {
			t.Errorf("FinalPath(%s) = %q, %v; want the long form %q", alias, got, err, target)
		}
	}
	if _, err := h.FinalPath(filepath.Join(dir, "missing")); err == nil {
		t.Errorf("FinalPath of a missing path succeeded")
	}
}

// Value 3 of NtfsDisable8dot3NameCreation depends on whether a volume is
// the system volume (M2-18 review round 1), so that answer is checked on
// the real machine: the Windows directory is on it, by definition.
func TestIsSystemVolume(t *testing.T) {
	win, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := isSystemVolume(filepath.Join(win, "System32")); err != nil || !got {
		t.Fatalf("isSystemVolume(%s) = %v, %v; want true", win, got, err)
	}
}
