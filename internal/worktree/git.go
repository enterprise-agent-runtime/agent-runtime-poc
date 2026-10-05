// Package worktree will manage the session git directory, worktree,
// checkpoints and host-side delivery of WRD-07 §10 and design A14 (M4).
// In M1 it provides the host git version probe for system.doctor.
package worktree

// sandboxed: runs only the fixed host command "git --version" for doctor;
// never agent input and never inside a session worktree.
import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

// MinGit is the minimum host git (A05 doctor check "git").
const MinGit = "2.34"

var gitVer = regexp.MustCompile(`git version (\d+)\.(\d+)`)

// GitVersion returns the host git version string ("2.47").
func GitVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "--version").Output()
	if err != nil {
		return "", err
	}
	m := gitVer.FindStringSubmatch(string(out))
	if m == nil {
		return "", strconv.ErrSyntax
	}
	return m[1] + "." + m[2], nil
}

// AtLeast compares "major.minor" versions.
func AtLeast(v, min string) bool {
	parse := func(s string) (int, int) {
		m := regexp.MustCompile(`^(\d+)\.(\d+)`).FindStringSubmatch(s)
		if m == nil {
			return 0, 0
		}
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		return a, b
	}
	a1, b1 := parse(v)
	a2, b2 := parse(min)
	return a1 > a2 || (a1 == a2 && b1 >= b2)
}
