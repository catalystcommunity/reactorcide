package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workflowengine"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

const (
	defaultWorkflowName = "Reactorcide Jobs"
)

type workflowStatusUpdater interface {
	UpdateWorkflowStatus(ctx context.Context, workflow *models.WorkflowInstance, nodes []models.WorkflowNode) error
}

type workflowStore interface {
	CreateWorkflowInstance(ctx context.Context, wf *models.WorkflowInstance) error
	GetWorkflowInstance(ctx context.Context, workflowID string) (*models.WorkflowInstance, error)
	// GetWorkflowInstanceByParentJobAndName find-or-create key for multi-workflow
	// spawning: one eval (parent job) owns at most one workflow per name.
	// Returns store.ErrNotFound when none exists yet.
	GetWorkflowInstanceByParentJobAndName(ctx context.Context, parentJobID, name string) (*models.WorkflowInstance, error)
	UpdateWorkflowInstance(ctx context.Context, wf *models.WorkflowInstance) error
	CreateWorkflowNode(ctx context.Context, node *models.WorkflowNode) error
	UpdateWorkflowNode(ctx context.Context, node *models.WorkflowNode) error
	ListWorkflowNodes(ctx context.Context, workflowID string) ([]models.WorkflowNode, error)
	GetWorkflowNodeByJobID(ctx context.Context, jobID string) (*models.WorkflowNode, error)
	GetWorkflowVars(ctx context.Context, workflowID string) (map[string]models.JSONB, error)
	UpsertWorkflowVar(ctx context.Context, v *models.WorkflowVar) error
	CreateWorkflowEvent(ctx context.Context, event *models.WorkflowEvent) error
	ListWorkflowEvents(ctx context.Context, workflowID string, limit, offset int) ([]models.WorkflowEvent, error)
}

type workflowHistoryStore interface {
	GetLastSuccessfulWorkflowNodeDuration(ctx context.Context, wf *models.WorkflowInstance, nodeName string) (*int64, error)
}

type workflowGraphStore interface {
	ListWorkflowDescendants(ctx context.Context, rootWorkflowID string) ([]models.WorkflowInstance, error)
	ListWorkflowNodesBySubtree(ctx context.Context, workflowID string) ([]models.WorkflowNode, error)
}

type workflowTriggerStore interface {
	GetWorkflowInstanceByTriggerOperation(ctx context.Context, parentJobID, operationID, name string) (*models.WorkflowInstance, error)
	CreateWorkflowInstanceForTrigger(ctx context.Context, wf *models.WorkflowInstance) error
}

type workflowOutputFile struct {
	Vars    map[string]interface{} `json:"vars"`
	Outputs map[string]interface{} `json:"outputs"`
}

func (tp *TriggerProcessor) workflowStore() (workflowStore, error) {
	ws, ok := tp.store.(workflowStore)
	if !ok {
		return nil, fmt.Errorf("store does not support workflows")
	}
	return ws, nil
}

// repoBasename returns the last path component of the parent job's CI/source
// repo URL, with any ".git" suffix removed and casing preserved (e.g.
// "https://github.com/catalystcommunity/reactorcide.git" -> "reactorcide").
// Returns "" when no source URL is set (e.g. a directly-submitted job).
func repoBasename(job *models.Job) string {
	url := ""
	if job.CISourceURL != nil && strings.TrimSpace(*job.CISourceURL) != "" {
		url = strings.TrimSpace(*job.CISourceURL)
	} else if job.SourceURL != nil && strings.TrimSpace(*job.SourceURL) != "" {
		url = strings.TrimSpace(*job.SourceURL)
	}
	if url == "" {
		return ""
	}
	url = strings.TrimRight(url, "/")
	url = strings.TrimSuffix(url, ".git")
	if i := strings.LastIndexAny(url, "/:"); i >= 0 {
		url = url[i+1:]
	}
	return strings.TrimSuffix(url, ".git")
}

