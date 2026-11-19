package uiapi

import (
	"context"
	"regexp"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/reporting"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/lib/pq"
)

// Organization settings of the management UI: shed actions, reporting
// defaults, required set defaults, and the VCS owner allowlist. Each
// operation has the same authorization as its REST route
// (handlers/shed_handler.go, reporting_handler.go, required_set_handler.go,
// vcs_owner_allowlist_handler.go). The services decide where a service
// exists, so REST and the UI cannot drift.

type uiOrgByName interface {
	GetOrganizationByName(context.Context, string) (*models.Organization, error)
}

// orgByName finds an organization by name. An unknown name is not_found.
func (s *UiService) orgByName(ctx context.Context, name string) (*models.Organization, error) {
	orgs, ok := s.deps.Store.(uiOrgByName)
	if !ok {
		return nil, NewServiceError("unavailable", "organizations are not available")
	}
	if err := requireNonEmpty("organization", name, 200); err != nil {
		return nil, err
	}
	org, err := orgs.GetOrganizationByName(ctx, strings.TrimSpace(name))
	if err != nil || org == nil {
		return nil, NewServiceError("not_found", "organization not found")
	}
	return org, nil
}

func shedActionsFromWire(a csilapi.ShedActions) models.ShedActions {
	return models.ShedActions{SkipDrafts: a.SkipDrafts, PauseSchedules: a.PauseSchedules, LowerPRPriority: a.LowerPrPriority,
		SkipAdvisory: a.SkipAdvisory, RefuseManualRuns: a.RefuseManualRuns}
}

// SetShedActions changes the actions of each shed level of an organization
// (an org admin), like PUT /api/v1/organizations/NAME/shed with actions.
func (s *UiService) SetShedActions(ctx context.Context, req csilapi.SetShedActionsRequest) (csilapi.SetShedLevelResponse, error) {
	ctx, id, _, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return csilapi.SetShedLevelResponse{}, err
	}
	if err := requireNonEmpty("org_id", req.OrgId, 64); err != nil {
		return csilapi.SetShedLevelResponse{}, err
	}
	reset := req.Reset != nil && *req.Reset
	if reset == (req.Degraded != nil || req.Critical != nil) {
		return csilapi.SetShedLevelResponse{}, NewServiceError("invalid_argument", "set degraded or critical actions, or reset")
	}
	if err := s.requireOrgAdminGrant(ctx, id, req.OrgId, tokencaps.OrganizationsManage); err != nil {
		return csilapi.SetShedLevelResponse{}, mapPermissionErr(err)
	}
	shed, err := s.shedStore()
	if err != nil {
		return csilapi.SetShedLevelResponse{}, err
	}
	before, err := shed.GetShedState(ctx, req.OrgId)
	if err != nil {
		return csilapi.SetShedLevelResponse{}, mapStoreErr(err, "organization not found")
	}
	var actions *models.ShedActionsConfig
	if !reset {
		config := before.Config
		if req.Degraded != nil {
			config.Degraded = shedActionsFromWire(*req.Degraded)
		}
		if req.Critical != nil {
			config.Critical = shedActionsFromWire(*req.Critical)
		}
		actions = &config
	}
	if err := shed.SetOrganizationShed(ctx, req.OrgId, "", actions, reset); err != nil {
		return csilapi.SetShedLevelResponse{}, mapStoreErr(err, "the shed actions could not be changed")
	}
	state, err := shed.GetShedState(ctx, req.OrgId)
	if err != nil {
		return csilapi.SetShedLevelResponse{}, mapStoreErr(err, "the shed actions could not be read")
	}
	s.recordAudit(ctx, req.OrgId, "shed.set_org", "organization", req.OrgId,
		models.JSONB{"from": before.OrgLevel, "to": state.OrgLevel, "level_in_force": state.Level, "actions": state.Config})
	return csilapi.SetShedLevelResponse{State: shedStateView(state)}, nil
}

// --- reporting defaults ---

func (s *UiService) reportingCaller(ctx context.Context) (context.Context, *reporting.Service, reporting.Caller, error) {
	ctx, id, user, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return ctx, nil, reporting.Caller{}, err
	}
	caller := reporting.Caller{Identity: id, Actor: principals.FromCredential(checkauth.GetPrincipalFromContext(ctx), user)}
	service := &reporting.Service{Store: s.deps.Store, Record: func(ctx context.Context, orgID, action, subjectType, subjectID string, details models.JSONB) {
		s.recordAudit(ctx, orgID, action, subjectType, subjectID, details)
	}}
	return ctx, service, caller, nil
}

func orgReportingToWire(settings *models.OrgReportingSettings) csilapi.OrgReportingSettingsView {
	return csilapi.OrgReportingSettingsView{ReportMode: settings.ReportMode, AgreementContexts: append([]string{}, settings.AgreementContexts...),
		CodeownersImport: settings.CodeownersImport, CodeownersCacheTtlSeconds: int64(settings.CodeownersCacheTTLSeconds),
		PathAcceptance: settings.PathAcceptance, PathOwners: pathOwnersToWire(settings.PathOwners)}
}

