package requiredset

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	subjectgrammar "github.com/catalystcommunity/reactorcide/coordinator_api/internal/subjects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// Authorization (decided for WP6, see docs/required-set.md):
//
//   - Read the set, and propose a promotion or a demotion: any role in the
//     project or its organization (project member and above). A service
//     principal token with such a role works.
//   - Turn the set on or off, and change the approver settings: an org
//     admin. The admin token is an org admin of every organization, so a
//     deployment with no logins can do all of it.
//   - Approve or reject a proposal, quarantine, and end a quarantine: an
//     approver of the entry. The approvers come from the first approver rule
//     that matches the branch pattern of the entry, else from the
//     required_set_approvers setting. With no subject configured, only org
//     admins approve. An org admin is always an approver: the org admin can
//     change the setting, so to refuse the org admin protects nothing.
//   - Withdraw a proposal: the proposer or an approver.
//   - Quarantine: also the owner group of the workflow, through the
//     OwnerApprover hook (WP7 sets it).

// OwnerApprover reports whether the caller is in the owner group of a
// workflow. WP7 (workflow owners) sets it. nil means no owners are known.
var OwnerApprover func(ctx context.Context, value any, project *models.Project, securityID string, caller Caller) bool

// Error is a refused or failed operation. Code is one of permission_denied,
// invalid_argument, not_found, conflict, unavailable, internal.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func newError(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrorCode returns the code of an Error, or "internal".
func ErrorCode(err error) string {
	var value *Error
	if errors.As(err, &value) {
		return value.Code
	}
	return "internal"
}

// Caller is the person or service that calls an operation.
type Caller struct {
	Identity authz.Identity
	// Actor is the principal for the records (added_by, proposed_by). It is
	// zero for the admin token that has no principal.
	Actor principals.Actor
	// ExternalSubjects are approver subjects that a live VCS lookup found
	// for the caller (vcs_user:, vcs_team:, repository_write). Only the
	// pull request comment path sets them, from a signed webhook. A caller
	// whose external subjects make it an approver may view, propose, and
	// decide also with no Reactorcide role.
	ExternalSubjects []string
}

// Recorder writes one audit event.
type Recorder func(ctx context.Context, orgID, action, subjectType, subjectID string, details models.JSONB)

