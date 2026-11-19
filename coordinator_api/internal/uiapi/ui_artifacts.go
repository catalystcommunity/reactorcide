package uiapi

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/artifacts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/provenance"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/testresults"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// WP15: job artifacts, test results, flaky tests, test quarantine, and
// provenance for the UI (docs/artifacts-and-tests.md). A read is authorized
// by the visibility of the job or the project, so the jobs of a public
// repository are public. A quarantine change needs a session or a token
// (testresults.QuarantineService has the rules).

// maxArtifactReadBytes is the largest part that read-job-artifact returns.
const maxArtifactReadBytes = 4 << 20

func (s *UiService) artifactService() *artifacts.Service {
	st, ok := s.deps.Store.(store.ArtifactStore)
	if !ok || s.deps.ObjectStore == nil {
		return nil
	}
	return &artifacts.Service{Store: st, Objects: s.deps.ObjectStore, Limits: artifacts.LimitsFromEnv()}
}

// visibleJob returns the job when the caller can view it.
func (s *UiService) visibleJob(ctx context.Context, jobID string) (*models.Job, error) {
	if err := requireNonEmpty("job_id", jobID, 64); err != nil {
		return nil, err
	}
	identity, _ := s.deps.resolveIdentity(ctx)
	job, err := s.deps.Store.GetJobByID(ctx, jobID)
	if err != nil {
		return nil, mapStoreErr(err, "job not found")
	}
	visible, err := s.deps.Resolver.CanViewJob(ctx, identity, job)
	if err != nil {
		return nil, NewServiceError("internal", "an internal error occurred")
	}
	if !visible {
		return nil, NewServiceError("not_found", "job not found")
	}
	return job, nil
}

func artifactToWire(a models.JobArtifact) csilapi.JobArtifactSummary {
	out := csilapi.JobArtifactSummary{ArtifactId: a.ArtifactID, Path: a.Path, Kind: a.Kind, MediaType: a.MediaType,
		SizeBytes: a.SizeBytes, Sha256: a.SHA256, Status: a.Status, CreatedAt: formatTime(a.CreatedAt)}
	if a.StatusReason != "" {
		reason := a.StatusReason
		out.StatusReason = &reason
	}
	return out
}

func (s *UiService) ListJobArtifacts(ctx context.Context, req csilapi.ListJobArtifactsRequest) (csilapi.ListJobArtifactsResponse, error) {
	job, err := s.visibleJob(ctx, req.JobId)
	if err != nil {
		return csilapi.ListJobArtifactsResponse{}, err
	}
	out := []csilapi.JobArtifactSummary{}
	st, ok := s.deps.Store.(store.ArtifactStore)
	if !ok {
		return csilapi.ListJobArtifactsResponse{Artifacts: out}, nil
	}
	list, err := st.ListJobArtifacts(ctx, job.JobID)
	if err != nil {
		return csilapi.ListJobArtifactsResponse{}, NewServiceError("internal", "could not read the job artifacts")
	}
	for _, item := range list {
		out = append(out, artifactToWire(item))
	}
	return csilapi.ListJobArtifactsResponse{Artifacts: out}, nil
}

