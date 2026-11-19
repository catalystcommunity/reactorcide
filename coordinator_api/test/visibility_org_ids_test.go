package test

// Postgres coverage of the "org_ids" filter of ListJobsVisibleTo and
// ListWorkflowSummariesVisibleTo: an API token sees only the organizations
// it covers, and an empty list matches nothing.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

func TestVisibleToLists_OrgIDsFilter(t *testing.T) {
	ctx := context.Background()
	vs := requireJobsVisibleToStore(t)
	ws := postgres_store.PostgresStore

	orgA := createTestUser(t)
	orgB := createTestUser(t)
	jobA := createTestJob(t, orgA.UserID, "completed")
	jobB := createTestJob(t, orgB.UserID, "completed")
	require.Equal(t, orgA.UserID, jobA.OrgID)
	require.Equal(t, orgB.UserID, jobB.OrgID)

	newWorkflow := func(org string) *models.WorkflowInstance {
		wf := &models.WorkflowInstance{UserID: org, Name: uniqueName("wf"), Status: "success", QueueName: "reactorcide-jobs"}
		require.NoError(t, ws.CreateWorkflowInstance(ctx, wf))
		return wf
	}
	wfA := newWorkflow(orgA.UserID)
	wfB := newWorkflow(orgB.UserID)

	both := []string{orgA.UserID, orgB.UserID}
	jobIDs := func(rows []models.Job) []string {
		var ids []string
		for _, r := range rows {
			if r.UserID == orgA.UserID || r.UserID == orgB.UserID {
				ids = append(ids, r.JobID)
			}
		}
		return ids
	}
	summaryIDs := func(rows []models.WorkflowSummary) []string {
		var ids []string
		for _, r := range rows {
			if r.UserID == orgA.UserID || r.UserID == orgB.UserID {
				ids = append(ids, r.WorkflowID)
			}
		}
		return ids
	}

	for _, tc := range []struct {
		name      string
		orgIDs    []string
		wantJobs  []string
		wantFlows []string
	}{
		// Summaries also list standalone jobs (rows keyed by job ID).
		{"one org", []string{orgA.UserID}, []string{jobA.JobID}, []string{wfA.WorkflowID, jobA.JobID}},
		{"both orgs", both, []string{jobA.JobID, jobB.JobID}, []string{wfA.WorkflowID, wfB.WorkflowID, jobA.JobID, jobB.JobID}},
		{"no orgs", []string{}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, admin := range []bool{true, false} {
				viewer := orgA.UserID
				// The global admin path skips the visibility predicate; the
				// owner path adds it. Both must honour org_ids. orgB's rows
				// are public, so the non-admin owner of orgA can see them.
				jobs, _, err := vs.ListJobsVisibleTo(ctx, viewer, admin, map[string]interface{}{"org_ids": tc.orgIDs}, 1000, 0)
				require.NoError(t, err)
				require.ElementsMatch(t, tc.wantJobs, jobIDs(jobs), "admin=%v", admin)

				flows, _, err := ws.ListWorkflowSummariesVisibleTo(ctx, viewer, admin, map[string]interface{}{"org_ids": tc.orgIDs}, 1000, 0)
				require.NoError(t, err)
				require.ElementsMatch(t, tc.wantFlows, summaryIDs(flows), "admin=%v", admin)
			}
		})
	}
}
