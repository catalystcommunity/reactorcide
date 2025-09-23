package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// Engine manages workflow execution using Corndogs for state management
type Engine struct {
	corndogsClient corndogs.ClientInterface
	workflows      map[string]WorkflowDefinition
	instances      map[string]*WorkflowInstance
	mu             sync.RWMutex
	logger         *logrus.Logger
}

// NewEngine creates a new workflow engine
func NewEngine(corndogsClient corndogs.ClientInterface, logger *logrus.Logger) *Engine {
	if logger == nil {
		logger = logrus.New()
	}

	return &Engine{
		corndogsClient: corndogsClient,
		workflows:      make(map[string]WorkflowDefinition),
		instances:      make(map[string]*WorkflowInstance),
		logger:         logger,
	}
}

// RegisterWorkflow registers a workflow definition
func (e *Engine) RegisterWorkflow(workflow WorkflowDefinition) error {
	if err := workflow.Validate(); err != nil {
		return fmt.Errorf("invalid workflow: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.workflows[workflow.Name] = workflow
	e.logger.WithField("workflow", workflow.Name).Info("Registered workflow")
	return nil
}

// LoadPredefinedWorkflows loads all predefined workflows
func (e *Engine) LoadPredefinedWorkflows() {
	for name, workflow := range PredefinedWorkflows {
		if err := e.RegisterWorkflow(workflow); err != nil {
			e.logger.WithError(err).WithField("workflow", name).Error("Failed to register predefined workflow")
		}
	}
}

// StartWorkflow starts a new workflow instance
func (e *Engine) StartWorkflow(ctx context.Context, workflowName string, parameters map[string]interface{}) (*WorkflowInstance, error) {
	e.mu.RLock()
	workflow, exists := e.workflows[workflowName]
	e.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("workflow '%s' not found", workflowName)
	}

	// Validate parameters
	if err := e.validateParameters(workflow, parameters); err != nil {
		return nil, fmt.Errorf("parameter validation failed: %w", err)
	}

	// Create workflow instance
	instance := &WorkflowInstance{
		InstanceID:    uuid.New().String(),
		WorkflowName:  workflowName,
		CurrentState:  workflow.InitialState,
		Parameters:    parameters,
		Context:       make(map[string]interface{}),
		StartedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		Status:        "running",
		StateHistory:  []StateTransition{},
		ActiveJobs:    []string{},
	}

	// Store instance
	e.mu.Lock()
	e.instances[instance.InstanceID] = instance
	e.mu.Unlock()

	// Create Corndogs task for workflow management
	taskPayload := &WorkflowTaskPayload{
		Type:         "workflow",
		InstanceID:   instance.InstanceID,
		WorkflowName: workflowName,
		CurrentState: workflow.InitialState,
		Parameters:   parameters,
		Context:      instance.Context,
	}

	// Determine priority based on workflow type
	priority := e.calculateWorkflowPriority(workflow, parameters)

	// Submit to Corndogs with custom state machine
	if err := e.submitWorkflowTask(ctx, instance, taskPayload, priority); err != nil {
		return nil, fmt.Errorf("failed to submit workflow task: %w", err)
	}

	// Process initial state
	if err := e.processState(ctx, instance, workflow.InitialState, "start"); err != nil {
		e.logger.WithError(err).WithField("instance", instance.InstanceID).Error("Failed to process initial state")
	}

	return instance, nil
}

// WorkflowTaskPayload represents the payload for workflow tasks in Corndogs
type WorkflowTaskPayload struct {
	Type         string                 `json:"type"`
	InstanceID   string                 `json:"instance_id"`
	WorkflowName string                 `json:"workflow_name"`
	CurrentState string                 `json:"current_state"`
	NextState    string                 `json:"next_state,omitempty"`
	Event        string                 `json:"event,omitempty"`
	Parameters   map[string]interface{} `json:"parameters"`
	Context      map[string]interface{} `json:"context"`
	JobResults   map[string]interface{} `json:"job_results,omitempty"`
}

// submitWorkflowTask submits a workflow task to Corndogs
func (e *Engine) submitWorkflowTask(ctx context.Context, instance *WorkflowInstance, payload *WorkflowTaskPayload, priority int64) error {
	// Payload will be used for future workflow state tracking
	_, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal workflow payload: %w", err)
	}

	cornPayload := &corndogs.TaskPayload{
		JobID:   instance.InstanceID,
		JobType: "workflow",
		Config: map[string]interface{}{
			"workflow_name": payload.WorkflowName,
			"current_state": payload.CurrentState,
			"next_state":    payload.NextState,
		},
		Source: map[string]interface{}{
			"type": "workflow",
		},
		Metadata: map[string]interface{}{
			"instance_id": instance.InstanceID,
			"started_at":  instance.StartedAt,
			"parameters":  instance.Parameters,
		},
	}

	// Custom states for workflow state machine
	currentCornState := fmt.Sprintf("workflow:%s:%s", payload.WorkflowName, payload.CurrentState)

	task, err := e.corndogsClient.SubmitTask(ctx, cornPayload, priority)
	if err != nil {
		return err
	}

	e.logger.WithFields(logrus.Fields{
		"instance_id": instance.InstanceID,
		"task_id":     task.Uuid,
		"state":       currentCornState,
		"priority":    priority,
	}).Info("Submitted workflow task to Corndogs")

	return nil
}

