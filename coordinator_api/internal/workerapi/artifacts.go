package workerapi

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/artifacts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/oidcissuer"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/provenance"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/testreports"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/testresults"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// WP15: artifact uploads, test report ingestion, and provenance
// (docs/artifacts-and-tests.md, docs/provenance.md). The worker uploads the
// files of a job through this service; it never connects to the object
// store.

// artifactService returns the artifact service of the coordinator, or nil
// when the store or the object store cannot hold artifacts.
func (d *Deps) artifactService() *artifacts.Service {
	if d == nil || d.ObjectStore == nil {
		return nil
	}
	st, ok := d.Store.(store.ArtifactStore)
	if !ok {
		return nil
	}
	return &artifacts.Service{Store: st, Objects: d.ObjectStore, Limits: artifacts.LimitsFromEnv()}
}

// withArtifactLease adds the artifact limits and the quarantined tests of
// the job to a lease.
func (d *Deps) withArtifactLease(ctx context.Context, job *models.Job, lease *csilapi.Lease) *csilapi.Lease {
	if lease == nil {
		return nil
	}
	if service := d.artifactService(); service != nil && service.Limits.Enabled {
		limits := service.Limits
		lease.Artifacts = &csilapi.ArtifactLease{MaxFileBytes: limits.MaxFileBytes, MaxJobBytes: limits.MaxJobBytes,
			MaxFiles: int64(limits.MaxFiles), ChunkBytes: int64(limits.ChunkBytes)}
	}
	quarantines, ok := d.Store.(store.TestQuarantineStore)
	if !ok || job.ProjectID == nil || *job.ProjectID == "" {
		return lease
	}
	rows, err := quarantines.ListTestQuarantines(ctx, *job.ProjectID, false, time.Now().UTC())
	if err != nil {
		logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to read the test quarantines for a lease")
		return lease
	}
	tests := make([]csilapi.QuarantinedTest, 0, len(rows))
	for _, row := range rows {
		tests = append(tests, csilapi.QuarantinedTest{TestId: row.TestID, Suite: row.Suite, Classname: row.Classname,
			Name: row.Name, ExpiresAt: row.ExpiresAt.UTC().Format(time.RFC3339)})
	}
	lease.QuarantinedTests = tests
	return lease
}

// artifactNamespace is the artifact namespace of the job (WP14 cache
// decision). A failure uses the default namespace.
func (d *Deps) artifactNamespace(ctx context.Context, job *models.Job) string {
	decision, err := d.cacheDecision(ctx, job)
	if err != nil {
		return artifacts.DefaultNamespace
	}
	return artifacts.ValidateNamespace(decision.ArtifactNamespace)
}

// UploadArtifact stores one chunk of one artifact of a leased job.
func (s *WorkerService) UploadArtifact(ctx context.Context, req csilapi.UploadArtifactRequest) (csilapi.UploadArtifactResponse, error) {
	wkr, _, err := s.resolveSession(ctx)
	if err != nil {
		return csilapi.UploadArtifactResponse{}, err
	}
	lease, err := s.deps.Store.GetWorkerLeaseByID(ctx, req.LeaseId)
	if err != nil {
		return csilapi.UploadArtifactResponse{}, leaseLookupError(err, "lease not found for this worker")
	}
	if lease.WorkerID != wkr.WorkerID {
		return csilapi.UploadArtifactResponse{}, uiapi.NewServiceError("not_found", "lease not found for this worker")
	}
	if !lease.IsActive() {
		return csilapi.UploadArtifactResponse{}, uiapi.NewServiceError("conflict", "lease is no longer active")
	}
	job, err := s.deps.Store.GetJobByID(ctx, lease.JobID)
	if err != nil {
		return csilapi.UploadArtifactResponse{}, leaseLookupError(err, "job not found for this lease")
	}
	if !leaseOwnsJob(lease, job) {
		return csilapi.UploadArtifactResponse{}, uiapi.NewServiceError("conflict", "lease is no longer active")
	}
	switch job.Status {
	case "scheduling", "running", "cancelling":
	default:
		return csilapi.UploadArtifactResponse{}, uiapi.NewServiceError("conflict", "the job is finished; upload artifacts before the result")
	}
	service := s.deps.artifactService()
	chunk := artifacts.ChunkRequest{Path: req.Path, Offset: req.Offset, Data: req.Data, Final: req.Final}
	if req.MediaType != nil {
		chunk.MediaType = *req.MediaType
	}
	if req.Kind != nil {
		chunk.Kind = *req.Kind
	}
	artifact, err := service.AppendChunk(ctx, job, s.deps.artifactNamespace(ctx, job), chunk)
	if err != nil {
		code := artifacts.ErrorCode(err)
		if code == "too_large" {
			// A final answer: the worker does not retry it.
			code = "invalid_argument"
		}
		if code == "internal" {
			logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to store an artifact chunk")
		}
		return csilapi.UploadArtifactResponse{}, uiapi.NewServiceError(code, err.Error())
	}
	resp := csilapi.UploadArtifactResponse{Ok: true, ArtifactId: artifact.ArtifactID, Size: artifact.SizeBytes}
	if artifact.SHA256 != "" {
		digest := artifact.SHA256
		resp.Sha256 = &digest
	}
	return resp, nil
}

