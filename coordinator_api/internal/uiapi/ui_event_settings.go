package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// projectDetail is projectToDetail plus the event settings in force after the
// organization values are applied. A store that cannot resolve them leaves
// the effective fields absent.
func (s *UiService) projectDetail(ctx context.Context, project *models.Project) csilapi.ProjectDetail {
	detail := projectToDetail(project)
	if settings, ok := s.deps.Store.(store.EventSettingsStore); ok {
		if value, err := settings.EffectiveDynamicJobs(ctx, project.OwnershipOrgID(), &project.ProjectID); err == nil {
			detail.EffectiveDynamicJobs = optionalText(value)
		}
		if value, err := settings.EffectiveDuplicateEvents(ctx, project.ProjectID); err == nil {
			detail.EffectiveDuplicateEvents = optionalText(value)
		}
	}
	if drafts, ok := s.deps.Store.(store.DraftShedStore); ok {
		if value, err := drafts.EffectiveDraftPRs(ctx, project.ProjectID); err == nil {
			detail.EffectiveDraftPrs = optionalText(value)
		}
	}
	if retries, ok := s.deps.Store.(store.InfraRetryStore); ok {
		if value, err := retries.EffectiveInfraRetries(ctx, project.OwnershipOrgID(), &project.ProjectID); err == nil {
			effective := int64(value)
			detail.EffectiveInfraRetries = &effective
		}
	}
	if egress, ok := s.deps.Store.(store.EgressControlStore); ok {
		if value, err := egress.EffectiveEgressControl(ctx, project.OwnershipOrgID(), &project.ProjectID, ""); err == nil {
			detail.EffectiveEgressControl = optionalBool(value)
		}
	}
	return detail
}

// applyProjectEventSettings validates and applies the event settings of an
// update request. It returns the audit details of what changed (nil when
// nothing did).
func applyProjectEventSettings(project *models.Project, duplicateEvents, dynamicJobs *string) (models.JSONB, error) {
	changed := models.JSONB{}
	if duplicateEvents != nil {
		switch *duplicateEvents {
		case models.DuplicateEventsInherit, models.DuplicateEventsRun, models.DuplicateEventsSkip:
		default:
			return nil, NewServiceError("invalid_argument", "duplicate_events must be inherit, run or skip")
		}
		if project.DuplicateEvents != *duplicateEvents {
			changed["duplicate_events"] = models.JSONB{"from": project.DuplicateEvents, "to": *duplicateEvents}
			project.DuplicateEvents = *duplicateEvents
		}
	}
	if dynamicJobs != nil {
		if !models.IsValidDynamicJobsProjectSetting(*dynamicJobs) {
			return nil, NewServiceError("invalid_argument", "dynamic_jobs must be inherit, allow or deny")
		}
		if project.DynamicJobs != *dynamicJobs {
			changed["dynamic_jobs"] = models.JSONB{"from": project.DynamicJobs, "to": *dynamicJobs}
			project.DynamicJobs = *dynamicJobs
		}
	}
	if len(changed) == 0 {
		return nil, nil
	}
	return changed, nil
}

// applyOrgEventSettings is the organization counterpart of
// applyProjectEventSettings. An organization has no "inherit".
func applyOrgEventSettings(org *models.Organization, duplicateEvents, dynamicJobs *string) (models.JSONB, error) {
	changed := models.JSONB{}
	if duplicateEvents != nil {
		if *duplicateEvents != models.DuplicateEventsRun && *duplicateEvents != models.DuplicateEventsSkip {
			return nil, NewServiceError("invalid_argument", "duplicate_events must be run or skip")
		}
		if org.DuplicateEvents != *duplicateEvents {
			changed["duplicate_events"] = models.JSONB{"from": org.DuplicateEvents, "to": *duplicateEvents}
			org.DuplicateEvents = *duplicateEvents
		}
	}
	if dynamicJobs != nil {
		if !models.IsValidDynamicJobsOrgSetting(*dynamicJobs) {
			return nil, NewServiceError("invalid_argument", "dynamic_jobs must be allow or deny")
		}
		if org.DynamicJobs != *dynamicJobs {
			changed["dynamic_jobs"] = models.JSONB{"from": org.DynamicJobs, "to": *dynamicJobs}
			org.DynamicJobs = *dynamicJobs
		}
	}
	if len(changed) == 0 {
		return nil, nil
	}
	return changed, nil
}

// applyProjectDraftPRs validates and applies the draft_prs value of an update
// request and adds the change to the audit details.
func applyProjectDraftPRs(project *models.Project, draftPRs *string, changed models.JSONB) (models.JSONB, error) {
	if draftPRs == nil {
		return changed, nil
	}
	if !models.IsValidDraftPRsProjectSetting(*draftPRs) {
		return nil, NewServiceError("invalid_argument", "draft_prs must be inherit, run, skip or skip_when_shedding")
	}
	if project.DraftPRs == *draftPRs {
		return changed, nil
	}
	if changed == nil {
		changed = models.JSONB{}
	}
	changed["draft_prs"] = models.JSONB{"from": project.DraftPRs, "to": *draftPRs}
	project.DraftPRs = *draftPRs
	return changed, nil
}
