package proxy

import (
	"context"
	"errors"
)

// Effect is a policy effect (CLAUDE.md §4 "Policy").
type Effect string

// The three effects.
const (
	EffectAllow            Effect = "allow"
	EffectDeny             Effect = "deny"
	EffectApprovalRequired Effect = "approval_required"
)

// Decision is the PDP's answer for one destination (A07 §5.2).
type Decision struct {
	Effect     Effect
	Rule       string // first matched rule; the event's rule
	DecisionID string // id of the policy.decision event; "" when none was written
	Reason     string // human-readable, for the 403 body and logs
	ApprovalID string // set with EffectApprovalRequired
}

// TaskRef identifies the task a proxy serves (A07 §12).
type TaskRef struct {
	SessionID   string
	TaskID      string
	ExecutionID string
	Key         string // task key, e.g. "implement"
	Class       string
	Mode        string
}

// EgressRequest is what the proxy asks the PDP about a destination that is
// not on the allowlist (A07 §5.3, §12).
type EgressRequest struct {
	Task           TaskRef
	SandboxID      string
	SandboxPurpose string // "task" | "harness" (context.sandbox_purpose, ID-12)
	CallID         string // tool call running in the sandbox; "" when none
	Method         string // CONNECT or the plain HTTP method
	Target         Target
	Allowlist      []string // context.task_egress_allow
}

// Decider is implemented by internal/policy; the proxy never evaluates
// rules itself (A07 §12).
type Decider interface {
	// DecideEgress decides a destination that is not on the allowlist.
	DecideEgress(ctx context.Context, req EgressRequest) (Decision, error)
	// WaitApproval blocks until the approval is resolved or ctx ends; it
	// returns the re-evaluated decision.
	WaitApproval(ctx context.Context, approvalID string) (Decision, error)
}

// RuleStaticDeny is the rule StaticDecider reports. No policy.decision
// event backs it, so its decision_id is null.
const RuleStaticDeny = "platform.egress-static-deny"

// ErrNoApprovals is returned by StaticDecider.WaitApproval.
var ErrNoApprovals = errors.New("proxy: static decider has no approvals (PDP arrives in M3)")

// StaticDecider is the M2 decider (D-032 candidate): the allowlist fast
// path in the proxy allows, and every destination that reaches the decider
// is denied. It never asks for an approval, so nothing is held. The PDP
// hand-off and the 120 s hold (A07 §5.2 step 4, §6) replace it in M3.
type StaticDecider struct{}

// DecideEgress denies.
func (StaticDecider) DecideEgress(context.Context, EgressRequest) (Decision, error) {
	return Decision{Effect: EffectDeny, Rule: RuleStaticDeny, Reason: "not on the task allowlist"}, nil
}

// WaitApproval fails: StaticDecider never returns approval_required.
func (StaticDecider) WaitApproval(context.Context, string) (Decision, error) {
	return Decision{Effect: EffectDeny, Rule: RuleStaticDeny}, ErrNoApprovals
}
