package workflows

import (
	"encoding/json"
	"fmt"
	"time"
)

// WorkflowState represents a state in the workflow state machine
type WorkflowState struct {
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Transitions     map[string]string `json:"transitions"` // event -> next state
	OnEnter         []Action          `json:"on_enter,omitempty"`
	OnExit          []Action          `json:"on_exit,omitempty"`
	TimeoutSeconds  int               `json:"timeout_seconds,omitempty"`
	TimeoutState    string            `json:"timeout_state,omitempty"`
	IsTerminal      bool              `json:"is_terminal,omitempty"`
	RetryPolicy     *RetryPolicy      `json:"retry_policy,omitempty"`
}

// WorkflowDefinition defines a complete workflow
type WorkflowDefinition struct {
	Name         string                    `json:"name"`
	Description  string                    `json:"description"`
	Version      string                    `json:"version"`
	InitialState string                    `json:"initial_state"`
	States       map[string]WorkflowState  `json:"states"`
	Parameters   map[string]ParameterDef   `json:"parameters,omitempty"`
	Metadata     map[string]interface{}    `json:"metadata,omitempty"`
}

// Action represents an action to be performed during state transitions
type Action struct {
	Type       string                 `json:"type"` // "run_job", "notify", "wait", "parallel", "conditional"
	Name       string                 `json:"name"`
	Parameters map[string]interface{} `json:"parameters"`
	OnSuccess  string                 `json:"on_success,omitempty"` // event to trigger
	OnFailure  string                 `json:"on_failure,omitempty"` // event to trigger
}

// ParameterDef defines a workflow parameter
type ParameterDef struct {
	Type         string      `json:"type"` // "string", "number", "boolean", "object"
	Required     bool        `json:"required"`
	Default      interface{} `json:"default,omitempty"`
	Description  string      `json:"description,omitempty"`
	Validation   string      `json:"validation,omitempty"` // regex or expression
}

// RetryPolicy defines retry behavior for a state
type RetryPolicy struct {
	MaxAttempts     int           `json:"max_attempts"`
	BackoffStrategy string        `json:"backoff_strategy"` // "exponential", "linear", "fixed"
	InitialDelay    time.Duration `json:"initial_delay"`
	MaxDelay        time.Duration `json:"max_delay,omitempty"`
	RetryableErrors []string      `json:"retryable_errors,omitempty"`
}