type outputDeclarations struct {
	TestReports []string `json:"test_reports"`
}

// declaredTestReports reads `test_reports` of the workflow output.
func declaredTestReports(workflowOutput *string) []string {
	if workflowOutput == nil || *workflowOutput == "" {
		return nil
	}
	var doc outputDeclarations
	if err := json.Unmarshal([]byte(*workflowOutput), &doc); err != nil {
		return nil
	}
	if len(doc.TestReports) > 1000 {
		doc.TestReports = doc.TestReports[:1000]
	}
	return doc.TestReports
}

type nodeByJob interface {
	GetWorkflowNodeByJobID(ctx context.Context, jobID string) (*models.WorkflowNode, error)
}

// nodeQuarantinePolicy is the quarantine_policy of the workflow node of
// the job ("" for none or for a job that is not a node).
func (d *Deps) nodeQuarantinePolicy(ctx context.Context, job *models.Job) string {
	nodes, ok := d.Store.(nodeByJob)
	if !ok || job.WorkflowID == nil || *job.WorkflowID == "" {
		return ""
	}
	node, err := nodes.GetWorkflowNodeByJobID(ctx, job.JobID)
	if err != nil || node == nil || node.JobSpec == nil {
		return ""
	}
	value, _ := node.JobSpec["quarantine_policy"].(string)
	if !models.ValidQuarantinePolicy(value) {
		return ""
	}
	return value
}

var provenanceSigners sync.Map // *Deps -> *oidcissuer.Issuer

// provenanceWriter returns the provenance writer, or nil when provenance
// is off (no builder ID or no master key).
func (d *Deps) provenanceWriter(service *artifacts.Service) *provenance.Writer {
	if d == nil || d.KeyManager == nil || service == nil {
		return nil
	}
	facts, ok := d.Store.(oidcissuer.FactsStore)
	if !ok {
		return nil
	}
	var signer *oidcissuer.Issuer
	if cached, found := provenanceSigners.Load(d); found {
		signer, _ = cached.(*oidcissuer.Issuer)
	} else {
		signer = provenance.NewSigner(d.Store, d.KeyManager, provenance.BuilderIDFromEnv(config.OIDCIssuerURL))
		provenanceSigners.Store(d, signer)
	}
	if signer == nil {
		return nil
	}
	return &provenance.Writer{Signer: signer, Facts: facts, Artifacts: service}
}

// processJobOutputs closes the artifacts of a finished job, ingests its
// test reports, and writes its provenance. It runs after the job status is
// final and before the workflow advances, so that the workflow node and the
// PR comment see the test summary. Every step is best effort: a failure is
// logged and never changes the job result.
func (s *WorkerService) processJobOutputs(ctx context.Context, lease *models.WorkerLease, job *models.Job, workflowOutput *string) {
	service := s.deps.artifactService()
	if service == nil {
		return
	}
	logger := logging.Log.WithField("job_id", job.JobID)
	workCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	list, err := service.Finalize(workCtx, job.JobID, declaredTestReports(workflowOutput))
	if err != nil {
		logger.WithError(err).Warn("Failed to close the job artifacts")
		return
	}
	if results, ok := s.deps.Store.(store.TestResultStore); ok {
		masker := secrets.NewMasker()
		masker.RegisterSecrets(s.secrets.get(lease.LeaseID))
		ingestor := &testresults.Ingestor{Results: results, Artifacts: service, Limits: testreports.DefaultLimits,
			FlakyWindow: testresults.FlakyWindowFromEnv(), Mask: masker.MaskString}
		if workflowOutput != nil {
			ingestor.TestExit = testresults.DeclaredTestExit([]byte(*workflowOutput))
		}
		if quarantines, ok := s.deps.Store.(store.TestQuarantineStore); ok {
			ingestor.Quarantines = quarantines
		}
		if _, err := ingestor.Ingest(workCtx, job, list, s.deps.nodeQuarantinePolicy(workCtx, job)); err != nil {
			logger.WithError(err).Warn("Failed to ingest the job test reports")
		}
	}
	if job.Status != "completed" {
		return
	}
	writer := s.deps.provenanceWriter(service)
	if writer == nil {
		return
	}
	output := []byte{}
	if workflowOutput != nil {
		output = []byte(*workflowOutput)
	}
	if _, err := writer.Write(workCtx, job, list, output, s.deps.artifactNamespace(workCtx, job)); err != nil && !errors.Is(err, provenance.ErrNoSubjects) {
		logger.WithError(err).Warn("Failed to write the job provenance")
	}
}
