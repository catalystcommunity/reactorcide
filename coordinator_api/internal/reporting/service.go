package reporting

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// Authorization (decided for WP10, docs/reporting-and-ownership.md):
//
//   - Read the settings, the agreement report, and the path acceptances: any
//     role in the project or its organization (project member and above),
//     or a token with projects:read.
//   - Change report_mode and agreement_contexts of a project: a project
//     owner or an org admin. These change only what Reactorcide writes to
//     the VCS.
//   - Change codeowners_import, path_acceptance, path_owners of a project,
//     and every organization value: an org admin. These change merge
//     reviews and admission.
//   - Request an acceptance: a workflow owner (WP7), a project owner, or an
//     org admin.
//   - Accept or reject a request: a person with the owner subject of the
//     row, or an org admin.
//   - Revoke: the owner side (as for accept), the workflow side (as for a
//     request), or an org admin.
//
// The admin token is an org admin of every organization, so a deployment
// with no logins can do all of it (owner decision D15).

// Caller is the person or service that calls an operation.
type Caller = requiredset.Caller

// Error is a refused or failed operation. Code is one of permission_denied,
// invalid_argument, not_found, conflict, unavailable, internal.
type Error = requiredset.Error

func newError(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrorCode returns the code of an Error, or "internal".
func ErrorCode(err error) string { return requiredset.ErrorCode(err) }

// Recorder writes one audit event.
type Recorder func(ctx context.Context, orgID, action, subjectType, subjectID string, details models.JSONB)

// Service runs the operations for every channel (REST, CLI, UI).
type Service struct {
	// Store must implement store.ReportingStore and authz.RoleStore.
	Store  any
	Record Recorder
	Now    func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) stores() (store.ReportingStore, *authz.Resolver, error) {
	reports, ok := s.Store.(store.ReportingStore)
	if !ok {
		return nil, nil, newError("unavailable", "reporting settings are not available")
	}
	roles, ok := s.Store.(authz.RoleStore)
	if !ok {
		return nil, nil, newError("unavailable", "reporting settings are not available")
	}
	return reports, authz.NewResolver(roles), nil
}

func (s *Service) record(ctx context.Context, orgID, action, subjectType, subjectID string, details models.JSONB) {
	if s.Record != nil {
		s.Record(ctx, orgID, action, subjectType, subjectID, details)
	}
}

// tokenAllows reports whether the token of the caller (if any) allows the
// capability. A session passes. A service principal token passes: its roles
// decide (the same rule as the required set).
func tokenAllows(caller Caller, orgID, capability string) bool {
	token := caller.Identity.Token
	if token == nil || caller.Identity.PrincipalID != "" {
		return true
	}
	return token.HasOrganization(orgID) && token.HasCapability(capability)
}

func roleless(caller Caller) bool {
	return caller.Identity.Token != nil && caller.Identity.UserID == "" && caller.Identity.PrincipalID == ""
}

// Permissions are what the caller may do in a project.
type Permissions struct {
	View bool `json:"view"`
	// ManageReporting: report_mode and agreement_contexts.
	ManageReporting bool `json:"manage_reporting"`
	// ManageOwnership: codeowners_import, path_acceptance, path_owners.
	ManageOwnership bool `json:"manage_ownership"`
	OrgAdmin        bool `json:"org_admin"`
}

// ProjectPermissions resolves the permissions of the caller.
func (s *Service) ProjectPermissions(ctx context.Context, caller Caller, project *models.Project) (Permissions, error) {
	_, resolver, err := s.stores()
	if err != nil {
		return Permissions{}, err
	}
	orgID := project.OwnershipOrgID()
	role, err := resolver.EffectiveRoleForProject(ctx, caller.Identity, project.ProjectID)
	if err != nil {
		return Permissions{}, newError("internal", "could not resolve the role of the caller")
	}
	if role == "" {
		if role, err = resolver.EffectiveRoleForOrg(ctx, caller.Identity, orgID); err != nil {
			return Permissions{}, newError("internal", "could not resolve the role of the caller")
		}
	}
	var perms Permissions
	perms.View = role != "" && tokenAllows(caller, orgID, tokencaps.ProjectsRead)
	if roleless(caller) {
		perms.View = tokenAllows(caller, orgID, tokencaps.ProjectsRead)
	}
	admin, err := s.isOrgAdmin(ctx, caller, orgID)
	if err != nil {
		return Permissions{}, err
	}
	owner, err := resolver.IsProjectOwner(ctx, caller.Identity, project.ProjectID)
	if err != nil {
		return Permissions{}, newError("internal", "could not resolve the role of the caller")
	}
	perms.OrgAdmin = admin
	perms.ManageOwnership = admin
	perms.ManageReporting = admin || (owner && tokenAllows(caller, orgID, tokencaps.ProjectsManage))
	if perms.ManageReporting {
		perms.View = true
	}
	return perms, nil
}

func (s *Service) isOrgAdmin(ctx context.Context, caller Caller, orgID string) (bool, error) {
	_, resolver, err := s.stores()
	if err != nil {
		return false, err
	}
	admin, err := resolver.IsOrgAdmin(ctx, caller.Identity, orgID)
	if err != nil {
		return false, newError("internal", "could not resolve the role of the caller")
	}
	return admin && tokenAllows(caller, orgID, tokencaps.PoliciesManage), nil
}

// SettingsView is the reporting settings of a project as the caller sees it.
type SettingsView struct {
	Project     *models.ProjectReportingSettings  `json:"project"`
	Org         *models.OrgReportingSettings      `json:"org"`
	Effective   models.EffectiveReportingSettings `json:"effective"`
	Permissions Permissions                       `json:"permissions"`
}

// GetSettings returns the settings of a project.
func (s *Service) GetSettings(ctx context.Context, caller Caller, project *models.Project) (*SettingsView, error) {
	reports, _, err := s.stores()
	if err != nil {
		return nil, err
	}
	perms, err := s.ProjectPermissions(ctx, caller, project)
	if err != nil {
		return nil, err
	}
	if !perms.View {
		return nil, newError("permission_denied", "you cannot read the settings of this project")
	}
	org, err := reports.GetOrgReportingSettings(ctx, project.OwnershipOrgID())
	if err != nil {
		return nil, newError("internal", "could not read the reporting settings")
	}
	settings, err := reports.GetProjectReportingSettings(ctx, project.ProjectID)
	if err != nil {
		return nil, newError("internal", "could not read the reporting settings")
	}
	if settings == nil {
		settings = &models.ProjectReportingSettings{ProjectID: project.ProjectID, OrgID: project.OwnershipOrgID()}
	}
	if org == nil {
		org = models.DefaultOrgReportingSettings(project.OwnershipOrgID())
		return &SettingsView{Project: settings, Org: org, Effective: models.ResolveReportingSettings(nil, settings), Permissions: perms}, nil
	}
	return &SettingsView{Project: settings, Org: org, Effective: models.ResolveReportingSettings(org, settings), Permissions: perms}, nil
}

// ProjectSettingsUpdate changes the project values. A nil field keeps the
// stored value. Inherit lists the fields to clear (they then inherit the
// organization value): report_mode, agreement_contexts, codeowners_import,
// codeowners_cache_ttl_seconds, path_acceptance, path_owners.
type ProjectSettingsUpdate struct {
	ReportMode                *string                 `json:"report_mode,omitempty"`
	AgreementContexts         *[]string               `json:"agreement_contexts,omitempty"`
	CodeownersImport          *string                 `json:"codeowners_import,omitempty"`
	CodeownersCacheTTLSeconds *int                    `json:"codeowners_cache_ttl_seconds,omitempty"`
	PathAcceptance            *string                 `json:"path_acceptance,omitempty"`
	PathOwners                *[]models.PathOwnerRule `json:"path_owners,omitempty"`
	Inherit                   []string                `json:"inherit,omitempty"`
}

var settingFields = map[string]bool{"report_mode": true, "agreement_contexts": true, "codeowners_import": true,
	"codeowners_cache_ttl_seconds": true, "path_acceptance": true, "path_owners": true}

func ownershipField(name string) bool {
	switch name {
	case "codeowners_import", "codeowners_cache_ttl_seconds", "path_acceptance", "path_owners":
		return true
	}
	return false
}

func validTTL(value int) bool { return value >= 10 && value <= 86400 }

// UpdateProjectSettings applies an update and writes an audit event.
func (s *Service) UpdateProjectSettings(ctx context.Context, caller Caller, project *models.Project, update ProjectSettingsUpdate) (*SettingsView, error) {
	reports, _, err := s.stores()
	if err != nil {
		return nil, err
	}
	perms, err := s.ProjectPermissions(ctx, caller, project)
	if err != nil {
		return nil, err
	}
	changed := map[string]bool{}
	for _, name := range update.Inherit {
		if !settingFields[name] {
			return nil, newError("invalid_argument", "unknown setting %q", name)
		}
		changed[name] = true
	}
	if update.ReportMode != nil {
		changed["report_mode"] = true
	}
	if update.AgreementContexts != nil {
		changed["agreement_contexts"] = true
	}
	if update.CodeownersImport != nil {
		changed["codeowners_import"] = true
	}
	if update.CodeownersCacheTTLSeconds != nil {
		changed["codeowners_cache_ttl_seconds"] = true
	}
	if update.PathAcceptance != nil {
		changed["path_acceptance"] = true
	}
	if update.PathOwners != nil {
		changed["path_owners"] = true
	}
	if len(changed) == 0 {
		return nil, newError("invalid_argument", "the update changes nothing")
	}
	for name := range changed {
		if ownershipField(name) && !perms.ManageOwnership {
			return nil, newError("permission_denied", "only an org admin can change %s", name)
		}
		if !ownershipField(name) && !perms.ManageReporting {
			return nil, newError("permission_denied", "only a project owner or an org admin can change %s", name)
		}
	}
	current, err := reports.GetProjectReportingSettings(ctx, project.ProjectID)
	if err != nil {
		return nil, newError("internal", "could not read the reporting settings")
	}
	next := models.ProjectReportingSettings{ProjectID: project.ProjectID, OrgID: project.OwnershipOrgID()}
	if current != nil {
		next = *current
		next.OrgID = project.OwnershipOrgID()
	}
	if update.ReportMode != nil {
		mode := strings.TrimSpace(*update.ReportMode)
		if !ValidReportMode(mode) {
			return nil, newError("invalid_argument", "report_mode must be full, statuses_only, or silent")
		}
		next.ReportMode = &mode
	}
	if update.AgreementContexts != nil {
		contexts, err := NormalizeAgreementContexts(*update.AgreementContexts)
		if err != nil {
			return nil, newError("invalid_argument", "%s", err.Error())
		}
		value := pqStrings(contexts)
		next.AgreementContexts = &value
	}
	if update.CodeownersImport != nil {
		value := strings.TrimSpace(*update.CodeownersImport)
		if !ValidCodeownersImport(value) {
			return nil, newError("invalid_argument", "codeowners_import must be off or on")
		}
		next.CodeownersImport = &value
	}
	if update.CodeownersCacheTTLSeconds != nil {
		if !validTTL(*update.CodeownersCacheTTLSeconds) {
			return nil, newError("invalid_argument", "codeowners_cache_ttl_seconds must be from 10 to 86400")
		}
		value := *update.CodeownersCacheTTLSeconds
		next.CodeownersCacheTTLSeconds = &value
	}
	if update.PathAcceptance != nil {
		value := strings.TrimSpace(*update.PathAcceptance)
		if !ValidPathAcceptance(value) {
			return nil, newError("invalid_argument", "path_acceptance must be off, warn, or enforce")
		}
		next.PathAcceptance = &value
	}
	if update.PathOwners != nil {
		rules, err := NormalizePathOwners(*update.PathOwners)
		if err != nil {
			return nil, newError("invalid_argument", "%s", err.Error())
		}
		next.PathOwners = &rules
	}
	for _, name := range update.Inherit {
		switch name {
		case "report_mode":
			next.ReportMode = nil
		case "agreement_contexts":
			next.AgreementContexts = nil
		case "codeowners_import":
			next.CodeownersImport = nil
		case "codeowners_cache_ttl_seconds":
			next.CodeownersCacheTTLSeconds = nil
		case "path_acceptance":
			next.PathAcceptance = nil
		case "path_owners":
			next.PathOwners = nil
		}
	}
	if err := reports.UpsertProjectReportingSettings(ctx, &next); err != nil {
		return nil, newError("internal", "could not store the reporting settings")
	}
	fields := []string{}
	for name := range changed {
		fields = append(fields, name)
	}
	sortStrings(fields)
	s.record(ctx, project.OwnershipOrgID(), "reporting.settings", "project", project.ProjectID, models.JSONB{
		"project_id": project.ProjectID, "fields": fields, "inherit": update.Inherit,
		"before": settingsDetails(current), "after": settingsDetails(&next), "actor": caller.Actor.PrincipalID,
	})
	return s.GetSettings(ctx, caller, project)
}

func settingsDetails(settings *models.ProjectReportingSettings) models.JSONB {
	if settings == nil {
		return models.JSONB{}
	}
	out := models.JSONB{}
	if settings.ReportMode != nil {
		out["report_mode"] = *settings.ReportMode
	}
	if settings.AgreementContexts != nil {
		out["agreement_contexts"] = []string(*settings.AgreementContexts)
	}
	if settings.CodeownersImport != nil {
		out["codeowners_import"] = *settings.CodeownersImport
	}
	if settings.CodeownersCacheTTLSeconds != nil {
		out["codeowners_cache_ttl_seconds"] = *settings.CodeownersCacheTTLSeconds
	}
	if settings.PathAcceptance != nil {
		out["path_acceptance"] = *settings.PathAcceptance
	}
	if settings.PathOwners != nil {
		out["path_owners"] = *settings.PathOwners
	}
	return out
}

// GetOrgSettings returns the organization values. An org member can read
// them.
func (s *Service) GetOrgSettings(ctx context.Context, caller Caller, orgID string) (*models.OrgReportingSettings, bool, error) {
	reports, resolver, err := s.stores()
	if err != nil {
		return nil, false, err
	}
	role, err := resolver.EffectiveRoleForOrg(ctx, caller.Identity, orgID)
	if err != nil {
		return nil, false, newError("internal", "could not resolve the role of the caller")
	}
	if (role == "" && !roleless(caller)) || !tokenAllows(caller, orgID, tokencaps.ProjectsRead) {
		return nil, false, newError("permission_denied", "you cannot read the settings of this organization")
	}
	admin, err := s.isOrgAdmin(ctx, caller, orgID)
	if err != nil {
		return nil, false, err
	}
	settings, err := reports.GetOrgReportingSettings(ctx, orgID)
	if err != nil {
		return nil, false, newError("internal", "could not read the reporting settings")
	}
	if settings == nil {
		settings = models.DefaultOrgReportingSettings(orgID)
	}
	return settings, admin, nil
}

// SetOrgSettings replaces the organization values. Only an org admin can.
func (s *Service) SetOrgSettings(ctx context.Context, caller Caller, settings models.OrgReportingSettings) (*models.OrgReportingSettings, error) {
	reports, _, err := s.stores()
	if err != nil {
		return nil, err
	}
	admin, err := s.isOrgAdmin(ctx, caller, settings.OrgID)
	if err != nil {
		return nil, err
	}
	if !admin {
		return nil, newError("permission_denied", "only an org admin can change the reporting settings of the organization")
	}
	if settings.ReportMode == "" {
		settings.ReportMode = models.ReportModeFull
	}
	if settings.CodeownersImport == "" {
		settings.CodeownersImport = models.CodeownersImportOff
	}
	if settings.PathAcceptance == "" {
		settings.PathAcceptance = models.PathAcceptanceOff
	}
	if settings.CodeownersCacheTTLSeconds == 0 {
		settings.CodeownersCacheTTLSeconds = models.DefaultCodeownersCacheTTLSeconds
	}
	if !ValidReportMode(settings.ReportMode) {
		return nil, newError("invalid_argument", "report_mode must be full, statuses_only, or silent")
	}
	if !ValidCodeownersImport(settings.CodeownersImport) {
		return nil, newError("invalid_argument", "codeowners_import must be off or on")
	}
	if !ValidPathAcceptance(settings.PathAcceptance) {
		return nil, newError("invalid_argument", "path_acceptance must be off, warn, or enforce")
	}
	if !validTTL(settings.CodeownersCacheTTLSeconds) {
		return nil, newError("invalid_argument", "codeowners_cache_ttl_seconds must be from 10 to 86400")
	}
	contexts, err := NormalizeAgreementContexts(settings.AgreementContexts)
	if err != nil {
		return nil, newError("invalid_argument", "%s", err.Error())
	}
	settings.AgreementContexts = pqStrings(contexts)
	rules, err := NormalizePathOwners(settings.PathOwners)
	if err != nil {
		return nil, newError("invalid_argument", "%s", err.Error())
	}
	settings.PathOwners = rules
	before, _ := reports.GetOrgReportingSettings(ctx, settings.OrgID)
	if err := reports.UpsertOrgReportingSettings(ctx, &settings); err != nil {
		return nil, newError("internal", "could not store the reporting settings")
	}
	s.record(ctx, settings.OrgID, "reporting.org_settings", "organization", settings.OrgID, models.JSONB{
		"before": before, "after": settings, "actor": caller.Actor.PrincipalID})
	return &settings, nil
}

// AgreementView is the agreement report of a project.
type AgreementView struct {
	Contexts []string             `json:"contexts"`
	Summary  Summary              `json:"summary"`
	Rows     []models.CIAgreement `json:"rows"`
}

// GetAgreement returns the agreement report of a project. since nil reads
// every stored row (at most limit).
func (s *Service) GetAgreement(ctx context.Context, caller Caller, project *models.Project, since *time.Time, limit int) (*AgreementView, error) {
	reports, _, err := s.stores()
	if err != nil {
		return nil, err
	}
	perms, err := s.ProjectPermissions(ctx, caller, project)
	if err != nil {
		return nil, err
	}
	if !perms.View {
		return nil, newError("permission_denied", "you cannot read the agreement report of this project")
	}
	settings, err := Effective(ctx, s.Store, project.OwnershipOrgID(), project.ProjectID)
	if err != nil {
		return nil, newError("internal", "could not read the reporting settings")
	}
	rows, err := reports.ListCIAgreements(ctx, project.ProjectID, since, limit)
	if err != nil {
		return nil, newError("internal", "could not read the agreement report")
	}
	return &AgreementView{Contexts: settings.AgreementContexts, Summary: Summarize(rows), Rows: rows}, nil
}

// AcceptanceRequest is a request for an acceptance.
type AcceptanceRequest struct {
	WorkflowSecurityID string `json:"workflow_security_id"`
	PathPattern        string `json:"path_pattern"`
	OwnerSubject       string `json:"owner_subject"`
	Reason             string `json:"reason,omitempty"`
	// Accept: the caller is the owner and accepts at once.
	Accept bool `json:"accept,omitempty"`
}

var workflowIDPattern = func(value string) bool {
	if value == "" || len(value) > 200 {
		return false
	}
	for _, c := range value {
		if c < 0x21 || c == 0x7f {
			return false
		}
	}
	return true
}

func (s *Service) callerSubjects(ctx context.Context, caller Caller, project *models.Project) ([]string, error) {
	have, err := requiredset.CallerSubjects(ctx, s.Store, caller, project)
	if err != nil {
		return nil, err
	}
	return append(have, linkedTeamSubjects(ctx, s.Store, caller)...), nil
}

// linkedTeamSubjects adds vcs_team:OWNER/SLUG for each Reactorcide group of
// the caller that is linked to a VCS team (WP8). A REST caller has no live
// team lookup, so this is how a code owners team can accept in the UI or
// the CLI.
func linkedTeamSubjects(ctx context.Context, value any, caller Caller) []string {
	principalID := caller.Identity.UserID
	if principalID == "" {
		principalID = caller.Identity.PrincipalID
	}
	groups, ok := value.(interface {
		ListGroupsForPrincipal(ctx context.Context, principalID string) ([]models.Group, error)
	})
	links, ok2 := value.(interface {
		ListGroupVCSLinks(ctx context.Context, groupID string) ([]models.GroupVCSLink, error)
	})
	if principalID == "" || !ok || !ok2 {
		return nil
	}
	list, err := groups.ListGroupsForPrincipal(ctx, principalID)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, group := range list {
		rows, err := links.ListGroupVCSLinks(ctx, group.GroupID)
		if err != nil {
			continue
		}
		for _, link := range rows {
			out = append(out, link.Subject())
		}
	}
	return out
}

func (s *Service) isPathOwner(ctx context.Context, caller Caller, project *models.Project, owner string) (bool, error) {
	admin, err := s.isOrgAdmin(ctx, caller, project.OwnershipOrgID())
	if err != nil {
		return false, err
	}
	if admin {
		return true, nil
	}
	if !tokenAllows(caller, project.OwnershipOrgID(), tokencaps.PoliciesManage) {
		return false, nil
	}
	have, err := s.callerSubjects(ctx, caller, project)
	if err != nil {
		return false, newError("internal", "could not resolve the subjects of the caller")
	}
	for _, subject := range have {
		if strings.EqualFold(subject, owner) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) isWorkflowSide(ctx context.Context, caller Caller, project *models.Project, workflowID string) (bool, error) {
	_, resolver, err := s.stores()
	if err != nil {
		return false, err
	}
	admin, err := s.isOrgAdmin(ctx, caller, project.OwnershipOrgID())
	if err != nil {
		return false, err
	}
	if admin {
		return true, nil
	}
	owner, err := resolver.IsProjectOwner(ctx, caller.Identity, project.ProjectID)
	if err != nil {
		return false, newError("internal", "could not resolve the role of the caller")
	}
	if owner && tokenAllows(caller, project.OwnershipOrgID(), tokencaps.PoliciesManage) {
		return true, nil
	}
	return requiredset.OwnerApprover != nil && requiredset.OwnerApprover(ctx, s.Store, project, workflowID, caller), nil
}

// RequestAcceptance stores a request (or, with Accept by an owner, an
// acceptance).
func (s *Service) RequestAcceptance(ctx context.Context, caller Caller, project *models.Project, req AcceptanceRequest) (*models.PathAcceptance, error) {
	reports, _, err := s.stores()
	if err != nil {
		return nil, err
	}
	req.WorkflowSecurityID = strings.TrimSpace(req.WorkflowSecurityID)
	req.PathPattern = strings.TrimSpace(req.PathPattern)
	req.OwnerSubject = strings.TrimSpace(req.OwnerSubject)
	if !workflowIDPattern(req.WorkflowSecurityID) {
		return nil, newError("invalid_argument", "workflow_security_id is invalid")
	}
	if req.PathPattern == "" || len(req.PathPattern) > MaxPatternLength {
		return nil, newError("invalid_argument", "path_pattern is invalid")
	}
	if err := ValidatePattern(req.PathPattern); err != nil {
		return nil, newError("invalid_argument", "%s", err.Error())
	}
	if !ValidPathOwnerSubject(req.OwnerSubject) {
		return nil, newError("invalid_argument", "owner_subject %q is not a valid owner subject", req.OwnerSubject)
	}
	if len(req.Reason) > 1000 {
		return nil, newError("invalid_argument", "reason is longer than 1000 characters")
	}
	workflowSide, err := s.isWorkflowSide(ctx, caller, project, req.WorkflowSecurityID)
	if err != nil {
		return nil, err
	}
	ownerSide := false
	if req.Accept || !workflowSide {
		if ownerSide, err = s.isPathOwner(ctx, caller, project, req.OwnerSubject); err != nil {
			return nil, err
		}
	}
	if req.Accept && !ownerSide {
		return nil, newError("permission_denied", "only the owner %s or an org admin can accept", req.OwnerSubject)
	}
	if !workflowSide && !ownerSide {
		return nil, newError("permission_denied", "only a workflow owner, a project owner, an owner of the path, or an org admin can request an acceptance")
	}
	now := s.now()
	actor := caller.Actor.Ptr()
	row := &models.PathAcceptance{ProjectID: project.ProjectID, OrgID: project.OwnershipOrgID(),
		WorkflowSecurityID: req.WorkflowSecurityID, PathPattern: req.PathPattern, OwnerSubject: req.OwnerSubject,
		State: models.PathAcceptanceRequested, Reason: strings.TrimSpace(req.Reason), RequestedBy: actor, RequestedAt: &now}
	if req.Accept {
		row.State, row.AcceptedBy, row.AcceptedAt = models.PathAcceptanceAccepted, actor, &now
	}
	if err := reports.CreatePathAcceptance(ctx, row); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			return nil, newError("conflict", "an open request or acceptance exists for this workflow, pattern, and owner")
		}
		return nil, newError("internal", "could not store the request")
	}
	action := "path_acceptance.request"
	if req.Accept {
		action = "path_acceptance.accept"
	}
	s.record(ctx, project.OwnershipOrgID(), action, "path_acceptance", row.AcceptanceID, acceptanceDetails(row, caller))
	return row, nil
}