// processState processes actions for a state
func (e *Engine) processState(ctx context.Context, instance *WorkflowInstance, stateName string, event string) error {
	e.mu.RLock()
	workflow, exists := e.workflows[instance.WorkflowName]
	e.mu.RUnlock()

	if !exists {
		return fmt.Errorf("workflow '%s' not found", instance.WorkflowName)
	}

	state, exists := workflow.States[stateName]
	if !exists {
		return fmt.Errorf("state '%s' not found", stateName)
	}

	// Record state transition
	e.recordTransition(instance, instance.CurrentState, stateName, event)

	// Update current state
	instance.CurrentState = stateName
	instance.UpdatedAt = time.Now()

	// Execute OnExit actions for previous state if any
	if instance.PreviousState != "" {
		if prevState, ok := workflow.States[instance.PreviousState]; ok {
			for _, action := range prevState.OnExit {
				if err := e.executeAction(ctx, instance, action); err != nil {
					e.logger.WithError(err).WithField("action", action.Name).Error("Failed to execute OnExit action")
				}
			}
		}
	}

	// Execute OnEnter actions for current state
	for _, action := range state.OnEnter {
		if err := e.executeAction(ctx, instance, action); err != nil {
			e.logger.WithError(err).WithField("action", action.Name).Error("Failed to execute OnEnter action")
			// Trigger failure event if defined
			if action.OnFailure != "" {
				return e.triggerEvent(ctx, instance, action.OnFailure)
			}
			return err
		}
		// Trigger success event if defined
		if action.OnSuccess != "" {
			if err := e.triggerEvent(ctx, instance, action.OnSuccess); err != nil {
				return err
			}
		}
	}

	// Check if terminal state
	if state.IsTerminal {
		instance.Status = "completed"
		now := time.Now()
		instance.CompletedAt = &now
		e.logger.WithField("instance", instance.InstanceID).Info("Workflow reached terminal state")
	}

	// Set up timeout if specified
	if state.TimeoutSeconds > 0 && state.TimeoutState != "" {
		go e.scheduleTimeout(ctx, instance, stateName, state.TimeoutSeconds, state.TimeoutState)
	}

	return nil
}

// executeAction executes a workflow action
func (e *Engine) executeAction(ctx context.Context, instance *WorkflowInstance, action Action) error {
	e.logger.WithFields(logrus.Fields{
		"instance": instance.InstanceID,
		"action":   action.Name,
		"type":     action.Type,
	}).Debug("Executing action")

	switch action.Type {
	case "run_job":
		return e.executeRunJob(ctx, instance, action)
	case "notify":
		return e.executeNotify(ctx, instance, action)
	case "wait":
		return e.executeWait(ctx, instance, action)
	case "parallel":
		return e.executeParallel(ctx, instance, action)
	case "conditional":
		return e.executeConditional(ctx, instance, action)
	default:
		return fmt.Errorf("unknown action type: %s", action.Type)
	}
}

// executeRunJob submits a job to Corndogs
func (e *Engine) executeRunJob(ctx context.Context, instance *WorkflowInstance, action Action) error {
	// Extract job parameters
	command, ok := action.Parameters["command"].(string)
	if !ok {
		return fmt.Errorf("command parameter required for run_job action")
	}

	// Create job payload
	jobPayload := &corndogs.TaskPayload{
		JobID:   uuid.New().String(),
		JobType: "job",
		Config: map[string]interface{}{
			"command":     command,
			"workflow_id": instance.InstanceID,
			"action_name": action.Name,
		},
		Source:   action.Parameters,
		Metadata: instance.Context,
	}

	// Calculate priority based on workflow context
	priority := e.calculateJobPriority(instance, action)

	// Submit job to Corndogs
	task, err := e.corndogsClient.SubmitTask(ctx, jobPayload, priority)
	if err != nil {
		return fmt.Errorf("failed to submit job: %w", err)
	}

	// Track active job
	instance.ActiveJobs = append(instance.ActiveJobs, task.Uuid)

	e.logger.WithFields(logrus.Fields{
		"instance": instance.InstanceID,
		"job_id":   task.Uuid,
		"command":  command,
		"priority": priority,
	}).Info("Submitted job for workflow action")

	return nil
}

