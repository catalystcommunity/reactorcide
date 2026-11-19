package test

import (
	"context"
	"net/http"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/diagnosis"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A principal removes its own diagnosis entry and its pull request section.
// It cannot remove the entry of another principal.
func TestDiagnosisDeleteOwnEntry(t *testing.T) {
	diagnosis.SetRateLimiter(nil)
	defer diagnosis.SetRateLimiter(nil)
	// A fresh router, and a fresh one after: the router keeps the object
	// store of its first build, and later tests set their own.
	handlers.ResetAppMux()
	defer handlers.ResetAppMux()
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		du := &DataUtils{db: tx}
		admin, err := du.CreateUser(DataSetup{"Username": "diag-del-admin", "Email": "diag-del-admin@example.com"})
		require.NoError(t, err)
		adminAuth, err := createTokenAuthHeader(ctx, tx, admin.UserID)
		require.NoError(t, err)
		org := newPrincipalOrg(t, ctx, "diag-del-org")
		job, err := du.CreateJob(DataSetup{"UserID": admin.UserID, "OrgID": org.OrgID, "Name": "build", "SourceType": "git",
			"JobCommand": "false", "Status": "failed"})
		require.NoError(t, err)
		rr := principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/diag-del-org/principals", adminAuth,
			map[string]string{"name": "diag-del-bot", "display_name": "Diag Del Bot"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/diag-del-org/principals/diag-del-bot/roles", adminAuth,
			map[string]string{"role": "member"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		bot := issuePrincipalToken(t, ctx, adminAuth, "diag-del-org", "diag-del-bot",
			tokencaps.JobsRead, tokencaps.WorkflowsRead, tokencaps.DiagnosisWrite)

		diagPath := "/api/v1/jobs/" + job.JobID + "/diagnosis"
		rr = principalRequest(t, ctx, http.MethodPut, diagPath, bot, map[string]any{"title": "bot view"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPut, diagPath, adminAuth, map[string]any{"title": "admin view"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

		// A pull request section of the bot entry.
		reports, err := ps.ListDiagnosisReportsForJob(ctx, job.JobID)
		require.NoError(t, err)
		require.Len(t, reports, 2)
		var botReport models.DiagnosisReport
		for _, report := range reports {
			if report.Title == "bot view" {
				botReport = report
			}
		}
		target := &models.VCSReportTarget{OrgID: org.OrgID, Provider: "github", Repository: "o/r", TargetType: "pull_request",
			ExternalTargetID: "7", RootMarker: "<!-- root -->", CurrentGeneration: 1}
		require.NoError(t, ps.UpsertVCSReportTarget(ctx, target))
		require.NoError(t, ps.UpsertVCSReportEntry(ctx, &models.VCSReportEntry{ReportTargetID: target.ReportTargetID,
			EntryKey: diagnosis.ReportEntryKey(&botReport), Generation: 1, Status: "failure", StructuredState: models.JSONB{}}))
		before, err := ps.GetVCSReportTarget(ctx, target.ReportTargetID)
		require.NoError(t, err)

		// The bot deletes its own entry. The admin entry stays.
		rr = principalRequest(t, ctx, http.MethodDelete, diagPath, bot, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), botReport.DiagnosisID)
		reports, err = ps.ListDiagnosisReportsForJob(ctx, job.JobID)
		require.NoError(t, err)
		require.Len(t, reports, 1)
		assert.Equal(t, "admin view", reports[0].Title)
		entries, err := ps.ListVCSReportEntries(ctx, target.ReportTargetID)
		require.NoError(t, err)
		assert.Empty(t, entries, "the pull request section is removed")
		after, err := ps.GetVCSReportTarget(ctx, target.ReportTargetID)
		require.NoError(t, err)
		assert.True(t, after.Dirty)
		assert.Greater(t, after.DesiredRevision, before.DesiredRevision)

		// A second delete finds nothing: the bot has no entry now, and it
		// cannot remove the admin entry.
		rr = principalRequest(t, ctx, http.MethodDelete, diagPath, bot, nil)
		assert.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
		reports, err = ps.ListDiagnosisReportsForJob(ctx, job.JobID)
		require.NoError(t, err)
		assert.Len(t, reports, 1)
	})
}