func (tp *TriggerProcessor) ensureWorkflow(ctx context.Context, parentJob *models.Job, spec *triggerWorkflowSpec) (*models.WorkflowInstance, error) {
	ws, err := tp.workflowStore()
	if err != nil {
		return nil, err
	}

	// A trigger-declared workflow name (from a .reactorcide workflow YAML) wins.
	// Otherwise fall back to the default, qualified by the repo basename so the
	// PR check reads e.g. "Reactorcide Jobs, repo: reactorcide" instead of a
	// bare, ambiguous "Reactorcide Jobs" -- the basename is the last path
	// component of the source repo (no ".git", casing preserved), not the URL.
	name := defaultWorkflowName
	if base := repoBasename(parentJob); base != "" {
		name = fmt.Sprintf("%s, repo: %s", defaultWorkflowName, base)
	}
	if spec != nil && strings.TrimSpace(spec.Name) != "" {
		name = strings.TrimSpace(spec.Name)
	}

	// Find-or-create by the caller's operation identity when it supplies one.
	// The legacy (parent job, name) key remains for old runnerlib clients.
	// Reprocessing the same operation reuses its workflow. An eval job that is
	// not in a workflow creates a root. A workflow job creates a child.
	if spec != nil && spec.OperationID != "" {
		securityID := strings.TrimSpace(spec.ID)
		if securityID == "" {
			securityID = name
		}
		if ts, ok := tp.store.(workflowTriggerStore); ok {
			if existing, err := ts.GetWorkflowInstanceByTriggerOperation(ctx, parentJob.JobID, spec.OperationID, securityID); err == nil && existing != nil {
				return existing, nil
			} else if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
		}
	} else if existing, err := ws.GetWorkflowInstanceByParentJobAndName(ctx, parentJob.JobID, name); err == nil && existing != nil {
		return existing, nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	parentJobID := parentJob.JobID
	wf := &models.WorkflowInstance{
		UserID:             parentJob.UserID,
		OrgID:              parentJob.OrgID,
		ProjectID:          parentJob.ProjectID,
		ParentJobID:        &parentJobID,
		Name:               name,
		WorkflowSecurityID: name,
		Status:             "evaluating",
		QueueName:          parentJob.QueueName,
		StatusContext:      name,
		TriggerType:        "runnerlib",
		WorkerClass:        parentJob.WorkerClass,
		ExecutionProfile:   parentJob.ExecutionProfile,
		CIOrigin:           parentJob.CIOrigin, CIRepository: parentJob.CIRepository, CISHA: parentJob.CISHA,
		PolicyRevision: parentJob.PolicyRevision, PolicyRuleID: parentJob.PolicyRuleID, ApprovalID: parentJob.ApprovalID,
	}
	if spec != nil {
		if strings.TrimSpace(spec.ID) != "" {
			wf.WorkflowSecurityID = strings.TrimSpace(spec.ID)
		}
		wf.SourceFile = strings.TrimSpace(spec.SourceFile)
		if spec.CIOrigin == "base" || spec.CIOrigin == "head" {
			wf.CIOrigin = spec.CIOrigin
		}
		if spec.CIRepository != "" {
			wf.CIRepository = spec.CIRepository
		}
		if spec.CISHA != "" {
			wf.CISHA = spec.CISHA
		}
		if spec.ExecutionProfile != "" {
			wf.ExecutionProfile = spec.ExecutionProfile
		}
		if spec.WorkerClass != "" {
			wf.WorkerClass = spec.WorkerClass
		}
		wf.PolicyRevision = spec.PolicyRevision
		wf.PolicyRuleID = spec.PolicyRuleID
		wf.ApprovalID = spec.ApprovalID
		wf.TriggerOperationID = spec.OperationID
		if spec.TriggerType != "" {
			wf.TriggerType = spec.TriggerType
		}
	}

	if parentJob.WorkflowID != nil && *parentJob.WorkflowID != "" {
		parentWorkflow, getErr := ws.GetWorkflowInstance(ctx, *parentJob.WorkflowID)
		if getErr != nil {
			return nil, fmt.Errorf("load parent workflow: %w", getErr)
		}
		parentWorkflowID := parentWorkflow.WorkflowID
		wf.ParentWorkflowID = &parentWorkflowID
		rootID := parentWorkflow.WorkflowID
		if parentWorkflow.RootWorkflowID != nil && *parentWorkflow.RootWorkflowID != "" {
			rootID = *parentWorkflow.RootWorkflowID
		}
		wf.RootWorkflowID = &rootID
		wf.OriginJobID = parentWorkflow.OriginJobID
		wf.OriginType = parentWorkflow.OriginType
		rootWorkflow, rootErr := ws.GetWorkflowInstance(ctx, rootID)
		if rootErr == nil {
			wf.StatusContext = rootWorkflow.StatusContext
			wf.CommentMarker = rootWorkflow.CommentMarker
			wf.VCSProvider = rootWorkflow.VCSProvider
			wf.VCSRepo = rootWorkflow.VCSRepo
			wf.PRNumber = rootWorkflow.PRNumber
			wf.CommitSHA = rootWorkflow.CommitSHA
		}
	} else {
		wf.OriginJobID = &parentJobID
		wf.OriginType = string(workflowEventType(parentJob))
	}

	if metadata, err := vcs.MetadataFromJob(parentJob); err == nil && metadata != nil {
		wf.VCSProvider = metadata.VCSProvider
		wf.VCSRepo = metadata.Repo
		if metadata.PRNumber > 0 {
			pr := metadata.PRNumber
			wf.PRNumber = &pr
		}
		wf.CommitSHA = metadata.CommitSHA
	}
	if wf.CommitSHA != "" && wf.ParentWorkflowID == nil {
		// Key the comment marker on both the commit and the triggering event
		// type so distinct workflows landing on the same commit (e.g. PR checks
		// vs a post-merge release run — common with rebase merges that preserve
		// the SHA) get separate PR comments instead of clobbering each other,
		// while a redelivered webhook or manual resubmit of the same event
		// reuses the marker and edits the existing comment.
		wf.CommentMarker = fmt.Sprintf("<!-- reactorcide:workflows:%s:%s -->", wf.CommitSHA, workflowEventType(parentJob))
	}

	var createErr error
	if wf.TriggerOperationID != "" {
		if ts, ok := tp.store.(workflowTriggerStore); ok {
			createErr = ts.CreateWorkflowInstanceForTrigger(ctx, wf)
		} else {
			createErr = ws.CreateWorkflowInstance(ctx, wf)
		}
	} else {
		createErr = ws.CreateWorkflowInstance(ctx, wf)
	}
	if createErr != nil {
		return nil, createErr
	}
	tp.recordWorkflowEvent(ctx, wf.WorkflowID, nil, nil, "workflow_evaluated", "created workflow from triggers", models.JSONB{
		"parent_job_id":        parentJob.JobID,
		"name":                 wf.Name,
		"root_workflow_id":     wf.RootWorkflowID,
		"parent_workflow_id":   wf.ParentWorkflowID,
		"trigger_operation_id": wf.TriggerOperationID,
	})
	return wf, nil
}

// workflowEventType returns the generic VCS event that spawned the workflow,
// read from the parent (eval) job's REACTORCIDE_EVENT_TYPE env var. It is folded
// into the PR comment marker so different event types on the same commit do not
// share (and clobber) one comment. Jobs submitted directly through the API/CLI
// carry no VCS event type; they are labeled directly_submitted, which keeps
// their marker distinct but harmless — such jobs have no VCS provider/repo/
// commit context, so the status updater posts nothing for them.
func workflowEventType(parentJob *models.Job) vcs.EventType {
	if parentJob != nil {
		if raw, ok := parentJob.JobEnvVars["REACTORCIDE_EVENT_TYPE"]; ok {
			if s, ok := raw.(string); ok && s != "" {
				return vcs.EventType(s)
			}
		}
	}
	return vcs.EventDirectlySubmitted
}

func (tp *TriggerProcessor) addWorkflowVars(ctx context.Context, wf *models.WorkflowInstance, vars map[string]interface{}, sourceNodeID *string, sourceJobID *string) error {
	for key, value := range vars {
		if err := tp.mergeWorkflowVar(ctx, wf.WorkflowID, key, value, sourceNodeID, sourceJobID); err != nil {
			return err
		}
	}
	return nil
}

func (tp *TriggerProcessor) mergeWorkflowVar(ctx context.Context, workflowID, key string, value interface{}, sourceNodeID *string, sourceJobID *string) error {
	ws, err := tp.workflowStore()
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	vars, err := ws.GetWorkflowVars(ctx, workflowID)
	if err != nil {
		return err
	}
	existing := make(map[string]interface{}, len(vars))
	for existingKey, existingValue := range vars {
		existing[existingKey] = workflowValueFromJSONB(existingValue)
	}
	added, mergeErr := workflowengine.MergeValue(existing, key, value)
	hash := workflowengine.ValueHash(value)
	if mergeErr != nil {
		if previous, ok := vars[key]; ok {
			tp.recordWorkflowEvent(ctx, workflowID, sourceNodeID, sourceJobID, "workflow_var_conflict", "conflicting workflow variable values", models.JSONB{
				"key":           key,
				"existing_hash": workflowengine.ValueHash(workflowValueFromJSONB(previous)),
				"new_hash":      hash,
			})
		}
		return mergeErr
	}
	if !added {
		tp.recordWorkflowEvent(ctx, workflowID, sourceNodeID, sourceJobID, "workflow_var_set", "duplicate workflow variable value ignored", models.JSONB{
			"key":        key,
			"value_hash": hash,
		})
		return nil
	}
	jsonValue := interfaceToJSONB(value)
	if err := ws.UpsertWorkflowVar(ctx, &models.WorkflowVar{
		WorkflowID:   workflowID,
		Key:          key,
		Value:        jsonValue,
		ValueHash:    hash,
		SourceNodeID: sourceNodeID,
		SourceJobID:  sourceJobID,
	}); err != nil {
		return err
	}
	tp.recordWorkflowEvent(ctx, workflowID, sourceNodeID, sourceJobID, "workflow_var_set", "workflow variable set", models.JSONB{
		"key":        key,
		"value_hash": hash,
	})
	return nil
}

func (tp *TriggerProcessor) createWorkflowNodes(ctx context.Context, wf *models.WorkflowInstance, specs []triggerJobSpec) error {
	expansionSpecs := make([]workflowengine.ExpansionSpec, len(specs))
	for i := range specs {
		expansionSpecs[i] = workflowengine.ExpansionSpec{Name: specs[i].JobName, ForEach: specs[i].ForEach, ItemVar: specs[i].ItemVar, Payload: specs[i]}
	}
	for _, expansion := range workflowengine.Expand(expansionSpecs) {
		spec := expansion.Payload.(triggerJobSpec)
		if expansion.ItemIndex != nil {
			spec.ItemVar = expansion.ItemVar
		}
		if err := tp.createWorkflowNode(ctx, wf, spec, expansion.ItemIndex, expansion.ItemValue); err != nil {
			return err
		}
	}
	return nil
}

func (tp *TriggerProcessor) createWorkflowNode(ctx context.Context, wf *models.WorkflowInstance, spec triggerJobSpec, itemIndex *int, item interface{}) error {
	ws, err := tp.workflowStore()
	if err != nil {
		return err
	}
	name := spec.JobName
	displayName := name
	env := cloneStringMap(spec.Env)
	var itemValue models.JSONB
	if itemIndex != nil {
		displayName = fmt.Sprintf("%s[%d]", name, *itemIndex)
		itemValue = interfaceToJSONB(item)
		itemVar := spec.ItemVar
		if itemVar == "" {
			itemVar = "ITEM"
		}
		env[itemVar] = stringifyWorkflowValue(item)
		spec.Env = env
		spec.ItemVar = itemVar
	}
	if spec.Condition == "" {
		spec.Condition = "all_success"
	}
	specBytes, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	var specJSON models.JSONB
	if err := json.Unmarshal(specBytes, &specJSON); err != nil {
		return err
	}
	node := &models.WorkflowNode{
		WorkflowID:  wf.WorkflowID,
		Name:        name,
		DisplayName: displayName,
		Status:      "pending",
		DependsOn:   pq.StringArray(spec.DependsOn),
		Condition:   spec.Condition,
		JobSpec:     specJSON,
		ItemIndex:   itemIndex,
		ItemValue:   itemValue,
		ItemVar:     spec.ItemVar,
	}
	if history, ok := tp.store.(workflowHistoryStore); ok {
		duration, err := history.GetLastSuccessfulWorkflowNodeDuration(ctx, wf, name)
		if err != nil {
			logging.Log.WithError(err).WithFields(map[string]interface{}{
				"workflow_id": wf.WorkflowID,
				"node_name":   name,
			}).Warn("Failed to load previous workflow node duration")
		} else if duration != nil {
			node.LastSuccessfulDurationMs = duration
		}
	}
	if err := ws.CreateWorkflowNode(ctx, node); err != nil {
		return err
	}
	tp.recordWorkflowEvent(ctx, wf.WorkflowID, &node.NodeID, nil, "node_waiting", "node registered", models.JSONB{
		"name":       node.DisplayName,
		"depends_on": []string(node.DependsOn),
		"condition":  node.Condition,
	})
	return nil
}

// EvaluateWorkflow is the exported form of evaluateWorkflow, used by
// internal/jobcontrol.RetryWorkflow to drive initial node submission for a
// freshly created workflow instance exactly the way ProcessTriggersFromData
// drives it for a brand-new one (same dependency/condition evaluation, same
// submitWorkflowNode path, same refreshWorkflowStatus at the end) — the
// alternative would be reimplementing that evaluation loop a second time in
// jobcontrol, which risks drifting from the worker's own semantics. The
// caller must pass a store implementing this package's full workflowStore
// interface (postgres_store.PostgresDbStore does); against a narrower store
// this returns ErrWorkflowsUnsupported-equivalent errors the same way
// ProcessTriggersFromData's own callers would see.
func (tp *TriggerProcessor) EvaluateWorkflow(ctx context.Context, wf *models.WorkflowInstance) ([]string, error) {
	return tp.evaluateWorkflow(ctx, wf)
}

type coordinatorWorkflowEngineAdapter struct {
	tp      *TriggerProcessor
	wf      *models.WorkflowInstance
	store   workflowStore
	nodes   []models.WorkflowNode
	created []string
}

func (a *coordinatorWorkflowEngineAdapter) Nodes(ctx context.Context) ([]workflowengine.Node, error) {
	nodes, err := a.store.ListWorkflowNodes(ctx, a.wf.WorkflowID)
	if err != nil {
		return nil, err
	}
	a.nodes = nodes
	return workflowRuleNodes(nodes), nil
}

func (a *coordinatorWorkflowEngineAdapter) ApplyDecision(ctx context.Context, decision workflowengine.Decision) error {
	node := workflowNodeByID(a.nodes, decision.NodeID)
	if node == nil {
		return fmt.Errorf("workflow node %q was not found", decision.NodeID)
	}
	switch decision.Action {
	case workflowengine.ActionWait:
		node.Status = "waiting"
		node.DecisionReason = decision.Reason
		if err := a.store.UpdateWorkflowNode(ctx, node); err != nil {
			return err
		}
		a.tp.recordWorkflowEvent(ctx, a.wf.WorkflowID, &node.NodeID, nil, "node_waiting", decision.Reason, nil)
	case workflowengine.ActionSkip:
		now := time.Now().UTC()
		node.Status = "skipped"
		node.DecisionReason = decision.Reason
		node.CompletedAt = &now
		if err := a.store.UpdateWorkflowNode(ctx, node); err != nil {
			return err
		}
		a.tp.recordWorkflowEvent(ctx, a.wf.WorkflowID, &node.NodeID, nil, "node_skipped", decision.Reason, nil)
	}
	return nil
}

func (a *coordinatorWorkflowEngineAdapter) Start(ctx context.Context, decision workflowengine.Decision) error {
	node := workflowNodeByID(a.nodes, decision.NodeID)
	if node == nil {
		return fmt.Errorf("workflow node %q was not found", decision.NodeID)
	}
	jobID, err := a.tp.submitWorkflowNode(ctx, a.wf, node)
	if err != nil {
		now := time.Now().UTC()
		node.Status = "failed"
		node.CompletedAt = &now
		node.DecisionReason = fmt.Sprintf("failed to submit workflow node: %v", err)
		if updateErr := a.store.UpdateWorkflowNode(ctx, node); updateErr != nil {
			return fmt.Errorf("%v; failed to record workflow node failure: %w", err, updateErr)
		}
		a.wf.LastError = node.DecisionReason
		if updateErr := a.store.UpdateWorkflowInstance(ctx, a.wf); updateErr != nil {
			return fmt.Errorf("%v; failed to record workflow failure: %w", err, updateErr)
		}
		a.tp.recordWorkflowEvent(ctx, a.wf.WorkflowID, &node.NodeID, node.JobID, "node_completed", node.DecisionReason, models.JSONB{
			"status": "failed",
		})
		return err
	}
	a.created = append(a.created, jobID)
	return nil
}

func (tp *TriggerProcessor) evaluateWorkflow(ctx context.Context, wf *models.WorkflowInstance) ([]string, error) {
	ws, err := tp.workflowStore()
	if err != nil {
		return nil, err
	}
	adapter := &coordinatorWorkflowEngineAdapter{tp: tp, wf: wf, store: ws}
	engine := workflowengine.Engine{Store: adapter, Executor: adapter}
	_, advanceErr := engine.Advance(ctx, -1)
	refreshErr := tp.refreshWorkflowStatus(ctx, wf)
	if advanceErr != nil {
		if refreshErr != nil {
			return adapter.created, fmt.Errorf("advance workflow: %v; refresh workflow status: %w", advanceErr, refreshErr)
		}
		return adapter.created, advanceErr
	}
	if refreshErr != nil {
		return adapter.created, refreshErr
	}
	return adapter.created, nil
}

func (tp *TriggerProcessor) submitWorkflowNode(ctx context.Context, wf *models.WorkflowInstance, node *models.WorkflowNode) (string, error) {
	ws, err := tp.workflowStore()
	if err != nil {
		return "", err
	}
	var spec triggerJobSpec
	specBytes, _ := json.Marshal(node.JobSpec)
	if err := json.Unmarshal(specBytes, &spec); err != nil {
		return "", err
	}
	parentJob, err := tp.store.GetJobByID(ctx, derefString(wf.ParentJobID))
	if err != nil {
		return "", err
	}
	authorityParent := *parentJob
	authorityParent.WorkerClass = wf.WorkerClass
	authorityParent.ExecutionProfile = wf.ExecutionProfile
	authorityParent.CIOrigin = wf.CIOrigin
	authorityParent.CIRepository = wf.CIRepository
	authorityParent.CISHA = wf.CISHA
	authorityParent.PolicyRevision = wf.PolicyRevision
	authorityParent.PolicyRuleID = wf.PolicyRuleID
	authorityParent.ApprovalID = wf.ApprovalID
	var executionProfile *models.ExecutionProfile
	limitCapabilities := true
	if ps, ok := tp.store.(triggerProfileStore); ok && wf.ExecutionProfile != "" {
		executionProfile, err = ps.GetExecutionProfile(ctx, wf.OrgID, wf.ExecutionProfile)
		if err != nil {
			return "", fmt.Errorf("load workflow execution profile: %w", err)
		}
		limitCapabilities = executionProfile.RuntimeCapabilities != nil
		authorityParent.Capabilities = executionProfile.RuntimeCapabilities
	}
	if wf.CIRepository != "" {
		authorityParent.CISourceURL = &wf.CIRepository
	}
	if wf.CISHA != "" {
		authorityParent.CISourceRef = &wf.CISHA
	}
	job, err := tp.buildJobFromTriggerWithCapabilityLimit(spec, &authorityParent, limitCapabilities)
	if err != nil {
		return "", err
	}
	if executionProfile != nil {
		if !executionProfile.MayRunAsRoot && (job.RunAsUser == "root" || job.RunAsUser == "0" || strings.HasPrefix(job.RunAsUser, "0:")) {
			return "", fmt.Errorf("execution profile %q denies root", executionProfile.Name)
		}
		if executionProfile.TimeoutCeilingSeconds != nil && job.TimeoutSeconds > *executionProfile.TimeoutCeilingSeconds {
			return "", fmt.Errorf("job timeout exceeds execution profile %q ceiling", executionProfile.Name)
		}
		if err := enforceResourceCeilings(job, executionProfile.ResourceCeilings); err != nil {
			return "", fmt.Errorf("execution profile %q: %w", executionProfile.Name, err)
		}
	}
	job.WorkflowID = &wf.WorkflowID
	job.WorkflowNodeID = &node.NodeID
	runID := uuid.New().String()
	job.WorkflowRunID = &runID
	job.WorkflowNodeName = node.DisplayName
	if err := tp.resolveJobQueue(ctx, job); err != nil {
		return "", err
	}
	if err := tp.store.CreateJob(ctx, job); err != nil {
		return "", err
	}
	node.JobID = &job.JobID
	node.Status = "submitted"
	node.DecisionReason = "dependencies satisfied and condition true"
	if err := ws.UpdateWorkflowNode(ctx, node); err != nil {
		return "", err
	}
	if tp.corndogsClient != nil {
		taskPayload := tp.buildTaskPayload(job)
		task, err := tp.corndogsClient.SubmitTaskToQueue(ctx, job.QueueName, taskPayload, int64(job.Priority))
		if err != nil {
			now := time.Now().UTC()
			job.Status = "failed"
			job.LastError = fmt.Sprintf("failed to submit to Corndogs: %v", err)
			_ = tp.store.UpdateJob(ctx, job)
			node.Status = "failed"
			node.CompletedAt = &now
			node.DecisionReason = job.LastError
			_ = ws.UpdateWorkflowNode(ctx, node)
			tp.recordWorkflowEvent(ctx, wf.WorkflowID, &node.NodeID, &job.JobID, "node_completed", node.DecisionReason, models.JSONB{
				"status": job.Status,
			})
			_ = tp.refreshWorkflowStatus(ctx, wf)
			return "", err
		}
		taskID := task.Uuid
		job.CorndogsTaskID = &taskID
		job.Status = task.CurrentState
		if err := tp.store.UpdateJob(ctx, job); err != nil {
			return "", err
		}
	}
	tp.recordWorkflowEvent(ctx, wf.WorkflowID, &node.NodeID, &job.JobID, "node_submitted", node.DecisionReason, models.JSONB{
		"job_id": job.JobID,
	})
	return job.JobID, nil
}

func (tp *TriggerProcessor) ProcessWorkflowCompletion(ctx context.Context, workspaceDir string, job *models.Job) error {
	ws, err := tp.workflowStore()
	if err != nil {
		return nil
	}
	if job.WorkflowID == nil || *job.WorkflowID == "" {
		return nil
	}
	wf, err := ws.GetWorkflowInstance(ctx, *job.WorkflowID)
	if err != nil {
		return err
	}
	node, err := ws.GetWorkflowNodeByJobID(ctx, job.JobID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if workspaceDir != "" {
		if err := tp.mergeWorkflowOutputFile(ctx, workspaceDir, wf, node, job); err != nil {
			return tp.failWorkflowNode(ctx, ws, wf, node, job, err)
		}
	}
	now := time.Now().UTC()
	node.Status = workflowNodeStatusFromJob(job.Status)
	node.CompletedAt = &now
	if job.StartedAt != nil && job.CompletedAt != nil && job.Status == "completed" {
		ms := job.CompletedAt.Sub(*job.StartedAt).Milliseconds()
		node.LastSuccessfulDurationMs = &ms
	}
	node.DecisionReason = fmt.Sprintf("job finished with status %s", job.Status)
	if err := ws.UpdateWorkflowNode(ctx, node); err != nil {
		return err
	}
	tp.recordWorkflowEvent(ctx, wf.WorkflowID, &node.NodeID, &job.JobID, "node_completed", node.DecisionReason, models.JSONB{
		"status":    job.Status,
		"exit_code": job.ExitCode,
	})
	_, err = tp.evaluateWorkflow(ctx, wf)
	return err
}

func (tp *TriggerProcessor) failWorkflowNode(ctx context.Context, ws workflowStore, wf *models.WorkflowInstance, node *models.WorkflowNode, job *models.Job, cause error) error {
	now := time.Now().UTC()
	node.Status = "failed"
	node.CompletedAt = &now
	node.DecisionReason = cause.Error()
	if err := ws.UpdateWorkflowNode(ctx, node); err != nil {
		return err
	}
	wf.LastError = cause.Error()
	if err := ws.UpdateWorkflowInstance(ctx, wf); err != nil {
		return err
	}
	tp.recordWorkflowEvent(ctx, wf.WorkflowID, &node.NodeID, &job.JobID, "node_completed", node.DecisionReason, models.JSONB{
		"status": job.Status,
		"error":  cause.Error(),
	})
	if err := tp.refreshWorkflowStatus(ctx, wf); err != nil {
		return err
	}
	return cause
}

func (tp *TriggerProcessor) ProcessWorkflowJobStarted(ctx context.Context, job *models.Job) error {
	ws, err := tp.workflowStore()
	if err != nil {
		return nil
	}
	if job.WorkflowID == nil || *job.WorkflowID == "" {
		return nil
	}
	wf, err := ws.GetWorkflowInstance(ctx, *job.WorkflowID)
	if err != nil {
		return err
	}
	node, err := ws.GetWorkflowNodeByJobID(ctx, job.JobID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	node.Status = "running"
	node.DecisionReason = "job is running"
	if err := ws.UpdateWorkflowNode(ctx, node); err != nil {
		return err
	}
	tp.recordWorkflowEvent(ctx, wf.WorkflowID, &node.NodeID, &job.JobID, "node_running", node.DecisionReason, nil)
	return tp.refreshWorkflowStatus(ctx, wf)
}

func (tp *TriggerProcessor) mergeWorkflowOutputFile(ctx context.Context, workspaceDir string, wf *models.WorkflowInstance, node *models.WorkflowNode, job *models.Job) error {
	path := filepath.Join(workspaceDir, "workflow-output.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return tp.mergeWorkflowOutputData(ctx, data, wf, node, job)
}

func (tp *TriggerProcessor) mergeWorkflowOutputData(ctx context.Context, data []byte, wf *models.WorkflowInstance, node *models.WorkflowNode, job *models.Job) error {
	var output workflowOutputFile
	if err := json.Unmarshal(data, &output); err != nil {
		return fmt.Errorf("parse workflow output file: %w", err)
	}
	for _, value := range workflowengine.OutputValues(node.Name, output.Vars, output.Outputs) {
		if err := tp.mergeWorkflowVar(ctx, wf.WorkflowID, value.Key, value.Value, &node.NodeID, &job.JobID); err != nil {
			return err
		}
	}
	return nil
}

// ProcessWorkflowCompletionData advances a workflow and merges an output
// document returned by a remote worker.
func (tp *TriggerProcessor) ProcessWorkflowCompletionData(ctx context.Context, data []byte, job *models.Job) error {
	ws, err := tp.workflowStore()
	if err != nil {
		return nil
	}
	if job.WorkflowID == nil || *job.WorkflowID == "" {
		return nil
	}
	wf, err := ws.GetWorkflowInstance(ctx, *job.WorkflowID)
	if err != nil {
		return err
	}
	node, err := ws.GetWorkflowNodeByJobID(ctx, job.JobID)
	if err != nil {
		return err
	}
	if len(data) > 0 {
		if err := tp.mergeWorkflowOutputData(ctx, data, wf, node, job); err != nil {
			return tp.failWorkflowNode(ctx, ws, wf, node, job, err)
		}
	}
	now := time.Now().UTC()
	node.Status = workflowNodeStatusFromJob(job.Status)
	node.CompletedAt = &now
	node.DecisionReason = fmt.Sprintf("job finished with status %s", job.Status)
	if err := ws.UpdateWorkflowNode(ctx, node); err != nil {
		return err
	}
	tp.recordWorkflowEvent(ctx, wf.WorkflowID, &node.NodeID, &job.JobID, "node_completed", node.DecisionReason, models.JSONB{"status": job.Status, "exit_code": job.ExitCode})
	_, err = tp.evaluateWorkflow(ctx, wf)
	return err
}

func (tp *TriggerProcessor) refreshWorkflowStatus(ctx context.Context, wf *models.WorkflowInstance) error {
	ws, err := tp.workflowStore()
	if err != nil {
		return err
	}
	nodes, err := ws.ListWorkflowNodes(ctx, wf.WorkflowID)
	if gs, ok := tp.store.(workflowGraphStore); ok {
		nodes, err = gs.ListWorkflowNodesBySubtree(ctx, wf.WorkflowID)
	}
	if err != nil {
		return err
	}
	if err := tp.updateWorkflowAggregate(ctx, ws, wf, nodes, wf.ParentWorkflowID == nil); err != nil {
		return err
	}
	if wf.ParentWorkflowID != nil {
		return tp.refreshWorkflowAncestors(ctx, ws, *wf.ParentWorkflowID)
	}
	return nil
}

func (tp *TriggerProcessor) refreshRootForChildRegistration(ctx context.Context, wf *models.WorkflowInstance) error {
	if wf == nil || wf.ParentWorkflowID == nil {
		return nil
	}
	return tp.refreshWorkflowStatus(ctx, wf)
}

func (tp *TriggerProcessor) refreshWorkflowAncestors(ctx context.Context, ws workflowStore, workflowID string) error {
	seen := map[string]bool{}
	for workflowID != "" {
		if seen[workflowID] {
			return fmt.Errorf("cycle in workflow graph at %s", workflowID)
		}
		seen[workflowID] = true
		wf, err := ws.GetWorkflowInstance(ctx, workflowID)
		if err != nil {
			return err
		}
		nodes, err := ws.ListWorkflowNodes(ctx, workflowID)
		if gs, ok := tp.store.(workflowGraphStore); ok {
			nodes, err = gs.ListWorkflowNodesBySubtree(ctx, workflowID)
		}
		if err != nil {
			return err
		}
		if err := tp.updateWorkflowAggregate(ctx, ws, wf, nodes, wf.ParentWorkflowID == nil); err != nil {
			return err
		}
		if wf.ParentWorkflowID == nil {
			return nil
		}
		workflowID = *wf.ParentWorkflowID
	}
	return nil
}

func (tp *TriggerProcessor) updateWorkflowAggregate(ctx context.Context, ws workflowStore, wf *models.WorkflowInstance, nodes []models.WorkflowNode, publish bool) error {
	old := wf.Status
	wf.Status = computeWorkflowStatus(nodes)
	if isWorkflowStatusTerminal(wf.Status) {
		if wf.CompletedAt == nil {
			now := time.Now().UTC()
			wf.CompletedAt = &now
		}
	} else {
		wf.CompletedAt = nil
	}
	if old != wf.Status {
		if err := ws.UpdateWorkflowInstance(ctx, wf); err != nil {
			return err
		}
		tp.recordWorkflowEvent(ctx, wf.WorkflowID, nil, nil, "workflow_status_changed", fmt.Sprintf("%s -> %s", old, wf.Status), nil)
	}
	if publish {
		if updater, ok := tp.statusUpdater.(workflowStatusUpdater); ok {
			if err := updater.UpdateWorkflowStatus(ctx, wf, nodes); err != nil {
				logging.Log.WithError(err).WithField("workflow_id", wf.WorkflowID).Warn("Failed to update root workflow VCS status")
			}
		}
	}
	return nil
}

func isWorkflowStatusTerminal(status string) bool {
	return status == "success" || status == "failed" || status == "skipped" || status == "cancelled"
}

func (tp *TriggerProcessor) recordWorkflowEvent(ctx context.Context, workflowID string, nodeID *string, jobID *string, eventType, reason string, details models.JSONB) {
	ws, err := tp.workflowStore()
	if err != nil {
		return
	}
	if workflowID == "" {
		return
	}
	if details == nil {
		details = models.JSONB{}
	}
	if err := ws.CreateWorkflowEvent(ctx, &models.WorkflowEvent{
		WorkflowID: workflowID,
		NodeID:     nodeID,
		JobID:      jobID,
		EventType:  eventType,
		Reason:     reason,
		Details:    details,
	}); err != nil {
		logging.Log.WithError(err).WithField("workflow_id", workflowID).Warn("Failed to record workflow event")
	}
}

func dependenciesReady(nodes []models.WorkflowNode, node *models.WorkflowNode) (bool, string) {
	return workflowengine.DependenciesReady(workflowRuleNodes(nodes), workflowRuleNode(*node))
}

func evaluateWorkflowCondition(nodes []models.WorkflowNode, node *models.WorkflowNode) (bool, string) {
	return workflowengine.EvaluateCondition(workflowRuleNodes(nodes), workflowRuleNode(*node))
}

// ComputeWorkflowStatus is the exported form of computeWorkflowStatus, used
// by internal/jobcontrol (CancelWorkflow) so the workflow-cancel cascade and
// the normal per-node completion path (refreshWorkflowStatus, above) agree
// on exactly one status-derivation rule instead of maintaining two.
func ComputeWorkflowStatus(nodes []models.WorkflowNode) string {
	return computeWorkflowStatus(nodes)
}

// computeWorkflowStatus derives the workflow instance's status from its
// nodes' statuses.
//
// Failure semantics (unchanged from before this cancel/kill wave): any node
// that reached "failed" or "timeout" on its own merits fails the whole
// workflow immediately, even while sibling nodes are still running —
// fail-fast, matches prior behavior.
//
// Cancellation semantics (new): a node lands on "cancelled" either because
// CancelWorkflow cascaded a cancel/kill onto every non-terminal node (the
// workflow itself was cancelled), or because one node's job was individually
// cancelled while siblings kept running independently. Unlike failure,
// cancellation is deliberately NOT eager: computeWorkflowStatus only reports
// "cancelled" once every node has reached a terminal state (allTerminal),
// with zero real failures among them. Until then it reports "running",
// leaving the workflow instance's own status wherever the caller last set it
// — for a CancelWorkflow-initiated cascade that's the transient
// "cancelling" value (see jobcontrol.CancelWorkflow), so the UI shows
// "cancelling" while a node's container is still mid-SIGTERM-grace rather
// than flickering to "running". A real failure still takes priority over
// any cancellation, eager or not: a genuinely failed node mixed with
// cascaded cancellations on its siblings is a failure, not a clean cancel.
func computeWorkflowStatus(nodes []models.WorkflowNode) string {
	return workflowengine.ComputeStatus(workflowRuleNodes(nodes))
}

func workflowNodeStatusFromJob(status string) string {
	switch status {
	case "completed":
		return "completed"
	case "failed", "cancelled", "timeout":
		return status
	default:
		return status
	}
}

func isWorkflowNodeTerminal(status string) bool {
	return workflowengine.Terminal(status)
}

func workflowRuleNodes(nodes []models.WorkflowNode) []workflowengine.Node {
	out := make([]workflowengine.Node, len(nodes))
	for i := range nodes {
		out[i] = workflowRuleNode(nodes[i])
	}
	return out
}

func workflowRuleNode(node models.WorkflowNode) workflowengine.Node {
	return workflowengine.Node{ID: node.NodeID, Name: node.Name, DisplayName: node.DisplayName, Status: node.Status, DependsOn: []string(node.DependsOn), Condition: node.Condition}
}

func workflowNodeByID(nodes []models.WorkflowNode, id string) *models.WorkflowNode {
	for i := range nodes {
		if nodes[i].NodeID == id {
			return &nodes[i]
		}
	}
	return nil
}

func isWorkflowNodeFailure(status string) bool {
	return status == "failed" || status == "cancelled" || status == "timeout"
}

func nodesByName(nodes []models.WorkflowNode, name string) []models.WorkflowNode {
	var result []models.WorkflowNode
	for _, node := range nodes {
		if node.Name == name {
			result = append(result, node)
		}
	}
	return result
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func interfaceToJSONB(value interface{}) models.JSONB {
	data, _ := json.Marshal(value)
	var result interface{}
	_ = json.Unmarshal(data, &result)
	return models.JSONB{"value": result}
}

func workflowValueFromJSONB(value models.JSONB) interface{} {
	if len(value) == 1 {
		if unwrapped, ok := value["value"]; ok {
			return unwrapped
		}
	}
	// Compatibility with object values stored before all workflow values used
	// the uniform wrapper.
	return map[string]interface{}(value)
}

// EncodeWorkflowVars returns the JSON object that runnerlib receives. The
// database uses a JSON object wrapper because WorkflowVar.Value is a JSONB
// map. This function removes that storage-only wrapper for every value.
func EncodeWorkflowVars(values map[string]models.JSONB) ([]byte, error) {
	decoded := make(map[string]interface{}, len(values))
	for key, value := range values {
		decoded[key] = workflowValueFromJSONB(value)
	}
	return json.Marshal(decoded)
}

func stringifyWorkflowValue(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64, bool:
		return fmt.Sprintf("%v", v)
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