// executeNotify sends a notification
func (e *Engine) executeNotify(ctx context.Context, instance *WorkflowInstance, action Action) error {
	channel, _ := action.Parameters["channel"].(string)
	message, _ := action.Parameters["message"].(string)

	e.logger.WithFields(logrus.Fields{
		"instance": instance.InstanceID,
		"channel":  channel,
		"message":  message,
	}).Info("Sending notification")

	// TODO: Implement actual notification logic (Slack, email, etc.)
	return nil
}

// executeWait waits for a specified duration
func (e *Engine) executeWait(ctx context.Context, instance *WorkflowInstance, action Action) error {
	duration, ok := action.Parameters["duration"].(float64)
	if !ok {
		return fmt.Errorf("duration parameter required for wait action")
	}

	time.Sleep(time.Duration(duration) * time.Second)
	return nil
}

// executeParallel executes multiple jobs in parallel
func (e *Engine) executeParallel(ctx context.Context, instance *WorkflowInstance, action Action) error {
	jobs, ok := action.Parameters["jobs"].([]map[string]interface{})
	if !ok {
		// Try to convert from []interface{}
		jobsInterface, ok := action.Parameters["jobs"].([]interface{})
		if !ok {
			return fmt.Errorf("jobs parameter required for parallel action")
		}
		jobs = make([]map[string]interface{}, len(jobsInterface))
		for i, j := range jobsInterface {
			job, ok := j.(map[string]interface{})
			if !ok {
				return fmt.Errorf("invalid job format in parallel action")
			}
			jobs[i] = job
		}
	}

	var wg sync.WaitGroup
	errors := make(chan error, len(jobs))

	for _, job := range jobs {
		wg.Add(1)
		go func(jobParams map[string]interface{}) {
			defer wg.Done()

			subAction := Action{
				Type:       "run_job",
				Name:       jobParams["name"].(string),
				Parameters: jobParams,
			}

			if err := e.executeRunJob(ctx, instance, subAction); err != nil {
				errors <- err
			}
		}(job)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		if err != nil {
			return err
		}
	}

	return nil
}

// executeConditional executes actions based on conditions
func (e *Engine) executeConditional(ctx context.Context, instance *WorkflowInstance, action Action) error {
	condition, ok := action.Parameters["condition"].(string)
	if !ok {
		return fmt.Errorf("condition parameter required for conditional action")
	}

	// Evaluate condition (simplified for now)
	result := e.evaluateCondition(instance, condition)

	if result {
		if thenAction, ok := action.Parameters["then"].(map[string]interface{}); ok {
			return e.executeAction(ctx, instance, Action{
				Type:       thenAction["type"].(string),
				Name:       thenAction["name"].(string),
				Parameters: thenAction["parameters"].(map[string]interface{}),
			})
		}
	} else {
		if elseAction, ok := action.Parameters["else"].(map[string]interface{}); ok {
			return e.executeAction(ctx, instance, Action{
				Type:       elseAction["type"].(string),
				Name:       elseAction["name"].(string),
				Parameters: elseAction["parameters"].(map[string]interface{}),
			})
		}
	}

	return nil
}

// evaluateCondition evaluates a condition expression
func (e *Engine) evaluateCondition(instance *WorkflowInstance, condition string) bool {
	// TODO: Implement proper condition evaluation
	// For now, just return true
	return true
}

// triggerEvent triggers a workflow event
func (e *Engine) triggerEvent(ctx context.Context, instance *WorkflowInstance, event string) error {
	e.mu.RLock()
	workflow, exists := e.workflows[instance.WorkflowName]
	e.mu.RUnlock()

	if !exists {
		return fmt.Errorf("workflow '%s' not found", instance.WorkflowName)
	}

	state, exists := workflow.States[instance.CurrentState]
	if !exists {
		return fmt.Errorf("state '%s' not found", instance.CurrentState)
	}

	nextState, exists := state.Transitions[event]
	if !exists {
		e.logger.WithFields(logrus.Fields{
			"instance": instance.InstanceID,
			"state":    instance.CurrentState,
			"event":    event,
		}).Warn("No transition for event")
		return nil
	}

	// Process transition to next state
	return e.processState(ctx, instance, nextState, event)
}

