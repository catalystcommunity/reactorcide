package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/artifacts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/oidcissuer"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/provenance"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/testresults"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// artifactRoutes serves the WP15 REST API (docs/artifacts-and-tests.md,
// docs/provenance.md). Every route works with the admin API token.
//
//	GET              /api/v1/jobs/JOB/artifacts
//	GET              /api/v1/jobs/JOB/artifacts/ARTIFACT_ID        (download)
//	GET              /api/v1/jobs/JOB/tests[?status=fail,error&limit=N]
//	GET              /api/v1/jobs/JOB/provenance                   (DSSE envelope)
//	GET              /api/v1/projects/PROJECT/tests/flaky[?days=N]
//	GET|POST         /api/v1/projects/PROJECT/test-quarantines[?all=true]
//	DELETE           /api/v1/projects/PROJECT/test-quarantines/QUARANTINE_ID
//	GET              /api/v1/provenance/keys                       (public JWKS)
//
// The job routes need jobs:read and the visibility of the job, the same as
// the job itself. The browser reads the same data through CSIL.
type artifactRoutes struct {
	BaseHandler
	store   store.Store
	objects objects.ObjectStore
	jobs    *JobHandler
	keys    *secrets.MasterKeyManager
}

func newArtifactRoutes(appStore store.Store, objectStore objects.ObjectStore, jobs *JobHandler, keys *secrets.MasterKeyManager) *artifactRoutes {
	return &artifactRoutes{store: appStore, objects: objectStore, jobs: jobs, keys: keys}
}

func (a *artifactRoutes) service() *artifacts.Service {
	st, ok := a.store.(store.ArtifactStore)
	if !ok || a.objects == nil {
		return nil
	}
	return &artifacts.Service{Store: st, Objects: a.objects, Limits: artifacts.LimitsFromEnv()}
}

// serveJob handles JOB/artifacts[/ID], JOB/tests, and JOB/provenance. It
// returns false for other paths.
func (a *artifactRoutes) serveJob(w http.ResponseWriter, r *http.Request, path string,
	transactionMiddleware, authMiddleware func(http.Handler) http.Handler) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return false
	}
	var handler http.HandlerFunc
	switch {
	case len(parts) == 2 && parts[1] == "artifacts":
		handler = func(w http.ResponseWriter, r *http.Request) { a.listArtifacts(w, r, parts[0]) }
	case len(parts) == 3 && parts[1] == "artifacts":
		handler = func(w http.ResponseWriter, r *http.Request) { a.downloadArtifact(w, r, parts[0], parts[2]) }
	case len(parts) == 2 && parts[1] == "tests":
		handler = func(w http.ResponseWriter, r *http.Request) { a.jobTests(w, r, parts[0]) }
	case len(parts) == 2 && parts[1] == "provenance":
		handler = func(w http.ResponseWriter, r *http.Request) { a.jobProvenance(w, r, parts[0]) }
	default:
		return false
	}
	transactionMiddleware(authMiddleware(handler)).ServeHTTP(w, r)
	return true
}

// serveProject handles PROJECT/tests/flaky and PROJECT/test-quarantines[/ID].
func (a *artifactRoutes) serveProject(w http.ResponseWriter, r *http.Request, parts []string,
	transactionMiddleware, authMiddleware func(http.Handler) http.Handler) bool {
	var handler http.HandlerFunc
	switch {
	case len(parts) == 3 && parts[1] == "tests" && parts[2] == "flaky":
		handler = func(w http.ResponseWriter, r *http.Request) { a.flakyTests(w, r, parts[0]) }
	case (len(parts) == 2 || len(parts) == 3) && parts[1] == "test-quarantines":
		id := ""
		if len(parts) == 3 {
			id = parts[2]
		}
		handler = func(w http.ResponseWriter, r *http.Request) { a.quarantines(w, r, parts[0], id) }
	default:
		return false
	}
	transactionMiddleware(authMiddleware(handler)).ServeHTTP(w, r)
	return true
}

