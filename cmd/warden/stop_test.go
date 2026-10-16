package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"warden.dev/warden/internal/platform"
)

// fakeWatch stands in for platform.Watch, so the outcomes of
// "warden daemon stop" are tested without a daemon or a real clock.
type fakeWatch struct {
	waitErr error
	closed  bool
	waited  time.Duration
}

func (f *fakeWatch) PID() int { return 4242 }
func (f *fakeWatch) Wait(d time.Duration) error {
	f.waited = d
	return f.waitErr
}
func (f *fakeWatch) Close() { f.closed = true }

// TestStopAndWait_Outcomes pins down what "warden daemon stop" reports
// (M2-1 review, 2026-10-16). Scripts rely on the exit code: 1 means wardend
// may still hold files under the home, so cleanup must not assume it can
// delete them. A pid that cannot be read or watched must not make stop fail:
// the daemon is still asked to shut down, and the user is told stop did not
// wait.
func TestStopAndWait_Outcomes(t *testing.T) {
	const home = "HOME-DIR"

	t.Run("exits in time: stopped, exit 0", func(t *testing.T) {
		w := &fakeWatch{}
		var out, errOut bytes.Buffer
		shut := 0
		err := stopAndWait(func() (procWatch, error) { return w, nil }, func() error { shut++; return nil }, 10*time.Second, &out, &errOut, home)
		if err != nil {
			t.Fatalf("stopAndWait = %v, want nil", err)
		}
		if shut != 1 || w.waited != 10*time.Second || !w.closed {
			t.Errorf("shutdown calls %d, waited %v, closed %v; want 1, 10s, true", shut, w.waited, w.closed)
		}
		if !strings.Contains(out.String(), "wardend stopped") {
			t.Errorf("stdout %q does not say wardend stopped", out.String())
		}
	})

	t.Run("does not exit in time: stderr names pid and home, exit 1", func(t *testing.T) {
		w := &fakeWatch{waitErr: fmt.Errorf("%w after 10s", platform.ErrStillRunning)}
		var out, errOut bytes.Buffer
		err := stopAndWait(func() (procWatch, error) { return w, nil }, func() error { return nil }, 10*time.Second, &out, &errOut, home)
		var ee exitError
		if !errors.As(err, &ee) || ee.code != 1 {
			t.Fatalf("stopAndWait = %v, want exitError{1}", err)
		}
		msg := errOut.String()
		for _, want := range []string{"did not exit within 10s", "4242", home} {
			if !strings.Contains(msg, want) {
				t.Errorf("stderr %q lacks %q", msg, want)
			}
		}
	})

	t.Run("pid unreadable: warns, still shuts down, exit 0", func(t *testing.T) {
		var out, errOut bytes.Buffer
		shut := 0
		open := func() (procWatch, error) { return nil, errors.New("cannot read the wardend pid: no such file") }
		err := stopAndWait(open, func() error { shut++; return nil }, 10*time.Second, &out, &errOut, home)
		if err != nil {
			t.Fatalf("stopAndWait = %v, want nil (an unreadable pid is a warning)", err)
		}
		if shut != 1 {
			t.Errorf("shutdown called %d times, want 1", shut)
		}
		if !strings.Contains(errOut.String(), "not waiting") {
			t.Errorf("stderr %q does not warn that stop did not wait", errOut.String())
		}
	})

	t.Run("shutdown fails: that error, no wait", func(t *testing.T) {
		w := &fakeWatch{}
		boom := errors.New("boom")
		err := stopAndWait(func() (procWatch, error) { return w, nil }, func() error { return boom }, 10*time.Second, &bytes.Buffer{}, &bytes.Buffer{}, home)
		if !errors.Is(err, boom) || w.waited != 0 || !w.closed {
			t.Errorf("stopAndWait = %v, waited %v, closed %v; want boom, 0, true", err, w.waited, w.closed)
		}
	})
}