// GetOrgReportingSettings returns the reporting defaults of an organization
// (a role in the organization).
func (s *UiService) GetOrgReportingSettings(ctx context.Context, req csilapi.GetOrgReportingSettingsRequest) (csilapi.GetOrgReportingSettingsResponse, error) {
	ctx, service, caller, err := s.reportingCaller(ctx)
	if err != nil {
		return csilapi.GetOrgReportingSettingsResponse{}, err
	}
	org, err := s.orgByName(ctx, req.Organization)
	if err != nil {
		return csilapi.GetOrgReportingSettingsResponse{}, err
	}
	settings, canManage, err := service.GetOrgSettings(ctx, caller, org.OrgID)
	if err != nil {
		return csilapi.GetOrgReportingSettingsResponse{}, reportingError(err)
	}
	return csilapi.GetOrgReportingSettingsResponse{Settings: orgReportingToWire(settings), CanManage: canManage}, nil
}

// PutOrgReportingSettings replaces the reporting defaults of an
// organization (an org admin).
func (s *UiService) PutOrgReportingSettings(ctx context.Context, req csilapi.PutOrgReportingSettingsRequest) (csilapi.GetOrgReportingSettingsResponse, error) {
	ctx, service, caller, err := s.reportingCaller(ctx)
	if err != nil {
		return csilapi.GetOrgReportingSettingsResponse{}, err
	}
	org, err := s.orgByName(ctx, req.Organization)
	if err != nil {
		return csilapi.GetOrgReportingSettingsResponse{}, err
	}
	in := req.Settings
	settings, err := service.SetOrgSettings(ctx, caller, models.OrgReportingSettings{OrgID: org.OrgID, ReportMode: in.ReportMode,
		AgreementContexts: pq.StringArray(append([]string{}, in.AgreementContexts...)), CodeownersImport: in.CodeownersImport,
		CodeownersCacheTTLSeconds: int(in.CodeownersCacheTtlSeconds), PathAcceptance: in.PathAcceptance,
		PathOwners: pathOwnersFromWire(in.PathOwners)})
	if err != nil {
		return csilapi.GetOrgReportingSettingsResponse{}, reportingError(err)
	}
	return csilapi.GetOrgReportingSettingsResponse{Settings: orgReportingToWire(settings), CanManage: true}, nil
}

// --- required set defaults ---

func approverRulesFromWire(rules []csilapi.RequiredSetApproverRule) []models.RequiredSetApproverRule {
	out := make([]models.RequiredSetApproverRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, models.RequiredSetApproverRule{Branches: append([]string{}, rule.Branches...), Approvers: append([]string{}, rule.Approvers...)})
	}
	return out
}

func (s *UiService) requiredSetOrgView(ctx context.Context, id authz.Identity, orgID string) (csilapi.GetRequiredSetOrgSettingsResponse, error) {
	sets, ok := s.deps.Store.(store.RequiredSetStore)
	if !ok {
		return csilapi.GetRequiredSetOrgSettingsResponse{}, NewServiceError("unavailable", "the required set is not available")
	}
	settings, err := sets.GetOrgRequiredSetSettings(ctx, orgID)
	if err != nil || settings == nil {
		return csilapi.GetRequiredSetOrgSettingsResponse{}, NewServiceError("internal", "could not read the required set settings")
	}
	admin, _ := s.deps.Resolver.IsOrgAdmin(ctx, id, orgID)
	admin = admin && requireTokenGrant(id, &orgID, tokencaps.PoliciesManage) == nil
	return csilapi.GetRequiredSetOrgSettingsResponse{Approvers: append([]string{}, settings.Approvers...),
		ApproverRules: approverRulesToWire(settings.ApproverRules), MaxQuarantineDays: int64(settings.MaxQuarantineDays), CanManage: admin}, nil
}

// GetRequiredSetOrgSettings returns the required set defaults of an
// organization (a role in the organization).
func (s *UiService) GetRequiredSetOrgSettings(ctx context.Context, req csilapi.GetRequiredSetOrgSettingsRequest) (csilapi.GetRequiredSetOrgSettingsResponse, error) {
	ctx, id, _, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return csilapi.GetRequiredSetOrgSettingsResponse{}, err
	}
	org, err := s.orgByName(ctx, req.Organization)
	if err != nil {
		return csilapi.GetRequiredSetOrgSettingsResponse{}, err
	}
	if err := s.requireOrgMember(ctx, id, org.OrgID, tokencaps.ProjectsRead, tokencaps.PoliciesManage); err != nil {
		return csilapi.GetRequiredSetOrgSettingsResponse{}, err
	}
	return s.requiredSetOrgView(ctx, id, org.OrgID)
}

