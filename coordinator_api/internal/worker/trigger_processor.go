package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	approvalstate "github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/egress"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagealias"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/owners"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/priority"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/profiles"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resources"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workflowevents"
	"gopkg.in/yaml.v3"
)

// TriggerProcessor handles reading triggers.json from completed eval jobs
// and creating/submitting the triggered jobs to Corndogs.
type TriggerProcessor struct {
	store          store.Store
	corndogsClient corndogs.ClientInterface
	statusUpdater  vcs.JobStatusUpdaterInterface
	objectStore    objects.ObjectStore
}

// TriggerWorkflowOutcome reports the durable result for one workflow in a
// multi-workflow trigger request. Disposition separates admission from job
// execution state so clients do not report a refused workflow as a build.
type TriggerWorkflowOutcome struct {
	WorkflowID         string   `json:"workflow_id"`
	WorkflowSecurityID string   `json:"workflow_security_id,omitempty"`
	Name               string   `json:"name"`
	Status             string   `json:"status"`
	Disposition        string   `json:"disposition"`
	Reason             string   `json:"reason,omitempty"`
	CreatedJobIDs      []string `json:"created_job_ids"`
}

// TriggerProcessingResult is the durable result of one trigger request.
type TriggerProcessingResult struct {
	CreatedJobIDs []string                 `json:"created_job_ids"`
	Workflows     []TriggerWorkflowOutcome `json:"workflows,omitempty"`
}

// InvalidTriggerError identifies a request that the client can correct.
type InvalidTriggerError struct {
	cause error
}

func (e *InvalidTriggerError) Error() string { return e.cause.Error() }
func (e *InvalidTriggerError) Unwrap() error { return e.cause }

func invalidTrigger(err error) error {
	return &InvalidTriggerError{cause: err}
}

// IsInvalidTriggerError reports whether trigger processing rejected the
// request shape before it started durable workflow work.
func IsInvalidTriggerError(err error) bool {
	var invalid *InvalidTriggerError
	return errors.As(err, &invalid)
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
	Workflows        []triggerWorkflowSpec `json:"workflows,omitempty"`
	PolicyCandidates *policyCandidateSet   `json:"policy_candidates,omitempty"`
	// Workflow + Jobs are the legacy single-workflow form (bare .reactorcide/
	// jobs), still accepted: they collapse to exactly one workflow.
	Workflow       *triggerWorkflowSpec `json:"workflow,omitempty"`
	Jobs           []triggerJobSpec     `json:"jobs"`
	ChangedCIPaths []string             `json:"changed_ci_paths,omitempty"`
	// Decisions is runnerlib's run / skipped / invalid decision for every
	// workflow file it considered, with structured reasons. Optional: an older
	// runnerlib sends none, and only the created workflows are recorded.
	Decisions []evalWorkflowDecision `json:"decisions,omitempty"`
	// ChangedFiles is the event's changed-file list (capped by runnerlib);
	// ChangedFilesTotal is the exact count.
	ChangedFiles      []string `json:"changed_files,omitempty"`
	ChangedFilesTotal int      `json:"changed_files_total,omitempty"`
}

type policyCandidateSet struct {
	Base []triggerWorkflowSpec `json:"base"`
	Head []triggerWorkflowSpec `json:"head"`
}

type policyViolation struct {
	Path       string `json:"path"`
	WorkflowID string `json:"workflow_id,omitempty"`
	Actor      string `json:"actor,omitempty"`
	Rule       string `json:"rule,omitempty"`
	BaseSHA    string `json:"base_sha,omitempty"`
	HeadSHA    string `json:"head_sha,omitempty"`
}

// ciPathAuthority is the authority that accepted the head's change to a CI
// file: a policy rule, an approval, or override-ci.
type ciPathAuthority struct {
	WorkflowID string
	Rule       string
	ApprovalID string
	Reason     string
}

// ciPathDecision is the authorization decision for one changed CI file.
// Authority is nil when the policy refused the change. WorkflowID names the
// workflow that uses a refused file.
type ciPathDecision struct {
	Path       string
	WorkflowID string
	Authority  *ciPathAuthority
}

type triggerWorkflowSpec struct {
	ID               string   `json:"id,omitempty"`
	Name             string   `json:"name"`
	SourceFile       string   `json:"source_file,omitempty"`
	CIOrigin         string   `json:"ci_origin,omitempty"`
	CIRepository     string   `json:"ci_repository,omitempty"`
	CISHA            string   `json:"ci_sha,omitempty"`
	ExecutionProfile string   `json:"execution_profile,omitempty"`
	WorkerClass      string   `json:"worker_class,omitempty"`
	PolicyRevision   string   `json:"policy_revision,omitempty"`
	PolicyRuleID     string   `json:"policy_rule_id,omitempty"`
	ApprovalID       *string  `json:"approval_id,omitempty"`
	DependencyPaths  []string `json:"dependency_paths,omitempty"`
	EventMatched     bool     `json:"event_matched,omitempty"`
	ExplicitID       bool     `json:"explicit_id,omitempty"`
	StatusContext    string   `json:"status_context,omitempty"` // YAML status.context; see ensureWorkflow
	// Owners and Contact are the YAML owners/contact blocks as the eval
	// reports them. They are facts, not authority, and the coordinator uses
	// them only from the TRUSTED definition (see assignTrustedOwnership).
	Owners  *owners.Spec        `json:"owners,omitempty"`
	Contact *owners.ContactSpec `json:"contact,omitempty"`
	// Ownership is the trusted owner snapshot that the coordinator chose for
	// this workflow. Nil means unknown. Never read from JSON.
	Ownership       *models.WorkflowOwnership `json:"-"`
	OwnershipSource string                    `json:"-"`
	Vars            map[string]interface{}    `json:"vars"`
	OperationID     string                    `json:"-"`
	TriggerType     string                    `json:"-"`
	// Jobs are this workflow's nodes (multi-workflow form). Empty in the legacy
	// single-workflow form, where jobs live in triggersFile.Jobs instead.
	Jobs          []triggerJobSpec                 `json:"jobs,omitempty"`
	NodeAuthority map[string]cipolicy.NodeDecision `json:"-"`
}

type triggerProfileStore interface {
	GetExecutionProfile(ctx context.Context, orgID, name string) (*models.ExecutionProfile, error)
}

