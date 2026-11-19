package uiapi

import (
	"context"
	"sort"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// Ref class rules and explain (docs/ref-classes.md). Reading needs a role in
// the organization. An organization rule needs an org admin; a project rule
// needs a project owner. A session, a user token, a service principal token,
// and the admin token all work.

type uiRefClassStore interface {
	store.RefClassStore
	GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error)
	GetProjectByID(ctx context.Context, projectID string) (*models.Project, error)
}

// refClassScope authorizes the caller for the scope and returns it.
func (s *UiService) refClassScope(ctx context.Context, organization string, projectID *string, write bool) (context.Context, uiRefClassStore, string, *string, bool, error) {
	ctx, id, _, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return ctx, nil, "", nil, false, err
	}
	rulesStore, ok := s.deps.Store.(uiRefClassStore)
	if !ok {
		return ctx, nil, "", nil, false, NewServiceError("unavailable", "ref class rules are not available")
	}
	org, err := rulesStore.GetOrganizationByName(ctx, organization)
	if err != nil {
		return ctx, nil, "", nil, false, mapStoreErr(err, "organization not found")
	}
	var scopeProject *string
	if projectID != nil && strings.TrimSpace(*projectID) != "" {
		project, err := rulesStore.GetProjectByID(ctx, strings.TrimSpace(*projectID))
		if err != nil || project.OwnershipOrgID() != org.OrgID {
			return ctx, nil, "", nil, false, NewServiceError("not_found", "project not found")
		}
		value := project.ProjectID
		scopeProject = &value
	}
	// A token needs projects:read for the organization (or policies:manage),
	// as in the REST route; a change needs policies:manage.
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
			return ctx, nil, "", nil, false, NewServiceError("permission_denied", "an org admin is required to change organization ref class rules")
		}
		return ctx, nil, "", nil, false, NewServiceError("permission_denied", "a project owner is required to change project ref class rules")
	}
	return ctx, rulesStore, org.OrgID, scopeProject, canManage, nil
}

func (s *UiService) canManageRefClasses(ctx context.Context, id authz.Identity, orgID string, projectID *string) bool {
	if requireTokenGrant(id, &orgID, tokencaps.PoliciesManage) != nil {
		return false
	}
	if projectID == nil {
		return s.deps.Resolver.RequireOrgAdmin(ctx, id, orgID) == nil
	}
	return s.deps.Resolver.RequireProjectOwner(ctx, id, *projectID) == nil
}

func refClassRuleToWire(rule models.RefClassRule) csilapi.RefClassRule {
	ruleID := rule.RuleID
	created, updated := formatTime(rule.CreatedAt), formatTime(rule.UpdatedAt)
	return csilapi.RefClassRule{
		RuleId: &ruleID, ProjectId: rule.ProjectID, Name: rule.Name, Position: int64(rule.Position),
		Description: optionalText(rule.Description), EventTypes: append([]string{}, rule.EventTypes...),
		BranchGlob: optionalText(rule.BranchGlob), TagGlob: optionalText(rule.TagGlob), BaseBranchGlob: optionalText(rule.BaseBranchGlob),
		IsPr: rule.IsPR, IsFork: rule.IsFork, IsDefaultBranch: rule.IsDefaultBranch,
		Classes: append([]string{}, rule.Classes...), Enabled: rule.Enabled, CreatedAt: &created, UpdatedAt: &updated,
	}
}

func textValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func refClassRulesToWire(rules []models.RefClassRule) []csilapi.RefClassRule {
	result := make([]csilapi.RefClassRule, 0, len(rules))
	for _, rule := range rules {
		result = append(result, refClassRuleToWire(rule))
	}
	return result
}

func refClassFactsToWire(facts models.RefClassFacts) csilapi.RefClassFacts {
	isPR, isFork, isDefault := facts.IsPR, facts.IsFork, facts.IsDefaultBranch
	return csilapi.RefClassFacts{Source: optionalText(facts.Source), EventType: optionalText(facts.EventType),
		Branch: optionalText(facts.Branch), HeadBranch: optionalText(facts.HeadBranch), Tag: optionalText(facts.Tag), BaseBranch: optionalText(facts.BaseBranch),
		DefaultBranch: optionalText(facts.DefaultBranch), IsPr: &isPR, IsFork: &isFork, IsDefaultBranch: &isDefault}
}

