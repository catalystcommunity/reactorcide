package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/profiles"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resources"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"gopkg.in/yaml.v3"
)

// TriggerProcessor handles reading triggers.json from completed eval jobs
// and creating/submitting the triggered jobs to Corndogs.
type TriggerProcessor struct {
	store          store.Store
	corndogsClient corndogs.ClientInterface
	statusUpdater  vcs.JobStatusUpdaterInterface
}

// NewTriggerProcessor creates a new TriggerProcessor.
func NewTriggerProcessor(store store.Store, corndogsClient corndogs.ClientInterface) *TriggerProcessor {
	return &TriggerProcessor{
		store:          store,
		corndogsClient: corndogsClient,
	}
}

// SetStatusUpdater wires a VCS status updater so that newly-created child
// jobs get registered as pending checks on their commit the moment they
// exist in the database, before the worker picks them up.
func (tp *TriggerProcessor) SetStatusUpdater(u vcs.JobStatusUpdaterInterface) {
	tp.statusUpdater = u
}

// triggersFile represents the top-level structure of triggers.json.
type triggersFile struct {
	Type        string `json:"type"`
	OperationID string `json:"operation_id,omitempty"`
	TriggerType string `json:"trigger_type,omitempty"`
	// Workflows is the multi-workflow form: an eval emits one entry per matched
	// .reactorcide workflow YAML, so one event can spawn several independently
	// named workflows (per team/product/etc). Takes precedence over the legacy
	// single-workflow fields below when present.
	Workflows []triggerWorkflowSpec `json:"workflows,omitempty"`
	// Workflow + Jobs are the legacy single-workflow form (bare .reactorcide/
	// jobs), still accepted: they collapse to exactly one workflow.
	Workflow         *triggerWorkflowSpec `json:"workflow,omitempty"`
	Jobs             []triggerJobSpec     `json:"jobs"`
	PolicyViolations []policyViolation    `json:"policy_violations,omitempty"`
	ChangedCIPaths   []string             `json:"changed_ci_paths,omitempty"`
	ActorSubjects    []string             `json:"actor_subjects,omitempty"`
}

type policyViolation struct {
	Path       string `json:"path"`
	WorkflowID string `json:"workflow_id,omitempty"`
	Actor      string `json:"actor,omitempty"`
	Rule       string `json:"rule,omitempty"`
	BaseSHA    string `json:"base_sha,omitempty"`
	HeadSHA    string `json:"head_sha,omitempty"`
}

type triggerWorkflowSpec struct {
	ID               string                 `json:"id,omitempty"`
	Name             string                 `json:"name"`
	SourceFile       string                 `json:"source_file,omitempty"`
	CIOrigin         string                 `json:"ci_origin,omitempty"`
	CIRepository     string                 `json:"ci_repository,omitempty"`
	CISHA            string                 `json:"ci_sha,omitempty"`
	ExecutionProfile string                 `json:"execution_profile,omitempty"`
	WorkerClass      string                 `json:"worker_class,omitempty"`
	PolicyRevision   string                 `json:"policy_revision,omitempty"`
	PolicyRuleID     string                 `json:"policy_rule_id,omitempty"`
	ApprovalID       *string                `json:"approval_id,omitempty"`
	DependencyPaths  []string               `json:"dependency_paths,omitempty"`
	Vars             map[string]interface{} `json:"vars"`
	OperationID      string                 `json:"-"`
	TriggerType      string                 `json:"-"`
	// Jobs are this workflow's nodes (multi-workflow form). Empty in the legacy
	// single-workflow form, where jobs live in triggersFile.Jobs instead.
	Jobs []triggerJobSpec `json:"jobs,omitempty"`
}

type triggerProfileStore interface {
	GetExecutionProfile(ctx context.Context, orgID, name string) (*models.ExecutionProfile, error)
}

type triggerApprovalStore interface {
	GetCIApprovalByID(ctx context.Context, approvalID string) (*models.CIApproval, error)
}

type triggerReportGenerationStore interface {
	CompleteVCSReportGeneration(ctx context.Context, targetID string, generation int64) error
}

func (tp *TriggerProcessor) completeReportGeneration(ctx context.Context, parentJob *models.Job) error {
	reports, ok := tp.store.(triggerReportGenerationStore)
	if !ok || parentJob == nil {
		return nil
	}
	targetID, _ := parentJob.JobEnvVars["REACTORCIDE_REPORT_TARGET_ID"].(string)
	if targetID == "" {
		return nil
	}
	var generation int64
	switch value := parentJob.JobEnvVars["REACTORCIDE_REPORT_GENERATION"].(type) {
	case int64:
		generation = value
	case int:
		generation = int64(value)
	case float64:
		generation = int64(value)
	}
	if generation <= 0 {
		return fmt.Errorf("invalid VCS report generation")
	}
	return reports.CompleteVCSReportGeneration(ctx, targetID, generation)
}

// triggerJobSpec represents a single triggered job from triggers.json.
type triggerJobSpec struct {
	JobFile        string            `json:"job_file"` // Path to YAML job definition, relative to source root
	JobName        string            `json:"job_name"`
	DependsOn      []string          `json:"depends_on"`
	Condition      string            `json:"condition"`
	Env            map[string]string `json:"env"`
	SourceType     string            `json:"source_type"`
	SourceURL      string            `json:"source_url"`
	SourceRef      string            `json:"source_ref"`
	CISourceType   string            `json:"ci_source_type"`
	CISourceURL    string            `json:"ci_source_url"`
	CISourceRef    string            `json:"ci_source_ref"`
	ContainerImage string            `json:"container_image"`
	JobCommand     string            `json:"job_command"`
	CodeDir        string            `json:"code_dir"`
	JobDir         string            `json:"job_dir"`
	WorkingDir     string            `json:"working_dir"`
	RunAsUser      string            `json:"run_as_user"`
	Priority       *int              `json:"priority"`
	Timeout        *int              `json:"timeout"`
	Capabilities   []string          `json:"capabilities"`
	// ImagePullSecrets holds Kubernetes Secret NAMES for pulling the job
	// image — never credentials. The worker enforces its allowlist before
	// Kubernetes Job creation.
	ImagePullSecrets []string      `json:"image_pull_secrets"`
	ForEach          []interface{} `json:"for_each"`
	ItemVar          string        `json:"item_var"`
	WorkerClass      string        `json:"worker_class"`

	// CIOrigin and ExecutionProfile carry a per-node authority override for
	// policy-controlled trusted base nodes inside a head-CI workflow. Only
	// runnerlib eval output sets them, and validateWorkflowAuthority verifies
	// them against the pinned coordinator policy before any node is created.
	CIOrigin         string `json:"ci_origin,omitempty"`
	ExecutionProfile string `json:"execution_profile,omitempty"`

	// Characteristics/Resources, when set, override the parent (eval) job's
	// characteristics/resources for this triggered job. See
	// buildJobFromTrigger.
	Characteristics map[string]interface{} `json:"characteristics"`
	Resources       map[string]interface{} `json:"resources"`
}