// PutRequiredSetOrgSettings replaces the required set defaults of an
// organization (an org admin; requiredset.Service decides).
func (s *UiService) PutRequiredSetOrgSettings(ctx context.Context, req csilapi.PutRequiredSetOrgSettingsRequest) (csilapi.GetRequiredSetOrgSettingsResponse, error) {
	ctx, id, user, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return csilapi.GetRequiredSetOrgSettingsResponse{}, err
	}
	org, err := s.orgByName(ctx, req.Organization)
	if err != nil {
		return csilapi.GetRequiredSetOrgSettingsResponse{}, err
	}
	caller := requiredset.Caller{Identity: id, Actor: principals.FromCredential(checkauth.GetPrincipalFromContext(ctx), user)}
	service := &requiredset.Service{Store: s.deps.Store, Record: func(ctx context.Context, orgID, action, subjectType, subjectID string, details models.JSONB) {
		s.recordAudit(ctx, orgID, action, subjectType, subjectID, details)
	}}
	if err := service.SetOrgSettings(ctx, caller, org.OrgID, append([]string{}, req.Approvers...), approverRulesFromWire(req.ApproverRules), int(req.MaxQuarantineDays)); err != nil {
		return csilapi.GetRequiredSetOrgSettingsResponse{}, requiredSetError(err)
	}
	return s.requiredSetOrgView(ctx, id, org.OrgID)
}

// --- VCS owner allowlist ---

type uiVCSOwnerAllowlistStore interface {
	GetOrganizationVCSOwnerAllowlist(ctx context.Context, orgID string) ([]string, error)
	SetOrganizationVCSOwnerAllowlist(ctx context.Context, orgID string, owners []string) ([]string, error)
}

// vcsOwnerLogin is the rule of handlers/vcs_owner_allowlist_handler.go: a VCS
// organization login.
var vcsOwnerLogin = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}$`)

func (s *UiService) vcsOwnerAllowlistScope(ctx context.Context, name string, read ...bool) (uiVCSOwnerAllowlistStore, *models.Organization, error) {
	// Only a global admin: the list lets one tenant read the teams of a VCS
	// owner with the shared credential.
	check := s.requireGlobalAdmin
	if len(read) > 0 && read[0] {
		check = s.requireGlobalAdminRead
	}
	if _, err := check(ctx); err != nil {
		return nil, nil, err
	}
	lists, ok := s.deps.Store.(uiVCSOwnerAllowlistStore)
	if !ok {
		return nil, nil, NewServiceError("unavailable", "the VCS owner allowlist is not available")
	}
	org, err := s.orgByName(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	return lists, org, nil
}

// GetVcsOwnerAllowlist returns the VCS owner allowlist of an organization.
func (s *UiService) GetVcsOwnerAllowlist(ctx context.Context, req csilapi.GetVcsOwnerAllowlistRequest) (csilapi.VcsOwnerAllowlistResponse, error) {
	lists, org, err := s.vcsOwnerAllowlistScope(ctx, req.Organization, true)
	if err != nil {
		return csilapi.VcsOwnerAllowlistResponse{}, err
	}
	owners, err := lists.GetOrganizationVCSOwnerAllowlist(ctx, org.OrgID)
	if err != nil {
		return csilapi.VcsOwnerAllowlistResponse{}, NewServiceError("internal", "could not read the VCS owner allowlist")
	}
	return csilapi.VcsOwnerAllowlistResponse{Owners: append([]string{}, owners...)}, nil
}

// PutVcsOwnerAllowlist replaces the VCS owner allowlist of an organization.
func (s *UiService) PutVcsOwnerAllowlist(ctx context.Context, req csilapi.PutVcsOwnerAllowlistRequest) (csilapi.VcsOwnerAllowlistResponse, error) {
	lists, org, err := s.vcsOwnerAllowlistScope(ctx, req.Organization)
	if err != nil {
		return csilapi.VcsOwnerAllowlistResponse{}, err
	}
	owners := make([]string, 0, len(req.Owners))
	for _, owner := range req.Owners {
		owner = strings.ToLower(strings.TrimSpace(owner))
		if !vcsOwnerLogin.MatchString(owner) {
			return csilapi.VcsOwnerAllowlistResponse{}, NewServiceError("invalid_argument",
				"an owner is a VCS organization login: lowercase letters, digits, and '-'")
		}
		owners = append(owners, owner)
	}
	before, _ := lists.GetOrganizationVCSOwnerAllowlist(ctx, org.OrgID)
	stored, err := lists.SetOrganizationVCSOwnerAllowlist(ctx, org.OrgID, owners)
	if err != nil {
		return csilapi.VcsOwnerAllowlistResponse{}, NewServiceError("internal", "could not save the VCS owner allowlist")
	}
	s.recordAudit(ctx, org.OrgID, "organization.vcs_owner_allowlist", "organization", org.OrgID, models.JSONB{"from": before, "to": stored})
	return csilapi.VcsOwnerAllowlistResponse{Owners: append([]string{}, stored...)}, nil
}