func acceptanceDetails(row *models.PathAcceptance, caller Caller) models.JSONB {
	return models.JSONB{"project_id": row.ProjectID, "workflow_security_id": row.WorkflowSecurityID,
		"path_pattern": row.PathPattern, "owner_subject": row.OwnerSubject, "state": row.State,
		"reason": row.Reason, "decision_reason": row.DecisionReason, "actor": caller.Actor.PrincipalID}
}

// Decisions on a row.
const (
	DecisionAccept = "accept"
	DecisionReject = "reject"
	DecisionRevoke = "revoke"
)

// DecideAcceptance accepts, rejects, or revokes a row.
func (s *Service) DecideAcceptance(ctx context.Context, caller Caller, project *models.Project, acceptanceID, decision, reason string) (*models.PathAcceptance, error) {
	reports, _, err := s.stores()
	if err != nil {
		return nil, err
	}
	row, err := reports.GetPathAcceptance(ctx, strings.TrimSpace(acceptanceID))
	if err != nil || row == nil || row.ProjectID != project.ProjectID {
		return nil, newError("not_found", "path acceptance not found")
	}
	if len(reason) > 1000 {
		return nil, newError("invalid_argument", "reason is longer than 1000 characters")
	}
	ownerSide, err := s.isPathOwner(ctx, caller, project, row.OwnerSubject)
	if err != nil {
		return nil, err
	}
	now := s.now()
	actor := caller.Actor.Ptr()
	row.DecisionReason = strings.TrimSpace(reason)
	var from []string
	switch decision {
	case DecisionAccept, DecisionReject:
		if !ownerSide {
			return nil, newError("permission_denied", "only the owner %s or an org admin can %s", row.OwnerSubject, decision)
		}
		from = []string{models.PathAcceptanceRequested}
		if decision == DecisionAccept {
			row.State, row.AcceptedBy, row.AcceptedAt = models.PathAcceptanceAccepted, actor, &now
		} else {
			row.State, row.RevokedBy, row.RevokedAt = models.PathAcceptanceRejected, actor, &now
		}
	case DecisionRevoke:
		if !ownerSide {
			workflowSide, err := s.isWorkflowSide(ctx, caller, project, row.WorkflowSecurityID)
			if err != nil {
				return nil, err
			}
			if !workflowSide {
				return nil, newError("permission_denied", "only the owner, a workflow owner, or an org admin can revoke")
			}
		}
		from = []string{models.PathAcceptanceRequested, models.PathAcceptanceAccepted}
		row.State, row.RevokedBy, row.RevokedAt = models.PathAcceptanceRevoked, actor, &now
	default:
		return nil, newError("invalid_argument", "decision must be accept, reject, or revoke")
	}
	if err := reports.TransitionPathAcceptance(ctx, row, from...); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, newError("conflict", "the row is not in a state that allows %s", decision)
		}
		return nil, newError("internal", "could not change the row")
	}
	s.record(ctx, project.OwnershipOrgID(), "path_acceptance."+decision, "path_acceptance", row.AcceptanceID, acceptanceDetails(row, caller))
	return row, nil
}

