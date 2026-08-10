package test

import (
	"context"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/stretchr/testify/require"
)

func TestVCSReportStoreOutboxAndAdvisoryLock(t *testing.T) {
	ctx := context.Background()
	organization, err := postgres_store.PostgresStore.GetDefaultOrganization(ctx)
	require.NoError(t, err)
	target := &models.VCSReportTarget{OrgID: organization.OrgID, Provider: "github", Repository: uniqueName("report/repo"),
		TargetType: "pull_request", ExternalTargetID: "17", RootMarker: "<!-- reactorcide:report:v1 -->"}
	require.NoError(t, postgres_store.PostgresStore.StartVCSReportGeneration(ctx, target, "head-sha-1"))
	require.NotEmpty(t, target.ReportTargetID)
	require.False(t, target.GenerationComplete)

	entry := &models.VCSReportEntry{ReportTargetID: target.ReportTargetID, EntryKey: "workflow-a", Generation: target.CurrentGeneration,
		Status: "running", StructuredState: models.JSONB{"title": "A"}}
	require.NoError(t, postgres_store.PostgresStore.UpsertVCSReportEntry(ctx, entry))
	stored, err := postgres_store.PostgresStore.GetVCSReportTarget(ctx, target.ReportTargetID)
	require.NoError(t, err)
	require.True(t, stored.Dirty)
	require.Greater(t, stored.DesiredRevision, int64(0))

	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- postgres_store.PostgresStore.WithVCSReportTargetLock(ctx, target.ReportTargetID,
			func(context.Context, *models.VCSReportTarget, []models.VCSReportEntry) error {
				close(locked)
				<-release
				return nil
			})
	}()
	<-locked
	err = postgres_store.PostgresStore.WithVCSReportTargetLock(ctx, target.ReportTargetID,
		func(context.Context, *models.VCSReportTarget, []models.VCSReportEntry) error { return nil })
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "locked")
	close(release)
	require.NoError(t, <-done)
}