// WorkflowInstance represents a running instance of a workflow
type WorkflowInstance struct {
	InstanceID     string                 `json:"instance_id"`
	WorkflowName   string                 `json:"workflow_name"`
	CurrentState   string                 `json:"current_state"`
	PreviousState  string                 `json:"previous_state,omitempty"`
	Parameters     map[string]interface{} `json:"parameters"`
	Context        map[string]interface{} `json:"context"` // runtime data
	StartedAt      time.Time              `json:"started_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
	CompletedAt    *time.Time             `json:"completed_at,omitempty"`
	Status         string                 `json:"status"` // "running", "completed", "failed", "cancelled"
	StateHistory   []StateTransition      `json:"state_history"`
	ActiveJobs     []string               `json:"active_jobs,omitempty"`
	RetryCount     int                    `json:"retry_count"`
	LastError      string                 `json:"last_error,omitempty"`
}

// StateTransition records a state transition
type StateTransition struct {
	FromState   string                 `json:"from_state"`
	ToState     string                 `json:"to_state"`
	Event       string                 `json:"event"`
	Timestamp   time.Time              `json:"timestamp"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// PredefinedWorkflows contains ready-to-use workflow definitions
var PredefinedWorkflows = map[string]WorkflowDefinition{
	"simple-ci": {
		Name:         "simple-ci",
		Description:  "Simple CI workflow with build and test stages",
		Version:      "1.0.0",
		InitialState: "pending",
		States: map[string]WorkflowState{
			"pending": {
				Name:        "pending",
				Description: "Workflow is pending start",
				Transitions: map[string]string{
					"start": "building",
				},
			},
			"building": {
				Name:        "building",
				Description: "Building the project",
				OnEnter: []Action{
					{
						Type: "run_job",
						Name: "build",
						Parameters: map[string]interface{}{
							"command": "make build",
						},
						OnSuccess: "build_success",
						OnFailure: "build_failed",
					},
				},
				Transitions: map[string]string{
					"build_success": "testing",
					"build_failed":  "failed",
				},
				TimeoutSeconds: 600,
				TimeoutState:   "failed",
			},
			"testing": {
				Name:        "testing",
				Description: "Running tests",
				OnEnter: []Action{
					{
						Type: "run_job",
						Name: "test",
						Parameters: map[string]interface{}{
							"command": "make test",
						},
						OnSuccess: "test_success",
						OnFailure: "test_failed",
					},
				},
				Transitions: map[string]string{
					"test_success": "completed",
					"test_failed":  "failed",
				},
				TimeoutSeconds: 1800,
				TimeoutState:   "failed",
			},
			"completed": {
				Name:        "completed",
				Description: "Workflow completed successfully",
				IsTerminal:  true,
				OnEnter: []Action{
					{
						Type: "notify",
						Name: "success_notification",
						Parameters: map[string]interface{}{
							"channel": "slack",
							"message": "Build and tests completed successfully",
						},
					},
				},
			},
			"failed": {
				Name:        "failed",
				Description: "Workflow failed",
				IsTerminal:  true,
				OnEnter: []Action{
					{
						Type: "notify",
						Name: "failure_notification",
						Parameters: map[string]interface{}{
							"channel": "slack",
							"message": "Build or tests failed",
						},
					},
				},
			},
		},
	},
	"deploy-pipeline": {
		Name:         "deploy-pipeline",
		Description:  "Deployment pipeline with staging and production",
		Version:      "1.0.0",
		InitialState: "pending",
		States: map[string]WorkflowState{
			"pending": {
				Name:        "pending",
				Description: "Deployment pending",
				Transitions: map[string]string{
					"start": "build",
				},
			},
			"build": {
				Name:        "build",
				Description: "Building deployment artifacts",
				OnEnter: []Action{
					{
						Type: "run_job",
						Name: "build_artifacts",
						Parameters: map[string]interface{}{
							"command": "make build-release",
						},
						OnSuccess: "build_complete",
						OnFailure: "build_failed",
					},
				},
				Transitions: map[string]string{
					"build_complete": "test",
					"build_failed":   "failed",
				},
				TimeoutSeconds: 900,
				TimeoutState:   "failed",
			},
			"test": {
				Name:        "test",
				Description: "Running integration tests",
				OnEnter: []Action{
					{
						Type: "parallel",
						Name: "run_tests",
						Parameters: map[string]interface{}{
							"jobs": []map[string]interface{}{
								{"name": "unit_tests", "command": "make test-unit"},
								{"name": "integration_tests", "command": "make test-integration"},
								{"name": "smoke_tests", "command": "make test-smoke"},
							},
						},
						OnSuccess: "tests_passed",
						OnFailure: "tests_failed",
					},
				},
				Transitions: map[string]string{
					"tests_passed": "deploy_staging",
					"tests_failed": "failed",
				},
				TimeoutSeconds: 1800,
				TimeoutState:   "failed",
			},
			"deploy_staging": {
				Name:        "deploy_staging",
				Description: "Deploying to staging environment",
				OnEnter: []Action{
					{
						Type: "run_job",
						Name: "deploy_to_staging",
						Parameters: map[string]interface{}{
							"command":     "make deploy-staging",
							"environment": "staging",
						},
						OnSuccess: "staging_deployed",
						OnFailure: "staging_failed",
					},
				},
				Transitions: map[string]string{
					"staging_deployed": "staging_validation",
					"staging_failed":   "rollback_staging",
				},
				TimeoutSeconds: 600,
				TimeoutState:   "rollback_staging",
				RetryPolicy: &RetryPolicy{
					MaxAttempts:     3,
					BackoffStrategy: "exponential",
					InitialDelay:    30 * time.Second,
					MaxDelay:        5 * time.Minute,
				},
			},
			"staging_validation": {
				Name:        "staging_validation",
				Description: "Validating staging deployment",
				OnEnter: []Action{
					{
						Type: "run_job",
						Name: "validate_staging",
						Parameters: map[string]interface{}{
							"command":     "make validate-staging",
							"environment": "staging",
						},
						OnSuccess: "staging_valid",
						OnFailure: "staging_invalid",
					},
				},
				Transitions: map[string]string{
					"staging_valid":   "await_approval",
					"staging_invalid": "rollback_staging",
				},
				TimeoutSeconds: 300,
				TimeoutState:   "rollback_staging",
			},
			"await_approval": {
				Name:        "await_approval",
				Description: "Awaiting manual approval for production deployment",
				OnEnter: []Action{
					{
						Type: "notify",
						Name: "approval_request",
						Parameters: map[string]interface{}{
							"channel": "slack",
							"message": "Staging deployment successful. Awaiting approval for production.",
						},
					},
				},
				Transitions: map[string]string{
					"approve":      "deploy_production",
					"reject":       "deployment_cancelled",
					"timeout":      "deployment_cancelled",
				},
				TimeoutSeconds: 3600, // 1 hour timeout for approval
				TimeoutState:   "deployment_cancelled",
			},
			"deploy_production": {
				Name:        "deploy_production",
				Description: "Deploying to production environment",
				OnEnter: []Action{
					{
						Type: "run_job",
						Name: "deploy_to_production",
						Parameters: map[string]interface{}{
							"command":     "make deploy-production",
							"environment": "production",
						},
						OnSuccess: "production_deployed",
						OnFailure: "production_failed",
					},
				},
				Transitions: map[string]string{
					"production_deployed": "production_validation",
					"production_failed":   "rollback_production",
				},
				TimeoutSeconds: 900,
				TimeoutState:   "rollback_production",
				RetryPolicy: &RetryPolicy{
					MaxAttempts:     2,
					BackoffStrategy: "exponential",
					InitialDelay:    1 * time.Minute,
					MaxDelay:        10 * time.Minute,
				},
			},
			"production_validation": {
				Name:        "production_validation",
				Description: "Validating production deployment",
				OnEnter: []Action{
					{
						Type: "run_job",
						Name: "validate_production",
						Parameters: map[string]interface{}{
							"command":     "make validate-production",
							"environment": "production",
						},
						OnSuccess: "deployment_success",
						OnFailure: "production_invalid",
					},
				},
				Transitions: map[string]string{
					"deployment_success": "completed",
					"production_invalid": "rollback_production",
				},
				TimeoutSeconds: 600,
				TimeoutState:   "rollback_production",
			},
			"rollback_staging": {
				Name:        "rollback_staging",
				Description: "Rolling back staging deployment",
				OnEnter: []Action{
					{
						Type: "run_job",
						Name: "rollback_staging",
						Parameters: map[string]interface{}{
							"command":     "make rollback-staging",
							"environment": "staging",
						},
						OnSuccess: "rollback_complete",
						OnFailure: "rollback_failed",
					},
				},
				Transitions: map[string]string{
					"rollback_complete": "failed",
					"rollback_failed":   "failed",
				},
				TimeoutSeconds: 600,
				TimeoutState:   "failed",
			},
			"rollback_production": {
				Name:        "rollback_production",
				Description: "Rolling back production deployment",
				OnEnter: []Action{
					{
						Type: "run_job",
						Name: "rollback_production",
						Parameters: map[string]interface{}{
							"command":     "make rollback-production",
							"environment": "production",
						},
						OnSuccess: "rollback_complete",
						OnFailure: "rollback_failed",
					},
					{
						Type: "notify",
						Name: "rollback_alert",
						Parameters: map[string]interface{}{
							"channel":  "pagerduty",
							"severity": "critical",
							"message":  "Production deployment failed, rollback initiated",
						},
					},
				},
				Transitions: map[string]string{
					"rollback_complete": "failed",
					"rollback_failed":   "critical_failure",
				},
				TimeoutSeconds: 900,
				TimeoutState:   "critical_failure",
			},
			"deployment_cancelled": {
				Name:        "deployment_cancelled",
				Description: "Deployment was cancelled",
				IsTerminal:  true,
				OnEnter: []Action{
					{
						Type: "notify",
						Name: "cancelled_notification",
						Parameters: map[string]interface{}{
							"channel": "slack",
							"message": "Deployment was cancelled",
						},
					},
				},
			},
			"completed": {
				Name:        "completed",
				Description: "Deployment completed successfully",
				IsTerminal:  true,
				OnEnter: []Action{
					{
						Type: "notify",
						Name: "success_notification",
						Parameters: map[string]interface{}{
							"channel": "slack",
							"message": "Production deployment completed successfully",
						},
					},
				},
			},
			"failed": {
				Name:        "failed",
				Description: "Deployment failed",
				IsTerminal:  true,
				OnEnter: []Action{
					{
						Type: "notify",
						Name: "failure_notification",
						Parameters: map[string]interface{}{
							"channel": "slack",
							"message": "Deployment failed",
						},
					},
				},
			},
			"critical_failure": {
				Name:        "critical_failure",
				Description: "Critical failure requiring immediate attention",
				IsTerminal:  true,
				OnEnter: []Action{
					{
						Type: "notify",
						Name: "critical_alert",
						Parameters: map[string]interface{}{
							"channel":  "pagerduty",
							"severity": "critical",
							"message":  "CRITICAL: Production deployment and rollback failed",
						},
					},
				},
			},
		},
		Parameters: map[string]ParameterDef{
			"environment": {
				Type:        "string",
				Required:    false,
				Default:     "staging",
				Description: "Target deployment environment",
				Validation:  "^(staging|production)$",
			},
			"version": {
				Type:        "string",
				Required:    true,
				Description: "Version to deploy",
				Validation:  "^v\\d+\\.\\d+\\.\\d+$",
			},
		},
	},
}

// Validate validates the workflow definition
func (w *WorkflowDefinition) Validate() error {
	if w.Name == "" {
		return fmt.Errorf("workflow name is required")
	}
	if w.InitialState == "" {
		return fmt.Errorf("initial state is required")
	}
	if len(w.States) == 0 {
		return fmt.Errorf("at least one state is required")
	}

	// Check if initial state exists
	if _, ok := w.States[w.InitialState]; !ok {
		return fmt.Errorf("initial state '%s' not found in states", w.InitialState)
	}

	// Validate each state
	for name, state := range w.States {
		if state.Name != name {
			return fmt.Errorf("state name mismatch: map key '%s' != state.Name '%s'", name, state.Name)
		}

		// Validate transitions
		for event, targetState := range state.Transitions {
			if _, ok := w.States[targetState]; !ok {
				return fmt.Errorf("state '%s' has transition '%s' to non-existent state '%s'", name, event, targetState)
			}
		}

		// Validate timeout state if specified
		if state.TimeoutState != "" {
			if _, ok := w.States[state.TimeoutState]; !ok {
				return fmt.Errorf("state '%s' has timeout_state to non-existent state '%s'", name, state.TimeoutState)
			}
		}
	}

	// Ensure at least one terminal state
	hasTerminal := false
	for _, state := range w.States {
		if state.IsTerminal {
			hasTerminal = true
			break
		}
	}
	if !hasTerminal {
		return fmt.Errorf("workflow must have at least one terminal state")
	}

	return nil
}

// ToJSON converts the workflow definition to JSON
func (w *WorkflowDefinition) ToJSON() ([]byte, error) {
	return json.MarshalIndent(w, "", "  ")
}

// FromJSON creates a workflow definition from JSON
func FromJSON(data []byte) (*WorkflowDefinition, error) {
	var workflow WorkflowDefinition
	if err := json.Unmarshal(data, &workflow); err != nil {
		return nil, fmt.Errorf("failed to unmarshal workflow: %w", err)
	}
	if err := workflow.Validate(); err != nil {
		return nil, fmt.Errorf("workflow validation failed: %w", err)
	}
	return &workflow, nil
}