type triggerApprovalListStore interface {
	ListCIApprovalsForHead(ctx context.Context, projectID string, prNumber int, headSHA string) ([]models.CIApproval, error)
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
	// RunAsAdministrator is run_as.administrator. nil means not set.
	RunAsAdministrator *bool    `json:"run_as_administrator,omitempty"`
	Priority           *int     `json:"priority"`
	Timeout            *int     `json:"timeout"`
	Capabilities       []string `json:"capabilities"`
	// ImagePullSecrets holds Kubernetes Secret NAMES for pulling the job
	// image — never credentials. The worker enforces its allowlist before
	// Kubernetes Job creation.
	ImagePullSecrets []string      `json:"image_pull_secrets"`
	ForEach          []interface{} `json:"for_each"`
	ItemVar          string        `json:"item_var"`
	WorkerClass      string        `json:"worker_class"`

	// CIOrigin and ExecutionProfile carry a per-node authority override for
	// policy-controlled trusted base nodes inside a head-CI workflow. The
	// coordinator sets these fields from the policy snapshot after Runnerlib
	// returns inactive workflow candidates.
	CIOrigin         string `json:"ci_origin,omitempty"`
	ExecutionProfile string `json:"execution_profile,omitempty"`

	// Characteristics/Resources, when set, override the parent (eval) job's
	// characteristics/resources for this triggered job. See
	// buildJobFromTrigger.
	Characteristics map[string]interface{} `json:"characteristics"`
	Resources       map[string]interface{} `json:"resources"`

	// Skip is set by the eval when the job's `if:` predicates are false for
	// this event. The node is created as skipped and never runs.
	// DecisionReasons explains the `if:` verdict either way (absent when the
	// job has no `if:`).
	Skip            bool                    `json:"skip,omitempty"`
	DecisionReasons []models.DecisionReason `json:"decision_reasons,omitempty"`

	// QuarantinePolicy is the node option `quarantine_policy` (WP15,
	// docs/artifacts-and-tests.md): "" or "report" keeps a failed job
	// failed; "ignore_quarantined" makes the node a success when every
	// failed test of the job reports is quarantined.
	QuarantinePolicy string `json:"quarantine_policy,omitempty"`

	// LoadError is set by resolveJobSpecs when the job_file of the node
	// cannot be loaded. The node is still created, and its submit fails as
	// not admitted with this error, so the run shows the failure.
	LoadError string `json:"load_error,omitempty"`
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

func runAsAdministratorFromSpec(spec *RunAsSpec) *bool {
	if spec == nil || spec.Administrator == nil {
		return nil
	}
	v := *spec.Administrator
	return &v
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
	return tp.processTriggersFromData(ctx, data, workspaceDir, parentJob, nil)
}

// ProcessTriggersFromDataWithOutcomes processes triggers and also returns a
// durable outcome for each workflow. A recorded workflow refusal is an
// outcome, not a request error, so other workflows continue.
func (tp *TriggerProcessor) ProcessTriggersFromDataWithOutcomes(ctx context.Context, data []byte, workspaceDir string, parentJob *models.Job) (TriggerProcessingResult, error) {
	result := TriggerProcessingResult{}
	createdJobIDs, err := tp.processTriggersFromData(ctx, data, workspaceDir, parentJob, &result)
	result.CreatedJobIDs = createdJobIDs
	return result, err
}

func (tp *TriggerProcessor) processTriggersFromData(ctx context.Context, data []byte, workspaceDir string, parentJob *models.Job, result *TriggerProcessingResult) ([]string, error) {
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(data, &topLevel); err != nil {
		return nil, invalidTrigger(fmt.Errorf("failed to parse triggers data: %w", err))
	}
	if _, versioned := topLevel["version"]; versioned {
		return nil, invalidTrigger(fmt.Errorf("trigger payload must not include a version field"))
	}
	var tf triggersFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return nil, invalidTrigger(fmt.Errorf("failed to parse triggers data: %w", err))
	}
	isEvalResult := strings.TrimSpace(tf.TriggerType) == "runnerlib_eval"
	if tf.Type != "trigger_job" {
		return nil, invalidTrigger(fmt.Errorf("unexpected trigger type: %q", tf.Type))
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
	if len(tf.ChangedFiles) > maxChangedFilesAccepted {
		return nil, invalidTrigger(fmt.Errorf("changed_files has more than %d entries", maxChangedFilesAccepted))
	}
	pathDecisions := []ciPathDecision(nil)
	policyNotes := map[string]models.DecisionReason{}
	if isEvalResult && tf.PolicyCandidates != nil {
		var err error
		batches, pathDecisions, policyNotes, err = tp.selectPolicyCandidates(ctx, parentJob, tf.PolicyCandidates, tf.ChangedCIPaths)
		if err != nil {
			return nil, err
		}
	} else if isEvalResult {
		policy, err := policySnapshot(parentJob)
		if err != nil {
			return nil, err
		}
		defaultProfile := "standard"
		if policy != nil {
			// defaults.by_ref_class can select the profile by ref class.
			defaultProfile, _ = policy.DefaultProfileFor(parentJob.RefClasses)
		}
		runAuthority, err := runAuthoritySnapshot(parentJob)
		if err != nil {
			return nil, err
		}
		if runAuthority.Profile != "" {
			defaultProfile = runAuthority.Profile
		}
		for i := range batches {
			if batches[i].CIOrigin == "head" || batches[i].PolicyRuleID != "" || batches[i].ApprovalID != nil {
				return nil, fmt.Errorf("runnerlib evaluation output cannot select CI policy authority")
			}
			tp.applyBaseAuthority(parentJob, &batches[i], defaultProfile)
			revision := ""
			if policy != nil {
				batches[i].PolicyRevision = policy.Revision
				revision = policy.Revision
			}
			policyNotes[workflowSecurityIDOf(batches[i])] = policyNote("base", defaultProfile, batches[i].WorkerClass, "", revision,
				"no CI definition this workflow uses changed, so the trusted base CI runs")
		}
	}
	if isEvalResult {
		audit.Record(ctx, tp.store, parentJob.OrgID, "ci_policy.input", "job", parentJob.JobID, models.JSONB{
			"project_id": parentJob.ProjectID, "policy_repository": parentJob.CIRepository,
			"policy_revision": envString(parentJob, "REACTORCIDE_CI_POLICY_REVISION"), "base_sha": parentJob.CISHA,
			"head_sha": stringValue(parentJob.SourceRef), "changed_ci_paths": tf.ChangedCIPaths,
			"actor": parentJob.JobEnvVars["REACTORCIDE_HEAD_ACTOR"], "actor_subjects": parentJob.JobEnvVars["REACTORCIDE_ACTOR_SUBJECTS"],
		})
	}
	if isEvalResult {
		tp.assignTrustedOwnership(ctx, parentJob, batches)
	} else {
		// Only the eval of the trusted CI definition can name an owner. A
		// job's own trigger output never does: a child workflow gets the
		// owner of its parent workflow (database trigger, migration 000053).
		for i := range batches {
			batches[i].Ownership, batches[i].OwnershipSource = nil, ""
		}
	}
	var carried map[string]carriedWorkflow
	if isEvalResult {
		batches, carried = tp.carryUnchangedWorkflows(ctx, parentJob, batches, policyNotes)
	}
	operationID := strings.TrimSpace(tf.OperationID)
	if isEvalResult && stringValue(parentJob.EventID) != "" {
		// The event is the eval's identity. Whatever operation ID the client
		// sent, an eval of one event (first run or retry) is one operation.
		operationID = stringValue(parentJob.EventID)
	}
	for i := range batches {
		batches[i].OperationID = operationID
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
				// False means the workflow did not declare `id:` and its
				// identity came from its filename, so a rename would move it to
				// a different policy rule (or none). Not a refusal -- the policy
				// already decided -- but an auditor reviewing why this workflow
				// matched should be able to see it.
				"workflow_id_explicit": batches[i].ExplicitID,
			})
		}
	}
	if isEvalResult {
		// A violation means a stored policy refused a CI change. A project
		// with no stored policy runs the event's own CI code and has nothing
		// to refuse (Reactorcide CI Policy applies only when a policy exists).
		storedPolicy, _ := policySnapshot(parentJob)
		if storedPolicy == nil {
			pathDecisions = nil
		} else if tf.PolicyCandidates == nil {
			pathDecisions = noCandidatePathDecisions(parentJob, tf.ChangedCIPaths)
		}
		policyViolations := []policyViolation{}
		accepted := []vcs.CIPolicyAcceptance{}
		for _, decision := range pathDecisions {
			if decision.Authority == nil {
				policyViolations = append(policyViolations, policyViolation{Path: decision.Path, WorkflowID: decision.WorkflowID,
					Actor: envString(parentJob, "REACTORCIDE_HEAD_ACTOR"), BaseSHA: parentJob.CISHA, HeadSHA: stringValue(parentJob.SourceRef)})
				continue
			}
			accepted = append(accepted, vcs.CIPolicyAcceptance{Path: decision.Path, WorkflowID: decision.Authority.WorkflowID,
				Rule: decision.Authority.Rule, ApprovalID: decision.Authority.ApprovalID, Reason: decision.Authority.Reason})
			audit.Record(ctx, tp.store, parentJob.OrgID, "ci_policy.accepted_path", "job", parentJob.JobID, models.JSONB{
				"path": decision.Path, "workflow_id": decision.Authority.WorkflowID, "rule": decision.Authority.Rule,
				"approval_id": decision.Authority.ApprovalID, "base_sha": parentJob.CISHA, "head_sha": stringValue(parentJob.SourceRef),
			})
		}
		for _, violation := range policyViolations {
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
			UpdateCIPolicyDecisions(context.Context, *models.Job, []vcs.CIPolicyViolation, []vcs.CIPolicyAcceptance) error
		}); ok {
			violations := make([]vcs.CIPolicyViolation, len(policyViolations))
			for i, item := range policyViolations {
				violations[i] = vcs.CIPolicyViolation{Path: item.Path, WorkflowID: item.WorkflowID, Actor: item.Actor,
					Rule: item.Rule, BaseSHA: item.BaseSHA, HeadSHA: item.HeadSHA}
			}
			if err := updater.UpdateCIPolicyDecisions(ctx, parentJob, violations, accepted); err != nil {
				logging.Log.WithError(err).Warn("Could not publish CI policy status")
			}
		}
	}

	if isEvalResult {
		// Owner acceptance of path dependencies (path_acceptance.go).
		batches = tp.applyPathAcceptance(ctx, parentJob, &tf, batches, policyNotes)
	}

	if isEvalResult {
		// Under a shed level that skips advisory workflows, only required
		// workflows run (advisory_shed.go).
		batches = tp.skipAdvisoryWhenShedding(ctx, parentJob, batches, policyNotes)
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
			tp.recordEvalDecisions(ctx, parentJob, &tf, batches, policyNotes, nil, carried)
			if err := tp.completeReportGeneration(ctx, parentJob); err != nil {
				return createdJobIDs, err
			}
		}
		return createdJobIDs, nil
	}

	var createdJobIDs []string
	var outcomes []TriggerWorkflowOutcome
	for i := range batches {
		ids, wf, err := tp.processWorkflowBatch(ctx, parentJob, &batches[i], workspaceDir)
		if err != nil {
			var recorded *recordedWorkflowFailure
			if wf != nil && errors.As(err, &recorded) {
				createdJobIDs = append(createdJobIDs, ids...)
				outcome := workflowOutcome(wf, ids, recorded.Disposition, recorded.Reason)
				outcomes = append(outcomes, outcome)
				if result != nil {
					result.Workflows = append(result.Workflows, outcome)
				}
				logging.Log.WithFields(map[string]interface{}{
					"parent_job_id":        parentJob.JobID,
					"workflow_id":          wf.WorkflowID,
					"workflow_security_id": wf.WorkflowSecurityID,
					"workflow_name":        wf.Name,
					"disposition":          recorded.Disposition,
					"reason":               recorded.Reason,
				}).Warn("Workflow was recorded without starting all jobs")
				audit.Record(ctx, tp.store, parentJob.OrgID, "workflow."+recorded.Disposition, "workflow", wf.WorkflowID, models.JSONB{
					"workflow_security_id": wf.WorkflowSecurityID,
					"workflow_name":        wf.Name,
					"node_id":              recorded.NodeID,
					"node_name":            recorded.NodeName,
					"profile":              wf.ExecutionProfile,
					"policy_rule_id":       wf.PolicyRuleID,
					"reason":               recorded.Reason,
				})
				continue
			}
			return createdJobIDs, err
		}
		createdJobIDs = append(createdJobIDs, ids...)
		if wf != nil {
			outcome := workflowOutcome(wf, ids, "accepted", "")
			outcomes = append(outcomes, outcome)
			if result != nil {
				result.Workflows = append(result.Workflows, outcome)
			}
		}
	}
	if isEvalResult {
		tp.recordEvalDecisions(ctx, parentJob, &tf, batches, policyNotes, outcomes, carried)
		if err := tp.completeReportGeneration(ctx, parentJob); err != nil {
			return createdJobIDs, err
		}
	}
	return createdJobIDs, nil
}

