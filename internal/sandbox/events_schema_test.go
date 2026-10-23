package sandbox

import (
	"encoding/json"
	"testing"

	"warden.dev/warden/internal/api/schema"
	"warden.dev/warden/internal/execproto"
)

// TestDestroyEvent_MatchesThePayloadSchema is the contract test for
// sandbox.destroy (internal/api/schema/events/payloads.json): the
// marshalled DestroyEvent validates with and without the optional A06
// §7.4 fields (resource, seccomp_kill; NEW in M2-15), for every resource
// kind exec.proc.wait knows, and the schema still rejects what it must
// (a negative killed_pids, as a hostile executor once produced, an
// unknown kind). M2-15 review round 2: the fields were added to the event
// but not to the schema.
func TestDestroyEvent_MatchesThePayloadSchema(t *testing.T) {
	r, err := schema.Load()
	if err != nil {
		t.Fatal(err)
	}
	base := DestroyEvent{SessionID: "ses_01JAXR8Q7M2V9KTC3F6YH5N0PB", SandboxID: testID, Reason: ReasonTaskEnd, KilledPIDs: 1, DurationMS: 12}
	valid := map[string]DestroyEvent{"plain": base}
	for _, k := range []execproto.ResourceKind{execproto.ResourceMemory, execproto.ResourcePids, execproto.ResourceCPU,
		execproto.ResourceFsize, execproto.ResourceTmpFull, execproto.ResourceDisk} {
		ev := base
		ev.Reason = ReasonResource
		ev.Resource = &execproto.WaitResource{Kind: k, Detail: "cgroup oom_kill"}
		valid["resource "+string(k)] = ev
	}
	sec := base
	sec.Reason, sec.SeccompKill = ReasonExecutorExit, true
	valid["seccomp kill"] = sec
	for name, ev := range valid {
		if err := r.ValidatePayload("sandbox.destroy", ev); err != nil {
			t.Errorf("%s: sandbox.destroy %+v does not validate: %v", name, ev, err)
		}
	}
	for name, raw := range map[string]string{
		"negative killed_pids": `{"sandbox_id":"` + testID + `","reason":"task_end","killed_pids":-1,"duration_ms":0}`,
		"unknown kind":         `{"sandbox_id":"` + testID + `","reason":"resource","killed_pids":0,"duration_ms":0,"resource":{"kind":"gpu"}}`,
		"seccomp_kill string":  `{"sandbox_id":"` + testID + `","reason":"executor_exit","killed_pids":0,"duration_ms":0,"seccomp_kill":"yes"}`,
	} {
		if err := r.ValidatePayload("sandbox.destroy", json.RawMessage(raw)); err == nil {
			t.Errorf("%s: schema accepted %s", name, raw)
		}
	}
	vio := ViolationEvent{SandboxID: testID, Kind: ViolationRootCheck, Detail: "processes survived teardown"}
	if err := r.ValidatePayload("sandbox.violation", vio); err != nil {
		t.Errorf("sandbox.violation %+v does not validate: %v", vio, err)
	}
}
