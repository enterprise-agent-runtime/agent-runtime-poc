package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"warden.dev/warden/internal/api"
	"warden.dev/warden/internal/platform"
)

// spawnHelperEnv turns this test binary into a stand-in wardend that exits
// with status 3 at once, as a wardend that lost the endpoint to another one
// does. It is started through platform.SpawnDaemon, the path "warden daemon
// start" uses (C-45, D-006; INV-H allows process starters only there).
const spawnHelperEnv = "WARDEN_TEST_SPAWN_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(spawnHelperEnv) == "exit3" {
		os.Exit(3)
	}
	os.Exit(m.Run())
}

// TestWaitUp_RealChildThatExits3 guards WH-4 (WH-1+2 review, reproduced in
// WSL): on Unix the spawned wardend was released, not reaped, so after it
// exited 3 it stayed a zombie that Alive reported as running. waitUp never
// started its grace period, a failed start cost 16 s and reported "did not
// come up within 15 s", and a lost race printed "started wardend (pid N)"
// for a process that had exited. TestWaitUp_ToleratesLostRace injects the
// exit probe and cannot see this; this test uses a real child through the
// real spawn path. It asserts the outcome, not the elapsed time.
func TestWaitUp_RealChildThatExits3(t *testing.T) {
	home := t.TempDir()
	t.Setenv(platform.EnvHome, home)
	t.Setenv(spawnHelperEnv, "exit3")
	l := platform.Layout{Home: home}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pause := func() { time.Sleep(20 * time.Millisecond) }

	// Nobody answers: the failure names the exit, not a timeout.
	d, err := platform.SpawnDaemon(exe, []string{"-test.run=^$"}, filepath.Join(home, "wardend.log"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_, err = waitUp(context.Background(), l, failingDial(errNotRunningDial), d.Exited, 250, 5, pause)
	if err == nil || !strings.Contains(err.Error(), "exited during start") || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("waitUp for a child that exited 3 = %v; want \"exited during start (exit status 3...)\"", err)
	}

	// Another daemon answers once ours has exited (a lost race): ours is
	// false, so startDaemon does not claim it started a process that exited.
	d2, err := platform.SpawnDaemon(exe, []string{"-test.run=^$"}, filepath.Join(home, "wardend.log"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	winner := func(context.Context, platform.Layout) (*api.Client, error) {
		if _, gone := d2.Exited(); gone {
			return nil, nil
		}
		return nil, errNotRunningDial
	}
	ours, err := waitUp(context.Background(), l, winner, d2.Exited, 250, 5, pause)
	if err != nil || ours {
		t.Fatalf("lost race: ours %v err %v; want attached to the other daemon, not ours", ours, err)
	}
}