func refClassFactsFromWire(facts *csilapi.RefClassFacts) models.RefClassFacts {
	if facts == nil {
		return models.RefClassFacts{}
	}
	value := models.RefClassFacts{Source: textValue(facts.Source), EventType: textValue(facts.EventType),
		Branch: textValue(facts.Branch), HeadBranch: textValue(facts.HeadBranch), Tag: textValue(facts.Tag), BaseBranch: textValue(facts.BaseBranch),
		DefaultBranch: textValue(facts.DefaultBranch)}
	if facts.IsPr != nil {
		value.IsPR = *facts.IsPr
	}
	if facts.IsFork != nil {
		value.IsFork = *facts.IsFork
	}
	if facts.IsDefaultBranch != nil {
		value.IsDefaultBranch = *facts.IsDefaultBranch
	}
	return value
}

func refClassReasonsToWire(reasons []models.RefClassReason) []csilapi.RefClassReason {
	result := make([]csilapi.RefClassReason, 0, len(reasons))
	for _, reason := range reasons {
		result = append(result, csilapi.RefClassReason{Class: reason.Class, RuleId: reason.RuleID,
			RuleName: optionalText(reason.RuleName), Scope: reason.Scope})
	}
	return result
}

func refClassBuiltinsToWire() []csilapi.RefClassBuiltin {
	builtin := refclass.BuiltinRuleNames()
	names := make([]string, 0, len(builtin))
	for name := range builtin {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]csilapi.RefClassBuiltin, 0, len(names))
	for _, name := range names {
		result = append(result, csilapi.RefClassBuiltin{RuleId: name, Classes: builtin[name]})
	}
	return result
}

func (s *UiService) ListRefClassRules(ctx context.Context, req csilapi.ListRefClassRulesRequest) (csilapi.ListRefClassRulesResponse, error) {
	ctx, rulesStore, orgID, projectID, canManage, err := s.refClassScope(ctx, req.Organization, req.ProjectId, false)
	if err != nil {
		return csilapi.ListRefClassRulesResponse{}, err
	}
	rules, err := rulesStore.ListRefClassRules(ctx, orgID, projectID)
	if err != nil {
		return csilapi.ListRefClassRulesResponse{}, NewServiceError("internal", "could not list ref class rules")
	}
	response := csilapi.ListRefClassRulesResponse{Rules: refClassRulesToWire(rules), Inherited: []csilapi.RefClassRule{},
		Builtin: refClassBuiltinsToWire(), CanManage: canManage}
	if projectID != nil {
		inherited, err := rulesStore.ListRefClassRules(ctx, orgID, nil)
		if err != nil {
			return csilapi.ListRefClassRulesResponse{}, NewServiceError("internal", "could not list ref class rules")
		}
		response.Inherited = refClassRulesToWire(inherited)
	}
	return response, nil
}

