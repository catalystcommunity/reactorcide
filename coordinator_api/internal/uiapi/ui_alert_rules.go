package uiapi

import (
	"context"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/observability"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// Alert rules (docs/observability.md). The authorization is the one of
// handlers/observability_handler.go: a rule with a project needs a project
// owner of that project; an owner_group rule with no project needs an org
// admin. A caller sees the rules of the projects it can view, and the rules
// with no project when it has a role in the organization.

func alertRuleDetailToCsil(r *models.AlertRule, canManage bool) csilapi.ObsAlertRule {
	out := alertRuleToCsil(r)
	out.Description = optionalText(r.Description)
	baseline, minRuns, fireAfter, resolveAfter := int64(r.BaselineDays), int64(r.MinRuns), int64(r.FireAfter), int64(r.ResolveAfter)
	margin := r.ResolveMargin
	out.BaselineDays, out.MinRuns, out.FireAfter, out.ResolveAfter, out.ResolveMargin = &baseline, &minRuns, &fireAfter, &resolveAfter, &margin
	out.CanManage = optionalBool(canManage)
	return out
}

// canManageAlertRule is ObservabilityHandler.canManageRule for a UI caller.
// A token needs organizations:manage for an org rule and projects:manage
// for a project rule, as in the REST routes.
func (s *UiService) canManageAlertRule(ctx context.Context, id authz.Identity, orgID string, rule *models.AlertRule) bool {
	if rule.ProjectID == nil {
		return s.canCreateOrgAlertRules(ctx, id, orgID)
	}
	if requireTokenGrant(id, &orgID, tokencaps.ProjectsManage) != nil {
		return false
	}
	owner, err := s.deps.Resolver.IsProjectOwner(ctx, id, *rule.ProjectID)
	return err == nil && owner
}

func (s *UiService) canCreateOrgAlertRules(ctx context.Context, id authz.Identity, orgID string) bool {
	if requireTokenGrant(id, &orgID, tokencaps.OrganizationsManage) != nil {
		return false
	}
	admin, err := s.deps.Resolver.IsOrgAdmin(ctx, id, orgID)
	return err == nil && admin
}

func applyAlertRuleInput(rule *models.AlertRule, in csilapi.AlertRuleInput) {
	if in.Description != nil {
		rule.Description = strings.TrimSpace(*in.Description)
	}
	if in.Condition != nil && *in.Condition != "" {
		rule.Condition = *in.Condition
	}
	if in.RefClassBucket != nil {
		rule.RefClassBucket = *in.RefClassBucket
	}
	if in.Threshold != nil {
		rule.Threshold = *in.Threshold
	}
	if in.Factor != nil {
		rule.Factor = *in.Factor
	}
	if in.ResolveMargin != nil {
		rule.ResolveMargin = *in.ResolveMargin
	}
	setInt := func(dst *int, src *int64) {
		if src != nil {
			*dst = int(*src)
		}
	}
	setInt(&rule.WindowHours, in.WindowHours)
	setInt(&rule.BaselineDays, in.BaselineDays)
	setInt(&rule.MinRuns, in.MinRuns)
	setInt(&rule.FireAfter, in.FireAfter)
	setInt(&rule.ResolveAfter, in.ResolveAfter)
	if in.Enabled != nil {
		rule.Enabled = *in.Enabled
	}
}

// ListAlertRules lists the alert rules the caller can see.
func (s *UiService) ListAlertRules(ctx context.Context, req csilapi.ListAlertRulesRequest) (csilapi.ListAlertRulesResponse, error) {
	st, err := obsStore(s)
	if err != nil {
		return csilapi.ListAlertRulesResponse{}, err
	}
	scope, _, err := s.observabilityScope(ctx, req.OrgId, req.ProjectId)
	if err != nil {
		return csilapi.ListAlertRulesResponse{}, err
	}
	id, _ := s.deps.resolveIdentity(ctx)
	visible := map[string]bool{}
	for _, projectID := range scope.ProjectIDs {
		visible[projectID] = true
	}
	member := s.requireOrgMember(ctx, id, req.OrgId, tokencaps.ProjectsRead) == nil
	rules, err := st.ListAlertRules(ctx, req.OrgId)
	if err != nil {
		return csilapi.ListAlertRulesResponse{}, NewServiceError("internal", "could not read the alert rules")
	}
	out := csilapi.ListAlertRulesResponse{Rules: []csilapi.ObsAlertRule{}}
	for i := range rules {
		rule := &rules[i]
		if rule.ProjectID == nil {
			if !member || (req.ProjectId != nil && *req.ProjectId != "") {
				continue
			}
		} else if !visible[*rule.ProjectID] {
			continue
		}
		out.Rules = append(out.Rules, alertRuleDetailToCsil(rule, s.canManageAlertRule(ctx, id, req.OrgId, rule)))
	}
	out.CanCreateOrgRules = s.canCreateOrgAlertRules(ctx, id, req.OrgId)
	return out, nil
}

// CreateAlertRule makes an alert rule.
func (s *UiService) CreateAlertRule(ctx context.Context, req csilapi.CreateAlertRuleRequest) (csilapi.AlertRuleResponse, error) {
	ctx, id, _, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return csilapi.AlertRuleResponse{}, err
	}
	st, err := obsStore(s)
	if err != nil {
		return csilapi.AlertRuleResponse{}, err
	}
	if err := requireNonEmpty("org_id", req.OrgId, 64); err != nil {
		return csilapi.AlertRuleResponse{}, err
	}
	rule := &models.AlertRule{OrgID: req.OrgId, Name: strings.TrimSpace(req.Name), Scope: req.Scope,
		WorkflowSecurityID: strings.TrimSpace(derefOr(req.WorkflowSecurityId, "")), OwnerGroup: strings.TrimSpace(derefOr(req.OwnerGroup, "")),
		Enabled: true, RefClassBucket: models.RefClassBucketDefault, ResolveMargin: 0.05}
	if id.PrincipalID != "" {
		creator := id.PrincipalID
		rule.CreatedBy = &creator
	}
	if req.ProjectId != nil && strings.TrimSpace(*req.ProjectId) != "" {
		project, err := s.deps.Store.GetProjectByID(ctx, strings.TrimSpace(*req.ProjectId))
		if err != nil || project == nil || project.OwnershipOrgID() != req.OrgId {
			return csilapi.AlertRuleResponse{}, NewServiceError("not_found", "project not found")
		}
		projectID := project.ProjectID
		rule.ProjectID = &projectID
	}
	applyAlertRuleInput(rule, req.Rule)
	observability.ApplyAlertDefaults(rule)
	if err := observability.ValidateAlertRule(rule); err != nil {
		return csilapi.AlertRuleResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	if !s.canManageAlertRule(ctx, id, req.OrgId, rule) {
		return csilapi.AlertRuleResponse{}, NewServiceError("forbidden", "a project rule needs a project owner; a rule with no project needs an org admin")
	}
	if err := st.CreateAlertRule(ctx, rule); err != nil {
		return csilapi.AlertRuleResponse{}, mapStoreErr(err, "the alert rule could not be made")
	}
	created, err := st.GetAlertRule(ctx, req.OrgId, rule.Name)
	if err != nil {
		return csilapi.AlertRuleResponse{}, mapStoreErr(err, "alert rule not found")
	}
	s.recordAudit(ctx, req.OrgId, "alert_rule.create", "alert_rule", created.RuleID, models.JSONB{
		"name": created.Name, "scope": created.Scope, "condition": created.Condition})
	return csilapi.AlertRuleResponse{Rule: alertRuleDetailToCsil(created, true)}, nil
}

// managedAlertRule loads a rule that the caller can change. A rule the caller
// cannot manage answers not_found when it cannot see it, else forbidden.
func (s *UiService) managedAlertRule(ctx context.Context, orgID, ruleID string) (context.Context, *models.AlertRule, error) {
	ctx, id, _, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return ctx, nil, err
	}
	st, err := obsStore(s)
	if err != nil {
		return ctx, nil, err
	}
	if err := requireNonEmpty("org_id", orgID, 64); err != nil {
		return ctx, nil, err
	}
	if err := requireNonEmpty("rule_id", ruleID, 64); err != nil {
		return ctx, nil, err
	}
	rule, err := st.GetAlertRule(ctx, orgID, ruleID)
	if err != nil || rule == nil || rule.OrgID != orgID {
		return ctx, nil, NewServiceError("not_found", "alert rule not found")
	}
	if !s.canManageAlertRule(ctx, id, orgID, rule) {
		return ctx, nil, NewServiceError("forbidden", "you cannot change this alert rule")
	}
	return ctx, rule, nil
}