// readableJob loads a job and checks that the caller can read it.
func (a *artifactRoutes) readableJob(w http.ResponseWriter, r *http.Request, jobID string) *models.Job {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return nil
	}
	job, err := a.store.GetJobByID(r.Context(), jobID)
	if err != nil {
		a.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return nil
	}
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil && checkauth.GetPrincipalFromContext(r.Context()) == nil {
		a.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return nil
	}
	if a.jobs == nil || !a.jobs.canSubjectAccessJob(r.Context(), user, job, tokencaps.JobsRead) {
		// Not found, not forbidden: do not tell a caller that a private job
		// exists.
		a.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return nil
	}
	return job
}

// ArtifactResponse is one artifact in a REST listing.
type ArtifactResponse struct {
	ArtifactID  string     `json:"artifact_id"`
	Name        string     `json:"name"`
	Path        string     `json:"path"`
	Kind        string     `json:"kind"`
	MediaType   string     `json:"media_type"`
	SizeBytes   int64      `json:"size_bytes"`
	SHA256      string     `json:"sha256,omitempty"`
	Status      string     `json:"status"`
	Reason      string     `json:"status_reason,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

func artifactResponse(a models.JobArtifact) ArtifactResponse {
	return ArtifactResponse{ArtifactID: a.ArtifactID, Name: a.Name, Path: a.Path, Kind: a.Kind, MediaType: a.MediaType,
		SizeBytes: a.SizeBytes, SHA256: a.SHA256, Status: a.Status, Reason: a.StatusReason, CreatedAt: a.CreatedAt, CompletedAt: a.CompletedAt}
}

func (a *artifactRoutes) listArtifacts(w http.ResponseWriter, r *http.Request, jobID string) {
	job := a.readableJob(w, r, jobID)
	if job == nil {
		return
	}
	out := []ArtifactResponse{}
	if st, ok := a.store.(store.ArtifactStore); ok {
		list, err := st.ListJobArtifacts(r.Context(), job.JobID)
		if err != nil {
			a.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		for _, item := range list {
			out = append(out, artifactResponse(item))
		}
	}
	a.respondWithJSON(w, http.StatusOK, map[string]any{"job_id": job.JobID, "artifacts": out})
}

func (a *artifactRoutes) downloadArtifact(w http.ResponseWriter, r *http.Request, jobID, artifactID string) {
	job := a.readableJob(w, r, jobID)
	if job == nil {
		return
	}
	service := a.service()
	if service == nil {
		a.respondWithError(w, http.StatusServiceUnavailable, store.ErrServiceUnavailable)
		return
	}
	artifact, err := service.Store.GetJobArtifact(r.Context(), artifactID)
	if err != nil || artifact.JobID != job.JobID {
		a.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	reader, err := service.Open(r.Context(), artifact)
	if err != nil {
		a.respondWithJSON(w, http.StatusConflict, ErrorResponse{Error: "conflict", Message: err.Error()})
		return
	}
	defer reader.Close()
	writeArtifactHeaders(w, artifact)
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("artifact download %s: %v", artifact.ArtifactID, err)
	}
}

// writeArtifactHeaders sets the download headers. The content type is
// always application/octet-stream with nosniff and an attachment
// disposition, so that an HTML artifact never runs in the API origin.
func writeArtifactHeaders(w http.ResponseWriter, artifact *models.JobArtifact) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(artifact.Path)}))
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.SizeBytes, 10))
	w.Header().Set("X-Reactorcide-Artifact-SHA256", artifact.SHA256)
	w.Header().Set("X-Reactorcide-Artifact-Media-Type", artifact.MediaType)
	w.Header().Set("Cache-Control", "private, max-age=0")
}

func (a *artifactRoutes) jobTests(w http.ResponseWriter, r *http.Request, jobID string) {
	job := a.readableJob(w, r, jobID)
	if job == nil {
		return
	}
	results, ok := a.store.(store.TestResultStore)
	if !ok {
		a.respondWithJSON(w, http.StatusOK, map[string]any{"job_id": job.JobID, "summary": nil, "results": []models.TestResult{}})
		return
	}
	var statuses []string
	for _, value := range strings.Split(r.URL.Query().Get("status"), ",") {
		switch value = strings.TrimSpace(value); value {
		case models.TestStatusPass, models.TestStatusFail, models.TestStatusError, models.TestStatusSkip:
			statuses = append(statuses, value)
		case "":
		default:
			a.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: "status must be pass, fail, error, or skip"})
			return
		}
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	var summary *models.JobTestSummary
	if found, err := results.GetJobTestSummary(r.Context(), job.JobID); err == nil {
		summary = found
	} else if !errors.Is(err, store.ErrNotFound) {
		a.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	rows, err := results.ListJobTestResults(r.Context(), job.JobID, statuses, limit)
	if err != nil {
		a.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	quarantined, flaky := a.testFlags(r, job, rows)
	type row struct {
		models.TestResult
		Quarantined bool `json:"quarantined"`
		Flaky       bool `json:"flaky"`
	}
	out := make([]row, 0, len(rows))
	for _, result := range rows {
		out = append(out, row{TestResult: result, Quarantined: quarantined[result.TestID], Flaky: flaky[result.TestID]})
	}
	a.respondWithJSON(w, http.StatusOK, map[string]any{"job_id": job.JobID, "summary": summary, "results": out})
}

// testFlags returns the quarantined and the flaky test IDs of the results.
func (a *artifactRoutes) testFlags(r *http.Request, job *models.Job, rows []models.TestResult) (map[string]bool, map[string]bool) {
	quarantined, flaky := map[string]bool{}, map[string]bool{}
	if job.ProjectID == nil || len(rows) == 0 {
		return quarantined, flaky
	}
	if st, ok := a.store.(store.TestQuarantineStore); ok {
		if active, err := st.ListTestQuarantines(r.Context(), *job.ProjectID, false, time.Now().UTC()); err == nil {
			for _, q := range active {
				quarantined[q.TestID] = true
			}
		}
	}
	if st, ok := a.store.(store.TestResultStore); ok {
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.TestID)
		}
		since := time.Now().UTC().Add(-testresults.FlakyWindowFromEnv())
		if found, err := st.GetTestFlakiness(r.Context(), *job.ProjectID, ids); err == nil {
			for id, row := range found {
				if row.FlakyCount > 0 && row.LastFlakyAt != nil && row.LastFlakyAt.After(since) {
					flaky[id] = true
				}
			}
		}
	}
	return quarantined, flaky
}

func (a *artifactRoutes) jobProvenance(w http.ResponseWriter, r *http.Request, jobID string) {
	job := a.readableJob(w, r, jobID)
	if job == nil {
		return
	}
	service := a.service()
	if service == nil {
		a.respondWithError(w, http.StatusServiceUnavailable, store.ErrServiceUnavailable)
		return
	}
	list, err := service.Store.ListJobArtifacts(r.Context(), job.JobID)
	if err != nil {
		a.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	for i := range list {
		if list[i].Kind != models.ArtifactKindProvenance || list[i].Status != models.ArtifactStatusComplete {
			continue
		}
		data, err := service.ReadRange(r.Context(), &list[i], 0, 4<<20)
		if err != nil {
			a.respondWithError(w, http.StatusServiceUnavailable, store.ErrServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", provenance.MediaType)
		w.Header().Set("X-Reactorcide-Artifact-SHA256", list[i].SHA256)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	}
	a.respondWithJSON(w, http.StatusNotFound, ErrorResponse{Error: "not_found", Message: "the job has no provenance"})
}

// ProvenanceKeys serves GET /api/v1/provenance/keys: the public JWKS of
// the provenance keys. It is public, like the OIDC JWKS.
func (a *artifactRoutes) ProvenanceKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	signer := provenance.NewSigner(a.store, a.encryptor(), provenance.BuilderIDFromEnv(config.OIDCIssuerURL))
	if signer == nil {
		a.respondWithJSON(w, http.StatusNotFound, ErrorResponse{Error: "not_found", Message: "provenance is not configured on this coordinator"})
		return
	}
	set, err := signer.JWKS(r.Context())
	if err != nil {
		log.Printf("provenance JWKS: %v", err)
		a.respondWithJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "service_unavailable", Message: "the provenance keys are not available"})
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	body, err := json.Marshal(map[string]any{"builder_id": signer.IssuerValue(), "keys": set.Keys})
	if err != nil {
		a.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (a *artifactRoutes) encryptor() oidcissuer.Encryptor {
	if a.keys == nil {
		return nil
	}
	return a.keys
}

// --- project routes ---

func (a *artifactRoutes) project(w http.ResponseWriter, r *http.Request, projectID string) *models.Project {
	project, err := a.store.GetProjectByID(r.Context(), projectID)
	if err != nil || project == nil {
		a.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return nil
	}
	return project
}

func (a *artifactRoutes) quarantineService() *testresults.QuarantineService {
	return &testresults.QuarantineService{Store: a.store, Record: func(ctx context.Context, orgID, action, subjectType, subjectID string, details models.JSONB) {
		audit.Record(ctx, a.store, orgID, action, subjectType, subjectID, details)
	}}
}

func (a *artifactRoutes) quarantineFail(w http.ResponseWriter, err error) {
	code := testresults.ErrorCode(err)
	status := http.StatusInternalServerError
	switch code {
	case "permission_denied":
		status = http.StatusForbidden
	case "invalid_argument":
		status = http.StatusBadRequest
	case "not_found":
		status = http.StatusNotFound
	case "unavailable":
		status = http.StatusNotImplemented
	}
	message := err.Error()
	if code == "internal" {
		message = "the test quarantine operation failed"
	}
	a.respondWithJSON(w, status, ErrorResponse{Error: code, Message: message})
}

func (a *artifactRoutes) flakyTests(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	project := a.project(w, r, projectID)
	if project == nil {
		return
	}
	service := a.quarantineService()
	caller := RequiredSetCaller(r.Context(), a.store)
	if err := service.MayView(r.Context(), caller, project); err != nil {
		a.quarantineFail(w, err)
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	window := testresults.FlakyWindowFromEnv()
	if days > 0 && days <= 3650 {
		window = time.Duration(days) * 24 * time.Hour
	}
	rows := []models.TestFlakiness{}
	if st, ok := a.store.(store.TestResultStore); ok {
		found, err := st.ListProjectFlakyTests(r.Context(), project.ProjectID, time.Now().UTC().Add(-window), 200)
		if err != nil {
			a.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		rows = found
	}
	a.respondWithJSON(w, http.StatusOK, map[string]any{"project_id": project.ProjectID, "window_days": int(window.Hours() / 24), "tests": rows})
}

// QuarantineTestRequest is the body of POST .../test-quarantines.
type QuarantineTestRequest struct {
	TestID             string `json:"test_id"`
	Suite              string `json:"suite"`
	Classname          string `json:"classname"`
	Name               string `json:"name"`
	Reason             string `json:"reason"`
	Until              string `json:"until"`
	Duration           string `json:"duration"`
	OwnerPrincipalID   string `json:"owner_principal_id"`
	WorkflowSecurityID string `json:"workflow_security_id"`
}

func (a *artifactRoutes) quarantines(w http.ResponseWriter, r *http.Request, projectID, quarantineID string) {
	project := a.project(w, r, projectID)
	if project == nil {
		return
	}
	service := a.quarantineService()
	caller := RequiredSetCaller(r.Context(), a.store)
	switch {
	case quarantineID == "" && r.Method == http.MethodGet:
		rows, err := service.List(r.Context(), caller, project, r.URL.Query().Get("all") == "true")
		if err != nil {
			a.quarantineFail(w, err)
			return
		}
		a.respondWithJSON(w, http.StatusOK, map[string]any{"project_id": project.ProjectID, "quarantines": rows})
	case quarantineID == "" && r.Method == http.MethodPost:
		var req QuarantineTestRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
			a.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_argument", Message: "the body must be a JSON object"})
			return
		}
		until, err := requiredset.ParseQuarantineExpiry(req.Until, req.Duration, time.Now().UTC())
		if err != nil {
			a.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_argument", Message: err.Error()})
			return
		}
		row, err := service.Quarantine(r.Context(), caller, project, testresults.QuarantineRequest{
			TestID: req.TestID, Suite: req.Suite, Classname: req.Classname, Name: req.Name, Reason: req.Reason, Until: until,
			OwnerPrincipalID: req.OwnerPrincipalID, WorkflowSecurityID: req.WorkflowSecurityID})
		if err != nil {
			a.quarantineFail(w, err)
			return
		}
		a.respondWithJSON(w, http.StatusCreated, row)
	case quarantineID != "" && r.Method == http.MethodDelete:
		if err := service.Unquarantine(r.Context(), caller, project, quarantineID, r.URL.Query().Get("workflow_security_id")); err != nil {
			a.quarantineFail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, fmt.Sprintf("Method %s not allowed", r.Method), http.StatusMethodNotAllowed)
	}
}
