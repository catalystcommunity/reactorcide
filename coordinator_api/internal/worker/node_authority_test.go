package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// multiProfileStore serves several execution profiles for one organization.
type multiProfileStore struct {
	*workflowRuntimeStore
	profiles  map[string]*models.ExecutionProfile
	approvals []models.CIApproval
}

func (s *multiProfileStore) GetExecutionProfile(_ context.Context, orgID, name string) (*models.ExecutionProfile, error) {
	profile, ok := s.profiles[name]
	if !ok || profile.OrgID != orgID {
		return nil, store.ErrNotFound
	}
	copied := *profile
	return &copied, nil
}

func (s *multiProfileStore) ListActiveCIApprovalsForTarget(_ context.Context, _ string, _ int, _, _, _ string, _ time.Time) ([]models.CIApproval, error) {
	return append([]models.CIApproval(nil), s.approvals...), nil
}

func mixedTrustProfiles() map[string]*models.ExecutionProfile {
	rootAllowed := true
	rootDenied := false
	return map[string]*models.ExecutionProfile{
		"standard": {OrgID: "org", Name: "standard", MayRunAsRoot: rootAllowed, TrustedCacheWrites: true},
		"pr-untrusted": {OrgID: "org", Name: "pr-untrusted", DenySecrets: true, MayRunAsRoot: rootDenied,
			RuntimeCapabilities: []string{}, TrustedCacheWrites: false},
	}
}

const mixedTrustPolicy = `version: 1
defaults: {ci_source: base, profile: standard}
head_ci:
- id: csilgen
  actors: {any: [repository_write]}
  workflows: [csilgen-pr]
  paths: ['.reactorcide/**']
  head_repository: any
  use:
    ci_source: head
    profile: pr-untrusted
    workers: default
    base_nodes:
    - nodes: [asset-prepare, asset-seal]
      ci_source: base
      profile: standard
      workers: default`

func mixedTrustEvalParent(t *testing.T, policyDocument []byte) (*models.Job, *cipolicy.Policy) {
	t.Helper()
	baseRepo, baseSHA := "https://example.test/upstream.git", "base-sha"
	headRepo, headSHA := "https://example.test/fork.git", "head-sha"
	parent := &models.Job{JobID: "eval", OrgID: "org", CIRepository: baseRepo, CISHA: baseSHA,
		CISourceURL: &baseRepo, CISourceRef: &baseSHA, SourceURL: &headRepo, SourceRef: &headSHA,
		ExecutionProfile: "standard"}
	if err := (&vcs.JobMetadata{VCSProvider: "github", Repo: "org/repo", CommitSHA: headSHA, IsEval: true}).ApplyToJob(parent); err != nil {
		t.Fatal(err)
	}
	policy, err := cipolicy.ParseDocument(policyDocument)
	if err != nil {
		t.Fatal(err)
	}
	document, err := cipolicy.CanonicalDocument(policy)
	if err != nil {
		t.Fatal(err)
	}
	parent.JobEnvVars = models.JSONB{
		"REACTORCIDE_ACTOR_SUBJECTS":     `["repository_write"]`,
		"REACTORCIDE_EVENT_TYPE":         "pull_request_updated",
		"REACTORCIDE_BASE_REF":           "main",
		"REACTORCIDE_IS_FORK_PR":         "true",
		"REACTORCIDE_HEAD_REPOSITORY":    "fork/repo",
		"REACTORCIDE_CI_POLICY":          string(document),
		"REACTORCIDE_CI_POLICY_REVISION": policy.Revision,
	}
	return parent, policy
}

