package worktree

import (
	"context"
	"testing"
)

func TestAtLeast(t *testing.T) {
	cases := []struct {
		v, min string
		ok     bool
	}{{"2.47", MinGit, true}, {"2.34", MinGit, true}, {"2.33", MinGit, false}, {"3.0", MinGit, true}, {"1.99", MinGit, false}, {"x", MinGit, false}}
	for _, c := range cases {
		if got := AtLeast(c.v, c.min); got != c.ok {
			t.Errorf("AtLeast(%q) = %v", c.v, got)
		}
	}
}

// TestGitVersion runs the host git (present on every CI runner and on the
// owner's machine); doctor relies on it.
func TestGitVersion(t *testing.T) {
	v, err := GitVersion(context.Background())
	if err != nil || !AtLeast(v, "2.0") {
		t.Fatalf("git version %q %v", v, err)
	}
}
