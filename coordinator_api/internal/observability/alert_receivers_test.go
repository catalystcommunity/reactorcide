package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// subsFake is the fake store with event subscriptions.
type subsFake struct {
	*fakeStore
	subs []models.EventSubscription
	err  error
}

func (f *subsFake) ListEventSubscriptions(_ context.Context, orgID string, _ *string) ([]models.EventSubscription, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []models.EventSubscription
	for _, s := range f.subs {
		if s.OrgID == orgID {
			out = append(out, s)
		}
	}
	return out, nil
}

func orgSub(org string, mod func(*models.EventSubscription)) models.EventSubscription {
	s := models.EventSubscription{OrgID: org, Scope: models.SubscriptionScopeOrg, Enabled: true, OwnerActive: true}
	if mod != nil {
		mod(&s)
	}
	return s
}

// A rule is evaluated only when a subscription receives its alert events.
func TestEvaluateAllSkipsRulesWithNoReceiver(t *testing.T) {
	now := base.Add(10 * time.Hour)
	bucket := BucketStart(now, Hourly)
	other := "other-project"
	cases := []struct {
		name     string
		subs     []models.EventSubscription
		listErr  error
		evaluate bool
	}{
		{name: "no subscriptions", evaluate: false},
		{name: "disabled subscription", subs: []models.EventSubscription{orgSub("o", func(s *models.EventSubscription) { s.Enabled = false })}},
		{name: "other event types only", subs: []models.EventSubscription{orgSub("o", func(s *models.EventSubscription) { s.EventTypes = []string{models.OutboundEventNodePostComplete} })}},
		{name: "other project", subs: []models.EventSubscription{orgSub("o", func(s *models.EventSubscription) { s.Scope, s.ProjectID = models.SubscriptionScopeProject, &other })}},
		{name: "other organization", subs: []models.EventSubscription{orgSub("o2", nil)}},
		{name: "all events of the org", subs: []models.EventSubscription{orgSub("o", nil)}, evaluate: true},
		{name: "alert.resolved only", subs: []models.EventSubscription{orgSub("o", func(s *models.EventSubscription) { s.EventTypes = []string{models.OutboundEventAlertResolved} })}, evaluate: true},
		{name: "subscriptions not readable", listErr: errors.New("db down"), evaluate: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFake()
			rule := passRule()
			rule.FireAfter = 1
			fs.rules = []models.AlertRule{*rule}
			r := rollup(bucket, 5, 5, 100)
			r.WorkflowSecurityID, r.RefClassBucket = "ci", "default"
			fs.hourly[bucket] = []models.JobRollup{r}
			e := &Evaluator{Store: &subsFake{fakeStore: fs, subs: tc.subs, err: tc.listErr}}
			firing, err := e.EvaluateAll(context.Background(), now)
			require.NoError(t, err)
			if tc.evaluate {
				assert.Equal(t, 1, firing[models.AlertConditionPassRateBelow])
				assert.Len(t, fs.outbox, 1)
			} else {
				assert.Equal(t, 0, firing[models.AlertConditionPassRateBelow])
				assert.Empty(t, fs.outbox)
				assert.Empty(t, fs.transitions)
				assert.Equal(t, rule.State, fs.rules[0].State, "a skipped rule keeps its state")
			}
		})
	}
}