// Service runs the required set operations for every channel (REST, CLI,
// UI), so the authorization and the audit events are the same everywhere.
type Service struct {
	// Store must implement store.RequiredSetStore and authz.RoleStore.
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

func (s *Service) sets() (store.RequiredSetStore, *authz.Resolver, error) {
	st, ok := s.Store.(store.RequiredSetStore)
	if !ok {
		return nil, nil, newError("unavailable", "the required set is not available")
	}
	roles, ok := s.Store.(authz.RoleStore)
	if !ok {
		return nil, nil, newError("unavailable", "the required set is not available")
	}
	return st, authz.NewResolver(roles), nil
}

func (s *Service) record(ctx context.Context, project *models.Project, action, subjectID string, details models.JSONB) {
	if s.Record == nil {
		return
	}
	details["project_id"] = project.ProjectID
	s.Record(ctx, project.OwnershipOrgID(), action, "required_set", subjectID, details)
}

// Permissions are what the caller may do with the set of a project.
type Permissions struct {
	View    bool
	Propose bool
	// Manage: turn the set on or off and change the approver settings.
	Manage bool
	// ApproveAllBranches: the caller is an approver of an entry for every
	// branch (the usual entry). Other entries can have other approvers.
	ApproveAllBranches bool
}

func (s *Service) permissions(ctx context.Context, caller Caller, project *models.Project) (Permissions, error) {
	_, resolver, err := s.sets()
	if err != nil {
		return Permissions{}, err
	}
	var perms Permissions
	role, err := resolver.EffectiveRoleForProject(ctx, caller.Identity, project.ProjectID)
	if err != nil {
		return Permissions{}, newError("internal", "could not resolve the role of the caller")
	}
	if role == "" {
		role, err = resolver.EffectiveRoleForOrg(ctx, caller.Identity, project.OwnershipOrgID())
		if err != nil {
			return Permissions{}, newError("internal", "could not resolve the role of the caller")
		}
	}
	orgID := project.OwnershipOrgID()
	perms.View = role != "" && tokenAllows(caller, orgID, tokencaps.ProjectsRead)
	if roleless(caller) {
		// An instance or service token with no principal roles (the admin
		// token of a deployment with no logins) acts by its capabilities.
		perms.View = tokenAllows(caller, orgID, tokencaps.ProjectsRead)
	}
	perms.Propose = perms.View && tokenAllows(caller, orgID, tokencaps.PoliciesManage)
	admin, err := s.isOrgAdmin(ctx, caller, orgID)
	if err != nil {
		return Permissions{}, err
	}
	perms.Manage = admin
	if !perms.View && len(caller.ExternalSubjects) > 0 {
		approver, _, _, err := s.isApprover(ctx, caller, project, "")
		if err != nil {
			return Permissions{}, err
		}
		perms.View, perms.Propose = approver, approver
	}
	if perms.View {
		approver, _, _, err := s.isApprover(ctx, caller, project, "")
		if err != nil {
			return Permissions{}, err
		}
		perms.ApproveAllBranches = approver
	}
	return perms, nil
}

// roleless reports whether the caller is a token with no roles of its own.
func roleless(caller Caller) bool {
	return caller.Identity.Token != nil && caller.Identity.UserID == "" && caller.Identity.PrincipalID == ""
}

// tokenAllows reports whether the token of the caller (if any) allows the
// capability in the organization. A session with no token passes. Every
// token must name the organization. A service principal token must have the
// capability in its ISSUED capabilities (NarrowServicePrincipalToken narrows
// a non-admin principal to read capabilities, so the effective set would
// refuse every non-admin service principal in an approver group); its roles
// and subjects then decide, the same as for an approval in a pull request
// comment. Other tokens need the capability in their effective set.
func tokenAllows(caller Caller, orgID, capability string) bool {
	token := caller.Identity.Token
	if token == nil {
		return true
	}
	if !token.HasOrganization(orgID) {
		return false
	}
	if token.IsServicePrincipalToken() {
		return token.HasIssuedCapability(capability)
	}
	return token.HasCapability(capability)
}

// isOrgAdmin reports whether the caller is an org admin and its token (if
// any) allows policy changes.
func (s *Service) isOrgAdmin(ctx context.Context, caller Caller, orgID string) (bool, error) {
	_, resolver, err := s.sets()
	if err != nil {
		return false, err
	}
	admin, err := resolver.IsOrgAdmin(ctx, caller.Identity, orgID)
	if err != nil {
		return false, newError("internal", "could not resolve the role of the caller")
	}
	return admin && tokenAllows(caller, orgID, tokencaps.PoliciesManage), nil
}

// approverSettings returns the effective approver settings of a project.
func (s *Service) approverSettings(ctx context.Context, project *models.Project) ([]string, []models.RequiredSetApproverRule, *models.OrgRequiredSetSettings, *models.ProjectRequiredSetSettings, error) {
	st, _, err := s.sets()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	org, err := st.GetOrgRequiredSetSettings(ctx, project.OwnershipOrgID())
	if err != nil {
		return nil, nil, nil, nil, newError("internal", "could not read the approver settings")
	}
	settings, err := st.GetProjectRequiredSetSettings(ctx, project.ProjectID)
	if err != nil {
		return nil, nil, nil, nil, newError("internal", "could not read the required set settings")
	}
	approvers, rules := EffectiveApproverSettings(org, settings)
	return approvers, rules, org, settings, nil
}

// isApprover reports whether the caller approves changes of an entry with
// branchPattern, and returns the approver subjects and their source.
func (s *Service) isApprover(ctx context.Context, caller Caller, project *models.Project, branchPattern string) (bool, []string, string, error) {
	defaults, rules, _, _, err := s.approverSettings(ctx, project)
	if err != nil {
		return false, nil, "", err
	}
	approvers, source := ApproversFor(defaults, rules, branchPattern)
	admin, err := s.isOrgAdmin(ctx, caller, project.OwnershipOrgID())
	if err != nil {
		return false, nil, "", err
	}
	if admin {
		return true, approvers, source, nil
	}
	if len(approvers) == 0 || !tokenAllows(caller, project.OwnershipOrgID(), tokencaps.PoliciesManage) {
		return false, approvers, source, nil
	}
	have, err := CallerSubjects(ctx, s.Store, caller, project)
	if err != nil {
		return false, nil, "", newError("internal", "could not resolve the subjects of the caller")
	}
	have = append(have, caller.ExternalSubjects...)
	return subjectgrammar.HasAny(have, approvers), approvers, source, nil
}

// IsApprover reports whether the caller approves changes of the entries of
// the project with branchPattern ("" is every branch).
func (s *Service) IsApprover(ctx context.Context, caller Caller, project *models.Project, branchPattern string) (bool, error) {
	approver, _, _, err := s.isApprover(ctx, caller, project, strings.TrimSpace(branchPattern))
	return approver, err
}

type orgByID interface {
	GetOrganizationByID(ctx context.Context, orgID string) (*models.Organization, error)
}

// CallerSubjects returns the approver subjects of a caller in a project: the
// subjects of the identities of its principal (principal:, identity:,
// vcs_user:, service:), its Reactorcide groups in the organization, and
// project_owner. The admin token has no principal and no subjects; it
// approves as an org admin. vcs_team and repository_write need a live VCS
// lookup and do not match here (docs/required-set.md).
func CallerSubjects(ctx context.Context, value any, caller Caller, project *models.Project) ([]string, error) {
	principalID := caller.Identity.UserID
	if principalID == "" {
		principalID = caller.Identity.PrincipalID
	}
	if principalID == "" || caller.Identity.Anonymous {
		return nil, nil
	}
	principalStore, ok := value.(principals.Store)
	if !ok {
		return nil, nil
	}
	resolution, err := principals.NewResolver(principalStore, 0).ResolveID(ctx, principalID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if resolution.Inactive {
		return nil, nil
	}
	orgID := project.OwnershipOrgID()
	scope := principals.SubjectContext{OrgID: orgID}
	if orgs, ok := value.(orgByID); ok {
		if org, err := orgs.GetOrganizationByID(ctx, orgID); err == nil && org != nil {
			scope.OrgName = org.Name
		}
	}
	result := principals.Subjects(resolution, scope)
	addGroups := func(groups []models.Group) {
		for _, group := range groups {
			if group.OrgID == orgID {
				result = append(result, subjectgrammar.PrefixGroup+group.Name)
			}
		}
	}
	if groups, ok := value.(principals.GroupStore); ok {
		list, err := groups.ListGroupsForPrincipal(ctx, resolution.Principal.PrincipalID)
		if err != nil {
			return nil, err
		}
		addGroups(list)
	}
	roles, ok := value.(authz.RoleStore)
	if ok && caller.Identity.UserID != "" {
		list, err := roles.ListGroupsForUser(ctx, caller.Identity.UserID)
		if err != nil {
			return nil, err
		}
		addGroups(list)
	}
	if ok {
		owner, err := authz.NewResolver(roles).IsProjectOwner(ctx, caller.Identity, project.ProjectID)
		if err != nil {
			return nil, err
		}
		if owner {
			result = append(result, subjectgrammar.ProjectOwner)
		}
	}
	seen := map[string]bool{}
	unique := []string{}
	for _, subject := range result {
		if !seen[subject] {
			seen[subject] = true
			unique = append(unique, subject)
		}
	}
	return unique, nil
}

// View is the required set of a project as the caller sees it.
type View struct {
	Enabled           bool
	EnabledAt         *time.Time
	EnabledBy         *string
	Rows              []Row
	Proposals         []models.RequiredSetProposal
	Approvers         []string
	ApproversSource   string
	ApproverRules     []models.RequiredSetApproverRule
	ApproversInherit  bool
	RulesInherit      bool
	OrgApprovers      []string
	OrgApproverRules  []models.RequiredSetApproverRule
	MaxQuarantineDays int
	Permissions       Permissions
}

// Get returns the view of the set of a project.
func (s *Service) Get(ctx context.Context, caller Caller, project *models.Project) (*View, error) {
	st, _, err := s.sets()
	if err != nil {
		return nil, err
	}
	perms, err := s.permissions(ctx, caller, project)
	if err != nil {
		return nil, err
	}
	if !perms.View {
		return nil, newError("permission_denied", "a role in the project or its organization is required")
	}
	defaults, rules, org, settings, err := s.approverSettings(ctx, project)
	if err != nil {
		return nil, err
	}
	entries, err := st.ListRequiredWorkflows(ctx, project.ProjectID)
	if err != nil {
		return nil, newError("internal", "could not list the required set")
	}
	known, err := st.ListKnownWorkflows(ctx, project.ProjectID)
	if err != nil {
		return nil, newError("internal", "could not list the workflows of the project")
	}
	proposals, err := st.ListRequiredSetProposals(ctx, project.ProjectID, "", 100)
	if err != nil {
		return nil, newError("internal", "could not list the proposals")
	}
	pending := []models.RequiredSetProposal{}
	for _, proposal := range proposals {
		if proposal.State == models.RequiredSetProposalPending {
			pending = append(pending, proposal)
		}
	}
	approvers, source := ApproversFor(defaults, nil, "")
	view := &View{
		Enabled: settings.Enabled, EnabledAt: settings.EnabledAt, EnabledBy: settings.EnabledBy,
		Rows:      Rows(settings.Enabled, known, entries, pending, s.now()),
		Proposals: proposals, Approvers: approvers, ApproversSource: source, ApproverRules: rules,
		ApproversInherit: settings.Approvers == nil, RulesInherit: settings.ApproverRules == nil,
		OrgApprovers: append([]string{}, org.Approvers...), OrgApproverRules: org.ApproverRules,
		MaxQuarantineDays: org.MaxQuarantineDays, Permissions: perms,
	}
	if view.ApproverRules == nil {
		view.ApproverRules = []models.RequiredSetApproverRule{}
	}
	return view, nil
}

// ciPolicyReader and orgReader are the store surfaces that
// requireTrustedDefinitions reads.
type ciPolicyReader interface {
	GetCIPolicyByProject(ctx context.Context, projectID string) (*models.CIPolicy, error)
}

// requireTrustedDefinitions refuses a project whose pull requests can run
// the head's own CI: a project with no stored CI policy in an organization
// that does not use the secure defaults profile. There, a pull request can
// remove or rename a required workflow in its own CI definition, and the
// required set cannot see that the workflow is missing. A stored policy or
// the built-in policy of the secure profile makes the eval read the trusted
// base definitions.
func (s *Service) requireTrustedDefinitions(ctx context.Context, project *models.Project) error {
	const why = "the required set needs trusted CI definitions: store a CI policy for the project " +
		"(reactorcide policy set) or use the secure defaults profile for the organization. " +
		"Without one, a pull request runs its own CI definition and can remove a required workflow"
	if policies, ok := s.Store.(ciPolicyReader); ok {
		policy, err := policies.GetCIPolicyByProject(ctx, project.ProjectID)
		switch {
		case err == nil && policy != nil:
			return nil
		case err != nil && !errors.Is(err, store.ErrNotFound):
			return newError("internal", "could not read the CI policy of the project")
		}
	}
	if orgs, ok := s.Store.(orgByID); ok {
		org, err := orgs.GetOrganizationByID(ctx, project.OwnershipOrgID())
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return newError("internal", "could not read the organization of the project")
		}
		if err == nil && org != nil && models.UsesBuiltInPolicy(org.DefaultsProfile) {
			return nil
		}
	}
	return newError("failed_precondition", "%s", why)
}

// SetEnabled turns the set of a project on or off. Turning it on adds every
// known workflow to the set; it returns the IDs that it added.
func (s *Service) SetEnabled(ctx context.Context, caller Caller, project *models.Project, enabled bool) ([]string, error) {
	st, _, err := s.sets()
	if err != nil {
		return nil, err
	}
	if admin, err := s.isOrgAdmin(ctx, caller, project.OwnershipOrgID()); err != nil || !admin {
		return nil, newError("permission_denied", "an org admin is required to turn the required set on or off")
	}
	if enabled {
		if err := s.requireTrustedDefinitions(ctx, project); err != nil {
			return nil, err
		}
	}
	seeded, err := st.SetRequiredSetEnabled(ctx, project.ProjectID, project.OwnershipOrgID(), enabled, caller.Actor.Ptr())
	if err != nil {
		return nil, newError("internal", "could not change the required set")
	}
	if seeded == nil {
		seeded = []string{}
	}
	action := "required_set.disable"
	details := models.JSONB{}
	if enabled {
		action = "required_set.enable"
		details["seeded"] = seeded
	}
	s.record(ctx, project, action, project.ProjectID, details)
	return seeded, nil
}

// ValidateApproverRules checks the approver rules.
func ValidateApproverRules(rules []models.RequiredSetApproverRule) error {
	for i, rule := range rules {
		if len(rule.Branches) == 0 {
			return fmt.Errorf("approver rule %d has no branches", i+1)
		}
		for _, glob := range rule.Branches {
			if err := validatePattern(glob, true); err != nil {
				return fmt.Errorf("approver rule %d: %w", i+1, err)
			}
		}
		if len(rule.Approvers) == 0 {
			return fmt.Errorf("approver rule %d has no approvers", i+1)
		}
		if err := ValidateSubjects(rule.Approvers); err != nil {
			return fmt.Errorf("approver rule %d: %w", i+1, err)
		}
	}
	return nil
}

// ValidateSubjects checks an approver subject list.
func ValidateSubjects(values []string) error {
	for _, subject := range values {
		if !subjectgrammar.Valid(strings.TrimSpace(subject)) {
			return fmt.Errorf("%q is not a valid subject", subject)
		}
	}
	return nil
}

func trimAll(values []string) []string {
	if values == nil {
		return nil
	}
	result := []string{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func normalizeRules(rules []models.RequiredSetApproverRule) []models.RequiredSetApproverRule {
	result := make([]models.RequiredSetApproverRule, 0, len(rules))
	for _, rule := range rules {
		result = append(result, models.RequiredSetApproverRule{Branches: trimAll(rule.Branches), Approvers: trimAll(rule.Approvers)})
	}
	return result
}

// SetProjectApprovers replaces the approver settings of a project. nil
// approvers or nil rules inherit the organization value.
func (s *Service) SetProjectApprovers(ctx context.Context, caller Caller, project *models.Project, approvers []string, rules *[]models.RequiredSetApproverRule) error {
	st, _, err := s.sets()
	if err != nil {
		return err
	}
	if admin, err := s.isOrgAdmin(ctx, caller, project.OwnershipOrgID()); err != nil || !admin {
		return newError("permission_denied", "an org admin is required to change the required set approvers")
	}
	approvers = trimAll(approvers)
	if err := ValidateSubjects(approvers); err != nil {
		return newError("invalid_argument", "required_set_approvers: %s", err.Error())
	}
	if rules != nil {
		normalized := normalizeRules(*rules)
		if err := ValidateApproverRules(normalized); err != nil {
			return newError("invalid_argument", "required_set_approver_rules: %s", err.Error())
		}
		rules = &normalized
	}
	if err := st.SetProjectRequiredSetApprovers(ctx, project.ProjectID, project.OwnershipOrgID(), approvers, rules); err != nil {
		return newError("internal", "could not save the approver settings")
	}
	details := models.JSONB{"approvers": approvers, "approvers_inherit": approvers == nil, "rules_inherit": rules == nil}
	if rules != nil {
		details["approver_rules"] = *rules
	}
	s.record(ctx, project, "required_set.approvers", project.ProjectID, details)
	return nil
}

// SetOrgSettings replaces the organization defaults. It needs an org admin.
func (s *Service) SetOrgSettings(ctx context.Context, caller Caller, orgID string, approvers []string, rules []models.RequiredSetApproverRule, maxQuarantineDays int) error {
	st, _, err := s.sets()
	if err != nil {
		return err
	}
	if admin, err := s.isOrgAdmin(ctx, caller, orgID); err != nil || !admin {
		return newError("permission_denied", "an org admin is required to change the required set settings")
	}
	approvers = trimAll(approvers)
	if err := ValidateSubjects(approvers); err != nil {
		return newError("invalid_argument", "required_set_approvers: %s", err.Error())
	}
	rules = normalizeRules(rules)
	if err := ValidateApproverRules(rules); err != nil {
		return newError("invalid_argument", "required_set_approver_rules: %s", err.Error())
	}
	if maxQuarantineDays == 0 {
		maxQuarantineDays = models.DefaultMaxQuarantineDays
	}
	if maxQuarantineDays < 1 || maxQuarantineDays > 365 {
		return newError("invalid_argument", "max_quarantine_days must be from 1 to 365")
	}
	settings := &models.OrgRequiredSetSettings{OrgID: orgID, Approvers: approvers, ApproverRules: rules, MaxQuarantineDays: maxQuarantineDays}
	if err := st.UpsertOrgRequiredSetSettings(ctx, settings); err != nil {
		return newError("internal", "could not save the required set settings")
	}
	if s.Record != nil {
		s.Record(ctx, orgID, "required_set.org_settings", "organization", orgID, models.JSONB{
			"approvers": approvers, "approver_rules": rules, "max_quarantine_days": maxQuarantineDays})
	}
	return nil
}

var controlChars = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// ValidateSecurityID checks a workflow security ID.
func ValidateSecurityID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("the workflow ID is empty")
	}
	if len(id) > 200 || controlChars.MatchString(id) || strings.HasPrefix(id, "file:") {
		return fmt.Errorf("%q is not a valid workflow ID", id)
	}
	return nil
}

func validatePattern(pattern string, required bool) error {
	if pattern == "" {
		if required {
			return errors.New("a branch glob is empty")
		}
		return nil
	}
	if len(pattern) > 200 || strings.IndexFunc(pattern, unicode.IsSpace) >= 0 || controlChars.MatchString(pattern) {
		return fmt.Errorf("%q is not a valid branch glob", pattern)
	}
	return nil
}

func validateReason(reason string) error {
	if len(reason) > 1000 {
		return errors.New("the reason has more than 1000 characters")
	}
	return nil
}

func hasEntry(entries []models.RequiredWorkflow, securityID, pattern string) *models.RequiredWorkflow {
	for i := range entries {
		if entries[i].WorkflowSecurityID == securityID && entries[i].BranchPattern == pattern {
			return &entries[i]
		}
	}
	return nil
}

// ProposeRequest is a promotion or a demotion.
type ProposeRequest struct {
	Action             string
	WorkflowSecurityID string
	BranchPattern      string
	Reason             string
	// Approve approves the proposal at once. The caller must be an approver.
	Approve bool
}

// Propose stores a pending promotion or demotion. With Approve, an approver
// proposes and approves in one call (two audit events).
func (s *Service) Propose(ctx context.Context, caller Caller, project *models.Project, req ProposeRequest) (*models.RequiredSetProposal, error) {
	st, _, err := s.sets()
	if err != nil {
		return nil, err
	}
	req.WorkflowSecurityID = strings.TrimSpace(req.WorkflowSecurityID)
	req.BranchPattern = strings.TrimSpace(req.BranchPattern)
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Action != models.RequiredSetActionPromote && req.Action != models.RequiredSetActionDemote {
		return nil, newError("invalid_argument", "the action must be promote or demote")
	}
	if err := ValidateSecurityID(req.WorkflowSecurityID); err != nil {
		return nil, newError("invalid_argument", "%s", err.Error())
	}
	if err := validatePattern(req.BranchPattern, false); err != nil {
		return nil, newError("invalid_argument", "%s", err.Error())
	}
	if err := validateReason(req.Reason); err != nil {
		return nil, newError("invalid_argument", "%s", err.Error())
	}
	perms, err := s.permissions(ctx, caller, project)
	if err != nil {
		return nil, err
	}
	// WP7: an owner of the workflow can propose changes for that workflow.
	if !perms.Propose && (OwnerApprover == nil || !OwnerApprover(ctx, s.Store, project, req.WorkflowSecurityID, caller)) {
		return nil, newError("permission_denied", "a role in the project or its organization is required to propose a change")
	}
	if req.Approve {
		approver, _, _, err := s.isApprover(ctx, caller, project, req.BranchPattern)
		if err != nil {
			return nil, err
		}
		if !approver {
			return nil, newError("permission_denied", "only an approver can propose and approve in one step")
		}
	}
	entries, err := st.ListRequiredWorkflows(ctx, project.ProjectID)
	if err != nil {
		return nil, newError("internal", "could not list the required set")
	}
	existing := hasEntry(entries, req.WorkflowSecurityID, req.BranchPattern)
	if req.Action == models.RequiredSetActionPromote && existing != nil {
		return nil, newError("conflict", "the workflow is already in the required set for this branch pattern")
	}
	if req.Action == models.RequiredSetActionDemote && existing == nil {
		return nil, newError("not_found", "the workflow is not in the required set for this branch pattern")
	}
	proposal := &models.RequiredSetProposal{ProjectID: project.ProjectID, OrgID: project.OwnershipOrgID(),
		WorkflowSecurityID: req.WorkflowSecurityID, BranchPattern: req.BranchPattern, Action: req.Action,
		Reason: req.Reason, ProposedBy: caller.Actor.Ptr(), ProposedIdentity: caller.Actor.Identity, ProposedAt: s.now()}
	if err := st.CreateRequiredSetProposal(ctx, proposal); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			return nil, newError("conflict", "the same change is already pending")
		}
		return nil, newError("internal", "could not store the proposal")
	}
	s.record(ctx, project, "required_set.propose", proposal.ProposalID, models.JSONB{
		"proposal_id": proposal.ProposalID, "action": proposal.Action, "workflow_security_id": proposal.WorkflowSecurityID,
		"branch_pattern": proposal.BranchPattern, "reason": proposal.Reason})
	if !req.Approve {
		return proposal, nil
	}
	return s.Decide(ctx, caller, project, proposal.ProposalID, models.RequiredSetProposalApproved, req.Reason)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Decide approves, rejects, or withdraws a pending proposal. state is