// UpdateAlertRule changes the configuration of an alert rule. Its scope
// cannot change.
func (s *UiService) UpdateAlertRule(ctx context.Context, req csilapi.UpdateAlertRuleRequest) (csilapi.AlertRuleResponse, error) {
	ctx, rule, err := s.managedAlertRule(ctx, req.OrgId, req.RuleId)
	if err != nil {
		return csilapi.AlertRuleResponse{}, err
	}
	st, _ := obsStore(s)
	before := *rule
	applyAlertRuleInput(rule, req.Rule)
	if err := observability.ValidateAlertRule(rule); err != nil {
		return csilapi.AlertRuleResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	reset := before.Condition != rule.Condition || before.RefClassBucket != rule.RefClassBucket || (before.Enabled && !rule.Enabled)
	if err := st.UpdateAlertRuleConfig(ctx, rule, reset); err != nil {
		return csilapi.AlertRuleResponse{}, mapStoreErr(err, "alert rule not found")
	}
	updated, err := st.GetAlertRule(ctx, req.OrgId, rule.RuleID)
	if err != nil {
		updated = rule
	}
	s.recordAudit(ctx, req.OrgId, "alert_rule.update", "alert_rule", rule.RuleID, models.JSONB{"name": rule.Name})
	return csilapi.AlertRuleResponse{Rule: alertRuleDetailToCsil(updated, true)}, nil
}

// DeleteAlertRule deletes an alert rule.
func (s *UiService) DeleteAlertRule(ctx context.Context, req csilapi.DeleteAlertRuleRequest) (csilapi.DeleteAlertRuleResponse, error) {
	ctx, rule, err := s.managedAlertRule(ctx, req.OrgId, req.RuleId)
	if err != nil {
		return csilapi.DeleteAlertRuleResponse{}, err
	}
	st, _ := obsStore(s)
	if err := st.DeleteAlertRule(ctx, req.OrgId, rule.RuleID); err != nil {
		return csilapi.DeleteAlertRuleResponse{}, mapStoreErr(err, "alert rule not found")
	}
	s.recordAudit(ctx, req.OrgId, "alert_rule.delete", "alert_rule", rule.RuleID, models.JSONB{"name": rule.Name})
	return csilapi.DeleteAlertRuleResponse{Deleted: true}, nil
}
