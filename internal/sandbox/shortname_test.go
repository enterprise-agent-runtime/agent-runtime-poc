package sandbox

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSpecValidate_ShortNameHostBase: the daemon builds every host path in a
// Spec and hands over long-form paths, so the deny-list's NTFS 8.3 rule
// (D-055), which defends against model-chosen aliases such as ENV~1, does
// not apply to them. PR #16 failed on GitHub's windows-latest runner, whose
// TEMP is C:\Users\RUNNER~1\..., because every layout built there was
// refused as "8.3-short-name". The layout here has such a segment on every
// OS. The deny-list globs still apply under it: ~/.ssh stays refused.
func TestSpecValidate_ShortNameHostBase(t *testing.T) {
	for _, custom := range []bool{false, true} {
		for _, level := range []Level{LevelL1, LevelL2} {
			l := shortNameLayout(t, custom)
			if !strings.Contains(l.tmp, "RUNNER~1") {
				t.Fatalf("layout base %q has no short-name segment", l.tmp)
			}
			s := validSpec(l, level)
			if err := s.Validate(); err != nil {
				t.Errorf("custom=%v %s: Validate() = %v, want nil", custom, level, err)
			}

			s = validSpec(l, level)
			s.MainObjects = filepath.Join(l.hostHome, ".ssh", "objects")
			if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "deny-listed") {
				t.Errorf("custom=%v %s: objects under ~/.ssh: Validate() = %v, want deny-listed", custom, level, err)
			}
			s = validSpec(l, level)
			s.ExecBin = filepath.Join(l.hostHome, ".ssh", "warden-exec")
			if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "deny-listed") {
				t.Errorf("custom=%v %s: executor under ~/.ssh: Validate() = %v, want deny-listed", custom, level, err)
			}
			if level == LevelL1 {
				// Toolchain roots are slash paths on every OS (L1 hosts), so
				// this part runs on Windows too, with a slash base.
				userHome := "/srv/RUNNER~1/home/ana"
				if slashHost(l) {
					userHome = filepath.ToSlash(l.hostHome)
				}
				s = validSpec(l, level)
				mise := userHome + "/.local/share/mise"
				s.Toolchains = []Toolchain{{Root: mise}}
				if err := s.Validate(); err != nil {
					t.Errorf("custom=%v: toolchain %s: Validate() = %v, want nil", custom, mise, err)
				}
				s.Toolchains = []Toolchain{{Root: userHome + "/.ssh"}}
				if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "deny-listed") {
					t.Errorf("custom=%v: toolchain ~/.ssh: Validate() = %v, want deny-listed", custom, err)
				}
			}
		}
	}
}
