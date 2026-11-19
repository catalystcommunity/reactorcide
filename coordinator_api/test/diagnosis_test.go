package test

// Postgres-backed coverage for diagnosis entries (WP17, migration 000065,
// docs/diagnosis.md): the store upsert, the REST API for a service
// principal with and without diagnosis:write, log reads by a service
// principal, the rate limit, and the failure class column of subscriptions.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/diagnosis"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/ratelimit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDiagnosisStore(t *testing.T) {
	ps := postgres_store.PostgresStore
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		user, err := du.CreateUser(DataSetup{"Username": "diag-store", "Email": "diag-store@example.com"})
		require.NoError(t, err)
		wf := &models.WorkflowInstance{UserID: user.UserID, Name: "ci", Status: "failed", QueueName: "reactorcide-jobs"}
		require.NoError(t, ps.CreateWorkflowInstance(ctx, wf))
		job, err := du.CreateJob(DataSetup{"UserID": user.UserID, "Name": "build", "SourceType": "git",
			"JobCommand": "false", "Status": "failed"})
		require.NoError(t, err)
		node := &models.WorkflowNode{WorkflowID: wf.WorkflowID, Name: "build", DisplayName: "build", Status: "failed", JobID: &job.JobID}
		require.NoError(t, ps.CreateWorkflowNode(ctx, node))
		other, err := du.CreateJob(DataSetup{"UserID": user.UserID, "Name": "other", "SourceType": "git", "JobCommand": "true"})
		require.NoError(t, err)

		svc := &models.Principal{OrgID: &job.OrgID, Name: "diag-store-bot", DisplayName: "Diag Store Bot"}
		require.NoError(t, ps.CreateServicePrincipal(ctx, svc))

		shifts := models.RunShifts{MemoryLimit: "8Gi"}
		entry := &models.DiagnosisReport{OrgID: job.OrgID, TargetType: models.DiagnosisTargetJob, JobID: &job.JobID,
			NodeName: "build", AuthorPrincipalID: svc.PrincipalID, AuthorKind: "service", AuthorDisplayName: "Diag Store Bot",
			Title: "first", Body: "body", Confidence: "low", SuggestedShifts: &shifts,
			Links: []models.DiagnosisLink{{Name: "log", URL: "https://example.com/log"}}}
		created, err := ps.UpsertDiagnosisReport(ctx, entry)
		require.NoError(t, err)
		assert.True(t, created)
		assert.Equal(t, 1, entry.Revision)
		firstID := entry.DiagnosisID

		again := &models.DiagnosisReport{OrgID: job.OrgID, TargetType: models.DiagnosisTargetJob, JobID: &job.JobID,
			AuthorPrincipalID: svc.PrincipalID, AuthorKind: "service", Title: "second"}
		created, err = ps.UpsertDiagnosisReport(ctx, again)
		require.NoError(t, err)
		assert.False(t, created, "the same principal replaces its entry")
		assert.Equal(t, firstID, again.DiagnosisID)
		assert.Equal(t, 2, again.Revision)
		assert.Nil(t, again.SuggestedShifts, "a replace clears the fields the new post does not set")
		assert.Empty(t, again.Links)

		// Another principal has its own entry; a workflow entry is separate.
		adminPrincipal := user.UserID
		_, err = ps.UpsertDiagnosisReport(ctx, &models.DiagnosisReport{OrgID: job.OrgID, TargetType: models.DiagnosisTargetJob,
			JobID: &job.JobID, AuthorPrincipalID: adminPrincipal, AuthorKind: "human", Title: "human view"})
		require.NoError(t, err)
		_, err = ps.UpsertDiagnosisReport(ctx, &models.DiagnosisReport{OrgID: job.OrgID, TargetType: models.DiagnosisTargetWorkflow,
			WorkflowID: &wf.WorkflowID, AuthorPrincipalID: svc.PrincipalID, AuthorKind: "service", Title: "workflow view"})
		require.NoError(t, err)
		_, err = ps.UpsertDiagnosisReport(ctx, &models.DiagnosisReport{OrgID: job.OrgID, TargetType: models.DiagnosisTargetJob,
			JobID: &other.JobID, AuthorPrincipalID: svc.PrincipalID, AuthorKind: "service", Title: "unrelated"})
		require.NoError(t, err)

		forJob, err := ps.ListDiagnosisReportsForJob(ctx, job.JobID)
		require.NoError(t, err)
		require.Len(t, forJob, 2)
		forWorkflow, err := ps.ListDiagnosisReportsForWorkflow(ctx, wf.WorkflowID)
		require.NoError(t, err)
		titles := []string{}
		for _, r := range forWorkflow {
			titles = append(titles, r.Title)
		}
		assert.ElementsMatch(t, []string{"second", "human view", "workflow view"}, titles,
			"a workflow lists its own entry and the entries of its node jobs")

		// The failure class column of subscriptions defaults to empty.
		var defaults []string
		require.NoError(t, tx.Raw(`SELECT column_default FROM information_schema.columns
			WHERE table_name = 'event_subscriptions' AND column_name = 'failure_classes'`).Scan(&defaults).Error)
		require.Len(t, defaults, 1)
		assert.Contains(t, defaults[0], "{}")

		// The database caps the body. Last: the error aborts the transaction.
		_, err = ps.UpsertDiagnosisReport(ctx, &models.DiagnosisReport{OrgID: job.OrgID, TargetType: models.DiagnosisTargetJob,
			JobID: &other.JobID, AuthorPrincipalID: adminPrincipal, AuthorKind: "human", Title: "big",
			Body: strings.Repeat("x", diagnosis.MaxBodyBytes+1)})
		require.Error(t, err)
	})
}