// jobDefinitionFile represents a YAML job definition file (e.g., .reactorcide/jobs/*.yaml).
type jobDefinitionFile struct {
	Name        string                 `yaml:"name"`
	Description string                 `yaml:"description"`
	Job         jobDefinitionJobConfig `yaml:"job"`
	Environment map[string]string      `yaml:"environment"`
}

// jobDefinitionJobConfig represents the job configuration within a YAML job definition.
type jobDefinitionJobConfig struct {
	Image        string     `yaml:"image"`
	Command      string     `yaml:"command"`
	CodeDir      string     `yaml:"code_dir"`
	JobDir       string     `yaml:"job_dir"`
	WorkingDir   string     `yaml:"working_dir"`
	RunAs        *RunAsSpec `yaml:"run_as"`
	Timeout      *int       `yaml:"timeout"`
	Priority     *int       `yaml:"priority"`
	RawCommand   bool       `yaml:"raw_command"`
	Capabilities []string   `yaml:"capabilities"`
	// ImagePullSecrets — Kubernetes Secret names only, see triggerJobSpec.
	ImagePullSecrets []string `yaml:"image_pull_secrets"`
	WorkerClass      string   `yaml:"worker_class"`
	// Characteristics/Resources -- see triggerJobSpec's doc comment.
	Characteristics map[string]interface{} `yaml:"characteristics"`
	Resources       map[string]interface{} `yaml:"resources"`
}

func runAsUserFromSpec(spec *RunAsSpec) string {
	if spec == nil {
		return ""
	}
	return spec.User
}

// ProcessTriggers reads triggers.json from the workspace directory of a completed
// eval job, creates the triggered jobs in the database, and submits them to Corndogs.
func (tp *TriggerProcessor) ProcessTriggers(ctx context.Context, workspaceDir string, parentJob *models.Job) error {
	triggersPath := filepath.Join(workspaceDir, "triggers.json")

	data, err := os.ReadFile(triggersPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No triggers file means no jobs to create - this is normal
			logging.Log.WithField("workspace", workspaceDir).Debug("No triggers.json found, skipping trigger processing")
			return nil
		}
		return fmt.Errorf("failed to read triggers file: %w", err)
	}

	_, err = tp.ProcessTriggersFromData(ctx, data, workspaceDir, parentJob)
	return err
}

