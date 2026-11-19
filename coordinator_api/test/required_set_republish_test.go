package test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

func TestRequiredSetRepublishStoreAndPass(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		org, project, _ := requiredSetProject(t, ctx, tx, "rs-republish")
		projectID := project.ProjectID
		now := time.Now().UTC()
		pr := func(n int) *int { return &n }
		newEvent := func(prNumber int, eventType, status string, received time.Time) *models.CIEvent {
			event := &models.CIEvent{OrgID: org.OrgID, ProjectID: &projectID, Source: models.CIEventSourceWebhook, Provider: "github",
				EventType: eventType, Repo: "rs-republish/app", CommitSHA: "sha", PRNumber: pr(prNumber), Status: status, ReceivedAt: received}
			require.NoError(t, tx.Create(event).Error)
			return event
		}
		// PR 1: two events, the newest is open.
		newEvent(1, "pull_request_opened", models.CIEventStatusEvaluated, now.Add(-2*time.Hour))
		open1 := newEvent(1, "pull_request_updated", models.CIEventStatusEvaluated, now.Add(-time.Hour))
		// PR 2: closed.
		newEvent(2, "pull_request_opened", models.CIEventStatusEvaluated, now.Add(-2*time.Hour))
		newEvent(2, "pull_request_closed", models.CIEventStatusEvaluated, now.Add(-time.Hour))
		// PR 3: too old.
		newEvent(3, "pull_request_opened", models.CIEventStatusEvaluated, now.Add(-60*24*time.Hour))

		ids, err := ps.ListOpenPullRequestEventIDs(ctx, projectID, now.Add(-requiredset.DefaultOpenEventWindow), 10)
		require.NoError(t, err)
		assert.Equal(t, []string{open1.EventID}, ids)

		// No change yet.
		changed, err := ps.ListRequiredSetChangedProjects(ctx, now.Add(-time.Minute), now)
		require.NoError(t, err)
		assert.NotContains(t, changed, projectID)

		// A quarantine that expired in the window marks the project.
		_, err = ps.SetRequiredSetEnabled(ctx, projectID, org.OrgID, true, nil)
		require.NoError(t, err)
		expired := now.Add(-30 * time.Second)
		require.NoError(t, ps.SetRequiredWorkflowQuarantine(ctx, projectID, "build", "", &expired, "flaky", nil))
		changed, err = ps.ListRequiredSetChangedProjects(ctx, now.Add(-time.Minute), now)
		require.NoError(t, err)
		assert.Contains(t, changed, projectID)
		changed, err = ps.ListRequiredSetChangedProjects(ctx, now.Add(-10*time.Second), now)
		require.NoError(t, err)
		assert.NotContains(t, changed, projectID, "an expiry before the window is not a change")

		// An audit event of a promotion marks the project.
		require.NoError(t, tx.Create(&models.AuditEvent{OrgID: &org.OrgID, Action: "required_set.promote", SubjectType: "required_set",
			Details: models.JSONB{"project_id": projectID}, CreatedAt: now}).Error)
		changed, err = ps.ListRequiredSetChangedProjects(ctx, now.Add(-10*time.Second), now.Add(time.Second))
		require.NoError(t, err)
		assert.Contains(t, changed, projectID)

		// The pass publishes the open event of the changed project.
		published := []string{}
		pass := &requiredset.RepublishPass{Store: ps, Now: func() time.Time { return now.Add(time.Second) },
			Publish: func(_ context.Context, eventID string) error { published = append(published, eventID); return nil }}
		_, err = pass.RunOnce(ctx)
		require.NoError(t, err)
		assert.Contains(t, published, open1.EventID)
	})
}
