package test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Capture SQL from the store so plan tests exercise the application queries.
type queryRecorder struct {
	logger.Interface
	queries []string
}

func (r *queryRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	r.queries = append(r.queries, sql)
}

type queryPlanNode struct {
	NodeType string          `json:"Node Type"`
	Relation string          `json:"Relation Name"`
	Index    string          `json:"Index Name"`
	Loops    float64         `json:"Actual Loops"`
	Hits     int64           `json:"Shared Hit Blocks"`
	Reads    int64           `json:"Shared Read Blocks"`
	Plans    []queryPlanNode `json:"Plans"`
}

func explainQuery(t *testing.T, tx *gorm.DB, query string) queryPlanNode {
	t.Helper()
	var raw []byte
	require.NoError(t, tx.Raw("EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query).Row().Scan(&raw))
	var plans []struct {
		Plan          queryPlanNode `json:"Plan"`
		ExecutionTime float64       `json:"Execution Time"`
	}
	require.NoError(t, json.Unmarshal(raw, &plans))
	require.Len(t, plans, 1)
	t.Logf("query execution: %.3f ms", plans[0].ExecutionTime)
	return plans[0].Plan
}

func walkQueryPlan(plan queryPlanNode, visit func(queryPlanNode)) {
	visit(plan)
	for _, child := range plan.Plans {
		walkQueryPlan(child, visit)
	}
}

func TestWorkflowListPlansDoNotScanRootsPerJob(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		owner, err := du.CreateUser(DataSetup{})
		require.NoError(t, err)
		require.NoError(t, tx.Exec(`INSERT INTO jobs (user_id, org_id, name, job_command, status)
   SELECT ?, ?, 'evaluation-' || n, 'true', 'completed' FROM generate_series(1, 500) n`, owner.UserID, owner.UserID).Error)
		require.NoError(t, tx.Exec(`INSERT INTO workflow_instances (user_id, org_id, parent_job_id, name, status, workflow_security_id)
   SELECT user_id, org_id, job_id, name, 'success', name FROM jobs WHERE user_id = ? LIMIT 200`, owner.UserID).Error)
		require.NoError(t, tx.Exec("ANALYZE jobs, workflow_instances").Error)
		recorder := &queryRecorder{Interface: logger.Default}
		queryCtx := context.WithValue(ctx, postgres_store.GetTxContextKey(), tx.Session(&gorm.Session{Logger: recorder}))
		ws := postgres_store.PostgresStore
		filters := map[string]interface{}{"user_id": owner.UserID}
		rows, total, err := ws.ListWorkflowSummariesVisibleTo(queryCtx, owner.UserID, true, filters, 50, 0)
		require.NoError(t, err)
		require.EqualValues(t, 500, total)
		require.Len(t, rows, 50)
		_, _, err = ws.ListWorkflowSummariesVisibleTo(queryCtx, "", false, filters, 50, 0)
		require.NoError(t, err)
		_, err = ws.ListWorkflowSummaries(queryCtx, filters, 50, 0)
		require.NoError(t, err)
		require.Len(t, recorder.queries, 5)
		for _, query := range recorder.queries {
			require.True(t, strings.HasPrefix(strings.TrimSpace(query), "WITH workflow_rows"))
			scans := 0
			walkQueryPlan(explainQuery(t, tx, query), func(node queryPlanNode) {
				if node.Relation == "workflow_instances" {
					scans++
					require.LessOrEqual(t, node.Loops, float64(1), "workflow roots must not be scanned once per job")
				}
			})
			require.Positive(t, scans)
		}
	})
}