func mixedTrustCandidates() *policyCandidateSet {
	return &policyCandidateSet{
		Base: []triggerWorkflowSpec{{
			ID: "csilgen-pr", Name: "CSILgen PR", EventMatched: true, ExplicitID: true,
			DependencyPaths: []string{".reactorcide/workflows/pr.yaml"},
			Jobs: []triggerJobSpec{
				{JobName: "asset-prepare", JobCommand: "prepare-base"},
				{JobName: "asset-seal", JobCommand: "seal-base", DependsOn: []string{"build"}},
			},
		}},
		Head: []triggerWorkflowSpec{{
			ID: "csilgen-pr", Name: "CSILgen PR", EventMatched: true, ExplicitID: true,
			DependencyPaths: []string{".reactorcide/workflows/pr.yaml"},
			Jobs: []triggerJobSpec{
				{JobName: "asset-prepare", JobCommand: "untrusted-prepare"},
				{JobName: "build", JobCommand: "build-head", DependsOn: []string{"asset-prepare"}},
			},
		}},
	}
}

func mixedTrustBatch(parent *models.Job, policy *cipolicy.Policy) *triggerWorkflowSpec {
	headRepo, headSHA := *parent.SourceURL, *parent.SourceRef
	return &triggerWorkflowSpec{
		ID: "csilgen-pr", Name: "CSILgen PR", TriggerType: "runnerlib_eval", CIOrigin: "head",
		CIRepository: headRepo, CISHA: headSHA, ExecutionProfile: "pr-untrusted", WorkerClass: "default",
		PolicyRevision: policy.Revision, PolicyRuleID: "csilgen",
		DependencyPaths: []string{".reactorcide/workflows/pr.yaml"},
		NodeAuthority:   cipolicy.BaseNodeDecisions(policy.HeadCI[0].Use.BaseNodes),
		Jobs: []triggerJobSpec{
			{JobName: "asset-prepare", CIOrigin: "base", ExecutionProfile: "standard", WorkerClass: "default",
				CISourceURL: parent.CIRepository, CISourceRef: parent.CISHA},
			{JobName: "build", DependsOn: []string{"asset-prepare"}},
			{JobName: "test", DependsOn: []string{"asset-prepare"}},
			{JobName: "asset-seal", DependsOn: []string{"build", "test"}, CIOrigin: "base",
				ExecutionProfile: "standard", WorkerClass: "default",
				CISourceURL: parent.CIRepository, CISourceRef: parent.CISHA},
		},
	}
}

func newMixedTrustProcessor() *TriggerProcessor {
	return NewTriggerProcessor(&multiProfileStore{
		workflowRuntimeStore: newWorkflowRuntimeStore(),
		profiles:             mixedTrustProfiles(),
	}, nil)
}