// ProcessTriggersFromData processes raw trigger JSON data, creates the triggered jobs
// in the database, submits them to Corndogs, and returns the created job IDs.
// workspaceDir is the host workspace directory used to resolve job_file references.
func (tp *TriggerProcessor) ProcessTriggersFromData(ctx context.Context, data []byte, workspaceDir string, parentJob *models.Job) ([]string, error) {
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(data, &topLevel); err != nil {
		return nil, fmt.Errorf("failed to parse triggers data: %w", err)
	}
	if _, versioned := topLevel["version"]; versioned {
		return nil, fmt.Errorf("trigger payload must not include a version field")
	}
	var tf triggersFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return nil, fmt.Errorf("failed to parse triggers data: %w", err)
	}
	isEvalResult := strings.TrimSpace(tf.TriggerType) == "runnerlib_eval"
	if tf.Type != "trigger_job" {
		return nil, fmt.Errorf("unexpected trigger type: %q", tf.Type)
	}
	if isEvalResult {
		audit.Record(ctx, tp.store, parentJob.OrgID, "ci_policy.input", "job", parentJob.JobID, models.JSONB{
			"project_id": parentJob.ProjectID, "policy_repository": parentJob.CIRepository,
			"policy_revision": parentJob.JobEnvVars["REACTORCIDE_CI_POLICY_REVISION"], "base_sha": parentJob.CISHA,
			"head_sha": stringValue(parentJob.SourceRef), "changed_ci_paths": tf.ChangedCIPaths,
			"actor": parentJob.JobEnvVars["REACTORCIDE_HEAD_ACTOR"], "actor_subjects": parentJob.JobEnvVars["REACTORCIDE_ACTOR_SUBJECTS"],
		})
	}
	// Normalize to a list of workflow batches. The multi-workflow form
	// (tf.Workflows) wins; otherwise the legacy single-workflow form
	// (tf.Workflow + tf.Jobs) collapses to exactly one batch.
	batches := tf.Workflows
	if len(batches) == 0 && (tf.Workflow != nil || len(tf.Jobs) > 0) {
		batch := triggerWorkflowSpec{Jobs: tf.Jobs}
		if tf.Workflow != nil {
			batch.Name = tf.Workflow.Name
			batch.Vars = tf.Workflow.Vars
		}
		batches = []triggerWorkflowSpec{batch}
	}
	for i := range batches {
		batches[i].OperationID = strings.TrimSpace(tf.OperationID)
		batches[i].TriggerType = strings.TrimSpace(tf.TriggerType)
		if batches[i].TriggerType == "" {
			batches[i].TriggerType = "runnerlib"
		}
		if !isEvalResult {
			// Only verified eval output can carry per-node authority. Any
			// other trigger source must not name node authority at all.
			for j := range batches[i].Jobs {
				if batches[i].Jobs[j].CIOrigin != "" || batches[i].Jobs[j].ExecutionProfile != "" {
					return nil, fmt.Errorf("triggered job %q cannot set node authority", batches[i].Jobs[j].JobName)
				}
			}
		}
		if isEvalResult {
			if err := tp.validateWorkflowAuthority(ctx, parentJob, &batches[i], tf.ChangedCIPaths); err != nil {
				return nil, err
			}
			audit.Record(ctx, tp.store, parentJob.OrgID, "ci_policy.decision", "workflow", batches[i].ID, models.JSONB{
				"project_id": parentJob.ProjectID, "ci_origin": batches[i].CIOrigin,
				"base_sha": parentJob.CISHA, "head_sha": stringValue(parentJob.SourceRef),
				"rule": batches[i].PolicyRuleID, "profile": batches[i].ExecutionProfile,
				"worker_class": batches[i].WorkerClass, "policy_revision": batches[i].PolicyRevision,
				"approval_id": batches[i].ApprovalID, "decision": "allowed",
			})
		}
	}
	if isEvalResult {
		headAuthorized := false
		for i := range batches {
			if batches[i].CIOrigin == "head" {
				headAuthorized = true
				break
			}
		}
		if headAuthorized {
			tf.PolicyViolations = nil
		} else {
			tf.PolicyViolations = make([]policyViolation, 0, len(tf.ChangedCIPaths))
			for _, pathValue := range uniqueNonEmpty(tf.ChangedCIPaths) {
				tf.PolicyViolations = append(tf.PolicyViolations, policyViolation{
					Path: pathValue, Actor: fmt.Sprint(parentJob.JobEnvVars["REACTORCIDE_HEAD_ACTOR"]),
					BaseSHA: parentJob.CISHA, HeadSHA: stringValue(parentJob.SourceRef),
				})
			}
		}
		for _, violation := range tf.PolicyViolations {
			logging.Log.WithFields(map[string]interface{}{
				"parent_job_id": parentJob.JobID, "path": violation.Path,
				"workflow_security_id": violation.WorkflowID, "rule": violation.Rule,
				"base_sha": violation.BaseSHA, "head_sha": violation.HeadSHA,
			}).Warn("CI policy rejected head CI; safe base workflow triggers will continue")
			audit.Record(ctx, tp.store, parentJob.OrgID, "ci_policy.violation", "job", parentJob.JobID, models.JSONB{
				"path": violation.Path, "workflow_id": violation.WorkflowID, "actor": violation.Actor,
				"rule": violation.Rule, "base_sha": violation.BaseSHA, "head_sha": violation.HeadSHA,
			})
		}
		if updater, ok := tp.statusUpdater.(interface {
			UpdateCIPolicyStatus(context.Context, *models.Job, []vcs.CIPolicyViolation) error
		}); ok {
			violations := make([]vcs.CIPolicyViolation, len(tf.PolicyViolations))
			for i, item := range tf.PolicyViolations {
				violations[i] = vcs.CIPolicyViolation{Path: item.Path, WorkflowID: item.WorkflowID, Actor: item.Actor,
					Rule: item.Rule, BaseSHA: item.BaseSHA, HeadSHA: item.HeadSHA}
			}
			if err := updater.UpdateCIPolicyStatus(ctx, parentJob, violations); err != nil {
				logging.Log.WithError(err).Warn("Could not publish CI policy status")
			}
		}
	}

	logger := logging.Log.WithField("parent_job_id", parentJob.JobID).WithField("workflow_count", len(batches))
	logger.Info("Processing triggers from eval job")

	// No workflow store (narrow test stores): fall back to submitting each
	// batch's jobs standalone, preserving the pre-workflow behavior.
	if _, err := tp.workflowStore(); err != nil {
		var createdJobIDs []string
		for i := range batches {
			specs, err := tp.resolveJobSpecs(batches[i].Jobs, workspaceDir)
			if err != nil {
				return createdJobIDs, err
			}
			for _, spec := range specs {
				jobID, err := tp.createAndSubmitJob(ctx, spec, parentJob)
				if err != nil {
					logger.WithError(err).WithField("job_name", spec.JobName).Error("Failed to create triggered job")
					continue
				}
				createdJobIDs = append(createdJobIDs, jobID)
			}
		}
		if isEvalResult {
			if err := tp.completeReportGeneration(ctx, parentJob); err != nil {
				return createdJobIDs, err
			}
		}
		return createdJobIDs, nil
	}

	var createdJobIDs []string
	for i := range batches {
		ids, err := tp.processWorkflowBatch(ctx, parentJob, &batches[i], workspaceDir)
		if err != nil {
			return createdJobIDs, err
		}
		createdJobIDs = append(createdJobIDs, ids...)
	}
	if isEvalResult {
		if err := tp.completeReportGeneration(ctx, parentJob); err != nil {
			return createdJobIDs, err
		}
	}
	return createdJobIDs, nil
}

