package scheduler

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// MockCornDogsClient for testing
type MockCornDogsClient struct {
	SubmitTaskFunc      func(ctx context.Context, payload *corndogs.TaskPayload, priority int64) (*pb.Task, error)
	GetTaskStateCountsFunc func(ctx context.Context) (int64, map[string]int64, error)
}

func (m *MockCornDogsClient) SubmitTask(ctx context.Context, payload *corndogs.TaskPayload, priority int64) (*pb.Task, error) {
	if m.SubmitTaskFunc != nil {
		return m.SubmitTaskFunc(ctx, payload, priority)
	}
	return &pb.Task{
		Uuid:         uuid.New().String(),
		CurrentState: "submitted",
		Priority:     priority,
	}, nil
}

func (m *MockCornDogsClient) GetNextTask(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
	return nil, nil
}

func (m *MockCornDogsClient) UpdateTask(ctx context.Context, taskID string, currentState string, newState string, payload []byte) (*pb.Task, error) {
	return nil, nil
}

func (m *MockCornDogsClient) SendHeartbeat(ctx context.Context, taskID string, currentState string, timeoutExtensionSeconds int64) (*pb.Task, error) {
	return nil, nil
}

func (m *MockCornDogsClient) CompleteTask(ctx context.Context, taskID string, currentState string) (*pb.Task, error) {
	return nil, nil
}

func (m *MockCornDogsClient) CancelTask(ctx context.Context, taskID string, currentState string) (*pb.Task, error) {
	return nil, nil
}

func (m *MockCornDogsClient) GetTaskByID(ctx context.Context, taskID string) (*pb.Task, error) {
	return nil, nil
}

func (m *MockCornDogsClient) CleanUpTimedOut(ctx context.Context) (int64, error) {
	return 0, nil
}

func (m *MockCornDogsClient) GetQueues(ctx context.Context) ([]string, int64, error) {
	return []string{"critical", "high-priority", "normal", "low-priority"}, 100, nil
}

func (m *MockCornDogsClient) GetQueueTaskCounts(ctx context.Context) (map[string]int64, int64, error) {
	return map[string]int64{
		"critical":      10,
		"high-priority": 25,
		"normal":        50,
		"low-priority":  15,
	}, 100, nil
}

func (m *MockCornDogsClient) GetTaskStateCounts(ctx context.Context) (int64, map[string]int64, error) {
	if m.GetTaskStateCountsFunc != nil {
		return m.GetTaskStateCountsFunc(ctx)
	}
	return 100, map[string]int64{
		"submitted":         50,
		"submitted-working": 30,
		"completed":         20,
	}, nil
}

func (m *MockCornDogsClient) Close() error {
	return nil
}

func TestPrioritySchedulerInitialization(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	mockClient := &MockCornDogsClient{}
	scheduler := NewPriorityScheduler(mockClient, logger)

	// Check that default queues are initialized
	expectedQueues := []string{"critical", "high-priority", "normal", "low-priority"}

	for _, queueName := range expectedQueues {
		if _, exists := scheduler.queueConfig[queueName]; !exists {
			t.Errorf("Expected queue '%s' not found", queueName)
		}
	}

	// Check priority ranges
	criticalQueue := scheduler.queueConfig["critical"]
	if criticalQueue.PriorityRange.Min != 90 || criticalQueue.PriorityRange.Max != 100 {
		t.Errorf("Critical queue priority range incorrect: got %d-%d, expected 90-100",
			criticalQueue.PriorityRange.Min, criticalQueue.PriorityRange.Max)
	}

	normalQueue := scheduler.queueConfig["normal"]
	if normalQueue.PriorityRange.Min != 30 || normalQueue.PriorityRange.Max != 69 {
		t.Errorf("Normal queue priority range incorrect: got %d-%d, expected 30-69",
			normalQueue.PriorityRange.Min, normalQueue.PriorityRange.Max)
	}

	// Check that routing rules are initialized
	if len(scheduler.routingRules) == 0 {
		t.Error("Expected routing rules to be initialized")
	}
}

func TestQueueRouting(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	mockClient := &MockCornDogsClient{}
	scheduler := NewPriorityScheduler(mockClient, logger)

	tests := []struct {
		name          string
		metadata      map[string]interface{}
		expectedQueue string
	}{
		{
			name: "Production rollback to critical",
			metadata: map[string]interface{}{
				"job_type": "rollback",
				"environment": "production",
			},
			expectedQueue: "critical",
		},
		{
			name: "Production deployment to high priority",
			metadata: map[string]interface{}{
				"job_type": "deploy",
				"environment": "production",
			},
			expectedQueue: "high-priority",
		},
		{
			name: "Staging deployment to normal",
			metadata: map[string]interface{}{
				"job_type": "deploy",
				"environment": "staging",
			},
			expectedQueue: "normal",
		},
		{
			name: "Main branch build to normal",
			metadata: map[string]interface{}{
				"job_type": "build",
				"branch": "main",
			},
			expectedQueue: "normal",
		},
		{
			name: "Feature branch build to low priority",
			metadata: map[string]interface{}{
				"job_type": "build",
				"branch": "feature/new-feature",
			},
			expectedQueue: "low-priority",
		},
		{
			name: "Scheduled job to low priority",
			metadata: map[string]interface{}{
				"trigger_type": "scheduled",
			},
			expectedQueue: "low-priority",
		},
		{
			name: "Unknown job type defaults to normal",
			metadata: map[string]interface{}{
				"job_type": "unknown",
			},
			expectedQueue: "normal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue := scheduler.determineTargetQueue(tt.metadata)
			if queue != tt.expectedQueue {
				t.Errorf("Expected queue '%s', got '%s'", tt.expectedQueue, queue)
			}
		})
	}
}

