package uiapi

import (
	"context"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/priority"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// Priority lanes, explain, and group priority shifts (docs/priority.md).
// Reading lanes needs a role in the organization. An organization lane needs
// an org admin; a project lane needs a project owner (the same authority as
// ref class rules). A group shift needs an org admin, as other group
// changes do.

type uiPriorityStore interface {
	store.PriorityStore
	GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error)
	GetProjectByID(ctx context.Context, projectID string) (*models.Project, error)
}

// priorityScope authorizes the caller for the scope and returns it.
func (s *UiService) priorityScope(ctx context.Context, organization string, projectID *string, write bool) (context.Context, uiPriorityStore, string, *string, bool, error) {
	ctx, id, _, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return ctx, nil, "", nil, false, err
	}
	lanesStore, ok := s.deps.Store.(uiPriorityStore)
	if !ok {
		return ctx, nil, "", nil, false, NewServiceError("unavailable", "priority lanes are not available")
	}
	org, err := lanesStore.GetOrganizationByName(ctx, organization)
	if err != nil {
		return ctx, nil, "", nil, false, mapStoreErr(err, "organization not found")
	}
	var scopeProject *string
	if projectID != nil && strings.TrimSpace(*projectID) != "" {
		project, err := lanesStore.GetProjectByID(ctx, strings.TrimSpace(*projectID))
		if err != nil || project.OwnershipOrgID() != org.OrgID {
			return ctx, nil, "", nil, false, NewServiceError("not_found", "project not found")
		}
		value := project.ProjectID
		scopeProject = &value
	}
	// A token needs projects:read for the organization (or policies:manage),
	// as in the REST route; a change needs policies:manage
	// (canManageRefClasses).
	if err := requireTokenGrant(id, &org.OrgID, tokencaps.ProjectsRead, tokencaps.PoliciesManage); err != nil {
		return ctx, nil, "", nil, false, err
	}
	role, err := s.deps.Resolver.EffectiveRoleForOrg(ctx, id, org.OrgID)
	if err != nil || role == "" {
		return ctx, nil, "", nil, false, NewServiceError("permission_denied", "a role in the organization is required")
	}
	canManage := s.canManageRefClasses(ctx, id, org.OrgID, scopeProject)
	if write && !canManage {
		if scopeProject == nil {
			return ctx, nil, "", nil, false, NewServiceError("permission_denied", "an org admin is required to change organization lanes")
		}
		return ctx, nil, "", nil, false, NewServiceError("permission_denied", "a project owner is required to change project lanes")
	}
	return ctx, lanesStore, org.OrgID, scopeProject, canManage, nil
}

func laneMatchToWire(lane models.PriorityLane) csilapi.PriorityLaneMatch {
	match := csilapi.PriorityLaneMatch{ClassesAny: append([]string{}, lane.ClassesAny...), ClassesNone: append([]string{}, lane.ClassesNone...),
		EventTypes: append([]string{}, lane.EventTypes...), Draft: lane.Draft, JobKind: optionalText(lane.JobKind)}
	if lane.Retry {
		retry := true
		match.Retry = &retry
	}
	return match
}

func storedLaneToWire(lane models.PriorityLane, scope string) csilapi.PriorityLane {
	return laneToWire(priority.Lane{PriorityLane: lane, Scope: scope, RuleID: lane.LaneID})
}

func laneToWire(lane priority.Lane) csilapi.PriorityLane {
	scope := lane.Scope
	out := csilapi.PriorityLane{LaneId: optionalText(lane.LaneID), RuleId: optionalText(lane.RuleID), ProjectId: lane.ProjectID,
		Name: lane.Name, Position: int64(lane.Position), Description: optionalText(lane.Description), Base: int64(lane.Base),
		Match: laneMatchToWire(lane.PriorityLane), MatchText: priority.MatchText(lane.PriorityLane), Enabled: lane.Enabled,
		Scope: optionalText(scope), Replaces: optionalText(lane.Replaces)}
	return out
}

func lanesToWire(lanes []priority.Lane) []csilapi.PriorityLane {
	out := make([]csilapi.PriorityLane, 0, len(lanes))
	for _, lane := range lanes {
		out = append(out, laneToWire(lane))
	}
	return out
}

func storedLanesToWire(lanes []models.PriorityLane, scope string) []csilapi.PriorityLane {
	out := make([]csilapi.PriorityLane, 0, len(lanes))
	for _, lane := range lanes {
		out = append(out, storedLaneToWire(lane, scope))
	}
	return out
}

func priorityShiftsToWire(shifts []models.PriorityShift) []csilapi.PriorityShift {
	out := make([]csilapi.PriorityShift, 0, len(shifts))
	for _, shift := range shifts {
		out = append(out, csilapi.PriorityShift{Source: shift.Source, Group: optionalText(shift.Group), Value: int64(shift.Value), Applied: shift.Applied})
	}
	return out
}