func workflowOutcome(wf *models.WorkflowInstance, jobIDs []string, disposition, reason string) TriggerWorkflowOutcome {
	ids := append([]string(nil), jobIDs...)
	if ids == nil {
		ids = []string{}
	}
	return TriggerWorkflowOutcome{
		WorkflowID: wf.WorkflowID, WorkflowSecurityID: wf.WorkflowSecurityID,
		Name: wf.Name, Status: wf.Status, Disposition: disposition,
		Reason: reason, CreatedJobIDs: ids,
	}
}

func (tp *TriggerProcessor) applyBaseAuthority(parentJob *models.Job, spec *triggerWorkflowSpec, profile string) {
	spec.CIOrigin = "base"
	spec.CIRepository = parentJob.CIRepository
	spec.CISHA = parentJob.CISHA
	spec.ExecutionProfile = profile
	spec.WorkerClass = "default"
	spec.PolicyRevision = ""
	spec.PolicyRuleID = ""
	spec.ApprovalID = nil
	spec.NodeAuthority = nil
}

func policySnapshot(parentJob *models.Job) (*cipolicy.Policy, error) {
	policyDocument := envString(parentJob, "REACTORCIDE_CI_POLICY")
	policyRevision := envString(parentJob, "REACTORCIDE_CI_POLICY_REVISION")
	if policyDocument == "" && policyRevision == "" {
		return nil, nil
	}
	policy, err := cipolicy.ParseDocument([]byte(policyDocument))
	if err != nil || policyDocument == "" || policyRevision == "" || policy.Revision != policyRevision {
		return nil, fmt.Errorf("coordinator CI policy snapshot failed integrity validation")
	}
	return policy, nil
}

