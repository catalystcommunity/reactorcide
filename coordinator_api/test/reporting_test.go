package test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/reporting"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coredb"
)

func reportingProject(t *testing.T, ctx context.Context, name string) (*models.Organization, *models.Project) {
	t.Helper()
	org := newPrincipalOrg(t, ctx, name)
	project := &models.Project{Name: name + "-app", RepoURL: "github.com/" + name + "/app", OrgID: org.OrgID}
	require.NoError(t, postgres_store.PostgresStore.CreateProject(ctx, project))
	return org, project
}

func TestReportingSettingsStore(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		org, project := reportingProject(t, ctx, "rp-store")

		// No rows: the built-in defaults (the behavior before the feature).
		effective, err := reporting.Effective(ctx, ps, org.OrgID, project.ProjectID)
		require.NoError(t, err)
		assert.Equal(t, models.ReportModeFull, effective.ReportMode)
		assert.Equal(t, models.CodeownersImportOff, effective.CodeownersImport)
		assert.Equal(t, models.PathAcceptanceOff, effective.PathAcceptance)
		assert.Equal(t, models.ReportModeFull, reporting.Mode(ctx, ps, org.OrgID, project.ProjectID))

		orgSettings := models.DefaultOrgReportingSettings(org.OrgID)
		orgSettings.ReportMode = models.ReportModeStatusesOnly
		orgSettings.AgreementContexts = pq.StringArray{"ci/build"}
		orgSettings.PathOwners = []models.PathOwnerRule{{Pattern: "/payments/", Owners: []string{"reactorcide_group:payments"}}}
		require.NoError(t, ps.UpsertOrgReportingSettings(ctx, orgSettings))
		mode := models.ReportModeSilent
		contexts := pq.StringArray{}
		rules := []models.PathOwnerRule{{Pattern: "/web/", Owners: []string{"reactorcide_group:web"}}}
		require.NoError(t, ps.UpsertProjectReportingSettings(ctx, &models.ProjectReportingSettings{ProjectID: project.ProjectID,
			OrgID: org.OrgID, ReportMode: &mode, AgreementContexts: &contexts, PathOwners: &rules}))
		effective, err = reporting.Effective(ctx, ps, org.OrgID, project.ProjectID)
		require.NoError(t, err)
		assert.Equal(t, models.ReportModeSilent, effective.ReportMode)
		assert.Equal(t, "project", effective.ReportModeSource)
		assert.Empty(t, effective.AgreementContexts, "an empty project list is a value")
		assert.Equal(t, "/web/", effective.PathOwners[0].Pattern)

		// A second upsert with nil values clears them (inherit).
		require.NoError(t, ps.UpsertProjectReportingSettings(ctx, &models.ProjectReportingSettings{ProjectID: project.ProjectID, OrgID: org.OrgID}))
		effective, err = reporting.Effective(ctx, ps, org.OrgID, project.ProjectID)
		require.NoError(t, err)
		assert.Equal(t, models.ReportModeStatusesOnly, effective.ReportMode)
		assert.Equal(t, []string{"ci/build"}, effective.AgreementContexts)
		assert.Equal(t, "/payments/", effective.PathOwners[0].Pattern)

		// The CHECK constraints refuse an unknown mode.
		bad := "loud"
		assert.Error(t, ps.UpsertProjectReportingSettings(ctx, &models.ProjectReportingSettings{ProjectID: project.ProjectID, OrgID: org.OrgID, ReportMode: &bad}))
	})
}

