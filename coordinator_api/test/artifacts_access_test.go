package test

// WP15: the access rules of job artifacts and test results. A read follows
// the visibility of the job: the jobs of a public project are public in the
// UI, a private job is "not found" to a caller without a role. REST needs a
// token with jobs:read.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/artifacts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func TestArtifactAccessPublicAndPrivate(t *testing.T) {
	initTestDB()
	require.NoError(t, initErr)
	ctx := context.Background()
	adminValue := uniqueName("wp15-admin-token")
	du := &DataUtils{db: testDB}
	adminToken, err := du.CreateAPIToken(DataSetup{"Name": "wp15-admin", "TokenHash": checkauth.HashAPIToken(adminValue),
		"IsActive": true, "SubjectType": "instance_token", "AllOrganizations": true, "AllCapabilities": true})
	require.NoError(t, err)

	owner := createTestUser(t)
	public := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("public"), false)
	private := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("private"), true)
	publicJob := createTestJobInProject(t, owner.UserID, public.ProjectID, "running")
	privateJob := createTestJobInProject(t, owner.UserID, private.ProjectID, "running")
	t.Cleanup(func() {
		testDB.Exec(`DELETE FROM jobs WHERE job_id IN (?, ?)`, publicJob.JobID, privateJob.JobID)
		testDB.Exec(`DELETE FROM projects WHERE project_id IN (?, ?)`, public.ProjectID, private.ProjectID)
		testDB.Exec(`DELETE FROM api_tokens WHERE token_id = ?`, adminToken.TokenID)
		testDB.Exec(`DELETE FROM principals WHERE principal_id = ?`, adminToken.TokenID)
	})

	objectStore := objects.NewMemoryObjectStore()
	handlers.ResetAppMux()
	defer handlers.ResetAppMux()
	handlers.SetObjectStore(objectStore)
	mux := handlers.GetAppMux()

	service := &artifacts.Service{Store: store.AppStore.(store.ArtifactStore), Objects: objectStore, Limits: artifacts.DefaultLimits}
	pub, err := service.AppendChunk(ctx, publicJob, "", artifacts.ChunkRequest{Path: "report.html", Data: []byte("<script>alert(1)</script>"), Final: true})
	require.NoError(t, err)
	priv, err := service.AppendChunk(ctx, privateJob, "", artifacts.ChunkRequest{Path: "secret.bin", Data: []byte("private"), Final: true})
	require.NoError(t, err)

	// CSIL, anonymous.
	listed, svcErr := csilCall(t, mux, "ReactorcideUi", "list-job-artifacts", csilapi.EncodeListJobArtifactsRequest,
		csilapi.DecodeListJobArtifactsResponse, csilapi.ListJobArtifactsRequest{JobId: publicJob.JobID}, "")
	require.Nil(t, svcErr, "%+v", svcErr)
	require.Len(t, listed.Artifacts, 1)
	assert.Equal(t, pub.SHA256, listed.Artifacts[0].Sha256)
	read, svcErr := csilCall(t, mux, "ReactorcideUi", "read-job-artifact", csilapi.EncodeReadJobArtifactRequest,
		csilapi.DecodeReadJobArtifactResponse, csilapi.ReadJobArtifactRequest{JobId: publicJob.JobID, ArtifactId: pub.ArtifactID}, "")
	require.Nil(t, svcErr, "%+v", svcErr)
	assert.Equal(t, "<script>alert(1)</script>", string(read.Data))
	assert.True(t, read.Eof)

	_, svcErr = csilCall(t, mux, "ReactorcideUi", "list-job-artifacts", csilapi.EncodeListJobArtifactsRequest,
		csilapi.DecodeListJobArtifactsResponse, csilapi.ListJobArtifactsRequest{JobId: privateJob.JobID}, "")
	require.NotNil(t, svcErr)
	assert.Equal(t, "not_found", svcErr.Code)
	_, svcErr = csilCall(t, mux, "ReactorcideUi", "read-job-artifact", csilapi.EncodeReadJobArtifactRequest,
		csilapi.DecodeReadJobArtifactResponse, csilapi.ReadJobArtifactRequest{JobId: privateJob.JobID, ArtifactId: priv.ArtifactID}, "")
	require.NotNil(t, svcErr)
	assert.Equal(t, "not_found", svcErr.Code)
	// An artifact of another job is not found through a visible job.
	_, svcErr = csilCall(t, mux, "ReactorcideUi", "read-job-artifact", csilapi.EncodeReadJobArtifactRequest,
		csilapi.DecodeReadJobArtifactResponse, csilapi.ReadJobArtifactRequest{JobId: publicJob.JobID, ArtifactId: priv.ArtifactID}, "")
	require.NotNil(t, svcErr)
	assert.Equal(t, "not_found", svcErr.Code)

	// CSIL with the admin token sees the private job.
	listed, svcErr = csilCall(t, mux, "ReactorcideUi", "list-job-artifacts", csilapi.EncodeListJobArtifactsRequest,
		csilapi.DecodeListJobArtifactsResponse, csilapi.ListJobArtifactsRequest{JobId: privateJob.JobID}, adminValue)
	require.Nil(t, svcErr, "%+v", svcErr)
	require.Len(t, listed.Artifacts, 1)

	// REST.
	get := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}
	assert.Equal(t, http.StatusUnauthorized, get("/api/v1/jobs/"+privateJob.JobID+"/artifacts", "").Code)
	rr := get("/api/v1/jobs/"+publicJob.JobID+"/artifacts/"+pub.ArtifactID, adminValue)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, "<script>alert(1)</script>", rr.Body.String())
	assert.Equal(t, "application/octet-stream", rr.Header().Get("Content-Type"), "an HTML artifact never runs in the API origin")
	assert.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"))
	assert.Contains(t, rr.Header().Get("Content-Disposition"), "attachment")
	assert.Equal(t, pub.SHA256, rr.Header().Get("X-Reactorcide-Artifact-SHA256"))
	assert.Equal(t, http.StatusNotFound, get("/api/v1/jobs/"+publicJob.JobID+"/artifacts/"+priv.ArtifactID, adminValue).Code)
	rr = get("/api/v1/jobs/"+privateJob.JobID+"/tests", adminValue)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), `"summary":null`)
	assert.Equal(t, http.StatusNotFound, get("/api/v1/jobs/"+privateJob.JobID+"/provenance", adminValue).Code)
}