func priorityDecisionToWire(d priority.Decision) csilapi.PriorityDecision {
	return csilapi.PriorityDecision{Lane: d.Lane, RuleId: d.RuleID, Scope: d.Scope, Base: int64(d.Base),
		Shifts: priorityShiftsToWire(d.Shifts), Final: int64(d.Final), RetainedFrom: optionalText(d.RetainedFrom), Summary: d.Summary()}
}

func (s *UiService) ListPriorityLanes(ctx context.Context, req csilapi.ListPriorityLanesRequest) (csilapi.ListPriorityLanesResponse, error) {
	ctx, lanesStore, orgID, projectID, canManage, err := s.priorityScope(ctx, req.Organization, req.ProjectId, false)
	if err != nil {
		return csilapi.ListPriorityLanesResponse{}, err
	}
	scope := priority.ScopeOrg
	if projectID != nil {
		scope = priority.ScopeProject
	}
	lanes, err := lanesStore.ListPriorityLanes(ctx, orgID, projectID)
	if err != nil {
		return csilapi.ListPriorityLanesResponse{}, NewServiceError("internal", "could not list priority lanes")
	}
	response := csilapi.ListPriorityLanesResponse{Lanes: storedLanesToWire(lanes, scope), Inherited: []csilapi.PriorityLane{},
		Builtin: lanesToWire(priority.Builtins()), CanManage: canManage,
		Bounds: csilapi.PriorityBounds{MinBase: priority.MinBase, MaxBase: priority.MaxBase, MinGroupShift: priority.MinGroupShift,
			MaxGroupShift: priority.MaxGroupShift, MaxRequested: priority.MaxRequested, ShedPrShift: priority.ShedPRShift}}
	if projectID != nil {
		inherited, err := lanesStore.ListPriorityLanes(ctx, orgID, nil)
		if err != nil {
			return csilapi.ListPriorityLanesResponse{}, NewServiceError("internal", "could not list priority lanes")
		}
		response.Inherited = storedLanesToWire(inherited, priority.ScopeOrg)
		response.Effective = lanesToWire(priority.Effective(inherited, lanes))
	} else {
		response.Effective = lanesToWire(priority.Effective(lanes, nil))
	}
	return response, nil
}

func (s *UiService) PutPriorityLane(ctx context.Context, req csilapi.PutPriorityLaneRequest) (csilapi.PutPriorityLaneResponse, error) {
	ctx, lanesStore, orgID, projectID, _, err := s.priorityScope(ctx, req.Organization, req.ProjectId, true)
	if err != nil {
		return csilapi.PutPriorityLaneResponse{}, err
	}
	base := int(req.Base)
	input := priority.LaneInput{Base: &base, Description: req.Description, Enabled: req.Enabled}
	if req.Position != nil {
		position := int(*req.Position)
		input.Position = &position
	}
	if req.Match != nil {
		input.Match = &priority.LaneMatch{ClassesAny: req.Match.ClassesAny, ClassesNone: req.Match.ClassesNone,
			EventTypes: req.Match.EventTypes, Draft: req.Match.Draft, JobKind: textValue(req.Match.JobKind),
			Retry: req.Match.Retry != nil && *req.Match.Retry}
	}
	lane, err := priority.BuildLane(ctx, lanesStore, orgID, projectID, req.Name, input)
	if err != nil {
		return csilapi.PutPriorityLaneResponse{}, NewServiceError("invalid_argument", strings.TrimPrefix(err.Error(), store.ErrInvalidInput.Error()+": "))
	}
	if err := lanesStore.UpsertPriorityLane(ctx, &lane); err != nil {
		return csilapi.PutPriorityLaneResponse{}, mapStoreErr(err, "priority lane not found")
	}
	s.recordAudit(ctx, orgID, "priority_lane.set", "priority_lane", lane.LaneID, models.JSONB{
		"name": lane.Name, "project_id": projectID, "base": lane.Base, "position": lane.Position, "enabled": lane.Enabled})
	scope := priority.ScopeOrg
	if projectID != nil {
		scope = priority.ScopeProject
	}
	return csilapi.PutPriorityLaneResponse{Lane: storedLaneToWire(lane, scope)}, nil
}

func (s *UiService) DeletePriorityLane(ctx context.Context, req csilapi.DeletePriorityLaneRequest) (csilapi.DeletePriorityLaneResponse, error) {
	ctx, lanesStore, orgID, projectID, _, err := s.priorityScope(ctx, req.Organization, req.ProjectId, true)
	if err != nil {
		return csilapi.DeletePriorityLaneResponse{}, err
	}
	lane, err := lanesStore.GetPriorityLane(ctx, orgID, projectID, req.Name)
	if err != nil {
		return csilapi.DeletePriorityLaneResponse{}, mapStoreErr(err, "priority lane not found")
	}
	if err := lanesStore.DeletePriorityLane(ctx, orgID, projectID, req.Name); err != nil {
		return csilapi.DeletePriorityLaneResponse{}, mapStoreErr(err, "priority lane not found")
	}
	s.recordAudit(ctx, orgID, "priority_lane.delete", "priority_lane", lane.LaneID, models.JSONB{"name": lane.Name, "project_id": projectID})
	return csilapi.DeletePriorityLaneResponse{Deleted: true}, nil
}