func TestCIAgreementStore(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		org, project := reportingProject(t, ctx, "rp-agree")
		pr := 4
		row := &models.CIAgreement{ProjectID: project.ProjectID, OrgID: org.OrgID, CommitSHA: "sha1", Provider: "github",
			Repository: "rp-agree/app", PRNumber: &pr, ReactorcideResult: models.AgreementResultSuccess}
		require.NoError(t, ps.RecordCIAgreementResult(ctx, row))
		due, err := ps.ListDueCIAgreements(ctx, time.Now().UTC().Add(time.Second), 10)
		require.NoError(t, err)
		require.Len(t, due, 1)
		first := due[0]

		// One read: the other CI is still running.
		next := reporting.Observe(first, []models.AgreementOtherResult{{Name: "ci/build", Kind: "status", State: "pending"}},
			[]string{"ci/build"}, time.Now().UTC())
		require.NoError(t, ps.UpdateCIAgreementObservation(ctx, &next))
		due, err = ps.ListDueCIAgreements(ctx, time.Now().UTC(), 10)
		require.NoError(t, err)
		assert.Empty(t, due, "the backoff holds the next read")

		// The late result completes the row.
		later := reporting.Observe(next, []models.AgreementOtherResult{{Name: "ci/build", Kind: "status", State: "failure"}},
			[]string{"ci/build"}, time.Now().UTC().Add(time.Hour))
		require.NoError(t, ps.UpdateCIAgreementObservation(ctx, &later))
		rows, err := ps.ListCIAgreements(ctx, project.ProjectID, nil, 0)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, models.AgreementStatusComplete, rows[0].Status)
		require.NotNil(t, rows[0].Agree)
		assert.False(t, *rows[0].Agree)
		assert.Equal(t, "failure", rows[0].OtherResults[0].State)
		assert.Equal(t, 2, rows[0].Attempts)

		// The same result of the same event again keeps the comparison.
		require.NoError(t, ps.RecordCIAgreementResult(ctx, &models.CIAgreement{ProjectID: project.ProjectID, OrgID: org.OrgID,
			CommitSHA: "sha1", ReactorcideResult: models.AgreementResultSuccess}))
		rows, _ = ps.ListCIAgreements(ctx, project.ProjectID, nil, 0)
		assert.Equal(t, models.AgreementStatusComplete, rows[0].Status)
		// A new result starts it again; an old observation then changes nothing.
		require.NoError(t, ps.RecordCIAgreementResult(ctx, &models.CIAgreement{ProjectID: project.ProjectID, OrgID: org.OrgID,
			CommitSHA: "sha1", ReactorcideResult: models.AgreementResultFailure}))
		require.NoError(t, ps.UpdateCIAgreementObservation(ctx, &later))
		rows, _ = ps.ListCIAgreements(ctx, project.ProjectID, nil, 0)
		assert.Equal(t, models.AgreementStatusPending, rows[0].Status)
		assert.Equal(t, models.AgreementResultFailure, rows[0].ReactorcideResult)
		assert.Equal(t, 0, rows[0].Attempts)

		future := time.Now().UTC().Add(time.Hour)
		rows, err = ps.ListCIAgreements(ctx, project.ProjectID, &future, 0)
		require.NoError(t, err)
		assert.Empty(t, rows)
	})
}

func TestPathAcceptanceStore(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		org, project := reportingProject(t, ctx, "rp-accept")
		row := &models.PathAcceptance{ProjectID: project.ProjectID, OrgID: org.OrgID, WorkflowSecurityID: "build",
			PathPattern: "/payments/", OwnerSubject: "reactorcide_group:payments", State: models.PathAcceptanceRequested}
		require.NoError(t, ps.CreatePathAcceptance(ctx, row))
		require.NotEmpty(t, row.AcceptanceID)
		assert.ErrorIs(t, ps.CreatePathAcceptance(ctx, &models.PathAcceptance{ProjectID: project.ProjectID, OrgID: org.OrgID,
			WorkflowSecurityID: "build", PathPattern: "/payments/", OwnerSubject: "reactorcide_group:payments",
			State: models.PathAcceptanceRequested}), store.ErrAlreadyExists, "one open row for each workflow, pattern, and owner")

		now := time.Now().UTC()
		row.State, row.AcceptedAt = models.PathAcceptanceAccepted, &now
		require.NoError(t, ps.TransitionPathAcceptance(ctx, row, models.PathAcceptanceRequested))
		assert.ErrorIs(t, ps.TransitionPathAcceptance(ctx, row, models.PathAcceptanceRequested), store.ErrConflict)
		accepted, err := ps.ListPathAcceptances(ctx, project.ProjectID, models.PathAcceptanceAccepted)
		require.NoError(t, err)
		require.Len(t, accepted, 1)
		assert.True(t, accepted[0].Active())

		row.State, row.RevokedAt = models.PathAcceptanceRevoked, &now
		require.NoError(t, ps.TransitionPathAcceptance(ctx, row, models.PathAcceptanceRequested, models.PathAcceptanceAccepted))
		// After a revoke a new request is allowed.
		require.NoError(t, ps.CreatePathAcceptance(ctx, &models.PathAcceptance{ProjectID: project.ProjectID, OrgID: org.OrgID,
			WorkflowSecurityID: "build", PathPattern: "/payments/", OwnerSubject: "reactorcide_group:payments", State: models.PathAcceptanceRequested}))
		all, err := ps.ListPathAcceptances(ctx, project.ProjectID, "")
		require.NoError(t, err)
		assert.Len(t, all, 2)
		_, err = ps.GetPathAcceptance(ctx, "not-a-uuid")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
}

func reportingAudits(t *testing.T, tx *gorm.DB, prefix string) []string {
	t.Helper()
	var events []models.AuditEvent
	require.NoError(t, tx.Where("action LIKE ?", prefix+"%").Order("created_at, audit_event_id").Find(&events).Error)
	actions := []string{}
	for _, event := range events {
		actions = append(actions, event.Action)
	}
	return actions
}