func TestPriorityCalculation(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	mockClient := &MockCornDogsClient{}
	scheduler := NewPriorityScheduler(mockClient, logger)

	tests := []struct {
		name         string
		metadata     map[string]interface{}
		queueName    string
		expectedMin  int64
		expectedMax  int64
	}{
		{
			name: "Explicit priority within range",
			metadata: map[string]interface{}{
				"priority": float64(95),
			},
			queueName:   "critical",
			expectedMin: 95,
			expectedMax: 95,
		},
		{
			name: "Explicit priority clamped to max",
			metadata: map[string]interface{}{
				"priority": float64(150),
			},
			queueName:   "critical",
			expectedMin: 100,
			expectedMax: 100,
		},
		{
			name: "Explicit priority clamped to min",
			metadata: map[string]interface{}{
				"priority": float64(-10),
			},
			queueName:   "low-priority",
			expectedMin: 0,
			expectedMax: 0,
		},
		{
			name: "Rollback gets priority boost",
			metadata: map[string]interface{}{
				"job_type": "rollback",
			},
			queueName:   "normal",
			expectedMin: 59, // Base 49 + 20 for rollback - 10 (middle of 30-69 range)
			expectedMax: 69, // Clamped to max
		},
		{
			name: "Production environment boost",
			metadata: map[string]interface{}{
				"job_type": "deploy",
				"environment": "production",
			},
			queueName:   "normal",
			expectedMin: 59, // Base 49 + 10 for deploy + 10 for production - 10
			expectedMax: 69,
		},
		{
			name: "Development environment penalty",
			metadata: map[string]interface{}{
				"job_type": "test",
				"environment": "development",
			},
			queueName:   "normal",
			expectedMin: 39, // Base 49 + 0 for test - 5 for development - 5
			expectedMax: 49,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queueConfig := scheduler.queueConfig[tt.queueName]
			priority := scheduler.calculatePriority(tt.metadata, queueConfig)

			if priority < tt.expectedMin || priority > tt.expectedMax {
				t.Errorf("Priority %d outside expected range [%d, %d]",
					priority, tt.expectedMin, tt.expectedMax)
			}
		})
	}
}

func TestSubmitJobWithRouting(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	var capturedPayload *corndogs.TaskPayload
	var capturedPriority int64

	mockClient := &MockCornDogsClient{
		SubmitTaskFunc: func(ctx context.Context, payload *corndogs.TaskPayload, priority int64) (*pb.Task, error) {
			capturedPayload = payload
			capturedPriority = priority
			return &pb.Task{
				Uuid:         uuid.New().String(),
				CurrentState: "submitted",
				Priority:     priority,
			}, nil
		},
	}

	scheduler := NewPriorityScheduler(mockClient, logger)

	jobPayload := &corndogs.TaskPayload{
		JobID:   uuid.New().String(),
		JobType: "deploy",
		Config:  make(map[string]interface{}),
		Source:  make(map[string]interface{}),
		Metadata: make(map[string]interface{}),
	}

	jobMetadata := map[string]interface{}{
		"job_type":    "deploy",
		"environment": "production",
		"priority":    float64(85),
	}

	ctx := context.Background()
	task, err := scheduler.SubmitJob(ctx, jobPayload, jobMetadata)

	if err != nil {
		t.Fatalf("Failed to submit job: %v", err)
	}

	if task == nil {
		t.Fatal("Expected task to be returned")
	}

	// Check that the job was routed to high-priority queue
	if queue, ok := capturedPayload.Metadata["queue"].(string); !ok || queue != "high-priority" {
		t.Errorf("Expected queue 'high-priority', got '%v'", queue)
	}

	// Check that priority was set correctly (clamped to queue max)
	if capturedPriority != 85 {
		t.Errorf("Expected priority 85, got %d", capturedPriority)
	}

	// Check that resource limits were added
	if limits, ok := capturedPayload.Config["resource_limits"].(map[string]string); !ok {
		t.Error("Expected resource limits to be added")
	} else {
		if limits["cpu"] != "2000m" {
			t.Errorf("Expected CPU limit '2000m', got '%s'", limits["cpu"])
		}
		if limits["memory"] != "4Gi" {
			t.Errorf("Expected memory limit '4Gi', got '%s'", limits["memory"])
		}
	}

	// Check that timeout was set from queue config
	if timeout, ok := capturedPayload.Config["timeout"].(int); !ok || timeout != 1800 {
		t.Errorf("Expected timeout 1800, got %v", capturedPayload.Config["timeout"])
	}
}