// ExplainPriority explains facts, or a stored job. A stored job needs the
// authority to manage the lanes of the scope, because a job of a private
// project is not visible to every member of the organization.
func (s *UiService) ExplainPriority(ctx context.Context, req csilapi.ExplainPriorityRequest) (csilapi.ExplainPriorityResponse, error) {
	ctx, lanesStore, orgID, projectID, canManage, err := s.priorityScope(ctx, req.Organization, req.ProjectId, false)
	if err != nil {
		return csilapi.ExplainPriorityResponse{}, err
	}
	var explanation priority.Explanation
	if jobID := textValue(req.JobId); jobID != "" {
		if !canManage {
			return csilapi.ExplainPriorityResponse{}, NewServiceError("permission_denied", "explaining a stored job needs the authority to manage the lanes")
		}
		job, err := s.deps.Store.GetJobByID(ctx, jobID)
		if err != nil || job.OwnershipOrgID() != orgID || (projectID != nil && (job.ProjectID == nil || *job.ProjectID != *projectID)) {
			return csilapi.ExplainPriorityResponse{}, NewServiceError("not_found", "job not found")
		}
		explanation = priority.ExplainJob(ctx, lanesStore, job)
	} else {
		kind := textValue(req.JobKind)
		if kind != "" && kind != models.PriorityJobKindEval && kind != models.PriorityJobKindWork {
			return csilapi.ExplainPriorityResponse{}, NewServiceError("invalid_argument", "job_kind must be eval or work")
		}
		facts := priority.Facts{OrgID: orgID, ProjectID: projectID, RefClasses: req.RefClasses, EventType: textValue(req.EventType),
			Draft: req.Draft != nil && *req.Draft, JobKind: kind}
		if req.Requested != nil {
			facts.Requested = int(*req.Requested)
		}
		explanation = priority.ExplainFacts(ctx, lanesStore, facts)
	}
	response := csilapi.ExplainPriorityResponse{Decision: priorityDecisionToWire(explanation.Decision), Lanes: []csilapi.PriorityLaneCheck{}}
	if explanation.Stored != nil {
		stored := priorityDecisionToWire(*explanation.Stored)
		response.Stored = &stored
	}
	for _, check := range explanation.Lanes {
		response.Lanes = append(response.Lanes, csilapi.PriorityLaneCheck{Name: check.Name, Scope: check.Scope, Base: int64(check.Base),
			Enabled: check.Enabled, Matched: check.Matched, MatchText: check.Match})
	}
	return response, nil
}

// SetGroupPriorityShift requires org admin (of the group's org) or global
// admin.
func (s *UiService) SetGroupPriorityShift(ctx context.Context, req csilapi.SetGroupPriorityShiftRequest) (csilapi.SetGroupPriorityShiftResponse, error) {
	ctx, id, _, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return csilapi.SetGroupPriorityShiftResponse{}, authErr
	}
	if err := requireNonEmpty("group_id", req.GroupId, 64); err != nil {
		return csilapi.SetGroupPriorityShiftResponse{}, err
	}
	group, err := s.deps.Store.GetGroupByID(ctx, req.GroupId)
	if err != nil {
		return csilapi.SetGroupPriorityShiftResponse{}, mapStoreErr(err, "group not found")
	}
	if err := s.requireOrgAdminGrant(ctx, id, group.OrgID, tokencaps.PoliciesManage, tokencaps.OrganizationsManage); err != nil {
		return csilapi.SetGroupPriorityShiftResponse{}, mapPermissionErr(err)
	}
	shifts, ok := s.deps.Store.(store.PriorityStore)
	if !ok {
		return csilapi.SetGroupPriorityShiftResponse{}, NewServiceError("unavailable", "group priority shifts are not available")
	}
	if err := priority.ValidateGroupShift(int(req.Shift)); err != nil {
		return csilapi.SetGroupPriorityShiftResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	previous := group.PriorityShift
	if err := shifts.SetGroupPriorityShift(ctx, group.GroupID, int(req.Shift)); err != nil {
		return csilapi.SetGroupPriorityShiftResponse{}, mapStoreErr(err, "group not found")
	}
	group.PriorityShift = int(req.Shift)
	s.recordAudit(ctx, group.OrgID, "group.priority_shift", "group", group.GroupID, models.JSONB{"name": group.Name, "from": previous, "to": group.PriorityShift})
	return csilapi.SetGroupPriorityShiftResponse{Group: groupToSummary(group)}, nil
}
