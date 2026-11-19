package handlers

import (
	"context"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

const defaultJobCommandForbidden = "default_job_command replaces the workflow evaluation; only a global administrator " +
	"(for example the admin API token) can set it"

// canSetDefaultJobCommand reports whether the caller may set a project
// default_job_command (plan item G3): a global principal with
// projects:manage whose roles permit it (the admin API token), or a user
// with the admin role.
func canSetDefaultJobCommand(ctx context.Context, appStore store.Store) bool {
	if principal := checkauth.GetPrincipalFromContext(ctx); principal != nil {
		return principalAllowsGlobal(ctx, appStore, tokencaps.ProjectsManage)
	}
	user := checkauth.GetUserFromContext(ctx)
	if user == nil {
		return false
	}
	for _, role := range user.Roles {
		if role == "admin" {
			return true
		}
	}
	return false
}

// applyProjectEventSettingsREST validates and sets the project event
// settings of a REST update.
func applyProjectEventSettingsREST(project *models.Project, req UpdateProjectRequest) error {
	if req.DuplicateEvents != nil {
		switch *req.DuplicateEvents {
		case models.DuplicateEventsInherit, models.DuplicateEventsRun, models.DuplicateEventsSkip:
			project.DuplicateEvents = *req.DuplicateEvents
		default:
			return errors.New("duplicate_events must be inherit, run or skip")
		}
	}
	if req.DynamicJobs != nil {
		if !models.IsValidDynamicJobsProjectSetting(*req.DynamicJobs) {
			return errors.New("dynamic_jobs must be inherit, allow or deny")
		}
		project.DynamicJobs = *req.DynamicJobs
	}
	if req.DraftPRs != nil {
		if !models.IsValidDraftPRsProjectSetting(*req.DraftPRs) {
			return errors.New("draft_prs must be inherit, run, skip or skip_when_shedding")
		}
		project.DraftPRs = *req.DraftPRs
	}
	return nil
}

// applyProjectSettingsREST validates and sets infra_retries, egress_control,
// and the event settings of a REST create or update.
func applyProjectSettingsREST(project *models.Project, req UpdateProjectRequest) error {
	if value, set, err := parseProjectInfraRetries(req.InfraRetries); err != nil {
		return err
	} else if set {
		project.InfraRetries = value
	}
	if req.EgressControl != nil {
		project.EgressControl = *req.EgressControl
	}
	return applyProjectEventSettingsREST(project, req)
}

// projectEventSettingDetails returns the event settings that a request set,
// with their new values, for an audit event.
func projectEventSettingDetails(project *models.Project, req UpdateProjectRequest) models.JSONB {
	details := models.JSONB{}
	if len(req.InfraRetries) > 0 {
		if project.InfraRetries == nil {
			details["infra_retries"] = "inherit"
		} else {
			details["infra_retries"] = *project.InfraRetries
		}
	}
	if req.EgressControl != nil {
		details["egress_control"] = project.EgressControl
	}
	if req.DuplicateEvents != nil {
		details["duplicate_events"] = project.DuplicateEvents
	}
	if req.DynamicJobs != nil {
		details["dynamic_jobs"] = project.DynamicJobs
	}
	if req.DraftPRs != nil {
		details["draft_prs"] = project.DraftPRs
	}
	return details
}

// projectEventSettingChanges returns the event settings that changed, each
// as {"from": old, "to": new}. It returns nil when nothing changed.
func projectEventSettingChanges(before, after *models.Project, req UpdateProjectRequest) models.JSONB {
	retries := func(value *int) any {
		if value == nil {
			return "inherit"
		}
		return *value
	}
	type change struct {
		key      string
		set      bool
		from, to any
	}
	changes := models.JSONB{}
	for _, c := range []change{
		{"infra_retries", len(req.InfraRetries) > 0, retries(before.InfraRetries), retries(after.InfraRetries)},
		{"egress_control", req.EgressControl != nil, before.EgressControl, after.EgressControl},
		{"duplicate_events", req.DuplicateEvents != nil, before.DuplicateEvents, after.DuplicateEvents},
		{"dynamic_jobs", req.DynamicJobs != nil, before.DynamicJobs, after.DynamicJobs},
		{"draft_prs", req.DraftPRs != nil, before.DraftPRs, after.DraftPRs},
	} {
		if c.set && c.from != c.to {
			changes[c.key] = map[string]any{"from": c.from, "to": c.to}
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return changes
}