// approved, rejected, or withdrawn.
func (s *Service) Decide(ctx context.Context, caller Caller, project *models.Project, proposalID, state, reason string) (*models.RequiredSetProposal, error) {
	st, _, err := s.sets()
	if err != nil {
		return nil, err
	}
	proposalID = strings.TrimSpace(proposalID)
	reason = strings.TrimSpace(reason)
	if !uuidPattern.MatchString(proposalID) {
		return nil, newError("not_found", "proposal not found")
	}
	if err := validateReason(reason); err != nil {
		return nil, newError("invalid_argument", "%s", err.Error())
	}
	proposal, err := st.GetRequiredSetProposal(ctx, proposalID)
	if err != nil || proposal.ProjectID != project.ProjectID {
		return nil, newError("not_found", "proposal not found")
	}
	perms, err := s.permissions(ctx, caller, project)
	if err != nil {
		return nil, err
	}
	if !perms.View {
		return nil, newError("permission_denied", "a role in the project or its organization is required")
	}
	approver, approvers, source, err := s.isApprover(ctx, caller, project, proposal.BranchPattern)
	if err != nil {
		return nil, err
	}
	switch state {
	case models.RequiredSetProposalApproved, models.RequiredSetProposalRejected:
		if !approver {
			return nil, newError("permission_denied", "only an approver of this branch pattern can decide the proposal (%s)", approverText(approvers, source))
		}
	case models.RequiredSetProposalWithdrawn:
		proposer := proposal.ProposedBy != nil && !caller.Actor.IsZero() && *proposal.ProposedBy == caller.Actor.PrincipalID
		if !proposer && !approver {
			return nil, newError("permission_denied", "only the proposer or an approver can withdraw the proposal")
		}
	default:
		return nil, newError("invalid_argument", "the decision must be approve, reject, or withdraw")
	}
	decided, err := st.DecideRequiredSetProposal(ctx, proposalID, state, caller.Actor.Ptr(), reason)
	switch {
	case errors.Is(err, store.ErrConflict):
		return nil, newError("conflict", "the proposal is no longer pending")
	case errors.Is(err, store.ErrNotFound):
		return nil, newError("not_found", "the workflow is no longer in the required set for this branch pattern")
	case err != nil:
		return nil, newError("internal", "could not decide the proposal")
	}
	action := map[string]string{models.RequiredSetProposalApproved: "required_set.approve",
		models.RequiredSetProposalRejected: "required_set.reject", models.RequiredSetProposalWithdrawn: "required_set.withdraw"}[state]
	details := models.JSONB{"proposal_id": decided.ProposalID, "action": decided.Action,
		"workflow_security_id": decided.WorkflowSecurityID, "branch_pattern": decided.BranchPattern,
		"reason": reason, "approvers": approvers, "approvers_source": source}
	s.record(ctx, project, action, decided.ProposalID, details)
	if state == models.RequiredSetProposalApproved {
		change := "required_set.promote"
		if decided.Action == models.RequiredSetActionDemote {
			change = "required_set.remove"
		}
		s.record(ctx, project, change, decided.ProposalID, models.JSONB{"proposal_id": decided.ProposalID,
			"workflow_security_id": decided.WorkflowSecurityID, "branch_pattern": decided.BranchPattern})
	}
	return decided, nil
}

