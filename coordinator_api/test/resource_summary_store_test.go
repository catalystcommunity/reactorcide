package test

// Postgres-backed coverage for postgres_store/resource_summary_operations.go
// and migration 000040: the "same job" filter (org, project, name, finished
// status, created_at range), newest-first order and limit, the summary join,
// the upsert replacing an older row, and the ON DELETE CASCADE.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestResourceSummaryStore(t *testing.T) {
	ctx := context.Background()
	rs, ok := store.AppStore.(store.ResourceSummaryStore)
	require.True(t, ok, "store.AppStore must implement store.ResourceSummaryStore")

	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	otherProject := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	name := uniqueName("build")
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	make := func(projectID *string, jobName, status string, created time.Time) *models.Job {
		du := &DataUtils{db: testDB}
		job, err := du.CreateJob(DataSetup{"UserID": owner.UserID, "ProjectID": projectID, "Name": jobName, "Status": status, "JobCommand": "true"})
		require.NoError(t, err)
		require.NoError(t, testDB.Model(&models.Job{}).Where("job_id = ?", job.JobID).Update("created_at", created).Error)
		return job
	}
	var runs []*models.Job
	for i := 0; i < 4; i++ {
		runs = append(runs, make(&project.ProjectID, name, []string{"completed", "failed", "cancelled", "timeout"}[i], base.Add(time.Duration(i)*time.Hour)))
	}
	make(&project.ProjectID, name, "running", base.Add(10*time.Hour))
	make(&otherProject.ProjectID, name, "completed", base)
	make(&project.ProjectID, name+"-other", "completed", base)
	noProject := make(nil, name, "completed", base)

	cpu := int64(750)
	require.NoError(t, rs.UpsertJobResourceSummary(ctx, &models.JobResourceSummary{JobID: runs[2].JobID, SummaryVersion: 1, ComputedAt: time.Now().UTC(), SampleCount: 3}))
	// Upsert replaces the row.
	require.NoError(t, rs.UpsertJobResourceSummary(ctx, &models.JobResourceSummary{JobID: runs[2].JobID, SummaryVersion: 2, ComputedAt: time.Now().UTC(), SampleCount: 9, CPUMaxMillicores: &cpu}))

	got, err := rs.ListJobRunsWithSummaries(ctx, store.JobRunHistoryFilter{OrgID: owner.UserID, ProjectID: &project.ProjectID, Name: name, Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 4)
	for i, run := range got {
		require.Equal(t, runs[3-i].JobID, run.Job.JobID, "newest first")
	}
	require.NotNil(t, got[1].Summary)
	require.Equal(t, 2, got[1].Summary.SummaryVersion)
	require.Equal(t, 9, got[1].Summary.SampleCount)
	require.Equal(t, cpu, *got[1].Summary.CPUMaxMillicores)
	require.Nil(t, got[1].Summary.MemoryMaxBytes)
	require.Nil(t, got[0].Summary)

	from, to := base.Add(30*time.Minute), base.Add(2*time.Hour)
	ranged, err := rs.ListJobRunsWithSummaries(ctx, store.JobRunHistoryFilter{OrgID: owner.UserID, ProjectID: &project.ProjectID, Name: name, From: &from, To: &to, Limit: 10})
	require.NoError(t, err)
	require.Len(t, ranged, 2)

	limited, err := rs.ListJobRunsWithSummaries(ctx, store.JobRunHistoryFilter{OrgID: owner.UserID, ProjectID: &project.ProjectID, Name: name, Limit: 1})
	require.NoError(t, err)
	require.Len(t, limited, 1)
	require.Equal(t, runs[3].JobID, limited[0].Job.JobID)

	unscoped, err := rs.ListJobRunsWithSummaries(ctx, store.JobRunHistoryFilter{OrgID: owner.UserID, Name: name, Limit: 10})
	require.NoError(t, err)
	require.Len(t, unscoped, 1)
	require.Equal(t, noProject.JobID, unscoped[0].Job.JobID)

	// The summary goes with its job.
	require.NoError(t, testDB.Exec("DELETE FROM jobs WHERE job_id = ?", runs[2].JobID).Error)
	var count int64
	require.NoError(t, testDB.Model(&models.JobResourceSummary{}).Where("job_id = ?", runs[2].JobID).Count(&count).Error)
	require.Zero(t, count)
}