func TestWorkflowReconcilerUsesStatusIndex(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		owner, err := du.CreateUser(DataSetup{})
		require.NoError(t, err)
		wf := &models.WorkflowInstance{UserID: owner.UserID, Name: "history", Status: "running", QueueName: "reactorcide-jobs"}
		require.NoError(t, postgres_store.PostgresStore.CreateWorkflowInstance(ctx, wf))
		// Retained job specifications make scanning historical nodes expensive.
		require.NoError(t, tx.Exec(`INSERT INTO jobs (user_id, org_id, name, job_command, status, completed_at)
            SELECT ?, ?, 'old-' || n, 'true', 'completed', now() - interval '1 hour'
            FROM generate_series(1, 4000) n`, owner.UserID, owner.UserID).Error)
		require.NoError(t, tx.Exec(`INSERT INTO workflow_nodes (workflow_id, name, display_name, status, job_id, job_spec)
            SELECT ?, name, 'old', CASE name WHEN 'old-1' THEN 'pending'
                WHEN 'old-2' THEN 'submitted' WHEN 'old-3' THEN 'running'
                WHEN 'old-4' THEN 'failed' WHEN 'old-5' THEN 'skipped' ELSE 'completed' END,
                job_id, jsonb_build_object('command', repeat('x', 1200))
            FROM jobs WHERE user_id = ?`, wf.WorkflowID, owner.UserID).Error)
		job, err := du.CreateJob(DataSetup{"UserID": owner.UserID})
		require.NoError(t, err)
		setJobState(t, tx, job.JobID, map[string]any{"status": "completed", "completed_at": time.Now().Add(-time.Hour)})
		require.NoError(t, tx.Model(&models.WorkflowNode{}).Where("workflow_id = ? AND status = 'running'", wf.WorkflowID).Update("job_id", job.JobID).Error)
		require.NoError(t, tx.Exec("ANALYZE workflow_nodes, jobs").Error)

		recorder := &queryRecorder{Interface: logger.Default}
		queryCtx := context.WithValue(ctx, postgres_store.GetTxContextKey(), tx.Session(&gorm.Session{Logger: recorder}))
		jobs, err := postgres_store.PostgresStore.ListFinalJobsWithOpenWorkflowNode(queryCtx, time.Now().Add(-2*time.Minute))
		require.NoError(t, err)
		require.True(t, jobIDs(jobs)[job.JobID])
		require.Len(t, recorder.queries, 1)
		checkIndex := func(plan queryPlanNode) {
			t.Helper()
			found := false
			walkQueryPlan(plan, func(node queryPlanNode) {
				if node.Index == "workflow_nodes_open_job_idx" {
					found = true
				}
			})
			require.True(t, found, "the reconciler must use the node status index: %+v", plan)
		}
		indexed := explainQuery(t, tx, recorder.queries[0])
		checkIndex(indexed)
		// Compare buffer work in the same fixture. Elapsed time varies with
		// host load, so it is recorded but is not a pass/fail condition.
		require.NoError(t, tx.Exec("SAVEPOINT without_status_index").Error)
		require.NoError(t, tx.Exec("DROP INDEX workflow_nodes_open_job_idx").Error)
		unindexed := explainQuery(t, tx, recorder.queries[0])
		require.NoError(t, tx.Exec("ROLLBACK TO SAVEPOINT without_status_index").Error)
		t.Logf("reconciler shared buffers: %d with index, %d without index", indexed.Hits+indexed.Reads, unindexed.Hits+unindexed.Reads)
		require.Less(t, indexed.Hits+indexed.Reads, unindexed.Hits+unindexed.Reads)

		// Keep the fixed node statuses when PostgreSQL uses a generic plan for
		// the parameterized job statuses.
		require.NoError(t, tx.Exec("SET LOCAL plan_cache_mode = 'force_generic_plan'").Error)
		prepared := strings.NewReplacer("'completed'", "$1", "'failed'", "$2", "'cancelled'", "$3", "'timeout'", "$4").Replace(recorder.queries[0])
		require.NoError(t, tx.Exec("PREPARE open_workflow_nodes(text, text, text, text) AS "+prepared).Error)
		checkIndex(explainQuery(t, tx, "EXECUTE open_workflow_nodes('completed', 'failed', 'cancelled', 'timeout')"))
		require.NoError(t, tx.Exec("DEALLOCATE open_workflow_nodes").Error)
	})
}