type diagnosisAnswer struct {
	DiagnosisID string `json:"diagnosis_id"`
	Revision    int    `json:"revision"`
	Created     *bool  `json:"created"`
	Author      struct {
		PrincipalID string `json:"principal_id"`
		Kind        string `json:"kind"`
		DisplayName string `json:"display_name"`
	} `json:"author"`
	SuggestedShifts map[string]any `json:"suggested_shifts"`
}

func issuePrincipalToken(t *testing.T, ctx context.Context, adminAuth, org, principal string, capabilities ...string) string {
	t.Helper()
	rr := principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/"+org+"/principals/"+principal+"/tokens", adminAuth,
		map[string]any{"name": strings.Join(capabilities, "-"), "capabilities": capabilities})
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var issued struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &issued))
	return "Bearer " + issued.Token
}

// TestDiagnosisAPIForServicePrincipals is the end-to-end flow of a diagnosis
// tool: read the logs of a failed job, post an entry, replace it.
func TestDiagnosisAPIForServicePrincipals(t *testing.T) {
	diagnosis.SetRateLimiter(nil)
	defer diagnosis.SetRateLimiter(nil)
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		memStore := objects.NewMemoryObjectStore()
		handlers.SetObjectStore(memStore)
		defer handlers.SetObjectStore(nil)

		du := &DataUtils{db: tx}
		admin, err := du.CreateUser(DataSetup{"Username": "diag-admin", "Email": "diag-admin@example.com"})
		require.NoError(t, err)
		adminAuth, err := createTokenAuthHeader(ctx, tx, admin.UserID)
		require.NoError(t, err)
		org := newPrincipalOrg(t, ctx, "diag-org")
		otherOrg := newPrincipalOrg(t, ctx, "diag-other")
		job, err := du.CreateJob(DataSetup{"UserID": admin.UserID, "OrgID": org.OrgID, "Name": "build", "SourceType": "git",
			"JobCommand": "false", "Status": "failed"})
		require.NoError(t, err)
		logs, _ := json.Marshal([]map[string]string{{"timestamp": "2026-11-16T00:00:00Z", "stream": "stdout", "message": "boom"}})
		require.NoError(t, memStore.Put(ctx, "logs/"+job.JobID+"/stdout.json", bytes.NewReader(logs), "application/json"))

		for _, spec := range []struct{ org, name string }{{"diag-org", "diag-bot"}, {"diag-other", "outsider"}} {
			rr := principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/"+spec.org+"/principals", adminAuth,
				map[string]string{"name": spec.name, "display_name": "Diag Bot " + spec.name})
			require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		}
		readCaps := []string{tokencaps.JobsRead, tokencaps.LogsRead, tokencaps.WorkflowsRead}
		withWrite := issuePrincipalToken(t, ctx, adminAuth, "diag-org", "diag-bot", append(readCaps, tokencaps.DiagnosisWrite)...)
		readOnly := issuePrincipalToken(t, ctx, adminAuth, "diag-org", "diag-bot", readCaps...)
		outsider := issuePrincipalToken(t, ctx, adminAuth, "diag-other", "outsider", append(readCaps, tokencaps.DiagnosisWrite)...)

		logsPath := "/api/v1/jobs/" + job.JobID + "/logs?stream=stdout"
		diagPath := "/api/v1/jobs/" + job.JobID + "/diagnosis"
		post := map[string]any{"title": "Out of memory", "body": "The test used <b>too much</b> memory. cc @team",
			"confidence": "high", "category": "oom", "links": []map[string]string{{"name": "dashboard", "url": "https://example.com/d"}},
			"suggested_shifts": map[string]any{"memory": "8Gi"}}

		// With no role, the principal reads nothing and posts nothing.
		rr := principalRequest(t, ctx, http.MethodGet, logsPath, withWrite, nil)
		assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/jobs/"+job.JobID+"/artifacts", withWrite, nil)
		assert.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPut, diagPath, withWrite, post)
		assert.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

		// A member role: logs:read and diagnosis:write apply.
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/diag-org/principals/diag-bot/roles", adminAuth,
			map[string]string{"role": "member"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodGet, logsPath, withWrite, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), "boom")
		// Artifacts follow the read rules of the job (WP15).
		rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/jobs/"+job.JobID+"/artifacts", withWrite, nil)
		assert.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

		rr = principalRequest(t, ctx, http.MethodPut, diagPath, readOnly, post)
		assert.Equal(t, http.StatusForbidden, rr.Code, "a token without diagnosis:write cannot post: %s", rr.Body.String())

		rr = principalRequest(t, ctx, http.MethodPut, diagPath, withWrite, post)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var first diagnosisAnswer
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &first))
		require.NotNil(t, first.Created)
		assert.True(t, *first.Created)
		assert.Equal(t, models.PrincipalKindService, first.Author.Kind)
		assert.Equal(t, "Diag Bot diag-bot", first.Author.DisplayName)
		assert.Equal(t, "8Gi", first.SuggestedShifts["memory"])

		post["title"] = "Out of memory in the test step"
		rr = principalRequest(t, ctx, http.MethodPut, diagPath, withWrite, post)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var second diagnosisAnswer
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &second))
		assert.False(t, *second.Created)
		assert.Equal(t, first.DiagnosisID, second.DiagnosisID)
		assert.Equal(t, 2, second.Revision)

		// Invalid input: a shift that only a new run can make, and HTML-free
		// but oversized text.
		bad := map[string]any{"title": "x", "suggested_shifts": map[string]any{"image": "evil:latest"}}
		rr = principalRequest(t, ctx, http.MethodPut, diagPath, withWrite, bad)
		assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPut, diagPath, withWrite, map[string]any{"title": strings.Repeat("t", 201)})
		assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPut, diagPath, withWrite, map[string]any{"title": "x", "unknown": 1})
		assert.Equal(t, http.StatusBadRequest, rr.Code, "unknown fields are refused")

		// The admin token works with no logins and has its own entry.
		rr = principalRequest(t, ctx, http.MethodPut, diagPath, adminAuth, map[string]any{"title": "admin note"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodGet, diagPath, readOnly, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var listed struct {
			Diagnoses []diagnosisAnswer `json:"diagnoses"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &listed))
		assert.Len(t, listed.Diagnoses, 2)

		// A principal of another organization does not see the job.
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/diag-other/principals/outsider/roles", adminAuth,
			map[string]string{"role": "member"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodGet, diagPath, outsider, nil)
		assert.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPut, diagPath, outsider, post)
		assert.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
		_ = otherOrg

		// The rate limit applies to each principal.
		diagnosis.SetRateLimiter(ratelimit.New(1, 1))
		rr = principalRequest(t, ctx, http.MethodPut, diagPath, withWrite, post)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPut, diagPath, withWrite, post)
		assert.Equal(t, http.StatusTooManyRequests, rr.Code, rr.Body.String())
		assert.NotEmpty(t, rr.Header().Get("Retry-After"))
		rr = principalRequest(t, ctx, http.MethodPut, diagPath, adminAuth, map[string]any{"title": "admin again"})
		assert.Equal(t, http.StatusOK, rr.Code, "another principal has its own bucket: %s", rr.Body.String())
	})
}

func TestDiagnosisMigrationUpAndDown(t *testing.T) {
	db := newScratchDBAt(t, "wp17_migrate", 64)
	var orgID string
	require.NoError(t, db.QueryRow(`INSERT INTO organizations (name, display_name) VALUES ('wp17-org', 'WP17') RETURNING org_id`).Scan(&orgID))

	require.NoError(t, goose.UpTo(db, "migrations", 65))
	var tables int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_name = 'diagnosis_reports'`).Scan(&tables))
	assert.Equal(t, 1, tables)
	var columns int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'event_subscriptions' AND column_name = 'failure_classes'`).Scan(&columns))
	assert.Equal(t, 1, columns)

	require.NoError(t, goose.DownTo(db, "migrations", 64))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_name = 'diagnosis_reports'`).Scan(&tables))
	assert.Zero(t, tables)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'event_subscriptions' AND column_name = 'failure_classes'`).Scan(&columns))
	assert.Zero(t, columns)
	var orgs int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM organizations WHERE org_id = $1`, orgID).Scan(&orgs))
	assert.Equal(t, 1, orgs, "Down keeps existing rows")
}