// noCandidatePathDecisions decides the changed CI files of an evaluation
// with no head candidates: only trusted base CI runs. override-ci still
// accepts the changes of its head. When the trusted CI is the event's own
// revision (a push to the default branch, a tag), the changes already run
// and there is nothing to decide.
func noCandidatePathDecisions(parentJob *models.Job, changedCIPaths []string) []ciPathDecision {
	runAuthority, _ := runAuthoritySnapshot(parentJob)
	if runAuthority.Gated || (parentJob.CISHA != "" && parentJob.CISHA == stringValue(parentJob.SourceRef)) {
		return nil
	}
	var decisions []ciPathDecision
	for _, pathValue := range uniqueNonEmpty(changedCIPaths) {
		decision := ciPathDecision{Path: pathValue}
		if runAuthority.Override != nil {
			decision.Authority = &ciPathAuthority{Rule: overrideRuleID, ApprovalID: runAuthority.Override.ApprovalID,
				Reason: fmt.Sprintf("override-ci approved by %s", runAuthority.Override.Person)}
		}
		decisions = append(decisions, decision)
	}
	return decisions
}

func candidateMap(candidates []triggerWorkflowSpec) (map[string]triggerWorkflowSpec, error) {
	result := make(map[string]triggerWorkflowSpec, len(candidates))
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.ID) == "" {
			return nil, fmt.Errorf("CI policy candidate is missing a workflow security ID")
		}
		if _, exists := result[candidate.ID]; exists {
			return nil, fmt.Errorf("duplicate CI policy candidate workflow ID %q", candidate.ID)
		}
		if candidate.CIOrigin != "" || candidate.PolicyRevision != "" || candidate.PolicyRuleID != "" || candidate.ApprovalID != nil {
			return nil, fmt.Errorf("CI policy candidate %q contains an authority decision", candidate.ID)
		}
		for _, job := range candidate.Jobs {
			if job.CIOrigin != "" || job.ExecutionProfile != "" {
				return nil, fmt.Errorf("CI policy candidate %q node %q contains an authority decision", candidate.ID, job.JobName)
			}
		}
		result[candidate.ID] = candidate
	}
	return result, nil
}

