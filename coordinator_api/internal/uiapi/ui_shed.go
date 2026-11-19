package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/shedding"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func shedActionsView(a models.ShedActions) csilapi.ShedActions {
	return csilapi.ShedActions{SkipDrafts: a.SkipDrafts, PauseSchedules: a.PauseSchedules, LowerPrPriority: a.LowerPRPriority,
		SkipAdvisory: a.SkipAdvisory, RefuseManualRuns: a.RefuseManualRuns}
}

func shedStateView(state models.ShedState) csilapi.ShedState {
	return csilapi.ShedState{GlobalLevel: state.GlobalLevel, OrgLevel: optionalText(state.OrgLevel), Level: state.Level,
		Source: optionalText(state.Source), Actions: shedActionsView(state.Actions),
		DegradedActions: shedActionsView(state.Config.Degraded), CriticalActions: shedActionsView(state.Config.Critical)}
}

func (s *UiService) shedStore() (store.DraftShedStore, error) {
	shed, ok := s.deps.Store.(store.DraftShedStore)
	if !ok {
		return nil, NewServiceError("unavailable", "shed levels are not available")
	}
	return shed, nil
}

// requireOrgMember permits a global admin and a caller with any role in the
// organization. A token also needs one of capabilities for the organization.
func (s *UiService) requireOrgMember(ctx context.Context, id authz.Identity, orgID string, capabilities ...string) error {
	if err := requireTokenGrant(id, &orgID, capabilities...); err != nil {
		return err
	}
	if admin, err := s.deps.Resolver.IsGlobalAdmin(ctx, id); err == nil && admin {
		return nil
	}
	role, err := s.deps.Resolver.EffectiveRoleForOrg(ctx, id, orgID)
	if err != nil || role == "" {
		return NewServiceError("forbidden", "you are not a member of this organization")
	}
	return nil
}

// GetShedState returns the global shed level, or the state in force for an
// organization. A token needs organizations:read (for the organization when
// org_id is given), as in GET /api/v1/shed.
func (s *UiService) GetShedState(ctx context.Context, req csilapi.GetShedStateRequest) (csilapi.GetShedStateResponse, error) {
	ctx, id, _, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return csilapi.GetShedStateResponse{}, err
	}
	if id.Token != nil && !id.Token.HasCapability(tokencaps.OrganizationsRead) {
		return csilapi.GetShedStateResponse{}, NewServiceError("forbidden", "the API token does not have the organizations:read capability")
	}
	orgID := ""
	if req.OrgId != nil {
		orgID = *req.OrgId
		if err := requireTokenGrant(id, &orgID, tokencaps.OrganizationsRead); err != nil {
			return csilapi.GetShedStateResponse{}, err
		}
		if err := s.requireOrgMember(ctx, id, orgID, tokencaps.OrganizationsRead); err != nil {
			return csilapi.GetShedStateResponse{}, err
		}
	}
	shed, err := s.shedStore()
	if err != nil {
		return csilapi.GetShedStateResponse{}, err
	}
	state, err := shed.GetShedState(ctx, orgID)
	if err != nil {
		return csilapi.GetShedStateResponse{}, mapStoreErr(err, "organization not found")
	}
	return csilapi.GetShedStateResponse{State: shedStateView(state)}, nil
}