func (tp *TriggerProcessor) validateWorkflowAuthority(ctx context.Context, parentJob *models.Job, spec *triggerWorkflowSpec, changedCIPaths []string) error {
	metadata, err := vcs.MetadataFromJob(parentJob)
	if err != nil || metadata == nil || !metadata.IsEval || spec.TriggerType != "runnerlib_eval" {
		return fmt.Errorf("runnerlib evaluation workflow authority requires an evaluation job")
	}
	if spec.CIOrigin != "base" && spec.CIOrigin != "head" {
		return fmt.Errorf("workflow %q has invalid CI origin", spec.ID)
	}
	expectedRepository, expectedSHA := parentJob.CIRepository, parentJob.CISHA
	if spec.CIOrigin == "head" {
		expectedRepository, expectedSHA = stringValue(parentJob.SourceURL), stringValue(parentJob.SourceRef)
		if spec.PolicyRevision == "" || spec.PolicyRuleID == "" {
			return fmt.Errorf("head workflow %q lacks a policy decision", spec.ID)
		}
	}
	if spec.CIRepository != expectedRepository || spec.CISHA != expectedSHA || expectedRepository == "" || expectedSHA == "" {
		return fmt.Errorf("workflow %q CI repository or SHA does not match the event authority", spec.ID)
	}
	if spec.ExecutionProfile == "" || spec.WorkerClass == "" {
		return fmt.Errorf("workflow %q must select an execution profile and worker class", spec.ID)
	}
	approvalSubjects := map[string]bool{}
	if spec.ApprovalID != nil {
		approvals, ok := tp.store.(triggerApprovalStore)
		if !ok {
			return fmt.Errorf("workflow %q approval cannot be verified", spec.ID)
		}
		approval, err := approvals.GetCIApprovalByID(ctx, *spec.ApprovalID)
		if err != nil || approval.OrgID != parentJob.OrgID || parentJob.ProjectID == nil || approval.ProjectID != *parentJob.ProjectID ||
			approval.HeadSHA != stringValue(parentJob.SourceRef) || approval.BaseSHA != parentJob.CISHA || approval.PolicyRevision != spec.PolicyRevision ||
			(approval.WorkflowScope != spec.ID && approval.WorkflowScope != "*") || approval.ExecutionProfile != spec.ExecutionProfile ||
			!approval.IsValid(time.Now().UTC(), stringValue(parentJob.SourceRef), spec.PolicyRevision) {
			return fmt.Errorf("workflow %q approval is not valid for this decision", spec.ID)
		}
		approvalSubjects[approval.ApproverSubject] = true
	}
	var nodeAuthority map[string]cipolicy.NodeDecision
	if spec.CIOrigin == "head" {
		policyDocument, _ := parentJob.JobEnvVars["REACTORCIDE_CI_POLICY"].(string)
		policyRevision, _ := parentJob.JobEnvVars["REACTORCIDE_CI_POLICY_REVISION"].(string)
		policy, err := cipolicy.ParseDocument([]byte(policyDocument))
		if err != nil || policyDocument == "" || policy.Revision != policyRevision || spec.PolicyRevision != policyRevision {
			return fmt.Errorf("workflow %q does not have a valid coordinator policy snapshot", spec.ID)
		}
		if len(changedCIPaths) == 0 || len(spec.DependencyPaths) == 0 || !intersects(changedCIPaths, spec.DependencyPaths) {
			return fmt.Errorf("workflow %q has no changed CI dependency", spec.ID)
		}
		actorSubjects := parseStringSet(parentJob.JobEnvVars["REACTORCIDE_ACTOR_SUBJECTS"])
		allPolicyPaths := uniqueNonEmpty(append(append([]string{}, changedCIPaths...), spec.DependencyPaths...))
		baseBranch := envString(parentJob, "REACTORCIDE_BASE_REF")
		if baseBranch == "" {
			baseBranch = envString(parentJob, "REACTORCIDE_PR_BASE_REF")
		}
		if baseBranch == "" {
			baseBranch = envString(parentJob, "REACTORCIDE_BRANCH")
		}
		relation := "same"
		if envString(parentJob, "REACTORCIDE_IS_FORK_PR") == "true" {
			relation = "fork"
		}
		decision, err := cipolicy.Decide(policy, cipolicy.Facts{
			WorkflowID: spec.ID, ChangedCIPaths: allPolicyPaths,
			Event: envString(parentJob, "REACTORCIDE_EVENT_TYPE"), BaseBranch: baseBranch,
			HeadRepositoryRelation: relation, ActorSubjects: actorSubjects, ApprovalSubjects: approvalSubjects,
		})
		if err != nil {
			return fmt.Errorf("workflow %q policy decision failed: %w", spec.ID, err)
		}
		if !decision.Allowed || decision.RuleID != spec.PolicyRuleID || decision.Profile != spec.ExecutionProfile || decision.WorkerClass != spec.WorkerClass {
			return fmt.Errorf("workflow %q is not authorized by the coordinator policy", spec.ID)
		}
		nodeAuthority = decision.BaseNodes
	}
	if ps, ok := tp.store.(triggerProfileStore); ok {
		parentName := parentJob.ExecutionProfile
		if parentName == "" {
			parentName = "standard"
		}
		parentProfile, parentErr := ps.GetExecutionProfile(ctx, parentJob.OrgID, parentName)
		childProfile, childErr := ps.GetExecutionProfile(ctx, parentJob.OrgID, spec.ExecutionProfile)
		if parentErr != nil || childErr != nil {
			return fmt.Errorf("workflow %q selects an unknown execution profile", spec.ID)
		}
		if err := profiles.WeakerOrEqual(childProfile, parentProfile); err != nil {
			return fmt.Errorf("workflow %q raises profile authority: %w", spec.ID, err)
		}
		if len(childProfile.AllowedWorkerClasses) > 0 && !containsString(childProfile.AllowedWorkerClasses, spec.WorkerClass) {
			return fmt.Errorf("workflow %q selects a worker class denied by profile %q", spec.ID, spec.ExecutionProfile)
		}
	}
	return tp.validateNodeAuthority(ctx, parentJob, spec, nodeAuthority)
}

// validateNodeAuthority verifies per-node trusted-base authority claims in an
// eval batch against the coordinator policy decision for that workflow. Every
// check fails closed: a claim without a policy grant, a policy grant without a
// matching node, an inexact base CI revision, an unknown or stronger profile,
// and a denied worker class all reject the batch.
func (tp *TriggerProcessor) validateNodeAuthority(ctx context.Context, parentJob *models.Job, spec *triggerWorkflowSpec, nodeAuthority map[string]cipolicy.NodeDecision) error {
	claimed := map[string]bool{}
	for i := range spec.Jobs {
		job := &spec.Jobs[i]
		if job.CIOrigin == "" && job.ExecutionProfile == "" {
			continue
		}
		grant, ok := nodeAuthority[job.JobName]
		if !ok {
			return fmt.Errorf("workflow %q node %q claims authority that no coordinator policy grants", spec.ID, job.JobName)
		}
		if claimed[job.JobName] {
			return fmt.Errorf("workflow %q claims authority for node %q more than one time", spec.ID, job.JobName)
		}
		claimed[job.JobName] = true
		if job.CIOrigin != grant.CISource || job.ExecutionProfile != grant.Profile || job.WorkerClass != grant.WorkerClass {
			return fmt.Errorf("workflow %q node %q authority does not match the coordinator policy", spec.ID, job.JobName)
		}
		if parentJob.CIRepository == "" || parentJob.CISHA == "" || job.CISourceURL != parentJob.CIRepository || job.CISourceRef != parentJob.CISHA {
			return fmt.Errorf("workflow %q node %q must use the exact trusted base CI revision", spec.ID, job.JobName)
		}
	}
	for name := range nodeAuthority {
		if !claimed[name] {
			return fmt.Errorf("workflow %q is missing policy-controlled base node %q", spec.ID, name)
		}
	}
	if len(nodeAuthority) == 0 {
		return nil
	}
	// A node profile must not raise authority above the evaluation job that
	// admits it, and the node worker class must be allowed by that profile.
	ps, ok := tp.store.(triggerProfileStore)
	if !ok {
		return fmt.Errorf("workflow %q node authority cannot be verified", spec.ID)
	}
	parentName := parentJob.ExecutionProfile
	if parentName == "" {
		parentName = "standard"
	}
	parentProfile, err := ps.GetExecutionProfile(ctx, parentJob.OrgID, parentName)
	if err != nil {
		return fmt.Errorf("workflow %q node authority cannot be verified", spec.ID)
	}
	for name, grant := range nodeAuthority {
		nodeProfile, err := ps.GetExecutionProfile(ctx, parentJob.OrgID, grant.Profile)
		if err != nil {
			return fmt.Errorf("workflow %q node %q selects an unknown execution profile", spec.ID, name)
		}
		if err := profiles.WeakerOrEqual(nodeProfile, parentProfile); err != nil {
			return fmt.Errorf("workflow %q node %q raises profile authority: %w", spec.ID, name, err)
		}
		if len(nodeProfile.AllowedWorkerClasses) > 0 && !containsString(nodeProfile.AllowedWorkerClasses, grant.WorkerClass) {
			return fmt.Errorf("workflow %q node %q selects a worker class denied by profile %q", spec.ID, name, grant.Profile)
		}
	}
	return nil
}

