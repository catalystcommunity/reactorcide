package models

import (
	"fmt"
)

// Draft pull request settings (plan item D4). A project value of "inherit"
// uses the organization value. The organization default is "run", the
// behavior before the setting.
const (
	DraftPRsInherit          = "inherit"
	DraftPRsRun              = "run"
	DraftPRsSkip             = "skip"
	DraftPRsSkipWhenShedding = "skip_when_shedding"
)

// IsValidDraftPRsOrgSetting reports whether value may be stored on an
// organization.
func IsValidDraftPRsOrgSetting(value string) bool {
	return value == DraftPRsRun || value == DraftPRsSkip || value == DraftPRsSkipWhenShedding
}

// IsValidDraftPRsProjectSetting reports whether value may be stored on a
// project.
func IsValidDraftPRsProjectSetting(value string) bool {
	return value == DraftPRsInherit || IsValidDraftPRsOrgSetting(value)
}

// EffectiveDraftPRs resolves a project setting against its organization
// setting. A missing or unknown value runs drafts, the behavior before the
// setting.
func EffectiveDraftPRs(projectSetting, orgSetting string) string {
	if projectSetting != DraftPRsInherit && IsValidDraftPRsOrgSetting(projectSetting) {
		return projectSetting
	}
	if IsValidDraftPRsOrgSetting(orgSetting) {
		return orgSetting
	}
	return DraftPRsRun
}

// Shed levels (plan item D4). The level in force for an organization is the
// higher of the global level and the organization level.
const (
	ShedLevelNormal   = "normal"
	ShedLevelDegraded = "degraded"
	ShedLevelCritical = "critical"
)

// GlobalSettingShedLevel is the global_settings key of the global shed level.
// The value is a JSON string.
const GlobalSettingShedLevel = "shed_level"

// IsValidShedLevel reports whether value is a known level.
func IsValidShedLevel(value string) bool {
	return value == ShedLevelNormal || value == ShedLevelDegraded || value == ShedLevelCritical
}

func shedRank(level string) int {
	switch level {
	case ShedLevelDegraded:
		return 1
	case ShedLevelCritical:
		return 2
	}
	return 0
}

// MaxShedLevel returns the higher of two levels. An unknown level counts as
// normal.
func MaxShedLevel(a, b string) string {
	if shedRank(b) > shedRank(a) {
		return b
	}
	if shedRank(a) == 0 {
		return ShedLevelNormal
	}
	return a
}

// ShedActions are the actions of one shed level.
type ShedActions struct {
	// SkipDrafts skips draft pull requests of projects whose draft_prs is
	// skip_when_shedding.
	SkipDrafts bool `json:"skip_drafts" yaml:"skip_drafts"`
	// PauseSchedules makes schedule fires fail with a shed reason instead of
	// starting work.
	PauseSchedules bool `json:"pause_schedules" yaml:"pause_schedules"`
	// LowerPRPriority gives pull request eval jobs a lower priority than
	// pushes.
	LowerPRPriority bool `json:"lower_pr_priority" yaml:"lower_pr_priority"`
	// SkipAdvisory skips advisory workflows (read through
	// shedding.SkipAdvisory).
	SkipAdvisory bool `json:"skip_advisory" yaml:"skip_advisory"`
	// RefuseManualRuns refuses manual runs from callers that are not
	// administrators.
	RefuseManualRuns bool `json:"refuse_manual_runs" yaml:"refuse_manual_runs"`
}

// Any reports whether the level does something.
func (a ShedActions) Any() bool {
	return a.SkipDrafts || a.PauseSchedules || a.LowerPRPriority || a.SkipAdvisory || a.RefuseManualRuns
}

// ShedActionsConfig is the actions of each level for one organization. The
// normal level has no actions.
type ShedActionsConfig struct {
	Degraded ShedActions `json:"degraded" yaml:"degraded"`
	Critical ShedActions `json:"critical" yaml:"critical"`
}

// DefaultShedActions returns the built-in actions. Critical has every action
// of degraded and adds skip_advisory and refuse_manual_runs.
func DefaultShedActions() ShedActionsConfig {
	degraded := ShedActions{SkipDrafts: true, PauseSchedules: true, LowerPRPriority: true}
	critical := degraded
	critical.SkipAdvisory = true
	critical.RefuseManualRuns = true
	return ShedActionsConfig{Degraded: degraded, Critical: critical}
}

// ForLevel returns the actions of a level. Normal has no actions.
func (c ShedActionsConfig) ForLevel(level string) ShedActions {
	switch level {
	case ShedLevelDegraded:
		return c.Degraded
	case ShedLevelCritical:
		return c.Critical
	}
	return ShedActions{}
}

// ShedState is the shed level in force for one organization and its actions.
type ShedState struct {
	GlobalLevel string `json:"global_level"`
	OrgLevel    string `json:"org_level,omitempty"`
	// Level is the higher of GlobalLevel and OrgLevel.
	Level string `json:"level"`
	// Source is "global", "org" or "" (normal). When both levels are the
	// same and not normal, it is "org".
	Source  string      `json:"source,omitempty"`
	Actions ShedActions `json:"actions"`
	// Config is the actions of each level for the organization.
	Config ShedActionsConfig `json:"config"`
}

// EffectiveShedState computes the state in force. actions nil uses the
// built-in actions.
func EffectiveShedState(globalLevel, orgLevel string, actions *ShedActionsConfig) ShedState {
	if !IsValidShedLevel(globalLevel) {
		globalLevel = ShedLevelNormal
	}
	if !IsValidShedLevel(orgLevel) {
		orgLevel = ShedLevelNormal
	}
	config := DefaultShedActions()
	if actions != nil {
		config = *actions
	}
	level := MaxShedLevel(globalLevel, orgLevel)
	state := ShedState{GlobalLevel: globalLevel, OrgLevel: orgLevel, Level: level, Config: config, Actions: config.ForLevel(level)}
	switch {
	case level == ShedLevelNormal:
	case shedRank(orgLevel) >= shedRank(globalLevel):
		state.Source = "org"
	default:
		state.Source = "global"
	}
	return state
}

// Describe returns a short text for statuses and errors, for example
// "shed level degraded (global)".
func (s ShedState) Describe() string {
	if s.Source == "" {
		return "shed level " + s.Level
	}
	return fmt.Sprintf("shed level %s (%s)", s.Level, s.Source)
}