// SetShedLevel sets the global level (global admin) or an organization level
// (organization admin). Each change is audited with the old and new level.
func (s *UiService) SetShedLevel(ctx context.Context, req csilapi.SetShedLevelRequest) (csilapi.SetShedLevelResponse, error) {
	ctx, id, _, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return csilapi.SetShedLevelResponse{}, err
	}
	if !models.IsValidShedLevel(req.Level) {
		return csilapi.SetShedLevelResponse{}, NewServiceError("invalid_argument", "level must be normal, degraded or critical")
	}
	reason := ""
	if req.Reason != nil {
		reason = *req.Reason
	}
	if req.OrgId == nil {
		if err := s.requireGlobalAdminGrant(ctx, id, tokencaps.OrganizationsManage); err != nil {
			return csilapi.SetShedLevelResponse{}, mapPermissionErr(err)
		}
		shed, err := s.shedStore()
		if err != nil {
			return csilapi.SetShedLevelResponse{}, err
		}
		before, _ := shed.GetShedState(ctx, "")
		if err := shed.SetGlobalShedLevel(ctx, req.Level); err != nil {
			return csilapi.SetShedLevelResponse{}, mapStoreErr(err, "the shed level could not be changed")
		}
		s.recordAudit(ctx, "", "shed.set_global", "global_setting", models.GlobalSettingShedLevel,
			models.JSONB{"from": before.GlobalLevel, "to": req.Level, "reason": reason})
		state, err := shed.GetShedState(ctx, "")
		if err != nil {
			return csilapi.SetShedLevelResponse{}, mapStoreErr(err, "the shed level could not be read")
		}
		return csilapi.SetShedLevelResponse{State: shedStateView(state)}, nil
	}
	orgID := *req.OrgId
	if err := s.requireOrgAdminGrant(ctx, id, orgID, tokencaps.OrganizationsManage); err != nil {
		return csilapi.SetShedLevelResponse{}, mapPermissionErr(err)
	}
	shed, err := s.shedStore()
	if err != nil {
		return csilapi.SetShedLevelResponse{}, err
	}
	before, err := shed.GetShedState(ctx, orgID)
	if err != nil {
		return csilapi.SetShedLevelResponse{}, mapStoreErr(err, "organization not found")
	}
	if err := shed.SetOrganizationShed(ctx, orgID, req.Level, nil, false); err != nil {
		return csilapi.SetShedLevelResponse{}, mapStoreErr(err, "the shed level could not be changed")
	}
	state, err := shed.GetShedState(ctx, orgID)
	if err != nil {
		return csilapi.SetShedLevelResponse{}, mapStoreErr(err, "the shed level could not be read")
	}
	s.recordAudit(ctx, orgID, "shed.set_org", "organization", orgID,
		models.JSONB{"from": before.OrgLevel, "to": req.Level, "level_in_force": state.Level, "reason": reason})
	return csilapi.SetShedLevelResponse{State: shedStateView(state)}, nil
}

// shedManualRunRefusal returns a non-empty reason when the shed level in
// force refuses a manual run from the caller (not an organization admin).
func (s *UiService) shedManualRunRefusal(ctx context.Context, id authz.Identity, orgID string) string {
	state := shedding.State(ctx, s.deps.Store, orgID)
	if !state.Actions.RefuseManualRuns {
		return ""
	}
	admin, err := s.deps.Resolver.IsOrgAdmin(ctx, id, orgID)
	// A token gets the admin exemption only with organizations:manage.
	admin = admin && requireTokenGrant(id, &orgID, tokencaps.OrganizationsManage) == nil
	return shedding.ManualRunRefusal(state, err == nil && admin)
}

// validateOrgDraftPRs checks a draft_prs update value. It returns the audit
// change, or nil when the value does not change.
func (s *UiService) validateOrgDraftPRs(org *models.Organization, value *string) (models.JSONB, error) {
	if value == nil {
		return nil, nil
	}
	if !models.IsValidDraftPRsOrgSetting(*value) {
		return nil, NewServiceError("invalid_argument", "draft_prs must be run, skip or skip_when_shedding")
	}
	if org.DraftPRs == *value {
		return nil, nil
	}
	return models.JSONB{"from": org.DraftPRs, "to": *value}, nil
}

func (s *UiService) writeOrgDraftPRs(ctx context.Context, org *models.Organization, value string) error {
	settings, ok := s.deps.Store.(store.DraftShedStore)
	if !ok {
		return NewServiceError("unavailable", "draft_prs is not available")
	}
	if err := settings.SetOrganizationDraftPRs(ctx, org.OrgID, value); err != nil {
		return mapStoreErr(err, "draft_prs could not be changed")
	}
	org.DraftPRs = value
	return nil
}
