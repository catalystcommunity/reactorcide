package handlers

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

type fakeRefClassRules struct {
	rules []models.RefClassRule
	err   error
}

func (f fakeRefClassRules) ListRefClassRules(context.Context, string, *string) ([]models.RefClassRule, error) {
	return f.rules, f.err
}
func (f fakeRefClassRules) ListEffectiveRefClassRules(context.Context, string, *string) ([]models.RefClassRule, error) {
	return f.rules, f.err
}
func (f fakeRefClassRules) GetRefClassRule(context.Context, string, *string, string) (*models.RefClassRule, error) {
	return nil, errors.New("unused")
}
func (f fakeRefClassRules) UpsertRefClassRule(context.Context, *models.RefClassRule) error {
	return nil
}
func (f fakeRefClassRules) DeleteRefClassRule(context.Context, string, *string, string) error {
	return nil
}

func TestWebhookRefClassFacts(t *testing.T) {
	repo := vcs.RepositoryInfo{FullName: "acme/app", DefaultBranch: "main"}
	cases := []struct {
		name  string
		event *vcs.WebhookEvent
		want  []string
	}{
		{"default branch push", &vcs.WebhookEvent{GenericEvent: vcs.EventPush, Repository: repo,
			Push: &vcs.PushInfo{Ref: "refs/heads/main"}}, []string{"default", "production"}},
		{"other branch push", &vcs.WebhookEvent{GenericEvent: vcs.EventPush, Repository: repo,
			Push: &vcs.PushInfo{Ref: "refs/heads/feature"}}, []string{}},
		{"tag", &vcs.WebhookEvent{GenericEvent: vcs.EventTagCreated, Repository: repo,
			Push: &vcs.PushInfo{Ref: "refs/tags/v1.0.0"}}, []string{"tag"}},
		{"same repo pr", &vcs.WebhookEvent{GenericEvent: vcs.EventPullRequestOpened, Repository: repo,
			PullRequest: &vcs.PullRequestInfo{HeadRef: "feature", BaseRef: "main"}}, []string{"pr"}},
		{"fork pr", &vcs.WebhookEvent{GenericEvent: vcs.EventPullRequestUpdated, Repository: repo,
			PullRequest: &vcs.PullRequestInfo{HeadRef: "main", BaseRef: "main", HeadRepository: &vcs.RepositoryInfo{FullName: "evil/app"}}},
			[]string{"fork", "pr"}},
	}
	yes := true
	rules := fakeRefClassRules{rules: []models.RefClassRule{{RuleID: "r1", Name: "production", Enabled: true,
		EventTypes: []string{"push"}, IsDefaultBranch: &yes, Classes: []string{"production"}}}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ciEvent := &models.CIEvent{OrgID: "org", Source: models.CIEventSourceWebhook}
			applyRefClassesToEvent(context.Background(), rules, ciEvent, webhookRefClassFacts(tc.event))
			if !reflect.DeepEqual([]string(ciEvent.RefClasses), tc.want) {
				t.Fatalf("classes = %v, want %v", ciEvent.RefClasses, tc.want)
			}
			if ciEvent.RefClassFacts == nil || ciEvent.RefClassFacts.Source != models.CIEventSourceWebhook {
				t.Fatalf("facts = %+v", ciEvent.RefClassFacts)
			}
		})
	}
}

// A fork pull request whose head branch is "main" never gets "default" or a
// class that a default branch rule gives.
func TestForkHeadNamedMainIsNotDefault(t *testing.T) {
	yes := true
	rules := fakeRefClassRules{rules: []models.RefClassRule{{RuleID: "r1", Name: "main", Enabled: true,
		BranchGlob: "main", Classes: []string{"production"}}, {RuleID: "r2", Name: "d", Enabled: true,
		IsDefaultBranch: &yes, EventTypes: []string{"push"}, Classes: []string{"trusted"}}}}
	event := &vcs.WebhookEvent{GenericEvent: vcs.EventPullRequestOpened, Repository: vcs.RepositoryInfo{DefaultBranch: "main"},
		PullRequest: &vcs.PullRequestInfo{HeadRef: "main", BaseRef: "main", HeadRepository: &vcs.RepositoryInfo{FullName: "evil/app"}}}
	ciEvent := &models.CIEvent{OrgID: "org", Source: models.CIEventSourceWebhook}
	applyRefClassesToEvent(context.Background(), rules, ciEvent, webhookRefClassFacts(event))
	if !reflect.DeepEqual([]string(ciEvent.RefClasses), []string{"fork", "pr"}) {
		t.Fatalf("classes = %v", ciEvent.RefClasses)
	}
}

func TestRuleLoadErrorUsesBuiltinsOnly(t *testing.T) {
	ciEvent := &models.CIEvent{OrgID: "org", Source: models.CIEventSourceSchedule}
	applyRefClassesToEvent(context.Background(), fakeRefClassRules{err: errors.New("db down")}, ciEvent,
		models.RefClassFacts{EventType: "schedule", Branch: "main"})
	if !reflect.DeepEqual([]string(ciEvent.RefClasses), []string{"schedule"}) {
		t.Fatalf("classes = %v", ciEvent.RefClasses)
	}
}

func TestManualJobRefClasses(t *testing.T) {
	job := &models.Job{OrgID: "org", JobEnvVars: models.JSONB{"REACTORCIDE_BRANCH": "main", "REACTORCIDE_REF_CLASSES": "production"}}
	applyManualRefClasses(context.Background(), fakeRefClassRules{}, job)
	if !reflect.DeepEqual([]string(job.RefClasses), []string{"manual"}) {
		t.Fatalf("classes = %v", job.RefClasses)
	}
	parent := "p"
	child := &models.Job{OrgID: "org", ParentJobID: &parent}
	applyManualRefClasses(context.Background(), fakeRefClassRules{}, child)
	if child.RefClasses != nil {
		t.Fatal("a job with a parent inherits; it is not classified as manual")
	}
}

func TestApplyCIEventToEvalJobCopiesClasses(t *testing.T) {
	job := &models.Job{}
	applyCIEventToEvalJob(job, &models.CIEvent{EventID: "e1", RefClasses: []string{"pr"}})
	if !reflect.DeepEqual([]string(job.RefClasses), []string{"pr"}) {
		t.Fatalf("classes = %v", job.RefClasses)
	}
}
