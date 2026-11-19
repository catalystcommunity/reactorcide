package uiapi

import (
	"context"
	"errors"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/schedule"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// Schedule fire listing limits.
const (
	defaultScheduleFireLimit = 20
	maxScheduleFireLimit     = 200
)

// scheduleNow is the clock for next-fire computation; tests replace it.
var scheduleNow = func() time.Time { return time.Now().UTC() }

// The token capabilities of the schedule operations. A schedule is
// project configuration: projects:read to read, projects:manage to change.
var (
	scheduleReadCapabilities  = []string{tokencaps.ProjectsRead, tokencaps.ProjectsManage}
	scheduleWriteCapabilities = []string{tokencaps.ProjectsManage}
)

func (s *UiService) scheduleStore() (store.ScheduleStore, error) {
	ss, ok := s.deps.Store.(store.ScheduleStore)
	if !ok {
		return nil, NewServiceError("internal", "schedules are not available on this coordinator")
	}
	return ss, nil
}

func scheduleToSummary(sched *models.CISchedule) csilapi.ScheduleSummary {
	return csilapi.ScheduleSummary{
		ScheduleId:  sched.ScheduleID,
		ProjectId:   sched.ProjectID,
		Name:        sched.Name,
		Branch:      sched.Branch,
		WorkflowRef: sched.WorkflowRef,
		Cron:        sched.Cron,
		Timezone:    sched.Timezone,
		Enabled:     sched.Enabled,
		NextFireAt:  formatTimePtr(sched.NextFireAt),
		LastFireAt:  formatTimePtr(sched.LastFireAt),
		LastEventId: sched.LastEventID,
		LastError:   sched.LastError,
		CreatedAt:   formatTime(sched.CreatedAt),
		UpdatedAt:   formatTime(sched.UpdatedAt),
	}
}

// validateScheduleInput checks every user-supplied part of a schedule and
// returns the next fire time (nil when disabled).
func validateScheduleInput(sched *models.CISchedule) (*time.Time, error) {
	if err := schedule.ValidateDefinition(sched.Name, sched.Branch, sched.WorkflowRef); err != nil {
		return nil, NewServiceError("invalid_argument", err.Error())
	}
	if err := schedule.Validate(sched.Cron, sched.Timezone); err != nil {
		return nil, NewServiceError("invalid_argument", err.Error())
	}
	if !sched.Enabled {
		return nil, nil
	}
	next, err := schedule.NextFireAfter(sched.Cron, sched.Timezone, scheduleNow())
	if err != nil {
		return nil, NewServiceError("invalid_argument", err.Error())
	}
	return &next, nil
}

func mapScheduleWriteErr(err error) error {
	if errors.Is(err, store.ErrAlreadyExists) {
		return NewServiceError("conflict", "a schedule with this name already exists in the project")
	}
	return mapStoreErr(err, "schedule not found")
}

// loadManagedSchedule loads a schedule and checks that the caller manages its
// project.
// loadManagedSchedule loads a schedule for a project owner. A token also
// needs one of capabilities for the organization of the schedule.
func (s *UiService) loadManagedSchedule(ctx context.Context, scheduleID string, capabilities ...string) (context.Context, *models.User, store.ScheduleStore, *models.CISchedule, error) {
	ctx, id, user, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return ctx, nil, nil, nil, authErr
	}
	if err := requireNonEmpty("schedule_id", scheduleID, 64); err != nil {
		return ctx, nil, nil, nil, err
	}
	ss, err := s.scheduleStore()
	if err != nil {
		return ctx, nil, nil, nil, err
	}
	sched, err := ss.GetSchedule(ctx, scheduleID)
	if err != nil {
		return ctx, nil, nil, nil, mapStoreErr(err, "schedule not found")
	}
	if err := requireTokenGrant(id, &sched.OrgID, capabilities...); err != nil {
		return ctx, nil, nil, nil, err
	}
	if err := s.deps.Resolver.RequireProjectOwner(ctx, id, sched.ProjectID); err != nil {
		return ctx, nil, nil, nil, mapPermissionErr(err)
	}
	return ctx, user, ss, sched, nil
}