// selectPolicyCandidates decides, per workflow, whether the head or the base
// CI definition runs. It also returns one structured policy reason per
// selected workflow, keyed by workflow security ID, so the event can explain
// the decision later.
//
// Policy applies only when the project stores one. Without a stored policy
// the eval job already runs the event's own CI code (or the project's
// configured CI source), so the evaluator sends no candidates; if a stale
// evaluator sends some anyway, the base definitions run.
func (tp *TriggerProcessor) selectPolicyCandidates(ctx context.Context, parentJob *models.Job, candidates *policyCandidateSet, changedCIPaths []string) ([]triggerWorkflowSpec, []ciPathDecision, map[string]models.DecisionReason, error) {
	notes := map[string]models.DecisionReason{}
	metadata, err := vcs.MetadataFromJob(parentJob)
	if err != nil || metadata == nil || !metadata.IsEval || parentJob.ProjectID == nil {
		return nil, nil, nil, fmt.Errorf("CI policy candidates require a project evaluation job")
	}
	baseByID, err := candidateMap(candidates.Base)
	if err != nil {
		return nil, nil, nil, err
	}
	headByID, err := candidateMap(candidates.Head)
	if err != nil {
		return nil, nil, nil, err
	}

	defaultProfile := "standard"
	policy, err := policySnapshot(parentJob)
	if err != nil {
		return nil, nil, nil, err
	}
	revision := ""
	if policy != nil {
		// defaults.by_ref_class can select the profile by ref class.
		defaultProfile, _ = policy.DefaultProfileFor(parentJob.RefClasses)
		revision = policy.Revision
	}
	runAuthority, err := runAuthoritySnapshot(parentJob)
	if err != nil {
		return nil, nil, nil, err
	}
	if runAuthority.Profile != "" {
		defaultProfile = runAuthority.Profile
	}

	// Approvals bind to the head SHA only. The current actions are loaded at
	// decision time; a revoke made after the event still takes effect.
	approvals := []models.CIApproval(nil)
	decisionTime := time.Now().UTC()
	headSHA := stringValue(parentJob.SourceRef)
	if (policy != nil || runAuthority.OwnerOverride != nil) && metadata.PRNumber > 0 {
		if approvalStore, ok := tp.store.(triggerApprovalListStore); ok {
			loaded, loadErr := approvalStore.ListCIApprovalsForHead(ctx, *parentJob.ProjectID, metadata.PRNumber, headSHA)
			if loadErr != nil {
				return nil, nil, nil, fmt.Errorf("load coordinator CI approvals: %w", loadErr)
			}
			// An approval binds to the exact head: the organization, project,
			// pull request, head repository, and head SHA of this eval.
			headRepository := envString(parentJob, "REACTORCIDE_HEAD_REPOSITORY")
			for _, approval := range loaded {
				if approval.OrgID == parentJob.OrgID && approval.ProjectID == *parentJob.ProjectID && approval.PRNumber == metadata.PRNumber &&
					approval.HeadSHA == headSHA && (headRepository == "" || approval.HeadRepository == headRepository) {
					approvals = append(approvals, approval)
				}
			}
		}
	}
	approvedHeadCI := map[string]bool{}
	for _, approval := range approvals {
		if approval.Kind != models.CIApprovalKindHeadCI {
			continue
		}
		for _, subject := range approval.Subjects {
			if !approvedHeadCI[subject] && approvalstate.Granted(approvals, models.CIApprovalKindHeadCI, headSHA, []string{subject}, decisionTime) != nil {
				approvedHeadCI[subject] = true
			}
		}
	}

	claimedPaths := map[string]bool{}
	for _, byID := range []map[string]triggerWorkflowSpec{baseByID, headByID} {
		for _, candidate := range byID {
			for _, dependency := range candidate.DependencyPaths {
				claimedPaths[dependency] = true
			}
		}
	}
	sharedPaths := []string{}
	for _, changed := range uniqueNonEmpty(changedCIPaths) {
		if !claimedPaths[changed] {
			sharedPaths = append(sharedPaths, changed)
		}
	}

	ids := make([]string, 0, len(baseByID)+len(headByID))
	seenIDs := map[string]bool{}
	for id := range baseByID {
		seenIDs[id] = true
		ids = append(ids, id)
	}
	for id := range headByID {
		if !seenIDs[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	actorSubjects := parseStringSet(parentJob.JobEnvVars["REACTORCIDE_ACTOR_SUBJECTS"])
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
	// decidePolicy asks the stored policy whether the head may change the CI
	// of workflow id. dependencies are the CI paths the workflow uses.
	decidePolicy := func(id string, dependencies, ownerSubjects []string) (cipolicy.Decision, error) {
		allPolicyPaths := uniqueNonEmpty(append(append([]string{}, changedCIPaths...), dependencies...))
		return cipolicy.Decide(policy, cipolicy.Facts{
			WorkflowID: id, ChangedCIPaths: allPolicyPaths,
			Event: envString(parentJob, "REACTORCIDE_EVENT_TYPE"), BaseBranch: baseBranch,
			HeadRepositoryRelation: relation, ActorSubjects: actorSubjects,
			ApprovalSubjects:      approvedHeadCI,
			RefClasses:            parentJob.RefClasses,
			WorkflowOwnerSubjects: ownerSubjects,
		})
	}
	// ownerSubjectsOf gives the owners of the TRUSTED definition of a
	// workflow (never the head's).
	ownerSubjectsOf := func(base triggerWorkflowSpec, hasBase bool) []string {
		if !hasBase {
			return nil
		}
		snapshot, ownerErr := owners.FromSpec(base.Owners, base.Contact, parentJob.CISHA)
		if ownerErr != nil {
			return nil
		}
		return owners.Subjects(snapshot)
	}
	// authorizedBy records, per workflow, the authority that accepted the
	// head's change to the CI that the workflow uses.
	authorizedBy := map[string]ciPathAuthority{}
	selected := make([]triggerWorkflowSpec, 0, len(ids))
	for _, id := range ids {
		base, hasBase := baseByID[id]
		head, hasHead := headByID[id]
		useHead := false
		var decision cipolicy.Decision
		var approvalID *string
		// considered is true when head CI was a real option for this workflow,
		// so the reason can say why it was or was not granted.
		considered := false
		baseReasons := []string{"no changed CI path is used by this workflow"}
		// The coordinator's stored policy is the ONLY authority on whether head
		// CI is granted. A workflow's declared identity is a fact the policy
		// matches against, not a second gate the repository holds.
		//
		// This used to also require head.ExplicitID -- that the workflow
		// declared its own `id:` rather than having one derived from its
		// filename. That made a repository able to veto, by omission, a grant
		// the coordinator had made: a policy could name a workflow and simply
		// never fire, with nothing to explain why. It was also not the security
		// control it looked like, because an actor who passes `actors.any` can
		// write any `id:` they like in their own pull request, so an explicit id
		// never prevented one workflow from claiming another's identity.
		//
		// What still gates every head-CI grant is the policy itself: actors,
		// workflows, paths, events, base_branches, head_repository and any
		// required approval. Removing this changes none of them.
		//
		// An implicit id is still worth KNOWING about, so it rides into the
		// ci_policy.decision audit record instead (see recordPolicyDecision's
		// "workflow_id_explicit"). Declaring `id:` remains the documented
		// practice: it keeps a rename from silently changing a workflow's
		// security identity.
		if !hasHead {
			baseReasons = []string{"the head revision has no workflow with this ID"}
		} else if !head.EventMatched {
			baseReasons = []string{"the head definition does not match this event"}
		} else if policy == nil && runAuthority.Override == nil {
			baseReasons = []string{"no coordinator CI policy is stored for this project"}
		}
		headChanged := false
		if hasHead && head.EventMatched {
			dependencies := uniqueNonEmpty(append(append([]string{}, head.DependencyPaths...), sharedPaths...))
			headChanged = len(changedCIPaths) > 0 && intersects(changedCIPaths, dependencies)
		}
		// The owner of the TRUSTED definition of this workflow (never the
		// head's). It stands in for workflow_owner in approver lists.
		ownerSubjects := ownerSubjectsOf(base, hasBase)
		// override-ci by an owner of this workflow, when override_approvers
		// names workflow_owner. It grants head CI for this workflow only.
		var ownerOverride *models.CIApproval
		if headChanged && runAuthority.Override == nil && runAuthority.OwnerOverride != nil && len(ownerSubjects) > 0 {
			ownerOverride = approvalstate.Granted(approvals, models.CIApprovalKindHeadCI, headSHA, ownerSubjects, decisionTime)
		}
		if runAuthority.Gated && runAuthority.Override == nil && ownerOverride == nil && headChanged {
			// approve-ci runs trusted base CI. The head's CI changes need
			// override-ci, also when a policy rule would grant them.
			considered = true
			baseReasons = []string{"the run gate holds this pull request; approve-ci runs trusted base CI, and the head's CI changes need override-ci"}
		} else if policy != nil && headChanged {
			dependencies := uniqueNonEmpty(append(append([]string{}, head.DependencyPaths...), sharedPaths...))
			{
				considered = true
				decision, err = decidePolicy(id, dependencies, ownerSubjects)
				if err != nil {
					return nil, nil, nil, fmt.Errorf("workflow %q policy decision failed: %w", id, err)
				}
				baseReasons = decision.Reasons
				if decision.Allowed {
					useHead = true
					if len(decision.ApprovalSubjects) > 0 {
						useHead = false
						baseReasons = []string{fmt.Sprintf("policy rule %s requires an approval from %s", decision.RuleID, strings.Join(decision.ApprovalSubjects, ", "))}
						if granted := approvalstate.Granted(approvals, models.CIApprovalKindHeadCI, headSHA, decision.ApprovalSubjects, decisionTime); granted != nil {
							value := granted.ApprovalID
							approvalID = &value
							useHead = true
						}
					}
				}
			}
		}

		if !useHead && headChanged && runAuthority.Override != nil {
			// override-ci: the head's own CI runs in its entirety with the
			// override profile. A matching policy rule above wins, because it
			// can be narrower (base nodes, its own profile).
			considered = true
			decision = cipolicy.Decision{CISource: "head", Profile: runAuthority.Override.Profile,
				WorkerClass: "default", RuleID: overrideRuleID, Allowed: true,
				Reasons: []string{fmt.Sprintf("override-ci approved by %s", runAuthority.Override.Person)}}
			value := runAuthority.Override.ApprovalID
			approvalID = &value
			useHead = true
		}
		if !useHead && headChanged && ownerOverride != nil {
			// override-ci by an owner of the trusted definition of this
			// workflow (A6). Only this workflow runs head CI.
			considered = true
			decision = cipolicy.Decision{CISource: "head", Profile: runAuthority.OwnerOverride.Profile,
				WorkerClass: "default", RuleID: overrideRuleID, Allowed: true,
				Reasons: []string{fmt.Sprintf("override-ci approved by %s, an owner of this workflow", ownerOverride.Person)}}
			value := ownerOverride.ApprovalID
			approvalID = &value
			useHead = true
		}

		if useHead && len(decision.BaseNodes) > 0 {
			if !hasBase {
				useHead = false
				baseReasons = []string{"policy rule grants base nodes, but the base revision has no such workflow"}
			} else {
				baseJobs := map[string]triggerJobSpec{}
				for _, job := range base.Jobs {
					baseJobs[job.JobName] = job
				}
				for name := range decision.BaseNodes {
					if _, ok := baseJobs[name]; !ok {
						useHead = false
						baseReasons = []string{fmt.Sprintf("policy rule grants base node %q, which the base workflow does not have", name)}
						break
					}
				}
				if useHead {
					claimed := map[string]bool{}
					for i := range head.Jobs {
						grant, ok := decision.BaseNodes[head.Jobs[i].JobName]
						if !ok {
							continue
						}
						head.Jobs[i] = baseJobs[head.Jobs[i].JobName]
						head.Jobs[i].CIOrigin = grant.CISource
						head.Jobs[i].ExecutionProfile = grant.Profile
						head.Jobs[i].WorkerClass = grant.WorkerClass
						head.Jobs[i].CISourceURL = parentJob.CIRepository
						head.Jobs[i].CISourceRef = parentJob.CISHA
						claimed[head.Jobs[i].JobName] = true
					}
					for name, grant := range decision.BaseNodes {
						if claimed[name] {
							continue
						}
						job := baseJobs[name]
						job.CIOrigin = grant.CISource
						job.ExecutionProfile = grant.Profile
						job.WorkerClass = grant.WorkerClass
						job.CISourceURL = parentJob.CIRepository
						job.CISourceRef = parentJob.CISHA
						head.Jobs = append(head.Jobs, job)
					}
				}
			}
		}
		if useHead {
			head.CIOrigin = "head"
			head.CIRepository = stringValue(parentJob.SourceURL)
			head.CISHA = stringValue(parentJob.SourceRef)
			head.ExecutionProfile = decision.Profile
			head.WorkerClass = decision.WorkerClass
			head.PolicyRevision = revision
			head.PolicyRuleID = decision.RuleID
			head.ApprovalID = approvalID
			head.NodeAuthority = decision.BaseNodes
			// A pull request cannot change the owner of the workflow it runs:
			// the owner comes from the trusted base definition, or is "none"
			// for a workflow that only the head has.
			if hasBase {
				tp.setOwnershipFromSpec(parentJob, &head, base.Owners, base.Contact)
			} else {
				tp.setOwnershipFromSpec(parentJob, &head, nil, nil)
			}
			selected = append(selected, head)
			reasons := decision.Reasons
			if len(reasons) == 0 {
				reasons = []string{fmt.Sprintf("policy rule %s granted head CI", decision.RuleID)}
			}
			notes[id] = policyNote("head", decision.Profile, decision.WorkerClass, decision.RuleID, revision, reasons...)
			authorizedBy[id] = ciPathAuthority{WorkflowID: id, Rule: decision.RuleID, ApprovalID: stringValue(approvalID), Reason: reasons[0]}
			continue
		}
		if hasBase && base.EventMatched {
			tp.applyBaseAuthority(parentJob, &base, defaultProfile)
			if policy != nil {
				base.PolicyRevision = policy.Revision
			}
			tp.setOwnershipFromSpec(parentJob, &base, base.Owners, base.Contact)
			selected = append(selected, base)
			if considered && len(baseReasons) > 0 {
				baseReasons = append([]string{"head CI was not granted"}, baseReasons...)
			}
			notes[id] = policyNote("base", defaultProfile, base.WorkerClass, "", base.PolicyRevision, baseReasons...)
		}
	}

	// Only a stored policy can refuse a CI change. Without one there is no
	// decision to report. A gated run reports none either: the gate, not the
	// policy, held the head's CI.
	if policy == nil || runAuthority.Gated {
		return selected, nil, notes, nil
	}

	// A workflow that does not run for this event (a release workflow on a
	// pull request) still owns its CI files. Its change needs an authority
	// too: an owner override or a policy rule for that workflow. Nothing of
	// it runs.
	dependenciesOf := map[string][]string{}
	for _, id := range ids {
		base, hasBase := baseByID[id]
		head, hasHead := headByID[id]
		deps := append([]string{}, sharedPaths...)
		if hasHead {
			deps = append(deps, head.DependencyPaths...)
		}
		if hasBase {
			deps = append(deps, base.DependencyPaths...)
		}
		dependenciesOf[id] = uniqueNonEmpty(deps)
		if _, ok := authorizedBy[id]; ok || runAuthority.Override != nil {
			continue
		}
		if hasHead && head.EventMatched {
			// Decided above, for the run.
			continue
		}
		if len(changedCIPaths) == 0 || !intersects(changedCIPaths, dependenciesOf[id]) {
			continue
		}
		ownerSubjects := ownerSubjectsOf(base, hasBase)
		if runAuthority.OwnerOverride != nil && len(ownerSubjects) > 0 {
			if granted := approvalstate.Granted(approvals, models.CIApprovalKindHeadCI, headSHA, ownerSubjects, decisionTime); granted != nil {
				authorizedBy[id] = ciPathAuthority{WorkflowID: id, Rule: overrideRuleID, ApprovalID: granted.ApprovalID,
					Reason: fmt.Sprintf("override-ci approved by %s, an owner of this workflow", granted.Person)}
				continue
			}
		}
		decision, decideErr := decidePolicy(id, dependenciesOf[id], ownerSubjects)
		if decideErr != nil {
			return nil, nil, nil, fmt.Errorf("workflow %q policy decision failed: %w", id, decideErr)
		}
		if !decision.Allowed {
			continue
		}
		authority := ciPathAuthority{WorkflowID: id, Rule: decision.RuleID,
			Reason: fmt.Sprintf("policy rule %s accepts the change", decision.RuleID)}
		if len(decision.ApprovalSubjects) > 0 {
			granted := approvalstate.Granted(approvals, models.CIApprovalKindHeadCI, headSHA, decision.ApprovalSubjects, decisionTime)
			if granted == nil {
				continue
			}
			authority.ApprovalID = granted.ApprovalID
			authority.Reason = fmt.Sprintf("policy rule %s accepts the change with the approval of %s", decision.RuleID, granted.Person)
		}
		authorizedBy[id] = authority
	}

	decisions := make([]ciPathDecision, 0, len(changedCIPaths))
	for _, pathValue := range uniqueNonEmpty(changedCIPaths) {
		decision := ciPathDecision{Path: pathValue}
		if runAuthority.Override != nil {
			// override-ci covers the whole head.
			decision.Authority = &ciPathAuthority{Rule: overrideRuleID, ApprovalID: runAuthority.Override.ApprovalID,
				Reason: fmt.Sprintf("override-ci approved by %s", runAuthority.Override.Person)}
			decisions = append(decisions, decision)
			continue
		}
		// The path is accepted when an authority accepted the change of a
		// workflow that uses it. Without one, the first workflow that uses
		// it is the one named in the refusal.
		for _, id := range ids {
			if !containsString(dependenciesOf[id], pathValue) {
				continue
			}
			if authority, ok := authorizedBy[id]; ok {
				decision.Authority = &authority
				break
			}
			if decision.WorkflowID == "" {
				decision.WorkflowID = id
			}
		}
		decisions = append(decisions, decision)
	}
	return selected, decisions, notes, nil
}

func (tp *TriggerProcessor) validateWorkflowAuthority(ctx context.Context, parentJob *models.Job, spec *triggerWorkflowSpec, _ []string) error {
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
		if spec.PolicyRuleID == "" || (spec.PolicyRevision == "" && spec.PolicyRuleID != overrideRuleID) {
			return fmt.Errorf("head workflow %q lacks a policy decision", spec.ID)
		}
	}
	if spec.CIRepository != expectedRepository || spec.CISHA != expectedSHA || expectedRepository == "" || expectedSHA == "" {
		return fmt.Errorf("workflow %q CI repository or SHA does not match the event authority", spec.ID)
	}
	if spec.ExecutionProfile == "" || spec.WorkerClass == "" {
		return fmt.Errorf("workflow %q must select an execution profile and worker class", spec.ID)
	}
	if spec.CIOrigin == "head" && (spec.PolicyRuleID == "" || (spec.PolicyRevision == "" && spec.PolicyRuleID != overrideRuleID)) {
		return fmt.Errorf("head workflow %q lacks a coordinator policy decision", spec.ID)
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
	return tp.validateNodeAuthority(ctx, parentJob, spec, spec.NodeAuthority)
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
		{"storage_request", job.ResourceStorageRequest, resources.ParseMemory},
		{"storage_limit", job.ResourceStorageLimit, resources.ParseMemory},
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
				// Keep the node, so that its failure is recorded on the
				// run (not admitted, with the load error). Do not drop it.
				if spec.JobName == "" {
					spec.JobName = spec.JobFile
				}
				// The error names the job_file, not the host workspace path.
				spec.LoadError = strings.ReplaceAll(err.Error(), filepath.Join(workspaceDir, "src")+string(filepath.Separator), "")
				specs = append(specs, spec)
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
func (tp *TriggerProcessor) processWorkflowBatch(ctx context.Context, parentJob *models.Job, spec *triggerWorkflowSpec, workspaceDir string) ([]string, *models.WorkflowInstance, error) {
	if len(spec.Jobs) == 0 {
		return nil, nil, nil
	}
	txStore, ok := tp.store.(workflowTransactionStore)
	if !ok {
		return tp.processWorkflowBatchUnit(ctx, parentJob, spec, workspaceDir)
	}

	var ids []string
	var wf *models.WorkflowInstance
	var batchErr error
	err := txStore.InTransaction(ctx, func(txCtx context.Context) error {
		txCtx = context.WithValue(txCtx, deferWorkflowPublishContextKey{}, true)
		ids, wf, batchErr = tp.processWorkflowBatchUnit(txCtx, parentJob, spec, workspaceDir)
		if batchErr == nil {
			return nil
		}
		var recorded *recordedWorkflowFailure
		if errors.As(batchErr, &recorded) {
			// The failed node and its reason are the intended durable outcome.
			return nil
		}
		return batchErr
	})
	if err != nil {
		return nil, nil, err
	}
	// Publish only after the workflow transaction commits. This prevents a
	// VCS target from linking to a workflow that a later rollback removes.
	if wf != nil {
		if publishErr := tp.refreshWorkflowStatus(ctx, wf); publishErr != nil {
			return ids, wf, publishErr
		}
	}
	return ids, wf, batchErr
}

func (tp *TriggerProcessor) processWorkflowBatchUnit(ctx context.Context, parentJob *models.Job, spec *triggerWorkflowSpec, workspaceDir string) ([]string, *models.WorkflowInstance, error) {
	if len(spec.Jobs) == 0 {
		return nil, nil, nil
	}
	specs := spec.Jobs
	if spec.TriggerType != "runnerlib_eval" {
		var err error
		specs, err = tp.resolveJobSpecs(spec.Jobs, workspaceDir)
		if err != nil {
			return nil, nil, err
		}
	}
	wf, err := tp.ensureWorkflow(ctx, parentJob, spec)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create workflow: %w", err)
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
			return nil, wf, fmt.Errorf("failed to add workflow vars: %w", err)
		}
	}
	existingNodes, err := tp.workflowStore()
	if err != nil {
		return nil, wf, err
	}
	registered, err := existingNodes.ListWorkflowNodes(ctx, wf.WorkflowID)
	if err != nil {
		return nil, wf, err
	}
	if len(registered) > 0 {
		ids, err := tp.evaluateWorkflow(ctx, wf)
		return ids, wf, err
	}
	if err := tp.createWorkflowNodes(ctx, wf, specs); err != nil {
		return nil, wf, fmt.Errorf("failed to create workflow nodes: %w", err)
	}
	if err := tp.refreshRootForChildRegistration(ctx, wf); err != nil {
		return nil, wf, fmt.Errorf("failed to register child workflow with root: %w", err)
	}
	ids, err := tp.evaluateWorkflow(ctx, wf)
	return ids, wf, err
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
		JobName:            def.Name,
		ContainerImage:     def.Job.Image,
		JobCommand:         def.Job.Command,
		CodeDir:            def.Job.CodeDir,
		JobDir:             def.Job.JobDir,
		WorkingDir:         def.Job.WorkingDir,
		RunAsUser:          runAsUserFromSpec(def.Job.RunAs),
		RunAsAdministrator: runAsAdministratorFromSpec(def.Job.RunAs),
		Timeout:            def.Job.Timeout,
		Priority:           def.Job.Priority,
		Capabilities:       def.Job.Capabilities,
		ImagePullSecrets:   def.Job.ImagePullSecrets,
		WorkerClass:        def.Job.WorkerClass,
		Env:                def.Environment,
		Characteristics:    def.Job.Characteristics,
		Resources:          def.Job.Resources,
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
	if overlay.RunAsAdministrator != nil {
		v := *overlay.RunAsAdministrator
		result.RunAsAdministrator = &v
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
	// A job with egress control waits for a worker with an egress proxy.
	egress.RouteJob(ctx, tp.store, job)
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

// resolveChildImage resolves the image of a triggered child job. A child
// that names no image inherits the image of its parent: then it carries
// the resolved image of the parent (reference, alias, digest), like a
// retry, and does not resolve the inherited reference again. Else, after
// a promotion, the old pinned reference of the parent would be refused,
// and the child would get another digest than its parent. The carry needs
// the same kind and platform as the parent; else the child resolves as
// any other job. CheckCarried refuses the child when no worker can get the
// image (the alias of the parent was deleted). While the parent is not
// terminal, the drain keeps its digest.
func (tp *TriggerProcessor) resolveChildImage(ctx context.Context, job *models.Job, spec triggerJobSpec, parentJob *models.Job) error {
	if spec.ContainerImage == "" && parentJob != nil {
		parent := imagealias.CarriedFromJob(parentJob)
		if parent.Digest != "" && parent.Ref != "" && imagealias.JobReference(job.RunnerImage) == imagealias.JobReference(parent.Ref) &&
			imagealias.JobPlatform(job) == imagealias.JobPlatform(parentJob) {
			imagealias.ApplyCarried(job, parent)
			return imagealias.CheckCarried(ctx, tp.store, job)
		}
	}
	_, err := imagealias.ResolveJob(ctx, tp.store, job)
	return err
}

// createAndSubmitJob creates a single job from a trigger spec and submits it to Corndogs.
// Returns the created job ID on success.
func (tp *TriggerProcessor) createAndSubmitJob(ctx context.Context, spec triggerJobSpec, parentJob *models.Job) (string, error) {
	if spec.LoadError != "" {
		return "", fmt.Errorf("job_file %q could not be loaded: %s", spec.JobFile, spec.LoadError)
	}
	job, err := tp.buildJobFromTrigger(spec, parentJob)
	if err != nil {
		return "", fmt.Errorf("failed to build triggered job: %w", err)
	}

	// The image alias of the job, before the queue (internal/imagealias).
	if err := tp.resolveChildImage(ctx, job, spec, parentJob); err != nil {
		return "", fmt.Errorf("triggered job %q: %w", job.Name, err)
	}
	if err := tp.resolveJobQueue(ctx, job); err != nil {
		return "", err
	}
	// The lane and group shifts give the queue priority. The priority of
	// the trigger spec moves the job inside its lane only (docs/priority.md).
	priority.Assign(ctx, tp.store, job)

	if err := tp.store.CreateJob(ctx, job); err != nil {
		return "", fmt.Errorf("failed to create job in database: %w", err)
	}
	// Announce the job now, not at its first status change. job_update fires
	// on a TRANSITION, so without this a freshly submitted job stays invisible
	// in an open list view until something else happens to it.
	workflowevents.JobCreated(ctx, job)

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
		job.SetFailure(models.FailureClassPlatform, "queue submission")
		if err := tp.store.UpdateJob(ctx, job); err != nil {
			logging.Log.WithError(err).WithField("job_id", job.JobID).Error("Failed to record queue submission failure of triggered job")
		}
	} else if err := RecordQueueSubmission(ctx, tp.store, job, task.Uuid, task.CurrentState); err != nil {
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
	// A trigger cannot replace a coordinator event fact: the parent's value
	// is kept. A fact the parent does not have stays unset, except event
	// context for a parent with no CI event (see triggerMaySupplyEventEnv).
	ignoredFacts := []string{}
	parentHasEvent := stringValue(parentJob.EventID) != ""
	for k, v := range spec.Env {
		if isReservedEventEnv(k) {
			parentValue, inherited := envVars[k]
			if !inherited && triggerMaySupplyEventEnv(k, parentHasEvent) {
				envVars[k] = v
			} else if !inherited || fmt.Sprint(parentValue) != v {
				ignoredFacts = append(ignoredFacts, k)
			}
			continue
		}
		envVars[k] = v
	}
	if len(ignoredFacts) > 0 {
		sort.Strings(ignoredFacts)
		logging.Log.WithFields(map[string]interface{}{
			"parent_job_id": parentJob.JobID, "job_name": spec.JobName, "ignored_env": ignoredFacts,
		}).Warn("Ignored trigger environment that tries to set coordinator event facts")
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
		storageRequest, storageLimit, err := resources.ParseStorage(spec.Resources)
		if err != nil {
			return nil, fmt.Errorf("invalid resources in triggered job %q: %w", spec.JobName, err)
		}
		job.ResourceStorageRequest = storageRequest
		job.ResourceStorageLimit = storageLimit
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
	if spec.RunAsAdministrator != nil {
		v := *spec.RunAsAdministrator
		job.RunAsAdministrator = &v
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
	if job.RunAsAdministrator != nil {
		payload.Config["run_as_administrator"] = *job.RunAsAdministrator
	}

	return payload
}