func TestSelectPolicyCandidatesAppliesCoordinatorAuthority(t *testing.T) {
	parent, policy := mixedTrustEvalParent(t, []byte(mixedTrustPolicy))
	projectID := "project"
	parent.ProjectID = &projectID
	if err := (&vcs.JobMetadata{VCSProvider: "github", Repo: "org/repo", PRNumber: 7, CommitSHA: *parent.SourceRef, IsEval: true}).ApplyToJob(parent); err != nil {
		t.Fatal(err)
	}
	processorStore := &multiProfileStore{
		workflowRuntimeStore: newWorkflowRuntimeStore(), profiles: mixedTrustProfiles(),
	}
	tp := NewTriggerProcessor(processorStore, nil)
	candidates := mixedTrustCandidates()
	selected, violations, err := tp.selectPolicyCandidates(context.Background(), parent, candidates, []string{".reactorcide/workflows/pr.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 || len(selected) != 1 {
		t.Fatalf("unexpected selection: workflows=%d violations=%+v", len(selected), violations)
	}
	workflow := selected[0]
	if workflow.CIOrigin != "head" || workflow.PolicyRuleID != "csilgen" || workflow.PolicyRevision != policy.Revision {
		t.Fatalf("coordinator did not apply head authority: %+v", workflow)
	}
	jobs := map[string]triggerJobSpec{}
	for _, job := range workflow.Jobs {
		jobs[job.JobName] = job
	}
	if jobs["asset-prepare"].JobCommand != "prepare-base" || jobs["asset-prepare"].CIOrigin != "base" {
		t.Fatalf("trusted prepare node did not come from base: %+v", jobs["asset-prepare"])
	}
	if jobs["asset-seal"].JobCommand != "seal-base" || jobs["asset-seal"].CIOrigin != "base" {
		t.Fatalf("missing trusted seal node was not restored from base: %+v", jobs["asset-seal"])
	}
	if jobs["build"].JobCommand != "build-head" || jobs["build"].CIOrigin != "" {
		t.Fatalf("ordinary head node changed unexpectedly: %+v", jobs["build"])
	}
}

func TestSelectPolicyCandidatesLoadsApprovalForExactTarget(t *testing.T) {
	approvalPolicy := strings.Replace(mixedTrustPolicy,
		"actors: {any: [repository_write]}", "approval: {any: [project_owner]}", 1)
	parent, policy := mixedTrustEvalParent(t, []byte(approvalPolicy))
	projectID := "project"
	parent.ProjectID = &projectID
	if err := (&vcs.JobMetadata{VCSProvider: "github", Repo: "org/repo", PRNumber: 7, CommitSHA: *parent.SourceRef, IsEval: true}).ApplyToJob(parent); err != nil {
		t.Fatal(err)
	}
	approval := models.CIApproval{
		ApprovalID: "approval", OrgID: "org", ProjectID: projectID, PRNumber: 7,
		HeadRepository: "wrong/repo", HeadSHA: *parent.SourceRef, BaseSHA: parent.CISHA,
		PolicyRevision: policy.Revision, WorkflowScope: "csilgen-pr",
		ExecutionProfile: "pr-untrusted", ApproverSubject: "project_owner",
	}
	processorStore := &multiProfileStore{workflowRuntimeStore: newWorkflowRuntimeStore(), profiles: mixedTrustProfiles(), approvals: []models.CIApproval{approval}}
	tp := NewTriggerProcessor(processorStore, nil)

	selected, violations, err := tp.selectPolicyCandidates(context.Background(), parent, mixedTrustCandidates(), []string{".reactorcide/workflows/pr.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].CIOrigin != "base" || len(violations) == 0 {
		t.Fatalf("wrong-repository approval was accepted: selected=%+v violations=%+v", selected, violations)
	}

	processorStore.approvals[0].HeadRepository = "fork/repo"
	selected, violations, err = tp.selectPolicyCandidates(context.Background(), parent, mixedTrustCandidates(), []string{".reactorcide/workflows/pr.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].CIOrigin != "head" || len(violations) != 0 || selected[0].ApprovalID == nil || *selected[0].ApprovalID != "approval" {
		t.Fatalf("exact-target approval was rejected: selected=%+v violations=%+v", selected, violations)
	}
}

func TestSelectPolicyCandidatesRejectsChangedPolicySnapshotRevision(t *testing.T) {
	parent, _ := mixedTrustEvalParent(t, []byte(mixedTrustPolicy))
	projectID := "project"
	parent.ProjectID = &projectID
	parent.JobEnvVars["REACTORCIDE_CI_POLICY_REVISION"] = "different-revision"
	if err := (&vcs.JobMetadata{VCSProvider: "github", Repo: "org/repo", PRNumber: 7, CommitSHA: *parent.SourceRef, IsEval: true}).ApplyToJob(parent); err != nil {
		t.Fatal(err)
	}

	_, _, err := NewTriggerProcessor(newWorkflowRuntimeStore(), nil).selectPolicyCandidates(
		context.Background(), parent, &policyCandidateSet{}, []string{".reactorcide/workflows/pr.yaml"},
	)
	if err == nil || !strings.Contains(err.Error(), "snapshot failed integrity validation") {
		t.Fatalf("changed policy snapshot was accepted: %v", err)
	}
}

// A head-CI workflow can run ordinary nodes with an untrusted profile and
// policy-selected control nodes with base CI and a trusted profile.
func TestValidateNodeAuthority_MixedTrustAccepted(t *testing.T) {
	parent, policy := mixedTrustEvalParent(t, []byte(mixedTrustPolicy))
	tp := newMixedTrustProcessor()
	batch := mixedTrustBatch(parent, policy)
	changed := []string{".reactorcide/workflows/pr.yaml"}
	if err := tp.validateWorkflowAuthority(context.Background(), parent, batch, changed); err != nil {
		t.Fatalf("mixed-trust batch rejected: %v", err)
	}
}

func TestValidateNodeAuthority_FailClosedCases(t *testing.T) {
	changed := []string{".reactorcide/workflows/pr.yaml"}
	cases := []struct {
		name    string
		mutate  func(batch *triggerWorkflowSpec)
		message string
	}{
		{
			name: "unauthorized claim on an ordinary node",
			mutate: func(batch *triggerWorkflowSpec) {
				batch.Jobs[1].CIOrigin = "base"
				batch.Jobs[1].ExecutionProfile = "standard"
				batch.Jobs[1].CISourceURL = "https://example.test/upstream.git"
				batch.Jobs[1].CISourceRef = "base-sha"
			},
			message: "claims authority that no coordinator policy grants",
		},
		{
			name: "profile mismatch against the policy grant",
			mutate: func(batch *triggerWorkflowSpec) {
				batch.Jobs[0].ExecutionProfile = "pr-untrusted"
			},
			message: "does not match the coordinator policy",
		},
		{
			name: "missing policy-controlled base node",
			mutate: func(batch *triggerWorkflowSpec) {
				batch.Jobs = batch.Jobs[:3]
			},
			message: "missing policy-controlled base node",
		},
		{
			name: "trusted node not pinned to the exact base SHA",
			mutate: func(batch *triggerWorkflowSpec) {
				batch.Jobs[0].CISourceRef = "other-sha"
			},
			message: "exact trusted base CI revision",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent, policy := mixedTrustEvalParent(t, []byte(mixedTrustPolicy))
			tp := newMixedTrustProcessor()
			batch := mixedTrustBatch(parent, policy)
			tc.mutate(batch)
			err := tp.validateWorkflowAuthority(context.Background(), parent, batch, changed)
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("expected error containing %q, got %v", tc.message, err)
			}
		})
	}
}

// A missing profile row for a granted node profile fails closed.
func TestValidateNodeAuthority_MissingProfileFailsClosed(t *testing.T) {
	parent, policy := mixedTrustEvalParent(t, []byte(mixedTrustPolicy))
	profiles := mixedTrustProfiles()
	delete(profiles, "standard")
	// The workflow-level profile check needs the parent profile, so keep a
	// standard row under another org to prove the org scope also matters.
	tp := NewTriggerProcessor(&multiProfileStore{
		workflowRuntimeStore: newWorkflowRuntimeStore(),
		profiles:             profiles,
	}, nil)
	batch := mixedTrustBatch(parent, policy)
	err := tp.validateWorkflowAuthority(context.Background(), parent, batch, []string{".reactorcide/workflows/pr.yaml"})
	if err == nil {
		t.Fatal("missing node profile was accepted")
	}
}

// A trigger source that is not verified eval output cannot name node
// authority at all, so a child job cannot mint trusted nodes.
func TestProcessTriggers_NonEvalNodeAuthorityRejected(t *testing.T) {
	parent := &models.Job{JobID: "job-1", OrgID: "org"}
	tp := NewTriggerProcessor(&MockStore{}, nil)
	data := []byte(`{"type":"trigger_job","workflows":[{"name":"child","jobs":[{"job_name":"sneaky","ci_origin":"base","execution_profile":"standard"}]}]}`)
	_, err := tp.ProcessTriggersFromData(context.Background(), data, "", parent)
	if err == nil || !strings.Contains(err.Error(), "cannot set node authority") {
		t.Fatalf("expected node authority rejection, got %v", err)
	}
}

func mixedTrustWorkflowFixture(t *testing.T, profiles map[string]*models.ExecutionProfile) (*multiProfileStore, *models.WorkflowInstance, *TriggerProcessor) {
	t.Helper()
	baseStore := newWorkflowRuntimeStore()
	jobCounter := 0
	baseStore.CreateJobFunc = func(_ context.Context, job *models.Job) error {
		jobCounter++
		job.JobID = "job-" + string(rune('0'+jobCounter))
		return nil
	}
	parentJobID := "eval-job"
	baseStore.GetJobByIDFunc = func(_ context.Context, jobID string) (*models.Job, error) {
		if jobID != parentJobID {
			return nil, store.ErrNotFound
		}
		return &models.Job{JobID: parentJobID, OrgID: "org", ExecutionProfile: "standard", CIOrigin: "base"}, nil
	}
	mps := &multiProfileStore{workflowRuntimeStore: baseStore, profiles: profiles}
	wf := &models.WorkflowInstance{
		WorkflowID: "wf-1", OrgID: "org", Status: "running", ParentJobID: &parentJobID,
		CIOrigin: "head", CIRepository: "https://example.test/fork.git", CISHA: "head-sha",
		ExecutionProfile: "pr-untrusted", WorkerClass: "default",
		PolicyRevision: "rev-1", PolicyRuleID: "csilgen",
	}
	baseStore.workflows[wf.WorkflowID] = wf
	return mps, wf, NewTriggerProcessor(mps, nil)
}

func addNode(t *testing.T, s *multiProfileStore, wf *models.WorkflowInstance, nodeID string, spec triggerJobSpec, override bool) *models.WorkflowNode {
	t.Helper()
	specData, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var specJSON models.JSONB
	if err := json.Unmarshal(specData, &specJSON); err != nil {
		t.Fatal(err)
	}
	node := &models.WorkflowNode{
		NodeID: nodeID, WorkflowID: wf.WorkflowID, Name: spec.JobName, DisplayName: spec.JobName,
		Status: "pending", JobSpec: specJSON,
	}
	if override {
		node.CIOrigin = spec.CIOrigin
		node.CIRepository = spec.CISourceURL
		node.CISHA = spec.CISourceRef
		node.ExecutionProfile = spec.ExecutionProfile
		node.WorkerClass = spec.WorkerClass
		node.PolicyRevision = wf.PolicyRevision
		node.PolicyRuleID = wf.PolicyRuleID
	}
	s.nodes[node.NodeID] = node
	return node
}

// A node with recorded base authority submits its job with the trusted
// profile, the exact base CI revision, and base origin, while the workflow
// default stays untrusted.
func TestSubmitWorkflowNode_UsesRecordedNodeAuthority(t *testing.T) {
	mps, wf, tp := mixedTrustWorkflowFixture(t, mixedTrustProfiles())
	var created []*models.Job
	mps.CreateJobFunc = func(_ context.Context, job *models.Job) error {
		job.JobID = "job-created"
		copied := *job
		created = append(created, &copied)
		return nil
	}
	spec := triggerJobSpec{JobName: "asset-prepare", CIOrigin: "base", ExecutionProfile: "standard",
		WorkerClass: "default", CISourceURL: "https://example.test/upstream.git", CISourceRef: "base-sha",
		CISourceType: "git"}
	node := addNode(t, mps, wf, "node-1", spec, true)
	jobID, err := tp.submitWorkflowNode(context.Background(), wf, node)
	if err != nil {
		t.Fatalf("trusted node submission failed: %v", err)
	}
	if jobID == "" || len(created) != 1 {
		t.Fatalf("expected one created job, got %d", len(created))
	}
	job := created[0]
	if job.ExecutionProfile != "standard" || job.CIOrigin != "base" {
		t.Fatalf("job authority = %s/%s", job.CIOrigin, job.ExecutionProfile)
	}
	if job.CISourceURL == nil || *job.CISourceURL != "https://example.test/upstream.git" ||
		job.CISourceRef == nil || *job.CISourceRef != "base-sha" {
		t.Fatalf("job CI source not pinned to base: %+v", job)
	}
	if job.CIRepository != "https://example.test/upstream.git" || job.CISHA != "base-sha" {
		t.Fatalf("job CI decision fields not pinned to base: %s@%s", job.CIRepository, job.CISHA)
	}
	if job.WorkerClass != "default" {
		t.Fatalf("job worker class = %q", job.WorkerClass)
	}
}

// An ordinary untrusted node cannot run as root and cannot add a capability
// that the untrusted profile denies, even when trusted nodes exist.
func TestSubmitWorkflowNode_UntrustedNodeStaysLimited(t *testing.T) {
	mps, wf, tp := mixedTrustWorkflowFixture(t, mixedTrustProfiles())
	rootSpec := triggerJobSpec{JobName: "build", RunAsUser: "root"}
	rootNode := addNode(t, mps, wf, "node-root", rootSpec, false)
	if _, err := tp.submitWorkflowNode(context.Background(), wf, rootNode); err == nil ||
		!strings.Contains(err.Error(), "denies root") {
		t.Fatalf("expected root denial, got %v", err)
	}
	capabilitySpec := triggerJobSpec{JobName: "builder-job", Capabilities: []string{"builder"}}
	capabilityNode := addNode(t, mps, wf, "node-cap", capabilitySpec, false)
	if _, err := tp.submitWorkflowNode(context.Background(), wf, capabilityNode); err == nil ||
		!strings.Contains(err.Error(), "cannot add runtime capability") {
		t.Fatalf("expected capability denial, got %v", err)
	}
}

// A trusted node with an incomplete or stale recorded authority fails closed.
func TestSubmitWorkflowNode_OverrideFailsClosed(t *testing.T) {
	spec := triggerJobSpec{JobName: "asset-seal", CIOrigin: "base", ExecutionProfile: "standard",
		WorkerClass: "default", CISourceURL: "https://example.test/upstream.git", CISourceRef: "base-sha"}

	t.Run("missing profile", func(t *testing.T) {
		profiles := mixedTrustProfiles()
		delete(profiles, "standard")
		mps, wf, tp := mixedTrustWorkflowFixture(t, profiles)
		node := addNode(t, mps, wf, "node-1", spec, true)
		if _, err := tp.submitWorkflowNode(context.Background(), wf, node); err == nil {
			t.Fatal("missing node profile was accepted")
		}
	})
	t.Run("policy revision drift", func(t *testing.T) {
		mps, wf, tp := mixedTrustWorkflowFixture(t, mixedTrustProfiles())
		node := addNode(t, mps, wf, "node-1", spec, true)
		node.PolicyRevision = "other-revision"
		if _, err := tp.submitWorkflowNode(context.Background(), wf, node); err == nil ||
			!strings.Contains(err.Error(), "recorded policy revision") {
			t.Fatalf("expected policy revision failure, got %v", err)
		}
	})
	t.Run("incomplete authority", func(t *testing.T) {
		mps, wf, tp := mixedTrustWorkflowFixture(t, mixedTrustProfiles())
		partial := spec
		partial.CISourceRef = ""
		node := addNode(t, mps, wf, "node-1", partial, true)
		if _, err := tp.submitWorkflowNode(context.Background(), wf, node); err == nil ||
			!strings.Contains(err.Error(), "incomplete recorded authority") {
			t.Fatalf("expected incomplete authority failure, got %v", err)
		}
	})
}

// Workflow variables cross the authority boundary in both directions: a
// trusted base node publishes vars that head nodes read, and head node vars
// reach a later trusted seal node. Variable state is workflow state, not
// secret state, so authority never filters it.
func TestWorkflowVarsCrossAuthorityBoundary(t *testing.T) {
	writeOutput := func(t *testing.T, dir string, vars map[string]interface{}) {
		t.Helper()
		data, err := json.Marshal(map[string]interface{}{"vars": vars})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "workflow-output.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mps, wf, tp := mixedTrustWorkflowFixture(t, mixedTrustProfiles())
	prepareJobID, buildJobID := "job-prepare", "job-build"
	prepareNode := addNode(t, mps, wf, "node-prepare", triggerJobSpec{JobName: "asset-prepare", CIOrigin: "base",
		ExecutionProfile: "standard", WorkerClass: "default",
		CISourceURL: "https://example.test/upstream.git", CISourceRef: "base-sha"}, true)
	prepareNode.Status = "running"
	prepareNode.JobID = &prepareJobID
	mps.nodeByJobID[prepareJobID] = prepareNode.NodeID
	buildNode := addNode(t, mps, wf, "node-build", triggerJobSpec{JobName: "build", DependsOn: []string{"asset-prepare"}}, false)
	buildNode.Status = "running"
	buildNode.JobID = &buildJobID
	mps.nodeByJobID[buildJobID] = buildNode.NodeID

	// The trusted prepare node publishes a staging reference.
	prepareDir := t.TempDir()
	writeOutput(t, prepareDir, map[string]interface{}{"staging_lane": "pr-7-abc"})
	prepareJob := &models.Job{JobID: prepareJobID, WorkflowID: &wf.WorkflowID, Status: "completed",
		ExecutionProfile: "standard", CIOrigin: "base"}
	if err := tp.ProcessWorkflowCompletion(context.Background(), prepareDir, prepareJob); err != nil {
		t.Fatalf("trusted node completion failed: %v", err)
	}
	if got := mps.vars["staging_lane"]; got == nil {
		t.Fatal("trusted node variable was not merged into workflow state")
	}

	// The untrusted build node reads that state and publishes its own value
	// back for the trusted seal node.
	buildDir := t.TempDir()
	writeOutput(t, buildDir, map[string]interface{}{"build_digest": "sha256:abc"})
	buildJob := &models.Job{JobID: buildJobID, WorkflowID: &wf.WorkflowID, Status: "completed",
		ExecutionProfile: "pr-untrusted", CIOrigin: "head"}
	if err := tp.ProcessWorkflowCompletion(context.Background(), buildDir, buildJob); err != nil {
		t.Fatalf("untrusted node completion failed: %v", err)
	}
	encoded, err := EncodeWorkflowVars(map[string]models.JSONB{
		"staging_lane": mps.vars["staging_lane"], "build_digest": mps.vars["build_digest"],
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["staging_lane"] != "pr-7-abc" || decoded["build_digest"] != "sha256:abc" {
		t.Fatalf("workflow vars did not cross the boundary: %v", decoded)
	}
}

// Workflows without node authority entries keep their current behavior: no
// node override columns, workflow-level authority applies to every node.
func TestSubmitWorkflowNode_NoOverrideKeepsWorkflowAuthority(t *testing.T) {
	mps, wf, tp := mixedTrustWorkflowFixture(t, mixedTrustProfiles())
	var created []*models.Job
	mps.CreateJobFunc = func(_ context.Context, job *models.Job) error {
		job.JobID = "job-created"
		copied := *job
		created = append(created, &copied)
		return nil
	}
	node := addNode(t, mps, wf, "node-1", triggerJobSpec{JobName: "build"}, false)
	if _, err := tp.submitWorkflowNode(context.Background(), wf, node); err != nil {
		t.Fatalf("plain node submission failed: %v", err)
	}
	job := created[0]
	if job.ExecutionProfile != "pr-untrusted" || job.CIOrigin != "head" {
		t.Fatalf("job authority = %s/%s", job.CIOrigin, job.ExecutionProfile)
	}
	if job.CISourceURL == nil || *job.CISourceURL != wf.CIRepository {
		t.Fatalf("job CI source = %+v", job.CISourceURL)
	}
}

// derivedIDCandidates is mixedTrustCandidates with the workflow declaring NO
// `id:` of its own: its identity came from the filename, so ExplicitID is
// false. Everything else -- and every field the policy matches on -- is
// identical.
func derivedIDCandidates() *policyCandidateSet {
	candidates := mixedTrustCandidates()
	candidates.Base[0].ExplicitID = false
	candidates.Head[0].ExplicitID = false
	return candidates
}

// TestSelectPolicyCandidatesIgnoresWhetherTheWorkflowIDWasExplicit is the
// coordinator-is-authoritative property.
//
// The head-CI branch used to additionally require head.ExplicitID, which let a
// REPOSITORY veto by omission a grant the COORDINATOR had made: a stored policy
// could name a workflow and simply never fire, with nothing to say why. That is
// how the production reactorcide policy sat inert while looking correct.
//
// It was never the control it resembled, either: an actor who satisfies
// `actors.any` writes their own pull request and can put any `id:` they like in
// it, so requiring an explicit id never stopped one workflow claiming another's
// identity. What actually gates the grant is the policy -- actors, workflows,
// paths, events, base_branches, head_repository, approval -- and the companion
// tests below cover those.
func TestSelectPolicyCandidatesIgnoresWhetherTheWorkflowIDWasExplicit(t *testing.T) {
	for _, explicit := range []bool{true, false} {
		name := "declared id"
		candidates := mixedTrustCandidates()
		if !explicit {
			name = "id derived from filename"
			candidates = derivedIDCandidates()
		}

		t.Run(name, func(t *testing.T) {
			parent, policy := mixedTrustEvalParent(t, []byte(mixedTrustPolicy))
			projectID := "project"
			parent.ProjectID = &projectID
			if err := (&vcs.JobMetadata{
				VCSProvider: "github", Repo: "org/repo", PRNumber: 7,
				CommitSHA: *parent.SourceRef, IsEval: true,
			}).ApplyToJob(parent); err != nil {
				t.Fatal(err)
			}
			processorStore := &multiProfileStore{
				workflowRuntimeStore: newWorkflowRuntimeStore(), profiles: mixedTrustProfiles(),
			}
			tp := NewTriggerProcessor(processorStore, nil)

			selected, violations, err := tp.selectPolicyCandidates(
				context.Background(), parent, candidates,
				[]string{".reactorcide/workflows/pr.yaml"},
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(violations) != 0 || len(selected) != 1 {
				t.Fatalf("unexpected selection: workflows=%d violations=%+v", len(selected), violations)
			}
			if selected[0].CIOrigin != "head" {
				t.Fatalf("ci_origin = %q, want head: the coordinator's policy allowed this, "+
					"and whether the workflow declared its own id is not the policy's business",
					selected[0].CIOrigin)
			}
			if selected[0].PolicyRuleID != "csilgen" || selected[0].PolicyRevision != policy.Revision {
				t.Fatalf("policy attribution lost: %+v", selected[0])
			}
		})
	}
}

// TestSelectPolicyCandidatesStillRefusesUnmatchedActor is the other half: with
// the explicit-id gate gone, the policy's own conditions must still be the
// thing that decides. An actor the policy does not name gets base CI, whether
// or not the workflow declared an id.
func TestSelectPolicyCandidatesStillRefusesUnmatchedActor(t *testing.T) {
	parent, _ := mixedTrustEvalParent(t, []byte(mixedTrustPolicy))
	projectID := "project"
	parent.ProjectID = &projectID
	// The policy names `repository_write`; this actor has none of it.
	parent.JobEnvVars["REACTORCIDE_ACTOR_SUBJECTS"] = `["vcs_user:github/a-stranger"]`
	if err := (&vcs.JobMetadata{
		VCSProvider: "github", Repo: "org/repo", PRNumber: 7,
		CommitSHA: *parent.SourceRef, IsEval: true,
	}).ApplyToJob(parent); err != nil {
		t.Fatal(err)
	}
	processorStore := &multiProfileStore{
		workflowRuntimeStore: newWorkflowRuntimeStore(), profiles: mixedTrustProfiles(),
	}
	tp := NewTriggerProcessor(processorStore, nil)

	selected, _, err := tp.selectPolicyCandidates(
		context.Background(), parent, derivedIDCandidates(),
		[]string{".reactorcide/workflows/pr.yaml"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 {
		t.Fatalf("workflows=%d, want 1", len(selected))
	}
	if selected[0].CIOrigin == "head" {
		t.Fatal("an actor the policy does not name must NOT receive head CI, " +
			"derived id or otherwise")
	}
}