func TestRoutingRulePriority(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	mockClient := &MockCornDogsClient{}
	scheduler := NewPriorityScheduler(mockClient, logger)

	// Clear default rules
	scheduler.routingRules = []RoutingRule{}

	// Add rules with different priorities
	scheduler.AddRoutingRule(RoutingRule{
		Name:     "low-priority-rule",
		Priority: 10,
		Conditions: []Condition{
			{Field: "job_type", Operator: "equals", Value: "test"},
		},
		TargetQueue: "low-priority",
	})

	scheduler.AddRoutingRule(RoutingRule{
		Name:     "high-priority-rule",
		Priority: 100,
		Conditions: []Condition{
			{Field: "job_type", Operator: "equals", Value: "test"},
		},
		TargetQueue: "high-priority",
	})

	// Test that higher priority rule wins
	metadata := map[string]interface{}{
		"job_type": "test",
	}

	queue := scheduler.determineTargetQueue(metadata)
	if queue != "high-priority" {
		t.Errorf("Expected high-priority rule to win, got queue '%s'", queue)
	}

	// Verify rules are sorted correctly
	if scheduler.routingRules[0].Priority != 100 {
		t.Errorf("Expected first rule to have priority 100, got %d", scheduler.routingRules[0].Priority)
	}
}

func TestConditionEvaluation(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	mockClient := &MockCornDogsClient{}
	scheduler := NewPriorityScheduler(mockClient, logger)

	tests := []struct {
		name      string
		condition Condition
		metadata  map[string]interface{}
		expected  bool
	}{
		{
			name: "Equals operator - match",
			condition: Condition{
				Field:    "job_type",
				Operator: "equals",
				Value:    "build",
			},
			metadata: map[string]interface{}{
				"job_type": "build",
			},
			expected: true,
		},
		{
			name: "Equals operator - no match",
			condition: Condition{
				Field:    "job_type",
				Operator: "equals",
				Value:    "build",
			},
			metadata: map[string]interface{}{
				"job_type": "test",
			},
			expected: false,
		},
		{
			name: "Contains operator - match",
			condition: Condition{
				Field:    "job_type",
				Operator: "contains",
				Value:    "roll",
			},
			metadata: map[string]interface{}{
				"job_type": "rollback",
			},
			expected: true,
		},
		{
			name: "Contains operator - no match",
			condition: Condition{
				Field:    "job_type",
				Operator: "contains",
				Value:    "deploy",
			},
			metadata: map[string]interface{}{
				"job_type": "build",
			},
			expected: false,
		},
		{
			name: "In operator - match",
			condition: Condition{
				Field:    "branch",
				Operator: "in",
				Value:    []string{"main", "master", "develop"},
			},
			metadata: map[string]interface{}{
				"branch": "main",
			},
			expected: true,
		},
		{
			name: "In operator - no match",
			condition: Condition{
				Field:    "branch",
				Operator: "in",
				Value:    []string{"main", "master", "develop"},
			},
			metadata: map[string]interface{}{
				"branch": "feature/test",
			},
			expected: false,
		},
		{
			name: "Field not present",
			condition: Condition{
				Field:    "missing_field",
				Operator: "equals",
				Value:    "value",
			},
			metadata: map[string]interface{}{
				"other_field": "value",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := scheduler.evaluateCondition(tt.condition, tt.metadata)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestGetQueueMetrics(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	mockClient := &MockCornDogsClient{
		GetTaskStateCountsFunc: func(ctx context.Context) (int64, map[string]int64, error) {
			return 100, map[string]int64{
				"submitted":         50,
				"submitted-working": 30,
				"completed":         20,
			}, nil
		},
	}

	scheduler := NewPriorityScheduler(mockClient, logger)

	ctx := context.Background()
	metrics, err := scheduler.GetQueueMetrics(ctx)

	if err != nil {
		t.Fatalf("Failed to get queue metrics: %v", err)
	}

	// Check that metrics were returned for all queues
	expectedQueues := []string{"critical", "high-priority", "normal", "low-priority"}

	for _, queueName := range expectedQueues {
		if _, exists := metrics[queueName]; !exists {
			t.Errorf("Expected metrics for queue '%s'", queueName)
		}
	}

	// Check specific metric values
	criticalMetrics := metrics["critical"]
	if criticalMetrics.MaxConcurrency != 10 {
		t.Errorf("Expected max concurrency 10 for critical queue, got %d", criticalMetrics.MaxConcurrency)
	}
}