func envString(job *models.Job, key string) string {
	value, _ := job.JobEnvVars[key].(string)
	return value
}

func parseStringSet(value interface{}) map[string]bool {
	result := map[string]bool{}
	raw, _ := value.(string)
	var values []string
	if json.Unmarshal([]byte(raw), &values) == nil {
		for _, item := range values {
			if item != "" {
				result[item] = true
			}
		}
	}
	return result
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func intersects(left, right []string) bool {
	values := map[string]bool{}
	for _, item := range left {
		values[item] = true
	}
	for _, item := range right {
		if values[item] {
			return true
		}
	}
	return false
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func enforceResourceCeilings(job *models.Job, ceilings models.JSONB) error {
	if len(ceilings) == 0 {
		return nil
	}
	checks := []struct {
		key, value string
		parse      func(string) (int64, error)
	}{
		{"cpu_request", job.ResourceCPURequest, resources.ParseCPU},
		{"cpu_limit", job.ResourceCPULimit, resources.ParseCPU},
		{"memory_limit", job.ResourceMemoryLimit, resources.ParseMemory},
	}
	for _, check := range checks {
		raw, ok := ceilings[check.key]
		if !ok || check.value == "" {
			continue
		}
		limit, err := check.parse(fmt.Sprint(raw))
		if err != nil {
			return fmt.Errorf("invalid %s ceiling", check.key)
		}
		requested, err := check.parse(check.value)
		if err != nil {
			return err
		}
		if requested > limit {
			return fmt.Errorf("%s exceeds ceiling", check.key)
		}
	}
	return nil
}

// resolveJobSpecs loads any job_file references (a workflow node can reference a
// reusable .reactorcide/jobs/*.yaml and overlay inline fields on top) for a
// batch of job specs.
func (tp *TriggerProcessor) resolveJobSpecs(jobs []triggerJobSpec, workspaceDir string) ([]triggerJobSpec, error) {
	specs := make([]triggerJobSpec, 0, len(jobs))
	for _, spec := range jobs {
		if spec.JobFile != "" {
			if workspaceDir == "" {
				return nil, fmt.Errorf("job_file %q requires workspace-backed trigger processing", spec.JobFile)
			}
			baseSpec, err := tp.loadJobFile(workspaceDir, spec.JobFile)
			if err != nil {
				logging.Log.WithError(err).WithField("job_file", spec.JobFile).Error("Failed to load job file")
				continue
			}
			jobFile := spec.JobFile
			spec = tp.overlaySpec(baseSpec, spec)
			spec.JobFile = jobFile
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// processWorkflowBatch creates one workflow instance (find-or-create by name for
// this parent eval) from a batch, registers its nodes, and evaluates it. The
// eval is the workflow's parent but does NOT join it, so a single event can
// spawn several independently-named workflows.
func (tp *TriggerProcessor) processWorkflowBatch(ctx context.Context, parentJob *models.Job, spec *triggerWorkflowSpec, workspaceDir string) ([]string, error) {
	if len(spec.Jobs) == 0 {
		return nil, nil
	}
	specs := spec.Jobs
	if spec.TriggerType != "runnerlib_eval" {
		var err error
		specs, err = tp.resolveJobSpecs(spec.Jobs, workspaceDir)
		if err != nil {
			return nil, err
		}
	}
	wf, err := tp.ensureWorkflow(ctx, parentJob, spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow: %w", err)
	}
	for i := range specs {
		if specs[i].CIOrigin != "" || specs[i].ExecutionProfile != "" {
			// Policy-controlled trusted base node: keep the verified base CI
			// pin and worker class that validateWorkflowAuthority checked.
			continue
		}
		specs[i].WorkerClass = wf.WorkerClass
		specs[i].CISourceURL = wf.CIRepository
		specs[i].CISourceRef = wf.CISHA
	}
	if len(spec.Vars) > 0 {
		if err := tp.addWorkflowVars(ctx, wf, spec.Vars, nil, &parentJob.JobID); err != nil {
			return nil, fmt.Errorf("failed to add workflow vars: %w", err)
		}
	}
	existingNodes, err := tp.workflowStore()
	if err != nil {
		return nil, err
	}
	registered, err := existingNodes.ListWorkflowNodes(ctx, wf.WorkflowID)
	if err != nil {
		return nil, err
	}
	if len(registered) > 0 {
		return tp.evaluateWorkflow(ctx, wf)
	}
	if err := tp.createWorkflowNodes(ctx, wf, specs); err != nil {
		return nil, fmt.Errorf("failed to create workflow nodes: %w", err)
	}
	if err := tp.refreshRootForChildRegistration(ctx, wf); err != nil {
		return nil, fmt.Errorf("failed to register child workflow with root: %w", err)
	}
	return tp.evaluateWorkflow(ctx, wf)
}

// loadJobFile reads a YAML job definition file from the workspace and converts it to a triggerJobSpec.
func (tp *TriggerProcessor) loadJobFile(workspaceDir, jobFile string) (triggerJobSpec, error) {
	filePath := filepath.Join(workspaceDir, "src", jobFile)

	data, err := os.ReadFile(filePath)
	if err != nil {
		return triggerJobSpec{}, fmt.Errorf("failed to read job file %q: %w", filePath, err)
	}

	var def jobDefinitionFile
	if err := yaml.Unmarshal(data, &def); err != nil {
		return triggerJobSpec{}, fmt.Errorf("failed to parse job file %q: %w", filePath, err)
	}

	spec := triggerJobSpec{
		JobName:          def.Name,
		ContainerImage:   def.Job.Image,
		JobCommand:       def.Job.Command,
		CodeDir:          def.Job.CodeDir,
		JobDir:           def.Job.JobDir,
		WorkingDir:       def.Job.WorkingDir,
		RunAsUser:        runAsUserFromSpec(def.Job.RunAs),
		Timeout:          def.Job.Timeout,
		Priority:         def.Job.Priority,
		Capabilities:     def.Job.Capabilities,
		ImagePullSecrets: def.Job.ImagePullSecrets,
		WorkerClass:      def.Job.WorkerClass,
		Env:              def.Environment,
		Characteristics:  def.Job.Characteristics,
		Resources:        def.Job.Resources,
	}

	return spec, nil
}

// overlaySpec overlays non-zero inline fields from the original trigger spec onto the base spec loaded from a job file.
func (tp *TriggerProcessor) overlaySpec(base, overlay triggerJobSpec) triggerJobSpec {
	result := base

	// Overlay simple string fields if non-empty
	if overlay.JobName != "" {
		result.JobName = overlay.JobName
	}
	if overlay.JobFile != "" {
		result.JobFile = overlay.JobFile
	}
	if overlay.WorkerClass != "" {
		result.WorkerClass = overlay.WorkerClass
	}
	if overlay.ContainerImage != "" {
		result.ContainerImage = overlay.ContainerImage
	}
	if overlay.JobCommand != "" {
		result.JobCommand = overlay.JobCommand
	}
	if overlay.SourceType != "" {
		result.SourceType = overlay.SourceType
	}
	if overlay.SourceURL != "" {
		result.SourceURL = overlay.SourceURL
	}
	if overlay.SourceRef != "" {
		result.SourceRef = overlay.SourceRef
	}
	if overlay.CISourceType != "" {
		result.CISourceType = overlay.CISourceType
	}
	if overlay.CISourceURL != "" {
		result.CISourceURL = overlay.CISourceURL
	}
	if overlay.CISourceRef != "" {
		result.CISourceRef = overlay.CISourceRef
	}
	if overlay.Condition != "" {
		result.Condition = overlay.Condition
	}
	if overlay.CodeDir != "" {
		result.CodeDir = overlay.CodeDir
	}
	if overlay.JobDir != "" {
		result.JobDir = overlay.JobDir
	}
	if overlay.WorkingDir != "" {
		result.WorkingDir = overlay.WorkingDir
	}
	if overlay.RunAsUser != "" {
		result.RunAsUser = overlay.RunAsUser
	}

	// Overlay pointer fields if non-nil
	if overlay.Priority != nil {
		result.Priority = overlay.Priority
	}
	if overlay.Timeout != nil {
		result.Timeout = overlay.Timeout
	}

	// Overlay slices if non-empty
	if len(overlay.DependsOn) > 0 {
		result.DependsOn = overlay.DependsOn
	}
	if len(overlay.Capabilities) > 0 {
		result.Capabilities = overlay.Capabilities
	}
	if len(overlay.ImagePullSecrets) > 0 {
		result.ImagePullSecrets = overlay.ImagePullSecrets
	}
	if len(overlay.ForEach) > 0 {
		result.ForEach = overlay.ForEach
	}
	if overlay.ItemVar != "" {
		result.ItemVar = overlay.ItemVar
	}
	if len(overlay.Characteristics) > 0 {
		result.Characteristics = overlay.Characteristics
	}
	if len(overlay.Resources) > 0 {
		result.Resources = overlay.Resources
	}

	// Merge env vars: base first, then overlay on top
	if len(overlay.Env) > 0 {
		if result.Env == nil {
			result.Env = make(map[string]string)
		}
		for k, v := range overlay.Env {
			result.Env[k] = v
		}
	}

	return result
}

// queueResolvingStore is the narrow store capability trigger/workflow job
// submission uses to resolve a job's characteristics to a queue UUID
// (find-or-create) before it is persisted/submitted. Defined here on the
// consumer side (repo convention: narrow interface + type assertion on the
// store), mirroring internal/handlers/job_handler.go's identical interface
// for the REST submit path. The concrete PostgresDbStore satisfies it via
// internal/store/postgres_store/queue_operations.go.
type queueResolvingStore interface {
	FindOrCreateQueueByCharacteristics(ctx context.Context, chars characteristics.Characteristics) (*models.Queue, error)
}

type orgQueueResolvingStore interface {
	FindOrCreateQueueForOrg(ctx context.Context, orgID, workerClass string, chars characteristics.Characteristics) (*models.Queue, error)
}

// resolveJobQueue resolves job.Characteristics to a queue (find-or-create)
// and sets job.QueueName to the resolved Queue.QueueUUID. A no-op (QueueName
// left as buildJobFromTrigger set it -- the parent's or an override's) when
// the store doesn't implement queueResolvingStore.
func (tp *TriggerProcessor) resolveJobQueue(ctx context.Context, job *models.Job) error {
	if qs, ok := tp.store.(orgQueueResolvingStore); ok {
		queue, err := qs.FindOrCreateQueueForOrg(ctx, job.OrgID, job.WorkerClass, job.Characteristics)
		if err != nil {
			return fmt.Errorf("resolving queue: %w", err)
		}
		job.QueueName = queue.QueueUUID
		return nil
	}
	qs, ok := tp.store.(queueResolvingStore)
	if !ok {
		return nil
	}
	queue, err := qs.FindOrCreateQueueByCharacteristics(ctx, job.Characteristics)
	if err != nil {
		return fmt.Errorf("resolving queue: %w", err)
	}
	job.QueueName = queue.QueueUUID
	return nil
}

// createAndSubmitJob creates a single job from a trigger spec and submits it to Corndogs.
// Returns the created job ID on success.
func (tp *TriggerProcessor) createAndSubmitJob(ctx context.Context, spec triggerJobSpec, parentJob *models.Job) (string, error) {
	job, err := tp.buildJobFromTrigger(spec, parentJob)
	if err != nil {
		return "", fmt.Errorf("failed to build triggered job: %w", err)
	}

	if err := tp.resolveJobQueue(ctx, job); err != nil {
		return "", err
	}

	if err := tp.store.CreateJob(ctx, job); err != nil {
		return "", fmt.Errorf("failed to create job in database: %w", err)
	}

	// Register as a pending check on the commit immediately, before Corndogs
	// submission, so branch protection sees every child as a required check
	// without waiting for a worker to pick it up.
	if tp.statusUpdater != nil {
		if err := tp.statusUpdater.UpdateJobStatus(ctx, job); err != nil {
			logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to register pending check for triggered job")
		}
	}

	if tp.corndogsClient == nil {
		return job.JobID, nil
	}

	taskPayload := tp.buildTaskPayload(job)

	task, err := tp.corndogsClient.SubmitTaskToQueue(ctx, job.QueueName, taskPayload, int64(job.Priority))
	if err != nil {
		logging.Log.WithError(err).WithField("job_id", job.JobID).Error("Failed to submit triggered job to Corndogs")
		job.Status = "failed"
		job.LastError = fmt.Sprintf("failed to submit to Corndogs: %v", err)
	} else {
		taskID := task.Uuid
		job.CorndogsTaskID = &taskID
		job.Status = task.CurrentState
	}

	if err := tp.store.UpdateJob(ctx, job); err != nil {
		logging.Log.WithError(err).WithField("job_id", job.JobID).Error("Failed to update triggered job after Corndogs submission")
	}

	logging.Log.WithFields(map[string]interface{}{
		"job_id":        job.JobID,
		"job_name":      job.Name,
		"parent_job_id": parentJob.JobID,
		"status":        job.Status,
	}).Info("Created triggered job")

	return job.JobID, nil
}

// buildJobFromTrigger creates a models.Job from a trigger spec and parent
// job. Returns an error only when the spec's own `characteristics`/
// `resources` block fails validation (internal/characteristics.
// ParseJobCharacteristics / internal/resources.ParseResources) -- every
// other field is either copied verbatim or defaulted, so it never fails.
func (tp *TriggerProcessor) buildJobFromTrigger(spec triggerJobSpec, parentJob *models.Job) (*models.Job, error) {
	return tp.buildJobFromTriggerWithCapabilityLimit(spec, parentJob, true)
}

// buildJobFromTriggerWithCapabilityLimit builds a child job and optionally
// limits its capabilities to the authority parent's capability set. Direct
// triggers always use the limit. Validated workflows can disable it when the
// selected execution profile has a null capability list, which means that the
// profile does not apply a capability limit.
func (tp *TriggerProcessor) buildJobFromTriggerWithCapabilityLimit(spec triggerJobSpec, parentJob *models.Job, limitCapabilities bool) (*models.Job, error) {
	now := time.Now().UTC()
	parentJobID := parentJob.JobID

	// Merge env vars: start with the parent's event context, then apply the
	// trigger's environment. REACTORCIDE_JOB_KIND describes the parent eval
	// process itself. If a child inherits "eval", runnerlib can select the eval
	// checkout layout for a normal workflow job and place source under
	// /job/src/head instead of the child's configured /job/src directory.
	envVars := models.JSONB{}
	if parentJob.JobEnvVars != nil {
		for k, v := range parentJob.JobEnvVars {
			if k == "REACTORCIDE_JOB_KIND" {
				continue
			}
			envVars[k] = v
		}
	}
	for k, v := range spec.Env {
		envVars[k] = v
	}

	job := &models.Job{
		CreatedAt:        now,
		UpdatedAt:        now,
		UserID:           parentJob.UserID,
		OrgID:            parentJob.OrgID,
		ProjectID:        parentJob.ProjectID,
		ParentJobID:      &parentJobID,
		Name:             spec.JobName,
		JobFile:          spec.JobFile,
		Description:      fmt.Sprintf("Triggered by eval job %s", parentJob.JobID),
		Status:           "submitted",
		QueueName:        parentJob.QueueName,
		JobEnvVars:       envVars,
		CodeDir:          DefaultJobCodeDir(parentJob.CodeDir),
		JobDir:           DefaultJobDir(parentJob.CodeDir, parentJob.JobDir),
		WorkerClass:      parentJob.WorkerClass,
		ExecutionProfile: parentJob.ExecutionProfile,
		CIOrigin:         parentJob.CIOrigin, CIRepository: parentJob.CIRepository, CISHA: parentJob.CISHA,
		PolicyRevision: parentJob.PolicyRevision, PolicyRuleID: parentJob.PolicyRuleID, ApprovalID: parentJob.ApprovalID,
	}
	if spec.WorkerClass != "" && spec.WorkerClass != parentJob.WorkerClass {
		return nil, fmt.Errorf("triggered job %q cannot raise or change inherited worker class", spec.JobName)
	}

	// Characteristics: the child spec overrides the parent's when it declares
	// its own `characteristics` block, otherwise it inherits the parent
	// (eval) job's characteristics wholesale. This mirrors QueueName's
	// inheritance above; createAndSubmitJob/submitWorkflowNode re-resolve
	// QueueName from this value right before submitting, so QueueName here is
	// only a starting point for stores that don't support queue resolution.
	if len(spec.Characteristics) > 0 {
		chars, err := characteristics.ParseJobCharacteristics(spec.Characteristics)
		if err != nil {
			return nil, fmt.Errorf("invalid characteristics in triggered job %q: %w", spec.JobName, err)
		}
		job.Characteristics = chars
	} else {
		job.Characteristics = parentJob.Characteristics
	}

	// Resources: only set when the child spec explicitly declares a
	// `resources` block; otherwise left empty so the resource_cpu_request/
	// resource_cpu_limit/resource_memory_limit column defaults apply on
	// insert (same "leave empty for DB defaults" behavior as every other
	// submit path -- see internal/resources.ParseResources).
	if len(spec.Resources) > 0 {
		cpuRequest, cpuLimit, memoryLimit, err := resources.ParseResources(spec.Resources)
		if err != nil {
			return nil, fmt.Errorf("invalid resources in triggered job %q: %w", spec.JobName, err)
		}
		job.ResourceCPURequest = cpuRequest
		job.ResourceCPULimit = cpuLimit
		job.ResourceMemoryLimit = memoryLimit
	}

	// Source configuration
	if spec.SourceType != "" {
		st := models.SourceType(spec.SourceType)
		job.SourceType = &st
	}
	if spec.SourceURL != "" {
		job.SourceURL = &spec.SourceURL
	}
	if spec.SourceRef != "" {
		job.SourceRef = &spec.SourceRef
	}

	// CI source configuration
	if spec.CISourceType != "" {
		cst := models.SourceType(spec.CISourceType)
		job.CISourceType = &cst
	}
	if spec.CISourceURL != "" {
		job.CISourceURL = &spec.CISourceURL
	}
	if spec.CISourceRef != "" {
		job.CISourceRef = &spec.CISourceRef
	}

	// Container and execution configuration
	if spec.ContainerImage != "" {
		job.RunnerImage = spec.ContainerImage
	} else {
		job.RunnerImage = parentJob.RunnerImage
	}
	if spec.JobCommand != "" {
		job.JobCommand = spec.JobCommand
	}
	if spec.CodeDir != "" {
		job.CodeDir = DefaultJobCodeDir(spec.CodeDir)
		if spec.JobDir == "" && spec.WorkingDir == "" {
			job.JobDir = DefaultJobDir(job.CodeDir, "")
		}
	}
	if spec.JobDir != "" {
		job.JobDir = DefaultJobDir(job.CodeDir, spec.JobDir)
	}
	if spec.WorkingDir != "" {
		job.JobDir = spec.WorkingDir
	}
	if spec.RunAsUser != "" {
		job.RunAsUser = spec.RunAsUser
	}
	if spec.Timeout != nil {
		job.TimeoutSeconds = *spec.Timeout
	} else {
		job.TimeoutSeconds = parentJob.TimeoutSeconds
	}
	if spec.Priority != nil {
		job.Priority = *spec.Priority
	}
	if len(spec.Capabilities) > 0 {
		allowed := make(map[string]bool, len(parentJob.Capabilities))
		for _, capability := range parentJob.Capabilities {
			allowed[capability] = true
		}
		if limitCapabilities {
			for _, capability := range spec.Capabilities {
				if !allowed[capability] {
					return nil, fmt.Errorf("triggered job %q cannot add runtime capability %q", spec.JobName, capability)
				}
			}
		}
		job.Capabilities = spec.Capabilities
	}
	if len(spec.ImagePullSecrets) > 0 {
		if err := ValidateImagePullSecretNames(spec.ImagePullSecrets); err != nil {
			return nil, fmt.Errorf("triggered job %q: %w", spec.JobName, err)
		}
		job.ImagePullSecrets = spec.ImagePullSecrets
	}

	// Copy event metadata from parent
	if parentJob.EventMetadata != nil {
		job.EventMetadata = parentJob.EventMetadata
	}

	// Copy VCS metadata (Notes) so child jobs can report commit status.
	// Strip the IsEval flag so child jobs actually update commit status.
	// Set the StatusContext to the job name so each job gets a distinct GitHub status check.
	if parentJob.Notes != "" {
		var metadata vcs.JobMetadata
		if err := json.Unmarshal([]byte(parentJob.Notes), &metadata); err == nil {
			metadata.IsEval = false
			metadata.StatusContext = spec.JobName
			if err := metadata.ApplyToJob(job); err != nil {
				job.Notes = parentJob.Notes
			}
		} else {
			job.Notes = parentJob.Notes
		}
	}

	return job, nil
}

// buildTaskPayload creates a Corndogs TaskPayload from a job.
func (tp *TriggerProcessor) buildTaskPayload(job *models.Job) *corndogs.TaskPayload {
	return BuildTaskPayload(job)
}

// BuildTaskPayload is the exported, receiver-free form of buildTaskPayload:
// it depends only on the job, not on any TriggerProcessor field, so it's
// safe to call from other packages that need to mirror the exact submission
// shape trigger_processor.go/workflow_runtime.go use — currently
// internal/jobcontrol.RetryJob, which resubmits a cloned job the same way a
// freshly triggered or workflow-node job is submitted.
func BuildTaskPayload(job *models.Job) *corndogs.TaskPayload {
	sourceTypeStr := ""
	if job.SourceType != nil {
		sourceTypeStr = string(*job.SourceType)
	}
	sourceURL := ""
	if job.SourceURL != nil {
		sourceURL = *job.SourceURL
	}
	sourceRef := ""
	if job.SourceRef != nil {
		sourceRef = *job.SourceRef
	}
	sourcePath := ""
	if job.SourcePath != nil {
		sourcePath = *job.SourcePath
	}

	payload := &corndogs.TaskPayload{
		JobID:   job.JobID,
		JobType: "run",
		Config: map[string]interface{}{
			"image":       job.RunnerImage,
			"command":     job.JobCommand,
			"working_dir": job.JobDir,
			"timeout":     job.TimeoutSeconds,
			"code_dir":    job.CodeDir,
			"job_dir":     job.JobDir,
			"run_as_user": job.RunAsUser,
		},
		Source: map[string]interface{}{
			"type":        sourceTypeStr,
			"url":         sourceURL,
			"ref":         sourceRef,
			"source_path": sourcePath,
		},
		Metadata: map[string]interface{}{
			"user_id":      job.UserID,
			"submitted_at": job.CreatedAt,
			"name":         job.Name,
			"description":  job.Description,
		},
	}

	if job.JobEnvVars != nil {
		payload.Config["environment"] = job.JobEnvVars
	}

	return payload
}