func (s *UiService) ListSchedules(ctx context.Context, req csilapi.ListSchedulesRequest) (csilapi.ListSchedulesResponse, error) {
	ctx, id, _, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return csilapi.ListSchedulesResponse{}, authErr
	}
	if err := requireNonEmpty("project_id", req.ProjectId, 64); err != nil {
		return csilapi.ListSchedulesResponse{}, err
	}
	project, err := s.deps.Store.GetProjectByID(ctx, req.ProjectId)
	if err != nil {
		return csilapi.ListSchedulesResponse{}, mapStoreErr(err, "project not found")
	}
	orgID := project.OwnershipOrgID()
	if err := requireTokenGrant(id, &orgID, scheduleReadCapabilities...); err != nil {
		return csilapi.ListSchedulesResponse{}, err
	}
	if err := s.deps.Resolver.RequireProjectOwner(ctx, id, req.ProjectId); err != nil {
		return csilapi.ListSchedulesResponse{}, mapPermissionErr(err)
	}
	ss, err := s.scheduleStore()
	if err != nil {
		return csilapi.ListSchedulesResponse{}, err
	}
	schedules, err := ss.ListSchedulesByProject(ctx, req.ProjectId)
	if err != nil {
		return csilapi.ListSchedulesResponse{}, mapStoreErr(err, "project not found")
	}
	out := make([]csilapi.ScheduleSummary, 0, len(schedules))
	for i := range schedules {
		out = append(out, scheduleToSummary(&schedules[i]))
	}
	return csilapi.ListSchedulesResponse{Schedules: out}, nil
}

func (s *UiService) CreateSchedule(ctx context.Context, req csilapi.CreateScheduleRequest) (csilapi.CreateScheduleResponse, error) {
	ctx, id, user, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return csilapi.CreateScheduleResponse{}, authErr
	}
	if err := requireNonEmpty("project_id", req.ProjectId, 64); err != nil {
		return csilapi.CreateScheduleResponse{}, err
	}
	project, err := s.deps.Store.GetProjectByID(ctx, req.ProjectId)
	if err != nil {
		return csilapi.CreateScheduleResponse{}, mapStoreErr(err, "project not found")
	}
	orgID := project.OwnershipOrgID()
	if err := requireTokenGrant(id, &orgID, scheduleWriteCapabilities...); err != nil {
		return csilapi.CreateScheduleResponse{}, err
	}
	if err := s.deps.Resolver.RequireProjectOwner(ctx, id, req.ProjectId); err != nil {
		return csilapi.CreateScheduleResponse{}, mapPermissionErr(err)
	}
	ss, err := s.scheduleStore()
	if err != nil {
		return csilapi.CreateScheduleResponse{}, err
	}
	sched := &models.CISchedule{
		OrgID:       project.OwnershipOrgID(),
		ProjectID:   project.ProjectID,
		Name:        req.Name,
		Branch:      req.Branch,
		WorkflowRef: req.WorkflowRef,
		Cron:        req.Cron,
		Timezone:    "UTC",
		Enabled:     true,
	}
	if req.Timezone != nil && *req.Timezone != "" {
		sched.Timezone = *req.Timezone
	}
	if req.Enabled != nil {
		sched.Enabled = *req.Enabled
	}
	next, err := validateScheduleInput(sched)
	if err != nil {
		return csilapi.CreateScheduleResponse{}, err
	}
	sched.NextFireAt = next
	if user != nil {
		sched.CreatedBy = &user.UserID
	}
	if err := ss.CreateSchedule(ctx, sched); err != nil {
		return csilapi.CreateScheduleResponse{}, mapScheduleWriteErr(err)
	}
	stored, err := ss.GetSchedule(ctx, sched.ScheduleID)
	if err != nil {
		return csilapi.CreateScheduleResponse{}, NewServiceError("internal", "failed to read the stored schedule")
	}
	audit.RecordUser(ctx, s.deps.Store, user, project.OwnershipOrgID(), "schedule.created", "schedule", stored.ScheduleID, scheduleAuditDetails(stored))
	return csilapi.CreateScheduleResponse{Schedule: scheduleToSummary(stored)}, nil
}