// scheduleTimeout schedules a timeout for a state
func (e *Engine) scheduleTimeout(ctx context.Context, instance *WorkflowInstance, stateName string, timeoutSeconds int, timeoutState string) {
	timer := time.NewTimer(time.Duration(timeoutSeconds) * time.Second)
	defer timer.Stop()

	select {
	case <-timer.C:
		// Check if still in the same state
		e.mu.RLock()
		currentState := instance.CurrentState
		e.mu.RUnlock()

		if currentState == stateName {
			e.logger.WithFields(logrus.Fields{
				"instance": instance.InstanceID,
				"state":    stateName,
				"timeout":  timeoutSeconds,
			}).Warn("State timeout reached")

			// Transition to timeout state
			if err := e.processState(ctx, instance, timeoutState, "timeout"); err != nil {
				e.logger.WithError(err).Error("Failed to process timeout transition")
			}
		}
	case <-ctx.Done():
		return
	}
}

// recordTransition records a state transition
func (e *Engine) recordTransition(instance *WorkflowInstance, fromState, toState, event string) {
	transition := StateTransition{
		FromState: fromState,
		ToState:   toState,
		Event:     event,
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"retry_count": instance.RetryCount,
		},
	}

	instance.PreviousState = fromState
	instance.StateHistory = append(instance.StateHistory, transition)
}

// validateParameters validates workflow parameters
func (e *Engine) validateParameters(workflow WorkflowDefinition, parameters map[string]interface{}) error {
	for name, def := range workflow.Parameters {
		value, exists := parameters[name]

		if def.Required && !exists {
			return fmt.Errorf("required parameter '%s' not provided", name)
		}

		if !exists && def.Default != nil {
			parameters[name] = def.Default
		}

		// TODO: Implement type checking and validation regex
		_ = value
	}

	return nil
}

// calculateWorkflowPriority calculates priority for a workflow
func (e *Engine) calculateWorkflowPriority(workflow WorkflowDefinition, parameters map[string]interface{}) int64 {
	basePriority := int64(50) // Default middle priority

	// Adjust based on workflow type
	switch workflow.Name {
	case "deploy-pipeline":
		basePriority = 80 // High priority for deployments
	case "simple-ci":
		basePriority = 60 // Medium-high for CI
	default:
		basePriority = 50
	}

	// Check if explicit priority in parameters
	if priority, ok := parameters["priority"].(float64); ok {
		return int64(priority)
	}

	// Check for environment-based priority
	if env, ok := parameters["environment"].(string); ok {
		switch env {
		case "production":
			basePriority += 20
		case "staging":
			basePriority += 10
		}
	}

	return basePriority
}

// calculateJobPriority calculates priority for a job within a workflow
func (e *Engine) calculateJobPriority(instance *WorkflowInstance, action Action) int64 {
	basePriority := e.calculateWorkflowPriority(e.workflows[instance.WorkflowName], instance.Parameters)

	// Adjust based on action type
	if actionPriority, ok := action.Parameters["priority"].(float64); ok {
		return int64(actionPriority)
	}

	// Critical actions get priority boost
	if action.Name == "rollback_production" || action.Name == "rollback_staging" {
		basePriority += 30
	}

	return basePriority
}

// GetInstance gets a workflow instance by ID
func (e *Engine) GetInstance(instanceID string) (*WorkflowInstance, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	instance, exists := e.instances[instanceID]
	if !exists {
		return nil, fmt.Errorf("workflow instance '%s' not found", instanceID)
	}

	return instance, nil
}

// ListInstances lists all workflow instances
func (e *Engine) ListInstances() []*WorkflowInstance {
	e.mu.RLock()
	defer e.mu.RUnlock()

	instances := make([]*WorkflowInstance, 0, len(e.instances))
	for _, instance := range e.instances {
		instances = append(instances, instance)
	}

	return instances
}

// ProcessCornDogsTask processes a Corndogs task for workflow state transitions
func (e *Engine) ProcessCornDogsTask(ctx context.Context, task *pb.Task) error {
	// Parse task payload to determine workflow action
	var payload WorkflowTaskPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("failed to unmarshal workflow payload: %w", err)
	}

	instance, err := e.GetInstance(payload.InstanceID)
	if err != nil {
		return err
	}

	// Handle state transition based on Corndogs state
	if payload.Event != "" {
		return e.triggerEvent(ctx, instance, payload.Event)
	}

	return nil
}