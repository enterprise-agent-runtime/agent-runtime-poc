package execproto

import (
	"errors"
	"fmt"
	"testing"
)

// The error codes are wire values in both directions: the daemon maps
// error.data.code to a sandbox.violation kind and a denied tool result
// (A06 §4.4, WRD-04 §7), and events persist the names. A renumbered or
// renamed code would make the daemon misclassify a violation as an ordinary
// failure (no sandbox.violation event, no audit trace of an escape attempt).
// The table below is A06 §4.4 copied by hand; it is the stability pin.
func TestErrorCodes(t *testing.T) {
	want := []struct {
		code      Code
		num       int
		name      string
		violation ViolationKind
	}{
		{ErrPathEscape, -32101, "path_escape", ViolationPathEscape},
		{ErrOutsideRoots, -32102, "outside_roots", ViolationRootCheck},
		{ErrDenyList, -32103, "deny_list", ViolationDenyList},
		{ErrNotFound, -32104, "not_found", ""},
		{ErrNotRegular, -32105, "not_regular", ""},
		{ErrNotAllowed, -32106, "not_allowed", ViolationRootCheck},
		{ErrShellString, -32107, "shell_string", ViolationRootCheck},
		{ErrProfileMismatch, -32108, "profile_mismatch", ViolationRootCheck},
		{ErrTooLarge, -32109, "too_large", ""},
		{ErrTimeout, -32110, "timeout", ""},
		{ErrInvalidArgument, -32111, "invalid_argument", ""},
		{ErrConflict, -32112, "conflict", ""},
		{ErrPatchFailed, -32113, "patch_failed", ""},
		{ErrRedactionMarker, -32114, "redaction_marker", ""},
		{ErrModeForbidden, -32115, "mode_forbidden", ViolationRootCheck},
		{ErrBranchMismatch, -32116, "branch_mismatch", ViolationRootCheck},
		{ErrBusy, -32117, "busy", ""},
		{ErrIOError, -32118, "io_error", ""},
		{ErrInvalidState, -32119, "invalid_state", ""},
		{ErrShuttingDown, -32120, "shutting_down", ""},
	}
	codes := Codes()
	if len(codes) != len(want) {
		t.Fatalf("Codes() has %d codes, A06 §4.4 has %d", len(codes), len(want))
	}
	nums, names := map[Code]bool{}, map[string]bool{}
	for i, w := range want {
		c := codes[i]
		if c != w.code || int(c) != w.num {
			t.Errorf("Codes()[%d] = %d, want %d", i, c, w.num)
		}
		if c < -32120 || c > -32101 {
			t.Errorf("%d outside the executor range -32120…-32101", c)
		}
		if nums[c] || names[c.Name()] {
			t.Errorf("%d %q is not unique", c, c.Name())
		}
		nums[c], names[c.Name()] = true, true
		if c.Name() != w.name {
			t.Errorf("%d.Name() = %q, want %q", c, c.Name(), w.name)
		}
		if c.Violation() != w.violation {
			t.Errorf("%s.Violation() = %q, want %q", w.name, c.Violation(), w.violation)
		}
		if back, ok := CodeByName(w.name); !ok || back != c {
			t.Errorf("CodeByName(%q) = %d, %v", w.name, back, ok)
		}

		e := NewError(c, "detail")
		if e.Data.Code != w.name || e.Data.Violation != (w.violation != "") || e.Data.Kind != w.violation {
			t.Errorf("NewError(%s).Data = %+v", w.name, e.Data)
		}
		wrapped := fmt.Errorf("fs.read: %w", e)
		if !errors.Is(wrapped, c) {
			t.Errorf("errors.Is(wrapped %s, %s) = false", w.name, w.name)
		}
		other := codes[(i+1)%len(codes)]
		if errors.Is(wrapped, other) {
			t.Errorf("errors.Is(%s, %s) = true", w.name, other.Name())
		}
	}
	for _, c := range []Code{-32100, -32121, 0, -32601} {
		if c.Name() != "" || c.Violation() != "" {
			t.Errorf("code %d outside the range has name %q / violation %q", c, c.Name(), c.Violation())
		}
	}
	if _, ok := CodeByName("permission_denied"); ok {
		t.Error("CodeByName accepted a name A06 does not define")
	}
}
