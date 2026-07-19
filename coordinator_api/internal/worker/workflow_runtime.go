package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func (tp *TriggerProcessor) ensureWorkflow(ctx context.Context, parentJob *models.Job, spec *triggerWorkflowSpec) (*models.WorkflowInstance, error) {
	ws, err := tp.workflowStore()
	if err != nil {
		return nil, err
	}
	if parentJob.WorkflowID != nil && *parentJob.WorkflowID != "" {
		return ws.GetWorkflowInstance(ctx, *parentJob.WorkflowID)
	}

	name := defaultWorkflowName
	if spec != nil && strings.TrimSpace(spec.Name) != "" {
		name = strings.TrimSpace(spec.Name)
	}

	parentJobID := parentJob.JobID
	wf := &models.WorkflowInstance{
		UserID:        parentJob.UserID,
		ProjectID:     parentJob.ProjectID,
		ParentJobID:   &parentJobID,
		Name:          name,
		Status:        "evaluating",
		QueueName:     parentJob.QueueName,
		StatusContext: name,
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
	if wf.CommitSHA != "" {
		// Key the comment marker on both the commit and the triggering event
		// type so distinct workflows landing on the same commit (e.g. PR checks
		// vs a post-merge release run — common with rebase merges that preserve
		// the SHA) get separate PR comments instead of clobbering each other,
		// while a redelivered webhook or manual resubmit of the same event
		// reuses the marker and edits the existing comment.
		wf.CommentMarker = fmt.Sprintf("<!-- reactorcide:workflows:%s:%s -->", wf.CommitSHA, workflowEventType(parentJob))
	}

	if err := ws.CreateWorkflowInstance(ctx, wf); err != nil {
		return nil, err
	}
	parentJob.WorkflowID = &wf.WorkflowID
	if err := tp.store.UpdateJob(ctx, parentJob); err != nil {
		return nil, err
	}
	tp.recordWorkflowEvent(ctx, wf.WorkflowID, nil, nil, "workflow_evaluated", "created workflow from triggers", models.JSONB{
		"parent_job_id": parentJob.JobID,
		"name":          wf.Name,
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
	jsonValue := interfaceToJSONB(value)
	hash := hashJSONB(jsonValue)
	vars, err := ws.GetWorkflowVars(ctx, workflowID)
	if err != nil {
		return err
	}
	if existing, ok := vars[key]; ok {
		existingHash := hashJSONB(existing)
		if existingHash != hash {
			tp.recordWorkflowEvent(ctx, workflowID, sourceNodeID, sourceJobID, "workflow_var_conflict", "conflicting workflow variable values", models.JSONB{
				"key":           key,
				"existing_hash": existingHash,
				"new_hash":      hash,
			})
			return fmt.Errorf("workflow variable %q conflict", key)
		}
		tp.recordWorkflowEvent(ctx, workflowID, sourceNodeID, sourceJobID, "workflow_var_set", "duplicate workflow variable value ignored", models.JSONB{
			"key":        key,
			"value_hash": hash,
		})
		return nil
	}
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
	for _, spec := range specs {
		items := spec.ForEach
		if len(items) == 0 {
			if err := tp.createWorkflowNode(ctx, wf, spec, nil, nil); err != nil {
				return err
			}
			continue
		}
		for i, item := range items {
			idx := i
			if err := tp.createWorkflowNode(ctx, wf, spec, &idx, item); err != nil {
				return err
			}
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

func (tp *TriggerProcessor) evaluateWorkflow(ctx context.Context, wf *models.WorkflowInstance) ([]string, error) {
	ws, err := tp.workflowStore()
	if err != nil {
		return nil, err
	}
	nodes, err := ws.ListWorkflowNodes(ctx, wf.WorkflowID)
	if err != nil {
		return nil, err
	}
	var created []string
	for i := range nodes {
		node := &nodes[i]
		if node.Status != "pending" && node.Status != "waiting" {
			continue
		}
		ready, reason := dependenciesReady(nodes, node)
		if !ready {
			node.Status = "waiting"
			node.DecisionReason = reason
			_ = ws.UpdateWorkflowNode(ctx, node)
			tp.recordWorkflowEvent(ctx, wf.WorkflowID, &node.NodeID, nil, "node_waiting", reason, nil)
			continue
		}
		ok, reason := evaluateWorkflowCondition(nodes, node)
		if !ok {
			now := time.Now().UTC()
			node.Status = "skipped"
			node.DecisionReason = reason
			node.CompletedAt = &now
			if err := ws.UpdateWorkflowNode(ctx, node); err != nil {
				return created, err
			}
			tp.recordWorkflowEvent(ctx, wf.WorkflowID, &node.NodeID, nil, "node_skipped", reason, nil)
			continue
		}
		jobID, err := tp.submitWorkflowNode(ctx, wf, node)
		if err != nil {
			return created, err
		}
		created = append(created, jobID)
	}
	if err := tp.refreshWorkflowStatus(ctx, wf); err != nil {
		return created, err
	}
	return created, nil
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
	job := tp.buildJobFromTrigger(spec, parentJob)
	job.WorkflowID = &wf.WorkflowID
	job.WorkflowNodeID = &node.NodeID
	runID := uuid.New().String()
	job.WorkflowRunID = &runID
	job.WorkflowNodeName = node.DisplayName
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
		task, err := tp.corndogsClient.SubmitTask(ctx, taskPayload, int64(job.Priority))
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
	var output workflowOutputFile
	if err := json.Unmarshal(data, &output); err != nil {
		return fmt.Errorf("parse workflow output file: %w", err)
	}
	if len(output.Vars) > 0 {
		if err := tp.addWorkflowVars(ctx, wf, output.Vars, &node.NodeID, &job.JobID); err != nil {
			return err
		}
	}
	if len(output.Outputs) > 0 {
		for key, value := range output.Outputs {
			outputKey := fmt.Sprintf("%s.%s", node.Name, key)
			if err := tp.mergeWorkflowVar(ctx, wf.WorkflowID, outputKey, value, &node.NodeID, &job.JobID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (tp *TriggerProcessor) refreshWorkflowStatus(ctx context.Context, wf *models.WorkflowInstance) error {
	ws, err := tp.workflowStore()
	if err != nil {
		return err
	}
	nodes, err := ws.ListWorkflowNodes(ctx, wf.WorkflowID)
	if err != nil {
		return err
	}
	old := wf.Status
	wf.Status = computeWorkflowStatus(nodes)
	if wf.Status == "success" || wf.Status == "failed" || wf.Status == "skipped" {
		now := time.Now().UTC()
		wf.CompletedAt = &now
	}
	if old != wf.Status {
		if err := ws.UpdateWorkflowInstance(ctx, wf); err != nil {
			return err
		}
		tp.recordWorkflowEvent(ctx, wf.WorkflowID, nil, nil, "workflow_status_changed", fmt.Sprintf("%s -> %s", old, wf.Status), nil)
	}
	if updater, ok := tp.statusUpdater.(workflowStatusUpdater); ok {
		if err := updater.UpdateWorkflowStatus(ctx, wf, nodes); err != nil {
			logging.Log.WithError(err).WithField("workflow_id", wf.WorkflowID).Warn("Failed to update workflow VCS status")
		}
	}
	return nil
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
	for _, dep := range node.DependsOn {
		group := nodesByName(nodes, dep)
		if len(group) == 0 {
			return false, "waiting for dependency " + dep + " to be registered"
		}
		for _, candidate := range group {
			if !isWorkflowNodeTerminal(candidate.Status) {
				return false, "waiting on " + candidate.DisplayName
			}
		}
	}
	return true, "all dependencies terminal"
}

func evaluateWorkflowCondition(nodes []models.WorkflowNode, node *models.WorkflowNode) (bool, string) {
	condition := strings.TrimSpace(node.Condition)
	if condition == "" || condition == "all_success" || condition == "all_success(needs)" {
		for _, dep := range node.DependsOn {
			for _, candidate := range nodesByName(nodes, dep) {
				if candidate.Status != "completed" {
					return false, "condition all_success(needs) is false"
				}
			}
		}
		return true, "condition all_success(needs) is true"
	}
	if condition == "always" || condition == "always()" {
		return true, "condition always() is true"
	}
	if condition == "any_failed" || condition == "any_failed(needs)" {
		for _, dep := range node.DependsOn {
			for _, candidate := range nodesByName(nodes, dep) {
				if isWorkflowNodeFailure(candidate.Status) {
					return true, "condition any_failed(needs) is true"
				}
			}
		}
		return false, "condition any_failed(needs) is false"
	}
	return false, "unsupported condition " + condition
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
	if len(nodes) == 0 {
		return "skipped"
	}
	allSkipped := true
	allTerminal := true
	anyCancelled := false
	for _, node := range nodes {
		if node.Status == "failed" || node.Status == "timeout" {
			return "failed"
		}
		if node.Status == "cancelled" {
			anyCancelled = true
		}
		if node.Status != "skipped" {
			allSkipped = false
		}
		if !isWorkflowNodeTerminal(node.Status) {
			allTerminal = false
		}
	}
	if !allTerminal {
		return "running"
	}
	if anyCancelled {
		return "cancelled"
	}
	if allSkipped {
		return "skipped"
	}
	return "success"
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
	return status == "completed" || status == "failed" || status == "cancelled" || status == "timeout" || status == "skipped"
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
	if m, ok := result.(map[string]interface{}); ok {
		return models.JSONB(m)
	}
	return models.JSONB{"value": result}
}

func hashJSONB(value models.JSONB) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
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

func unwrapWorkflowJSONB(value models.JSONB) interface{} {
	if value == nil {
		return nil
	}
	if len(value) == 1 {
		if wrapped, ok := value["value"]; ok {
			return wrapped
		}
	}
	return map[string]interface{}(value)
}

func workflowScalarString(value interface{}) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case int:
		return strconv.Itoa(v), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case nil:
		return "", false
	default:
		return "", false
	}
}

func workflowUserEnvName(key string) string {
	var b strings.Builder
	b.WriteString("RC_WFU_")
	for _, r := range strings.ToUpper(key) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	return b.String()
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
