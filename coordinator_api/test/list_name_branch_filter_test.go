package test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

// TestListNameAndBranchFilters checks the name and branch filters of the
// job and workflow lists, and the source branch of each row.
func TestListNameAndBranchFilters(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		user := createTestUserIn(t, tx)
		newJob := func(name string, env models.JSONB) *models.Job {
			job := createTestJobIn(t, tx, user.UserID, "completed")
			require.NoError(t, tx.Model(job).Updates(map[string]interface{}{"name": name, "job_env_vars": env}).Error)
			job.Name, job.JobEnvVars = name, env
			return job
		}
		prOpened := newJob("lint-check", models.JSONB{"REACTORCIDE_EVENT_TYPE": "pull_request_opened",
			"REACTORCIDE_HEAD_REF": "todpunk/alpha", "REACTORCIDE_BRANCH": "main"})
		push := newJob("unit-tests", models.JSONB{"REACTORCIDE_EVENT_TYPE": "push", "REACTORCIDE_BRANCH": "main"})
		merged := newJob("Lint-Docs", models.JSONB{"REACTORCIDE_EVENT_TYPE": "pull_request_merged",
			"REACTORCIDE_HEAD_REF": "todpunk/beta", "REACTORCIDE_BRANCH": "main"})

		assert.Equal(t, "todpunk/alpha", prOpened.SourceBranch())
		assert.Equal(t, "main", push.SourceBranch())
		assert.Equal(t, "main", merged.SourceBranch(), "a merge runs on the base branch")

		jobIDs := func(filters map[string]interface{}) []string {
			filters["user_id"] = user.UserID
			jobs, total, err := ps.ListJobsVisibleTo(ctx, user.UserID, true, filters, 50, 0)
			require.NoError(t, err)
			require.Equal(t, int64(len(jobs)), total)
			ids := []string{}
			for _, job := range jobs {
				ids = append(ids, job.JobID)
			}
			return ids
		}
		assert.ElementsMatch(t, []string{prOpened.JobID, merged.JobID}, jobIDs(map[string]interface{}{"name_pattern": "lint"}))
		assert.ElementsMatch(t, []string{merged.JobID}, jobIDs(map[string]interface{}{"name_pattern": "*docs"}))
		assert.ElementsMatch(t, []string{prOpened.JobID}, jobIDs(map[string]interface{}{"branch_pattern": "todpunk/*"}))
		assert.ElementsMatch(t, []string{push.JobID, merged.JobID}, jobIDs(map[string]interface{}{"branch_pattern": "MAIN"}))
		assert.Empty(t, jobIDs(map[string]interface{}{"branch_pattern": "todpunk"}), "no wildcard matches the whole branch")
		assert.Empty(t, jobIDs(map[string]interface{}{"name_pattern": "lint_check"}), "_ is literal")

		alpha := &models.WorkflowInstance{UserID: user.UserID, Name: uniqueName("wf-alpha"), Status: "success",
			QueueName: "reactorcide-jobs", ParentJobID: &prOpened.JobID}
		require.NoError(t, ps.CreateWorkflowInstance(ctx, alpha))
		mainWF := &models.WorkflowInstance{UserID: user.UserID, Name: uniqueName("wf-main"), Status: "success",
			QueueName: "reactorcide-jobs", ParentJobID: &push.JobID}
		require.NoError(t, ps.CreateWorkflowInstance(ctx, mainWF))

		workflows := func(filters map[string]interface{}) []models.WorkflowSummary {
			filters["user_id"] = user.UserID
			rows, total, err := ps.ListWorkflowSummariesVisibleTo(ctx, user.UserID, true, filters, 50, 0)
			require.NoError(t, err)
			require.Equal(t, int64(len(rows)), total)
			return rows
		}
		rows := workflows(map[string]interface{}{"name_pattern": "wf-", "branch_pattern": "todpunk/*"})
		require.Len(t, rows, 1)
		assert.Equal(t, alpha.WorkflowID, rows[0].WorkflowID)
		assert.Equal(t, "todpunk/alpha", rows[0].Branch)

		rows = workflows(map[string]interface{}{"name_pattern": "WF-MAIN"})
		require.Len(t, rows, 1)
		assert.Equal(t, mainWF.WorkflowID, rows[0].WorkflowID)
		assert.Equal(t, "main", rows[0].Branch)

		// The loose job rows get the branch filter too.
		for _, row := range workflows(map[string]interface{}{"branch_pattern": "main"}) {
			assert.Equal(t, "main", row.Branch, row.Name)
		}
	})
}