func (s *UiService) ReadJobArtifact(ctx context.Context, req csilapi.ReadJobArtifactRequest) (csilapi.ReadJobArtifactResponse, error) {
	job, err := s.visibleJob(ctx, req.JobId)
	if err != nil {
		return csilapi.ReadJobArtifactResponse{}, err
	}
	service := s.artifactService()
	if service == nil {
		return csilapi.ReadJobArtifactResponse{}, NewServiceError("unavailable", "artifact storage is not configured")
	}
	artifact, err := service.Store.GetJobArtifact(ctx, strings.TrimSpace(req.ArtifactId))
	if err != nil || artifact.JobID != job.JobID {
		return csilapi.ReadJobArtifactResponse{}, NewServiceError("not_found", "artifact not found")
	}
	length := int64(maxArtifactReadBytes)
	if req.Length != nil && *req.Length > 0 && *req.Length < length {
		length = *req.Length
	}
	if req.Offset < 0 || req.Offset > artifact.SizeBytes {
		return csilapi.ReadJobArtifactResponse{}, NewServiceError("invalid_argument", "the offset is outside the artifact")
	}
	data, err := service.ReadRange(ctx, artifact, req.Offset, length)
	if err != nil {
		code := artifacts.ErrorCode(err)
		if code == "internal" {
			code = "unavailable"
		}
		return csilapi.ReadJobArtifactResponse{}, NewServiceError(code, err.Error())
	}
	return csilapi.ReadJobArtifactResponse{Data: data, SizeBytes: artifact.SizeBytes, Sha256: artifact.SHA256,
		Eof: req.Offset+int64(len(data)) >= artifact.SizeBytes}, nil
}

func summaryToWire(summary *models.JobTestSummary) *csilapi.JobTestSummaryView {
	if summary == nil {
		return nil
	}
	errors := append([]string{}, summary.ParseErrors...)
	return &csilapi.JobTestSummaryView{Reports: int64(summary.Reports), Total: int64(summary.Total), Passed: int64(summary.Passed),
		Failed: int64(summary.Failed), Errored: int64(summary.Errored), Skipped: int64(summary.Skipped),
		QuarantinedFailures: int64(summary.QuarantinedFailures), Flaky: int64(summary.Flaky), Truncated: summary.Truncated,
		ParseErrors: errors, QuarantinePolicy: summary.QuarantinePolicy, QuarantineOverride: summary.QuarantineOverride}
}

func (s *UiService) GetJobTestResults(ctx context.Context, req csilapi.GetJobTestResultsRequest) (csilapi.GetJobTestResultsResponse, error) {
	job, err := s.visibleJob(ctx, req.JobId)
	if err != nil {
		return csilapi.GetJobTestResultsResponse{}, err
	}
	resp := csilapi.GetJobTestResultsResponse{Results: []csilapi.TestResultView{}}
	if job.ProjectID != nil {
		projectID := *job.ProjectID
		resp.ProjectId = &projectID
	}
	results, ok := s.deps.Store.(store.TestResultStore)
	if !ok {
		return resp, nil
	}
	summary, err := results.GetJobTestSummary(ctx, job.JobID)
	if err == nil {
		resp.Summary = summaryToWire(summary)
	} else if err != store.ErrNotFound {
		return csilapi.GetJobTestResultsResponse{}, NewServiceError("internal", "could not read the test summary")
	}
	var statuses []string
	if req.FailedOnly != nil && *req.FailedOnly {
		statuses = []string{models.TestStatusFail, models.TestStatusError}
	}
	limit := 500
	if req.Limit != nil && *req.Limit > 0 && *req.Limit < 2000 {
		limit = int(*req.Limit)
	}
	rows, err := results.ListJobTestResults(ctx, job.JobID, statuses, limit)
	if err != nil {
		return csilapi.GetJobTestResultsResponse{}, NewServiceError("internal", "could not read the test results")
	}
	quarantined, flaky := map[string]bool{}, map[string]bool{}
	if job.ProjectID != nil && len(rows) > 0 {
		if st, ok := s.deps.Store.(store.TestQuarantineStore); ok {
			if active, err := st.ListTestQuarantines(ctx, *job.ProjectID, false, time.Now().UTC()); err == nil {
				for _, q := range active {
					quarantined[q.TestID] = true
				}
			}
		}
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.TestID)
		}
		since := time.Now().UTC().Add(-testresults.FlakyWindowFromEnv())
		if found, err := results.GetTestFlakiness(ctx, *job.ProjectID, ids); err == nil {
			for id, row := range found {
				flaky[id] = row.FlakyCount > 0 && row.LastFlakyAt != nil && row.LastFlakyAt.After(since)
			}
		}
	}
	for _, row := range rows {
		view := csilapi.TestResultView{TestId: row.TestID, Suite: row.Suite, Classname: row.Classname, Name: row.Name,
			Status: row.Status, DurationMs: row.DurationMs, Message: row.Message, File: row.File,
			Quarantined: quarantined[row.TestID], Flaky: flaky[row.TestID]}
		if row.Line != nil {
			line := int64(*row.Line)
			view.Line = &line
		}
		resp.Results = append(resp.Results, view)
	}
	if job.ProjectID != nil {
		resp.CanQuarantine = s.canManageQuarantine(ctx, *job.ProjectID)
	}
	return resp, nil
}

