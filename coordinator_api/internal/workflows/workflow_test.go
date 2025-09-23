package workflows

import (
	"encoding/json"
	"testing"
)

func TestWorkflowDefinitionValidation(t *testing.T) {
	tests := []struct {
		name        string
		workflow    WorkflowDefinition
		expectError bool
		errorMsg    string
	}{
		{
			name: "Valid simple workflow",
			workflow: WorkflowDefinition{
				Name:         "test-workflow",
				Description:  "Test workflow",
				Version:      "1.0.0",
				InitialState: "start",
				States: map[string]WorkflowState{
					"start": {
						Name: "start",
						Transitions: map[string]string{
							"next": "end",
						},
					},
					"end": {
						Name:       "end",
						IsTerminal: true,
					},
				},
			},
			expectError: false,
		},
		{
			name: "Missing workflow name",
			workflow: WorkflowDefinition{
				Description:  "Test workflow",
				InitialState: "start",
				States: map[string]WorkflowState{
					"start": {
						Name:       "start",
						IsTerminal: true,
					},
				},
			},
			expectError: true,
			errorMsg:    "workflow name is required",
		},
		{
			name: "Missing initial state",
			workflow: WorkflowDefinition{
				Name:        "test-workflow",
				Description: "Test workflow",
				States: map[string]WorkflowState{
					"start": {
						Name:       "start",
						IsTerminal: true,
					},
				},
			},
			expectError: true,
			errorMsg:    "initial state is required",
		},
		{
			name: "No states defined",
			workflow: WorkflowDefinition{
				Name:         "test-workflow",
				Description:  "Test workflow",
				InitialState: "start",
				States:       map[string]WorkflowState{},
			},
			expectError: true,
			errorMsg:    "at least one state is required",
		},
		{
			name: "Initial state not in states",
			workflow: WorkflowDefinition{
				Name:         "test-workflow",
				Description:  "Test workflow",
				InitialState: "missing",
				States: map[string]WorkflowState{
					"start": {
						Name:       "start",
						IsTerminal: true,
					},
				},
			},
			expectError: true,
			errorMsg:    "initial state 'missing' not found in states",
		},
		{
			name: "State name mismatch",
			workflow: WorkflowDefinition{
				Name:         "test-workflow",
				InitialState: "start",
				States: map[string]WorkflowState{
					"start": {
						Name:       "different",
						IsTerminal: true,
					},
				},
			},
			expectError: true,
			errorMsg:    "state name mismatch",
		},
		{
			name: "Transition to non-existent state",
			workflow: WorkflowDefinition{
				Name:         "test-workflow",
				InitialState: "start",
				States: map[string]WorkflowState{
					"start": {
						Name: "start",
						Transitions: map[string]string{
							"next": "missing",
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "transition 'next' to non-existent state 'missing'",
		},
		{
			name: "Invalid timeout state",
			workflow: WorkflowDefinition{
				Name:         "test-workflow",
				InitialState: "start",
				States: map[string]WorkflowState{
					"start": {
						Name:           "start",
						TimeoutSeconds: 10,
						TimeoutState:   "missing",
						IsTerminal:     true,
					},
				},
			},
			expectError: true,
			errorMsg:    "timeout_state to non-existent state 'missing'",
		},
		{
			name: "No terminal state",
			workflow: WorkflowDefinition{
				Name:         "test-workflow",
				InitialState: "start",
				States: map[string]WorkflowState{
					"start": {
						Name: "start",
						Transitions: map[string]string{
							"next": "middle",
						},
					},
					"middle": {
						Name: "middle",
						Transitions: map[string]string{
							"next": "start",
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "workflow must have at least one terminal state",
		},
		{
			name: "Complex valid workflow",
			workflow: WorkflowDefinition{
				Name:         "complex-workflow",
				Description:  "Complex workflow with multiple paths",
				Version:      "1.0.0",
				InitialState: "pending",
				States: map[string]WorkflowState{
					"pending": {
						Name: "pending",
						Transitions: map[string]string{
							"start": "running",
						},
					},
					"running": {
						Name: "running",
						Transitions: map[string]string{
							"success": "completed",
							"failure": "failed",
							"timeout": "failed",
						},
						TimeoutSeconds: 3600,
						TimeoutState:   "failed",
						OnEnter: []Action{
							{
								Type: "run_job",
								Name: "main_job",
								Parameters: map[string]interface{}{
									"command": "make build",
								},
							},
						},
					},
					"completed": {
						Name:       "completed",
						IsTerminal: true,
					},
					"failed": {
						Name:       "failed",
						IsTerminal: true,
						OnEnter: []Action{
							{
								Type: "notify",
								Name: "failure_notification",
								Parameters: map[string]interface{}{
									"channel": "slack",
								},
							},
						},
					},
				},
				Parameters: map[string]ParameterDef{
					"environment": {
						Type:     "string",
						Required: true,
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.workflow.Validate()

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error containing '%s', got nil", tt.errorMsg)
				} else if tt.errorMsg != "" {
					errStr := err.Error()
					found := false
					for i := 0; i <= len(errStr)-len(tt.errorMsg); i++ {
						if errStr[i:i+len(tt.errorMsg)] == tt.errorMsg {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("Expected error containing '%s', got '%s'", tt.errorMsg, err.Error())
					}
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

func TestPredefinedWorkflows(t *testing.T) {
	for name, workflow := range PredefinedWorkflows {
		t.Run(name, func(t *testing.T) {
			// Test validation
			if err := workflow.Validate(); err != nil {
				t.Errorf("Predefined workflow '%s' failed validation: %v", name, err)
			}

			// Test JSON serialization
			jsonData, err := workflow.ToJSON()
			if err != nil {
				t.Errorf("Failed to serialize workflow '%s': %v", name, err)
			}

			// Test JSON deserialization
			var decoded WorkflowDefinition
			if err := json.Unmarshal(jsonData, &decoded); err != nil {
				t.Errorf("Failed to deserialize workflow '%s': %v", name, err)
			}

			// Verify the deserialized workflow is valid
			if err := decoded.Validate(); err != nil {
				t.Errorf("Deserialized workflow '%s' failed validation: %v", name, err)
			}

			// Check specific workflow properties
			switch name {
			case "simple-ci":
				if decoded.InitialState != "pending" {
					t.Errorf("Expected initial state 'pending', got '%s'", decoded.InitialState)
				}
				if len(decoded.States) != 5 {
					t.Errorf("Expected 5 states, got %d", len(decoded.States))
				}

			case "deploy-pipeline":
				if decoded.InitialState != "pending" {
					t.Errorf("Expected initial state 'pending', got '%s'", decoded.InitialState)
				}
				// Should have parameters defined
				if len(decoded.Parameters) != 2 {
					t.Errorf("Expected 2 parameters, got %d", len(decoded.Parameters))
				}
				// Check for critical failure state
				if _, ok := decoded.States["critical_failure"]; !ok {
					t.Error("Missing critical_failure state")
				}
			}
		})
	}
}

func TestWorkflowJSONSerialization(t *testing.T) {
	workflow := WorkflowDefinition{
		Name:         "test-workflow",
		Description:  "Test workflow",
		Version:      "1.0.0",
		InitialState: "start",
		States: map[string]WorkflowState{
			"start": {
				Name: "start",
				Transitions: map[string]string{
					"next": "process",
				},
				OnEnter: []Action{
					{
						Type: "run_job",
						Name: "init",
						Parameters: map[string]interface{}{
							"command": "echo 'Starting'",
						},
					},
				},
			},
			"process": {
				Name: "process",
				Transitions: map[string]string{
					"done": "end",
				},
				TimeoutSeconds: 60,
				TimeoutState:   "end",
				RetryPolicy: &RetryPolicy{
					MaxAttempts:     3,
					BackoffStrategy: "exponential",
				},
			},
			"end": {
				Name:       "end",
				IsTerminal: true,
			},
		},
		Parameters: map[string]ParameterDef{
			"input": {
				Type:        "string",
				Required:    true,
				Description: "Input parameter",
			},
		},
	}

	// Test ToJSON
	jsonData, err := workflow.ToJSON()
	if err != nil {
		t.Fatalf("Failed to serialize workflow: %v", err)
	}

	// Test FromJSON
	decoded, err := FromJSON(jsonData)
	if err != nil {
		t.Fatalf("Failed to deserialize workflow: %v", err)
	}

	// Verify fields
	if decoded.Name != workflow.Name {
		t.Errorf("Name mismatch: expected %s, got %s", workflow.Name, decoded.Name)
	}
	if decoded.InitialState != workflow.InitialState {
		t.Errorf("InitialState mismatch: expected %s, got %s", workflow.InitialState, decoded.InitialState)
	}
	if len(decoded.States) != len(workflow.States) {
		t.Errorf("States count mismatch: expected %d, got %d", len(workflow.States), len(decoded.States))
	}
	if len(decoded.Parameters) != len(workflow.Parameters) {
		t.Errorf("Parameters count mismatch: expected %d, got %d", len(workflow.Parameters), len(decoded.Parameters))
	}
}

func TestStateTransitions(t *testing.T) {
	workflow := PredefinedWorkflows["simple-ci"]

	tests := []struct {
		name           string
		currentState   string
		event          string
		expectedNext   string
		shouldTransition bool
	}{
		{
			name:           "Pending to building",
			currentState:   "pending",
			event:          "start",
			expectedNext:   "building",
			shouldTransition: true,
		},
		{
			name:           "Building success to testing",
			currentState:   "building",
			event:          "build_success",
			expectedNext:   "testing",
			shouldTransition: true,
		},
		{
			name:           "Building failure to failed",
			currentState:   "building",
			event:          "build_failed",
			expectedNext:   "failed",
			shouldTransition: true,
		},
		{
			name:           "Testing success to completed",
			currentState:   "testing",
			event:          "test_success",
			expectedNext:   "completed",
			shouldTransition: true,
		},
		{
			name:           "Invalid event from pending",
			currentState:   "pending",
			event:          "invalid_event",
			expectedNext:   "",
			shouldTransition: false,
		},
		{
			name:           "Event from terminal state",
			currentState:   "completed",
			event:          "any_event",
			expectedNext:   "",
			shouldTransition: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := workflow.States[tt.currentState]
			nextState, exists := state.Transitions[tt.event]

			if tt.shouldTransition {
				if !exists {
					t.Errorf("Expected transition for event '%s' from state '%s'", tt.event, tt.currentState)
				}
				if nextState != tt.expectedNext {
					t.Errorf("Expected next state '%s', got '%s'", tt.expectedNext, nextState)
				}
			} else {
				if exists {
					t.Errorf("Unexpected transition for event '%s' from state '%s'", tt.event, tt.currentState)
				}
			}
		})
	}
}

func TestRetryPolicy(t *testing.T) {
	workflow := PredefinedWorkflows["deploy-pipeline"]

	// Check staging deployment retry policy
	stagingState := workflow.States["deploy_staging"]
	if stagingState.RetryPolicy == nil {
		t.Error("Expected retry policy for deploy_staging state")
	} else {
		if stagingState.RetryPolicy.MaxAttempts != 3 {
			t.Errorf("Expected 3 max attempts, got %d", stagingState.RetryPolicy.MaxAttempts)
		}
		if stagingState.RetryPolicy.BackoffStrategy != "exponential" {
			t.Errorf("Expected exponential backoff, got %s", stagingState.RetryPolicy.BackoffStrategy)
		}
	}

	// Check production deployment retry policy
	prodState := workflow.States["deploy_production"]
	if prodState.RetryPolicy == nil {
		t.Error("Expected retry policy for deploy_production state")
	} else {
		if prodState.RetryPolicy.MaxAttempts != 2 {
			t.Errorf("Expected 2 max attempts, got %d", prodState.RetryPolicy.MaxAttempts)
		}
	}
}