// AcceptancesView lists the rows of a project, with the ownership map that
// admission uses now.
type AcceptancesView struct {
	Mode        string                  `json:"path_acceptance"`
	Rows        []models.PathAcceptance `json:"rows"`
	PathOwners  []models.PathOwnerRule  `json:"path_owners"`
	Source      string                  `json:"ownership_source"`
	Permissions Permissions             `json:"permissions"`
}

// ListAcceptances lists the rows of a project.
func (s *Service) ListAcceptances(ctx context.Context, caller Caller, project *models.Project, state string) (*AcceptancesView, error) {
	reports, _, err := s.stores()
	if err != nil {
		return nil, err
	}
	perms, err := s.ProjectPermissions(ctx, caller, project)
	if err != nil {
		return nil, err
	}
	if !perms.View {
		return nil, newError("permission_denied", "you cannot read the path acceptances of this project")
	}
	switch state {
	case "", models.PathAcceptanceRequested, models.PathAcceptanceAccepted, models.PathAcceptanceRejected, models.PathAcceptanceRevoked:
	default:
		return nil, newError("invalid_argument", "unknown state %q", state)
	}
	rows, err := reports.ListPathAcceptances(ctx, project.ProjectID, state)
	if err != nil {
		return nil, newError("internal", "could not read the path acceptances")
	}
	settings, err := Effective(ctx, s.Store, project.OwnershipOrgID(), project.ProjectID)
	if err != nil {
		return nil, newError("internal", "could not read the reporting settings")
	}
	source := "registry"
	if settings.CodeownersImport == models.CodeownersImportOn {
		source = "codeowners"
	}
	return &AcceptancesView{Mode: settings.PathAcceptance, Rows: rows, PathOwners: settings.PathOwners, Source: source,
		Permissions: perms}, nil
}