func (s *UiService) quarantineService() *testresults.QuarantineService {
	return &testresults.QuarantineService{Store: s.deps.Store, Record: func(ctx context.Context, orgID, action, subjectType, subjectID string, details models.JSONB) {
		s.recordAudit(ctx, orgID, action, subjectType, subjectID, details)
	}}
}

// quarantineCaller resolves the caller for a quarantine read. An anonymous
// caller gets an anonymous identity.
func (s *UiService) quarantineCaller(ctx context.Context) testresults.Caller {
	id, user, principal := s.deps.resolveIdentityDetails(ctx)
	return requiredset.Caller{Identity: id, Actor: principals.FromCredential(principal, user)}
}

// canManageQuarantine reports whether the caller can quarantine the tests of
// the project (for the UI buttons). The operation itself checks again.
func (s *UiService) canManageQuarantine(ctx context.Context, projectID string) bool {
	projects, ok := s.deps.Store.(uiProjectByID)
	if !ok {
		return false
	}
	project, err := projects.GetProjectByID(ctx, projectID)
	if err != nil {
		return false
	}
	caller := s.quarantineCaller(ctx)
	if caller.Identity.Anonymous {
		return false
	}
	return s.quarantineService().MayManage(ctx, caller, project, "") == nil
}

// testProjectScope resolves a visible project.
func (s *UiService) testProjectScope(ctx context.Context, projectID string) (*models.Project, testresults.Caller, error) {
	projects, ok := s.deps.Store.(uiProjectByID)
	if !ok {
		return nil, testresults.Caller{}, NewServiceError("unavailable", "test results are not available")
	}
	project, err := projects.GetProjectByID(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return nil, testresults.Caller{}, NewServiceError("not_found", "project not found")
	}
	caller := s.quarantineCaller(ctx)
	if caller.Identity.Anonymous {
		visible, err := s.deps.Resolver.CanViewProject(ctx, authz.AnonymousIdentity(), project)
		if err != nil || !visible {
			return nil, testresults.Caller{}, NewServiceError("not_found", "project not found")
		}
		return project, caller, nil
	}
	if err := s.quarantineService().MayView(ctx, caller, project); err != nil {
		return nil, testresults.Caller{}, quarantineError(err)
	}
	return project, caller, nil
}

func quarantineError(err error) error {
	code := testresults.ErrorCode(err)
	if code == "internal" {
		return NewServiceError("internal", "the test quarantine operation failed")
	}
	return NewServiceError(code, err.Error())
}

func (s *UiService) ListFlakyTests(ctx context.Context, req csilapi.ListFlakyTestsRequest) (csilapi.ListFlakyTestsResponse, error) {
	project, _, err := s.testProjectScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.ListFlakyTestsResponse{}, err
	}
	window := testresults.FlakyWindowFromEnv()
	if req.Days != nil && *req.Days > 0 && *req.Days <= 3650 {
		window = time.Duration(*req.Days) * 24 * time.Hour
	}
	resp := csilapi.ListFlakyTestsResponse{Tests: []csilapi.FlakyTestView{}, WindowDays: int64(window.Hours() / 24)}
	results, ok := s.deps.Store.(store.TestResultStore)
	if !ok {
		return resp, nil
	}
	rows, err := results.ListProjectFlakyTests(ctx, project.ProjectID, time.Now().UTC().Add(-window), 200)
	if err != nil {
		return csilapi.ListFlakyTestsResponse{}, NewServiceError("internal", "could not list the flaky tests")
	}
	for _, row := range rows {
		view := csilapi.FlakyTestView{TestId: row.TestID, Suite: row.Suite, Classname: row.Classname, Name: row.Name,
			FlakyCount: int64(row.FlakyCount), TotalRuns: int64(row.TotalRuns)}
		if row.LastFlakyAt != nil {
			text := formatTime(*row.LastFlakyAt)
			view.LastFlakyAt = &text
		}
		if row.LastFlakySHA != "" {
			sha := row.LastFlakySHA
			view.LastFlakySha = &sha
		}
		resp.Tests = append(resp.Tests, view)
	}
	return resp, nil
}

