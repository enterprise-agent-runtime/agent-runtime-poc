package platform

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// spawnHelperEnv turns this test binary into a stand-in wardend that exits
// with status 3 at once, as a wardend that lost the endpoint does (WH-4).
// Spawning the test binary itself goes through SpawnDaemon, the one
// process starter this package may use (INV-H, spawn*.go), and starts no
// agent-requested process.
const spawnHelperEnv = "WARDEN_TEST_SPAWN_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(spawnHelperEnv) == "exit3" {
		os.Exit(3)
	}
	os.Exit(m.Run())
}

// TestSpawnDaemon_ReapsChildThatExits3 guards WH-4 (WH-1+2 review): on
// Unix SpawnDaemon released the child instead of reaping it, so a wardend
// that exited at once stayed a zombie, kill(pid, 0) kept succeeding and
// Alive stayed true. waitUp then never started its grace period: a failed
// start took 16 s and said "did not come up within 15 s" instead of "exited
// during start". Uses the real spawn path with a real child that exits 3;
// the deadline only bounds the test, nothing is asserted about time.
func TestSpawnDaemon_ReapsChildThatExits3(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	t.Setenv(spawnHelperEnv, "exit3")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	d, err := SpawnDaemon(exe, []string{"-test.run=^$"}, filepath.Join(t.TempDir(), "wardend.log"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, gone := d.Exited()
		if gone {
			if st.Code != 3 || st.String() != "exit status 3" {
				t.Fatalf("child exited with %+v (%s); want exit status 3", st, st)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the child (pid %d) exits with status 3 at once but is still running after 10 s (Alive %v): not reaped", d.PID, Alive(d.PID))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if Alive(d.PID) {
		t.Fatalf("pid %d still Alive after its exit was seen: a zombie", d.PID)
	}
	if st, gone := d.Exited(); !gone || st.Code != 3 {
		t.Fatalf("second Exited = %+v %v; want the recorded exit status 3", st, gone)
	}
}