func approverText(approvers []string, source string) string {
	if len(approvers) == 0 {
		return "approvers: org admins"
	}
	return fmt.Sprintf("approvers from %s: %s", source, strings.Join(approvers, ", "))
}

// QuarantineRequest quarantines one entry until Until.
type QuarantineRequest struct {
	WorkflowSecurityID string
	BranchPattern      string
	Until              time.Time
	Reason             string
}

func (s *Service) mayQuarantine(ctx context.Context, caller Caller, project *models.Project, securityID, pattern string) error {
	perms, err := s.permissions(ctx, caller, project)
	if err != nil {
		return err
	}
	if !perms.View {
		return newError("permission_denied", "a role in the project or its organization is required")
	}
	approver, approvers, source, err := s.isApprover(ctx, caller, project, pattern)
	if err != nil {
		return err
	}
	if approver {
		return nil
	}
	if OwnerApprover != nil && tokenAllows(caller, project.OwnershipOrgID(), tokencaps.PoliciesManage) &&
		OwnerApprover(ctx, s.Store, project, securityID, caller) {
		return nil
	}
	return newError("permission_denied", "only an approver of this branch pattern or an owner of the workflow can change its quarantine (%s)", approverText(approvers, source))
}

// Quarantine removes an entry from the set until an expiry. The expiry is
// required and must be within the maximum of the organization. At the expiry
// the entry is required again, with no action.
func (s *Service) Quarantine(ctx context.Context, caller Caller, project *models.Project, req QuarantineRequest) (*models.RequiredWorkflow, error) {
	st, _, err := s.sets()
	if err != nil {
		return nil, err
	}
	req.WorkflowSecurityID = strings.TrimSpace(req.WorkflowSecurityID)
	req.BranchPattern = strings.TrimSpace(req.BranchPattern)
	req.Reason = strings.TrimSpace(req.Reason)
	if err := ValidateSecurityID(req.WorkflowSecurityID); err != nil {
		return nil, newError("invalid_argument", "%s", err.Error())
	}
	if req.Reason == "" {
		return nil, newError("invalid_argument", "a quarantine needs a reason")
	}
	if err := validateReason(req.Reason); err != nil {
		return nil, newError("invalid_argument", "%s", err.Error())
	}
	if err := s.mayQuarantine(ctx, caller, project, req.WorkflowSecurityID, req.BranchPattern); err != nil {
		return nil, err
	}
	_, _, org, settings, err := s.approverSettings(ctx, project)
	if err != nil {
		return nil, err
	}
	if !settings.Enabled {
		return nil, newError("invalid_argument", "the required set of the project is off; every workflow is required")
	}
	now := s.now()
	maxDays := org.MaxQuarantineDays
	if maxDays <= 0 {
		maxDays = models.DefaultMaxQuarantineDays
	}
	until := req.Until.UTC()
	if !until.After(now) {
		return nil, newError("invalid_argument", "the expiry must be in the future")
	}
	if until.After(now.Add(time.Duration(maxDays) * 24 * time.Hour)) {
		return nil, newError("invalid_argument", "the expiry must be at most %d days from now", maxDays)
	}
	if err := st.SetRequiredWorkflowQuarantine(ctx, project.ProjectID, req.WorkflowSecurityID, req.BranchPattern, &until, req.Reason, caller.Actor.Ptr()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, newError("not_found", "the workflow is not in the required set for this branch pattern")
		}
		return nil, newError("internal", "could not quarantine the workflow")
	}
	s.record(ctx, project, "required_set.quarantine", req.WorkflowSecurityID, models.JSONB{
		"workflow_security_id": req.WorkflowSecurityID, "branch_pattern": req.BranchPattern,
		"until": until.Format(time.RFC3339), "reason": req.Reason})
	entries, err := st.ListRequiredWorkflows(ctx, project.ProjectID)
	if err != nil {
		return nil, newError("internal", "could not list the required set")
	}
	return hasEntry(entries, req.WorkflowSecurityID, req.BranchPattern), nil
}

// Unquarantine ends a quarantine before its expiry.
func (s *Service) Unquarantine(ctx context.Context, caller Caller, project *models.Project, securityID, pattern string) error {
	st, _, err := s.sets()
	if err != nil {
		return err
	}
	securityID, pattern = strings.TrimSpace(securityID), strings.TrimSpace(pattern)
	if err := ValidateSecurityID(securityID); err != nil {
		return newError("invalid_argument", "%s", err.Error())
	}
	if err := s.mayQuarantine(ctx, caller, project, securityID, pattern); err != nil {
		return err
	}
	if err := st.SetRequiredWorkflowQuarantine(ctx, project.ProjectID, securityID, pattern, nil, "", nil); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return newError("not_found", "the workflow is not in the required set for this branch pattern")
		}
		return newError("internal", "could not end the quarantine")
	}
	s.record(ctx, project, "required_set.unquarantine", securityID, models.JSONB{
		"workflow_security_id": securityID, "branch_pattern": pattern})
	return nil
}
