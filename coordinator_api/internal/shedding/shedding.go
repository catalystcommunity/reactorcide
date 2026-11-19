// Package shedding reads the shed level in force and decides what work the
// coordinator skips (plan item D4). Shedding is overload protection: it never
// gives one tenant more capacity than another (owner decision D10).
//
// Every read fails open: when the store cannot tell the level, the level is
// normal, so a database problem never stops work by itself.
package shedding

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/sirupsen/logrus"
)

// State returns the shed state in force for orgID. A store without shed
// settings, or a failed read, returns the normal state.
func State(ctx context.Context, value any, orgID string) models.ShedState {
	normal := models.EffectiveShedState(models.ShedLevelNormal, models.ShedLevelNormal, nil)
	shed, ok := value.(store.DraftShedStore)
	if !ok {
		return normal
	}
	state, err := shed.GetShedState(ctx, orgID)
	if err != nil {
		logrus.WithError(err).WithField("org_id", orgID).Warn("Could not read the shed level; using normal")
		return normal
	}
	return state
}

// SkipAdvisory reports whether advisory workflows of orgID must be skipped
// at the level in force. The advisory workflow code (required set, plan item
// A1) reads it before it starts an advisory workflow, and records the reason
// from AdvisorySkipReason.
func SkipAdvisory(ctx context.Context, value any, orgID string) bool {
	return State(ctx, value, orgID).Actions.SkipAdvisory
}

// AdvisorySkipReason is the decision reason text of a skipped advisory
// workflow.
func AdvisorySkipReason(state models.ShedState) string {
	return fmt.Sprintf("advisory workflow skipped: %s", state.Describe())
}

// DraftDecision decides whether a draft pull request runs. draftPRs is the
// effective draft_prs setting. It returns skip=true and the reason text when
// the draft does not run, and the reason kind (draft or shed).
func DraftDecision(draftPRs string, state models.ShedState) (skip bool, kind, reason string) {
	switch draftPRs {
	case models.DraftPRsSkip:
		return true, models.SkipReasonDraft, "draft pull request (draft_prs: skip)"
	case models.DraftPRsSkipWhenShedding:
		if state.Actions.SkipDrafts {
			return true, models.SkipReasonShed, fmt.Sprintf("draft pull request while %s (draft_prs: skip_when_shedding)", state.Describe())
		}
	}
	return false, "", ""
}

// ScheduleRefusal returns a non-empty reason when the level in force pauses
// schedules.
func ScheduleRefusal(state models.ShedState) string {
	if !state.Actions.PauseSchedules {
		return ""
	}
	return fmt.Sprintf("schedules are paused: %s", state.Describe())
}

// ManualRunRefusal returns a non-empty reason when the level in force
// refuses manual runs from a caller that is not an administrator.
func ManualRunRefusal(state models.ShedState, admin bool) string {
	if admin || !state.Actions.RefuseManualRuns {
		return ""
	}
	return fmt.Sprintf("manual runs are refused for non-administrators: %s", state.Describe())
}