func quarantineToWire(q models.TestQuarantine) csilapi.TestQuarantineView {
	view := csilapi.TestQuarantineView{QuarantineId: q.QuarantineID, TestId: q.TestID, Suite: q.Suite, Classname: q.Classname,
		Name: q.Name, OwnerPrincipalId: q.OwnerPrincipalID, Reason: q.Reason, ExpiresAt: formatTime(q.ExpiresAt), CreatedAt: formatTime(q.CreatedAt)}
	if q.CreatedBy != "" {
		value := q.CreatedBy
		view.CreatedBy = &value
	}
	view.RemovedAt = optionalTimeText(q.RemovedAt)
	if q.RemovedBy != "" {
		value := q.RemovedBy
		view.RemovedBy = &value
	}
	return view
}

func (s *UiService) ListTestQuarantines(ctx context.Context, req csilapi.ListTestQuarantinesRequest) (csilapi.ListTestQuarantinesResponse, error) {
	project, caller, err := s.testProjectScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.ListTestQuarantinesResponse{}, err
	}
	st, ok := s.deps.Store.(store.TestQuarantineStore)
	if !ok {
		return csilapi.ListTestQuarantinesResponse{Quarantines: []csilapi.TestQuarantineView{}}, nil
	}
	rows, err := st.ListTestQuarantines(ctx, project.ProjectID, req.IncludeEnded != nil && *req.IncludeEnded, time.Now().UTC())
	if err != nil {
		return csilapi.ListTestQuarantinesResponse{}, NewServiceError("internal", "could not list the quarantines")
	}
	resp := csilapi.ListTestQuarantinesResponse{Quarantines: []csilapi.TestQuarantineView{}}
	for _, row := range rows {
		resp.Quarantines = append(resp.Quarantines, quarantineToWire(row))
	}
	resp.CanManage = !caller.Identity.Anonymous && s.quarantineService().MayManage(ctx, caller, project, "") == nil
	return resp, nil
}

// manageScope resolves a caller with a session or a token, and the project.
func (s *UiService) manageScope(ctx context.Context, projectID string) (context.Context, testresults.Caller, *models.Project, error) {
	ctx, id, user, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return ctx, testresults.Caller{}, nil, err
	}
	projects, ok := s.deps.Store.(uiProjectByID)
	if !ok {
		return ctx, testresults.Caller{}, nil, NewServiceError("unavailable", "test quarantine is not available")
	}
	project, err := projects.GetProjectByID(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return ctx, testresults.Caller{}, nil, NewServiceError("not_found", "project not found")
	}
	actor := principals.FromCredential(checkauth.GetPrincipalFromContext(ctx), user)
	return ctx, requiredset.Caller{Identity: id, Actor: actor}, project, nil
}