func (s *UiService) UpdateSchedule(ctx context.Context, req csilapi.UpdateScheduleRequest) (csilapi.UpdateScheduleResponse, error) {
	ctx, user, ss, sched, err := s.loadManagedSchedule(ctx, req.ScheduleId, scheduleWriteCapabilities...)
	if err != nil {
		return csilapi.UpdateScheduleResponse{}, err
	}
	before := *sched
	if req.Name != nil {
		sched.Name = *req.Name
	}
	if req.Branch != nil {
		sched.Branch = *req.Branch
	}
	if req.WorkflowRef != nil {
		sched.WorkflowRef = *req.WorkflowRef
	}
	if req.Cron != nil {
		sched.Cron = *req.Cron
	}
	if req.Timezone != nil {
		sched.Timezone = *req.Timezone
		if sched.Timezone == "" {
			sched.Timezone = "UTC"
		}
	}
	if req.Enabled != nil {
		sched.Enabled = *req.Enabled
	}
	next, err := validateScheduleInput(sched)
	if err != nil {
		return csilapi.UpdateScheduleResponse{}, err
	}
	// Keep the planned time when nothing that decides it changed, so an edit
	// of the name does not move the next fire. A re-enabled schedule starts
	// from now: the disabled period is not caught up.
	timingChanged := before.Cron != sched.Cron || before.Timezone != sched.Timezone || before.Enabled != sched.Enabled
	if timingChanged || before.NextFireAt == nil {
		sched.NextFireAt = next
	}
	if !sched.Enabled {
		sched.NextFireAt = nil
	}
	if err := ss.UpdateSchedule(ctx, sched); err != nil {
		return csilapi.UpdateScheduleResponse{}, mapScheduleWriteErr(err)
	}
	stored, err := ss.GetSchedule(ctx, sched.ScheduleID)
	if err != nil {
		return csilapi.UpdateScheduleResponse{}, NewServiceError("internal", "failed to read the stored schedule")
	}
	details := scheduleAuditDetails(stored)
	details["previous"] = scheduleAuditDetails(&before)
	audit.RecordUser(ctx, s.deps.Store, user, stored.OrgID, "schedule.updated", "schedule", stored.ScheduleID, details)
	return csilapi.UpdateScheduleResponse{Schedule: scheduleToSummary(stored)}, nil
}

func (s *UiService) DeleteSchedule(ctx context.Context, req csilapi.DeleteScheduleRequest) (csilapi.DeleteScheduleResponse, error) {
	ctx, user, ss, sched, err := s.loadManagedSchedule(ctx, req.ScheduleId, scheduleWriteCapabilities...)
	if err != nil {
		return csilapi.DeleteScheduleResponse{}, err
	}
	if err := ss.DeleteSchedule(ctx, sched.ScheduleID); err != nil {
		return csilapi.DeleteScheduleResponse{}, mapStoreErr(err, "schedule not found")
	}
	audit.RecordUser(ctx, s.deps.Store, user, sched.OrgID, "schedule.deleted", "schedule", sched.ScheduleID, scheduleAuditDetails(sched))
	return csilapi.DeleteScheduleResponse{Ok: true}, nil
}

func (s *UiService) ListScheduleFires(ctx context.Context, req csilapi.ListScheduleFiresRequest) (csilapi.ListScheduleFiresResponse, error) {
	ctx, _, ss, sched, err := s.loadManagedSchedule(ctx, req.ScheduleId, scheduleReadCapabilities...)
	if err != nil {
		return csilapi.ListScheduleFiresResponse{}, err
	}
	limit := defaultScheduleFireLimit
	if req.Limit != nil {
		if *req.Limit < 1 || *req.Limit > maxScheduleFireLimit {
			return csilapi.ListScheduleFiresResponse{}, NewServiceError("invalid_argument", "limit must be between 1 and 200")
		}
		limit = int(*req.Limit)
	}
	fires, err := ss.ListScheduleFires(ctx, sched.ScheduleID, limit)
	if err != nil {
		return csilapi.ListScheduleFiresResponse{}, mapStoreErr(err, "schedule not found")
	}
	out := make([]csilapi.ScheduleFireSummary, 0, len(fires))
	for _, fire := range fires {
		out = append(out, csilapi.ScheduleFireSummary{
			FireId:      fire.FireID,
			ScheduleId:  fire.ScheduleID,
			FireTime:    formatTime(fire.FireTime),
			Status:      fire.Status,
			EventId:     fire.EventID,
			Error:       fire.Error,
			CompletedAt: formatTimePtr(fire.CompletedAt),
		})
	}
	return csilapi.ListScheduleFiresResponse{Fires: out}, nil
}

func scheduleAuditDetails(sched *models.CISchedule) models.JSONB {
	return models.JSONB{
		"project_id":   sched.ProjectID,
		"name":         sched.Name,
		"branch":       sched.Branch,
		"workflow_ref": sched.WorkflowRef,
		"cron":         sched.Cron,
		"timezone":     sched.Timezone,
		"enabled":      sched.Enabled,
	}
}
