package approvals

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RunAuthorityEnv is the reserved fact that carries the run gate and override
// decision the coordinator made for an eval job. It is absent for a run that
// is neither gated nor overridden.
const RunAuthorityEnv = "REACTORCIDE_RUN_AUTHORITY"

// OverrideRuleID is the rule ID recorded on a workflow that runs head CI
// because of an override-ci approval, not because of a policy rule.
const OverrideRuleID = "override-ci"

// RunAuthority is the decoded REACTORCIDE_RUN_AUTHORITY fact.
type RunAuthority struct {
	// Gated is true when the run gate held the pull request and an approval
	// released it. A gated run uses trusted base CI only, unless Override is
	// set.
	Gated bool `json:"gated,omitempty"`
	// Profile replaces the default profile of every base workflow. Empty
	// keeps the normal profile.
	Profile string `json:"profile,omitempty"`
	// RunApprovalID is the action that released the gate.
	RunApprovalID string `json:"run_approval_id,omitempty"`
	// Override is set when an override-ci approval lets the head's own CI run.
	Override *RunOverride `json:"override,omitempty"`
}

// RunOverride is an override-ci grant.
type RunOverride struct {
	ApprovalID string `json:"approval_id"`
	Person     string `json:"person"`
	Profile    string `json:"profile"`
}

// Encode returns the fact value, or "" when nothing is gated or overridden.
func (a RunAuthority) Encode() (string, error) {
	if !a.Gated && a.Override == nil && a.Profile == "" {
		return "", nil
	}
	encoded, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// DecodeRunAuthority reads the fact value. An empty value is no authority.
func DecodeRunAuthority(value string) (RunAuthority, error) {
	authority := RunAuthority{}
	value = strings.TrimSpace(value)
	if value == "" {
		return authority, nil
	}
	if err := json.Unmarshal([]byte(value), &authority); err != nil {
		return RunAuthority{}, fmt.Errorf("run authority fact failed validation: %w", err)
	}
	if authority.Override != nil && (authority.Override.ApprovalID == "" || authority.Override.Profile == "") {
		return RunAuthority{}, fmt.Errorf("run authority override needs an approval and a profile")
	}
	return authority, nil
}
