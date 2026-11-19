package uiapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// alertFakeStore adds alert rules to the fake store. The other
// observability methods are not used by these tests (the nil interface
// panics if one is called).
type alertFakeStore struct {
	*fakeStore
	store.ObservabilityStore
	rules map[string]models.AlertRule
}

func (s *alertFakeStore) ListAlertRules(_ context.Context, orgID string) ([]models.AlertRule, error) {
	out := []models.AlertRule{}
	for _, rule := range s.rules {
		if rule.OrgID == orgID {
			out = append(out, rule)
		}
	}
	return out, nil
}

func (s *alertFakeStore) GetAlertRule(_ context.Context, orgID, ref string) (*models.AlertRule, error) {
	for _, rule := range s.rules {
		if rule.OrgID == orgID && (rule.RuleID == ref || rule.Name == ref) {
			copy := rule
			return &copy, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *alertFakeStore) CreateAlertRule(_ context.Context, rule *models.AlertRule) error {
	rule.RuleID = "rule-" + rule.Name
	s.rules[rule.RuleID] = *rule
	return nil
}

func (s *alertFakeStore) UpdateAlertRuleConfig(_ context.Context, rule *models.AlertRule, _ bool) error {
	s.rules[rule.RuleID] = *rule
	return nil
}

func (s *alertFakeStore) DeleteAlertRule(_ context.Context, _ string, ruleID string) error {
	delete(s.rules, ruleID)
	return nil
}

func TestAlertRulesAuthorization(t *testing.T) {
	deps, st := newTestDeps(t)
	alerts := &alertFakeStore{fakeStore: st, rules: map[string]models.AlertRule{}}
	deps.Store = alerts
	st.putUser(models.User{UserID: "owner"})
	st.putUser(models.User{UserID: "admin"})
	st.putUser(models.User{UserID: "member"})
	project := st.putProject(models.Project{UserID: strPtr("org-1"), OrgID: "org-1", IsPrivate: true})
	seedProjectOwner(st, "owner", project.ProjectID)
	seedOrgAdmin(st, "admin", "org-1")
	seedProjectMember(st, "member", project.ProjectID)
	ui := NewUiService(deps)
	owner := mintSessionCtx(t, deps, "owner")

	rule := csilapi.AlertRuleInput{Condition: strPtr("pass_rate_below"), Threshold: floatPtr(0.8)}
	_, err := ui.CreateAlertRule(owner, csilapi.CreateAlertRuleRequest{OrgId: "org-1", Name: "slow-tests", Scope: "owner_group", OwnerGroup: strPtr("payments"), Rule: rule})
	requireCode(t, err, "forbidden")
	_, err = ui.CreateAlertRule(owner, csilapi.CreateAlertRuleRequest{OrgId: "org-1", Name: "Bad Name", Scope: "project", ProjectId: strPtr(project.ProjectID), Rule: rule})
	requireCode(t, err, "invalid_argument")

	created, err := ui.CreateAlertRule(owner, csilapi.CreateAlertRuleRequest{OrgId: "org-1", Name: "pass-rate", Scope: "project", ProjectId: strPtr(project.ProjectID), Rule: rule})
	requireOK(t, err)
	if created.Rule.Threshold != 0.8 || created.Rule.WindowHours != 24 || derefOr(created.Rule.ProjectId, "") != project.ProjectID {
		t.Fatalf("created = %+v", created.Rule)
	}

	// A member sees the rule and cannot change it.
	member := mintSessionCtx(t, deps, "member")
	list, err := ui.ListAlertRules(member, csilapi.ListAlertRulesRequest{OrgId: "org-1"})
	requireOK(t, err)
	if len(list.Rules) != 1 || list.Rules[0].CanManage == nil || *list.Rules[0].CanManage || list.CanCreateOrgRules {
		t.Fatalf("member list = %+v", list)
	}
	_, err = ui.UpdateAlertRule(member, csilapi.UpdateAlertRuleRequest{OrgId: "org-1", RuleId: created.Rule.RuleId, Rule: csilapi.AlertRuleInput{Enabled: boolPtr(false)}})
	requireCode(t, err, "forbidden")

	// An anonymous caller sees no private project rule.
	anon, err := ui.ListAlertRules(anonCtx(), csilapi.ListAlertRulesRequest{OrgId: "org-1"})
	requireOK(t, err)
	if len(anon.Rules) != 0 {
		t.Fatalf("anonymous list = %+v", anon.Rules)
	}

	updated, err := ui.UpdateAlertRule(owner, csilapi.UpdateAlertRuleRequest{OrgId: "org-1", RuleId: created.Rule.RuleId,
		Rule: csilapi.AlertRuleInput{Threshold: floatPtr(0.95), Enabled: boolPtr(false)}})
	requireOK(t, err)
	if updated.Rule.Threshold != 0.95 || updated.Rule.Enabled {
		t.Fatalf("updated = %+v", updated.Rule)
	}

	admin := mintSessionCtx(t, deps, "admin")
	group, err := ui.CreateAlertRule(admin, csilapi.CreateAlertRuleRequest{OrgId: "org-1", Name: "team", Scope: "owner_group", OwnerGroup: strPtr("payments"),
		Rule: csilapi.AlertRuleInput{Condition: strPtr("duration_regression")}})
	requireOK(t, err)
	if group.Rule.Factor != 1.5 {
		t.Fatalf("owner group rule = %+v", group.Rule)
	}
	adminList, err := ui.ListAlertRules(admin, csilapi.ListAlertRulesRequest{OrgId: "org-1"})
	requireOK(t, err)
	if len(adminList.Rules) != 2 || !adminList.CanCreateOrgRules {
		t.Fatalf("admin list = %+v", adminList)
	}
	// A project filter leaves out the rules with no project.
	projectList, err := ui.ListAlertRules(admin, csilapi.ListAlertRulesRequest{OrgId: "org-1", ProjectId: strPtr(project.ProjectID)})
	requireOK(t, err)
	if len(projectList.Rules) != 1 {
		t.Fatalf("project list = %+v", projectList.Rules)
	}

	_, err = ui.DeleteAlertRule(owner, csilapi.DeleteAlertRuleRequest{OrgId: "org-1", RuleId: group.Rule.RuleId})
	requireCode(t, err, "forbidden")
	deleted, err := ui.DeleteAlertRule(owner, csilapi.DeleteAlertRuleRequest{OrgId: "org-1", RuleId: created.Rule.RuleId})
	requireOK(t, err)
	if !deleted.Deleted || len(alerts.rules) != 1 {
		t.Fatalf("delete = %+v rules = %d", deleted, len(alerts.rules))
	}
	_, err = ui.DeleteAlertRule(owner, csilapi.DeleteAlertRuleRequest{OrgId: "org-1", RuleId: "nope"})
	requireCode(t, err, "not_found")
}

func floatPtr(v float64) *float64 { return &v }