func (s *UiService) QuarantineTest(ctx context.Context, req csilapi.QuarantineTestRequest) (csilapi.QuarantineTestResponse, error) {
	ctx, caller, project, err := s.manageScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.QuarantineTestResponse{}, err
	}
	until, err := requiredset.ParseExpiryText(req.Until, time.Now().UTC())
	if err != nil {
		return csilapi.QuarantineTestResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	row, err := s.quarantineService().Quarantine(ctx, caller, project, testresults.QuarantineRequest{
		TestID: textValue(req.TestId), Suite: textValue(req.Suite), Classname: textValue(req.Classname), Name: textValue(req.Name),
		Reason: req.Reason, Until: until, OwnerPrincipalID: textValue(req.OwnerPrincipalId), WorkflowSecurityID: textValue(req.WorkflowSecurityId)})
	if err != nil {
		return csilapi.QuarantineTestResponse{}, quarantineError(err)
	}
	return csilapi.QuarantineTestResponse{Quarantine: quarantineToWire(*row)}, nil
}

func (s *UiService) UnquarantineTest(ctx context.Context, req csilapi.UnquarantineTestRequest) (csilapi.UnquarantineTestResponse, error) {
	ctx, caller, project, err := s.manageScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.UnquarantineTestResponse{}, err
	}
	if err := s.quarantineService().Unquarantine(ctx, caller, project, req.QuarantineId, textValue(req.WorkflowSecurityId)); err != nil {
		return csilapi.UnquarantineTestResponse{}, quarantineError(err)
	}
	return csilapi.UnquarantineTestResponse{Done: true}, nil
}

func (s *UiService) GetJobProvenance(ctx context.Context, req csilapi.GetJobProvenanceRequest) (csilapi.GetJobProvenanceResponse, error) {
	job, err := s.visibleJob(ctx, req.JobId)
	if err != nil {
		return csilapi.GetJobProvenanceResponse{}, err
	}
	service := s.artifactService()
	if service == nil {
		return csilapi.GetJobProvenanceResponse{}, nil
	}
	list, err := service.Store.ListJobArtifacts(ctx, job.JobID)
	if err != nil {
		return csilapi.GetJobProvenanceResponse{}, NewServiceError("internal", "could not read the job artifacts")
	}
	for i := range list {
		if list[i].Kind != models.ArtifactKindProvenance || list[i].Status != models.ArtifactStatusComplete {
			continue
		}
		data, err := service.ReadRange(ctx, &list[i], 0, 4<<20)
		if err != nil {
			return csilapi.GetJobProvenanceResponse{}, NewServiceError("unavailable", "could not read the provenance")
		}
		envelopeText := string(data)
		resp := csilapi.GetJobProvenanceResponse{Present: true, EnvelopeJson: &envelopeText}
		message, statement, kid := s.verifyProvenance(ctx, data)
		if message != "" {
			resp.Error = &message
			return resp, nil
		}
		resp.Verified = true
		resp.KeyId = &kid
		resp.StatementJson = &statement
		return resp, nil
	}
	return csilapi.GetJobProvenanceResponse{Present: false}, nil
}

// verifyProvenance checks an envelope with the provenance keys of this
// coordinator. It returns an error message, or the statement JSON and the
// key ID.
func (s *UiService) verifyProvenance(ctx context.Context, data []byte) (string, string, string) {
	if s.deps.KeyManager == nil {
		return "provenance keys are not configured on this coordinator", "", ""
	}
	signer := provenance.NewSigner(s.deps.Store, s.deps.KeyManager, provenance.BuilderIDFromEnv(config.OIDCIssuerURL))
	if signer == nil {
		return "provenance keys are not configured on this coordinator", "", ""
	}
	keys, err := signer.JWKS(ctx)
	if err != nil {
		return "the provenance keys are not available", "", ""
	}
	envelope, err := provenance.ParseEnvelope(data)
	if err != nil {
		return err.Error(), "", ""
	}
	payload, kid, err := provenance.Verify(envelope, keys)
	if err != nil {
		return err.Error(), "", ""
	}
	statement, err := provenance.ParseStatement(envelope.PayloadType, payload)
	if err != nil {
		return err.Error(), "", ""
	}
	pretty, err := json.MarshalIndent(statement, "", "  ")
	if err != nil {
		return "the statement could not be shown", "", ""
	}
	return "", string(pretty), kid
}