// The no-login flow through REST with the admin token.
func TestReportingAPI(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		adminUser, err := du.CreateUser(DataSetup{"Username": "rp-admin", "Email": "rp@example.com"})
		require.NoError(t, err)
		adminAuth, err := createTokenAuthHeader(ctx, tx, adminUser.UserID)
		require.NoError(t, err)
		_, project := reportingProject(t, ctx, "rp-api")
		base := "/api/v1/projects/" + project.ProjectID

		rr := principalRequest(t, ctx, http.MethodGet, base+"/reporting", adminAuth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var view struct {
			Effective   models.EffectiveReportingSettings `json:"effective"`
			Permissions reporting.Permissions             `json:"permissions"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
		assert.Equal(t, "full", view.Effective.ReportMode)
		assert.True(t, view.Permissions.ManageOwnership)

		rr = principalRequest(t, ctx, http.MethodPut, base+"/reporting", adminAuth, map[string]any{
			"report_mode": "silent", "agreement_contexts": []string{"ci/build"}, "path_acceptance": "enforce",
			"path_owners": []map[string]any{{"pattern": "/payments/", "owners": []string{"reactorcide_group:payments"}}}})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
		assert.Equal(t, "silent", view.Effective.ReportMode)
		assert.Equal(t, "enforce", view.Effective.PathAcceptance)

		rr = principalRequest(t, ctx, http.MethodPut, base+"/reporting", adminAuth, map[string]any{"report_mode": "loud"})
		assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPut, base+"/reporting", adminAuth, map[string]any{"inherit": []string{"report_mode"}})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
		assert.Equal(t, "full", view.Effective.ReportMode)

		rr = principalRequest(t, ctx, http.MethodPut, "/api/v1/organizations/rp-api/reporting-settings", adminAuth,
			map[string]any{"report_mode": "statuses_only", "agreement_contexts": []string{"lint"}})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodGet, base+"/reporting", adminAuth, nil)
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
		assert.Equal(t, "statuses_only", view.Effective.ReportMode)
		assert.Equal(t, "org", view.Effective.ReportModeSource)

		rr = principalRequest(t, ctx, http.MethodGet, base+"/agreement?since=7d", adminAuth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), `"contexts":["ci/build"]`)

		// Path acceptances: request, accept, revoke.
		rr = principalRequest(t, ctx, http.MethodPost, base+"/path-acceptances", adminAuth, map[string]any{
			"workflow_security_id": "build", "path_pattern": "/payments/", "owner_subject": "reactorcide_group:payments", "reason": "schema"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		var row models.PathAcceptance
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &row))
		assert.Equal(t, models.PathAcceptanceRequested, row.State)
		rr = principalRequest(t, ctx, http.MethodPost, base+"/path-acceptances", adminAuth, map[string]any{
			"workflow_security_id": "build", "path_pattern": "/payments/", "owner_subject": "reactorcide_group:payments"})
		assert.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, base+"/path-acceptances/"+row.AcceptanceID+"/accept", adminAuth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, base+"/path-acceptances/"+row.AcceptanceID+"/reject", adminAuth, nil)
		assert.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, base+"/path-acceptances/"+row.AcceptanceID+"/revoke", adminAuth, map[string]any{"reason": "moved"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodGet, base+"/path-acceptances?state=revoked", adminAuth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), row.AcceptanceID)
		rr = principalRequest(t, ctx, http.MethodPost, base+"/path-acceptances", adminAuth, map[string]any{
			"workflow_security_id": "build", "path_pattern": "!bad", "owner_subject": "reactorcide_group:payments"})
		assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

		// No credential: refused.
		rr = principalRequest(t, ctx, http.MethodGet, base+"/reporting", "", nil)
		assert.True(t, rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden, rr.Body.String())

		assert.Equal(t, []string{"reporting.settings", "reporting.settings", "reporting.org_settings"}, reportingAudits(t, tx, "reporting."))
		assert.Equal(t, []string{"path_acceptance.request", "path_acceptance.accept", "path_acceptance.revoke"}, reportingAudits(t, tx, "path_acceptance."))
	})
}

func TestReportingMigrationDown(t *testing.T) {
	baseURI := os.Getenv("TEST_DB_URI")
	require.NotEmpty(t, baseURI)
	parsed, err := url.Parse(baseURI)
	require.NoError(t, err)
	name := fmt.Sprintf("reporting_migrate_%d", time.Now().UnixNano())
	admin, err := sql.Open("postgres", baseURI)
	require.NoError(t, err)
	_, err = admin.Exec(fmt.Sprintf(`CREATE DATABASE %q`, name))
	require.NoError(t, err)
	scratch := *parsed
	scratch.Path = "/" + name
	db, err := sql.Open("postgres", scratch.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, name))
		_ = admin.Close()
	})
	goose.SetBaseFS(coredb.Migrations)
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.UpTo(db, "migrations", 56))
	tables := `SELECT count(*) FROM information_schema.tables WHERE table_name IN
	    ('org_reporting_settings', 'project_reporting_settings', 'ci_agreement', 'path_acceptances')`
	var count int
	require.NoError(t, db.QueryRow(tables).Scan(&count))
	assert.Equal(t, 4, count)
	require.NoError(t, goose.DownTo(db, "migrations", 55))
	require.NoError(t, db.QueryRow(tables).Scan(&count))
	assert.Equal(t, 0, count)
	require.NoError(t, goose.UpTo(db, "migrations", 56))
}
