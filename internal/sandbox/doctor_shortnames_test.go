package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shortFS is the HostFS seam for the 8.3 doctor check: only
// ShortNameCreation matters, and it records which directory was asked.
type shortFS struct {
	on    bool
	err   error
	asked []string
}

func (f *shortFS) CaseSensitiveDir(string) (bool, error) { return true, nil }
func (f *shortFS) ShortNameCreation(dir string) (bool, error) {
	f.asked = append(f.asked, dir)
	return f.on, f.err
}
func (f *shortFS) HasShortAlias(string) (bool, error) { return false, nil }
func (f *shortFS) FinalPath(p string) (string, error) { return p, nil }
func (f *shortFS) CaseAliased(string) (bool, error)   { return false, nil }

// TestShortNamesCheck_Verdict: the L2 spec builder refuses a worktree on a
// volume that creates 8.3 names (short_names_enabled, M2-18), and its hint
// sends the user to warden doctor, so doctor must say so before any task
// does: warn (not fail: L1 elsewhere and a moved WARDEN_HOME are both fine)
// with the same fix text, ok when creation is off, and warn with the
// reason when the answer is unknown.
func TestShortNamesCheck_Verdict(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name   string
		fs     *shortFS
		home   string
		status string
		fix    bool
		detail string
		data   any
	}{
		{"creation on", &shortFS{on: true}, home, "warn", true, "creates 8.3 short names", true},
		{"creation off", &shortFS{on: false}, home, "ok", false, "does not create 8.3 short names", false},
		{"unknown", &shortFS{err: errors.New("access denied")}, home, "warn", true, "could not be determined: access denied", nil},
		{"no home", &shortFS{on: false}, "", "warn", true, "could not be determined", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shortNamesCheck(c.home, c.fs)
			if got.ID != "sandbox.short_names" || got.Group != "sandbox" || got.Blocking {
				t.Fatalf("check identity %+v", got)
			}
			if got.Status != c.status {
				t.Errorf("status %q, want %q", got.Status, c.status)
			}
			if !strings.Contains(got.Detail, c.detail) {
				t.Errorf("detail %q lacks %q", got.Detail, c.detail)
			}
			if c.fix != (got.FixHint != nil) {
				t.Fatalf("fix hint = %v, want present=%v", got.FixHint, c.fix)
			}
			if c.status == "warn" && c.data == true && *got.FixHint != ShortNamesFix {
				t.Errorf("fix hint %q is not the L2 refusal's text %q", *got.FixHint, ShortNamesFix)
			}
			if c.data != nil && got.Data["short_names"] != c.data {
				t.Errorf("data %v, want short_names=%v", got.Data, c.data)
			}
		})
	}
}

// The check asks about the sessions directory, where worktrees live, once
// it exists, and the home before that.
func TestShortNamesCheck_AsksSessionsDir(t *testing.T) {
	home := t.TempDir()
	fs := &shortFS{}
	shortNamesCheck(home, fs)
	sessions := filepath.Join(home, "sessions")
	if err := os.Mkdir(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	shortNamesCheck(home, fs)
	if len(fs.asked) != 2 || fs.asked[0] != home || fs.asked[1] != sessions {
		t.Fatalf("asked %q, want [%s %s]", fs.asked, home, sessions)
	}
}

// The fix text follows the owner's decision of 2026-10-16: move
// WARDEN_HOME first, an administrator's fsutil second.
func TestShortNamesFix_Wording(t *testing.T) {
	for _, want := range []string{
		"WARDEN_HOME on a volume without 8.3 name creation",
		"warden doctor",
		"`fsutil 8dot3name query <drive>:` needs elevation",
		"an administrator",
		"`fsutil 8dot3name set <drive>: 1`",
		"affects new names only",
		"`fsutil 8dot3name strip`",
	} {
		if !strings.Contains(ShortNamesFix, want) {
			t.Errorf("ShortNamesFix lacks %q: %s", want, ShortNamesFix)
		}
	}
	if i, j := strings.Index(ShortNamesFix, "WARDEN_HOME"), strings.Index(ShortNamesFix, "administrator"); i < 0 || i > j {
		t.Errorf("moving WARDEN_HOME must come first: %s", ShortNamesFix)
	}
}

// system.doctor's schema caps detail and fix_hint at 1000 characters
// (api/types.json doctorCheck); a long home path or OS error must not make
// the result invalid.
func TestShortNamesCheck_FitsTheSchema(t *testing.T) {
	long := strings.Repeat("é", 1500)
	for _, c := range []Check{
		shortNamesCheck(t.TempDir(), &shortFS{err: errors.New(long)}),
		shortNamesCheck(filepath.Join(t.TempDir(), long), &shortFS{on: true}),
	} {
		if n := len([]rune(c.Detail)); n > 1000 || n == 0 {
			t.Errorf("detail has %d characters", n)
		}
		if c.FixHint == nil || len([]rune(*c.FixHint)) > 1000 {
			t.Errorf("fix hint %v", c.FixHint)
		}
		if len([]rune(c.Title)) > 120 {
			t.Errorf("title too long")
		}
	}
}