func (s *UiService) PutRefClassRule(ctx context.Context, req csilapi.PutRefClassRuleRequest) (csilapi.PutRefClassRuleResponse, error) {
	ctx, rulesStore, orgID, projectID, _, err := s.refClassScope(ctx, req.Organization, req.ProjectId, true)
	if err != nil {
		return csilapi.PutRefClassRuleResponse{}, err
	}
	wire := req.Rule
	eventTypes := append([]string{}, wire.EventTypes...)
	rule := models.RefClassRule{OrgID: orgID, ProjectID: projectID, Name: strings.TrimSpace(wire.Name), Position: int(wire.Position),
		Description: textValue(wire.Description), EventTypes: eventTypes,
		BranchGlob: textValue(wire.BranchGlob), TagGlob: textValue(wire.TagGlob), BaseBranchGlob: textValue(wire.BaseBranchGlob),
		IsPR: wire.IsPr, IsFork: wire.IsFork, IsDefaultBranch: wire.IsDefaultBranch,
		Classes: refclass.Normalize(wire.Classes), Enabled: wire.Enabled}
	if err := refclass.ValidateRule(rule); err != nil {
		return csilapi.PutRefClassRuleResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	if err := rulesStore.UpsertRefClassRule(ctx, &rule); err != nil {
		return csilapi.PutRefClassRuleResponse{}, mapStoreErr(err, "ref class rule not found")
	}
	s.recordAudit(ctx, orgID, "ref_class_rule.set", "ref_class_rule", rule.RuleID, models.JSONB{
		"name": rule.Name, "project_id": projectID, "classes": []string(rule.Classes), "enabled": rule.Enabled})
	return csilapi.PutRefClassRuleResponse{Rule: refClassRuleToWire(rule)}, nil
}

func (s *UiService) DeleteRefClassRule(ctx context.Context, req csilapi.DeleteRefClassRuleRequest) (csilapi.DeleteRefClassRuleResponse, error) {
	ctx, rulesStore, orgID, projectID, _, err := s.refClassScope(ctx, req.Organization, req.ProjectId, true)
	if err != nil {
		return csilapi.DeleteRefClassRuleResponse{}, err
	}
	rule, err := rulesStore.GetRefClassRule(ctx, orgID, projectID, req.Name)
	if err != nil {
		return csilapi.DeleteRefClassRuleResponse{}, mapStoreErr(err, "ref class rule not found")
	}
	if err := rulesStore.DeleteRefClassRule(ctx, orgID, projectID, req.Name); err != nil {
		return csilapi.DeleteRefClassRuleResponse{}, mapStoreErr(err, "ref class rule not found")
	}
	s.recordAudit(ctx, orgID, "ref_class_rule.delete", "ref_class_rule", rule.RuleID, models.JSONB{"name": rule.Name, "project_id": projectID})
	return csilapi.DeleteRefClassRuleResponse{Deleted: true}, nil
}

func (s *UiService) ExplainRefClasses(ctx context.Context, req csilapi.ExplainRefClassesRequest) (csilapi.ExplainRefClassesResponse, error) {
	ctx, _, orgID, projectID, _, err := s.refClassScope(ctx, req.Organization, req.ProjectId, false)
	if err != nil {
		return csilapi.ExplainRefClassesResponse{}, err
	}
	explanation, err := refclass.ExplainRequest(ctx, s.deps.Store, orgID, projectID, textValue(req.EventId), refClassFactsFromWire(req.Facts))
	if err != nil {
		return csilapi.ExplainRefClassesResponse{}, mapStoreErr(err, "event not found")
	}
	response := csilapi.ExplainRefClassesResponse{Facts: refClassFactsToWire(explanation.Facts),
		Classes: append([]string{}, explanation.Classes...), Reasons: refClassReasonsToWire(explanation.Reasons),
		RuleIds: append([]string{}, explanation.RuleIDs...), Rules: []csilapi.RefClassRuleCheck{}}
	for _, rule := range explanation.Rules {
		response.Rules = append(response.Rules, csilapi.RefClassRuleCheck{RuleId: rule.RuleID, Name: rule.Name, Scope: rule.Scope,
			Enabled: rule.Enabled, Matched: rule.Matched, Classes: append([]string{}, rule.Classes...)})
	}
	if explanation.EventID != "" {
		response.StoredClasses = append([]string{}, explanation.StoredClasses...)
	}
	return response, nil
}

// setGrantRefClasses replaces the ref class conditions of a secret grant.
func setGrantRefClasses(grant *models.SecretGrant, anyOf, noneOf []string) error {
	if err := refclass.ValidateClasses(anyOf); err != nil {
		return NewServiceError("invalid_argument", "ref_classes_any: "+err.Error())
	}
	if err := refclass.ValidateClasses(noneOf); err != nil {
		return NewServiceError("invalid_argument", "ref_classes_none: "+err.Error())
	}
	grant.RefClassesAny = refclass.Normalize(anyOf)
	grant.RefClassesNone = refclass.Normalize(noneOf)
	return nil
}
