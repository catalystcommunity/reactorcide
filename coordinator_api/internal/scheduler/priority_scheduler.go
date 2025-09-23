package scheduler

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
	"github.com/sirupsen/logrus"
)

// PriorityScheduler manages job scheduling with priority-based queue routing
type PriorityScheduler struct {
	corndogsClient corndogs.ClientInterface
	queueConfig    map[string]QueueConfig
	routingRules   []RoutingRule
	mu             sync.RWMutex
	logger         *logrus.Logger
}

// QueueConfig defines configuration for a specific queue
type QueueConfig struct {
	Name            string        `json:"name"`
	Description     string        `json:"description"`
	PriorityRange   PriorityRange `json:"priority_range"`
	MaxConcurrency  int           `json:"max_concurrency"`
	TimeoutSeconds  int           `json:"timeout_seconds"`
	RetryPolicy     RetryPolicy   `json:"retry_policy"`
	ResourceLimits  ResourceLimits `json:"resource_limits"`
}

// PriorityRange defines the priority range for a queue
type PriorityRange struct {
	Min int64 `json:"min"`
	Max int64 `json:"max"`
}

// RetryPolicy defines retry behavior
type RetryPolicy struct {
	MaxRetries      int           `json:"max_retries"`
	BackoffStrategy string        `json:"backoff_strategy"` // "exponential", "linear", "fixed"
	InitialDelay    time.Duration `json:"initial_delay"`
	MaxDelay        time.Duration `json:"max_delay"`
}

// ResourceLimits defines resource constraints for jobs in a queue
type ResourceLimits struct {
	MaxCPU    string `json:"max_cpu"`    // e.g., "2000m" for 2 CPUs
	MaxMemory string `json:"max_memory"` // e.g., "4Gi"
	MaxDisk   string `json:"max_disk"`   // e.g., "10Gi"
}

// RoutingRule defines how jobs are routed to queues
type RoutingRule struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Priority    int                    `json:"priority"` // Rule priority (higher = evaluated first)
	Conditions  []Condition            `json:"conditions"`
	TargetQueue string                 `json:"target_queue"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// Condition defines a routing condition
type Condition struct {
	Field    string      `json:"field"`    // e.g., "job_type", "user_id", "environment"
	Operator string      `json:"operator"` // e.g., "equals", "contains", "matches", "in"
	Value    interface{} `json:"value"`
}

// NewPriorityScheduler creates a new priority scheduler
func NewPriorityScheduler(corndogsClient corndogs.ClientInterface, logger *logrus.Logger) *PriorityScheduler {
	if logger == nil {
		logger = logrus.New()
	}

	scheduler := &PriorityScheduler{
		corndogsClient: corndogsClient,
		queueConfig:    make(map[string]QueueConfig),
		routingRules:   []RoutingRule{},
		logger:         logger,
	}

	// Initialize default queues
	scheduler.InitializeDefaultQueues()

	return scheduler
}

// InitializeDefaultQueues sets up default queue configurations
func (s *PriorityScheduler) InitializeDefaultQueues() {
	// Critical queue for urgent production tasks
	s.RegisterQueue(QueueConfig{
		Name:        "critical",
		Description: "Critical production tasks",
		PriorityRange: PriorityRange{
			Min: 90,
			Max: 100,
		},
		MaxConcurrency: 10,
		TimeoutSeconds: 300,
		RetryPolicy: RetryPolicy{
			MaxRetries:      5,
			BackoffStrategy: "exponential",
			InitialDelay:    10 * time.Second,
			MaxDelay:        5 * time.Minute,
		},
		ResourceLimits: ResourceLimits{
			MaxCPU:    "4000m",
			MaxMemory: "8Gi",
			MaxDisk:   "50Gi",
		},
	})

	// High priority queue for deployments and important builds
	s.RegisterQueue(QueueConfig{
		Name:        "high-priority",
		Description: "High priority tasks (deployments, releases)",
		PriorityRange: PriorityRange{
			Min: 70,
			Max: 89,
		},
		MaxConcurrency: 20,
		TimeoutSeconds: 1800,
		RetryPolicy: RetryPolicy{
			MaxRetries:      3,
			BackoffStrategy: "exponential",
			InitialDelay:    30 * time.Second,
			MaxDelay:        10 * time.Minute,
		},
		ResourceLimits: ResourceLimits{
			MaxCPU:    "2000m",
			MaxMemory: "4Gi",
			MaxDisk:   "20Gi",
		},
	})

	// Normal queue for regular CI builds
	s.RegisterQueue(QueueConfig{
		Name:        "normal",
		Description: "Normal priority tasks (CI builds, tests)",
		PriorityRange: PriorityRange{
			Min: 30,
			Max: 69,
		},
		MaxConcurrency: 50,
		TimeoutSeconds: 3600,
		RetryPolicy: RetryPolicy{
			MaxRetries:      2,
			BackoffStrategy: "linear",
			InitialDelay:    1 * time.Minute,
			MaxDelay:        15 * time.Minute,
		},
		ResourceLimits: ResourceLimits{
			MaxCPU:    "1000m",
			MaxMemory: "2Gi",
			MaxDisk:   "10Gi",
		},
	})

	// Low priority queue for non-critical tasks
	s.RegisterQueue(QueueConfig{
		Name:        "low-priority",
		Description: "Low priority tasks (scheduled jobs, maintenance)",
		PriorityRange: PriorityRange{
			Min: 0,
			Max: 29,
		},
		MaxConcurrency: 30,
		TimeoutSeconds: 7200,
		RetryPolicy: RetryPolicy{
			MaxRetries:      1,
			BackoffStrategy: "fixed",
			InitialDelay:    5 * time.Minute,
			MaxDelay:        5 * time.Minute,
		},
		ResourceLimits: ResourceLimits{
			MaxCPU:    "500m",
			MaxMemory: "1Gi",
			MaxDisk:   "5Gi",
		},
	})

	// Initialize default routing rules
	s.InitializeDefaultRoutingRules()
}

// InitializeDefaultRoutingRules sets up default routing rules
func (s *PriorityScheduler) InitializeDefaultRoutingRules() {
	// Route production deployments to high priority
	s.AddRoutingRule(RoutingRule{
		Name:        "production-deployments",
		Description: "Route production deployments to high priority queue",
		Priority:    100,
		Conditions: []Condition{
			{Field: "job_type", Operator: "equals", Value: "deploy"},
			{Field: "environment", Operator: "equals", Value: "production"},
		},
		TargetQueue: "high-priority",
	})

	// Route rollbacks to critical queue
	s.AddRoutingRule(RoutingRule{
		Name:        "rollbacks",
		Description: "Route rollback operations to critical queue",
		Priority:    110,
		Conditions: []Condition{
			{Field: "job_type", Operator: "contains", Value: "rollback"},
		},
		TargetQueue: "critical",
	})

	// Route staging deployments to normal queue
	s.AddRoutingRule(RoutingRule{
		Name:        "staging-deployments",
		Description: "Route staging deployments to normal queue",
		Priority:    90,
		Conditions: []Condition{
			{Field: "job_type", Operator: "equals", Value: "deploy"},
			{Field: "environment", Operator: "equals", Value: "staging"},
		},
		TargetQueue: "normal",
	})

	// Route CI builds based on branch
	s.AddRoutingRule(RoutingRule{
		Name:        "main-branch-builds",
		Description: "Route main branch builds to normal queue",
		Priority:    80,
		Conditions: []Condition{
			{Field: "job_type", Operator: "equals", Value: "build"},
			{Field: "branch", Operator: "in", Value: []string{"main", "master", "develop"}},
		},
		TargetQueue: "normal",
	})

	// Route feature branch builds to low priority
	s.AddRoutingRule(RoutingRule{
		Name:        "feature-branch-builds",
		Description: "Route feature branch builds to low priority queue",
		Priority:    70,
		Conditions: []Condition{
			{Field: "job_type", Operator: "equals", Value: "build"},
			{Field: "branch", Operator: "matches", Value: "^(feature|bugfix|hotfix)/.*"},
		},
		TargetQueue: "low-priority",
	})

	// Route scheduled jobs to low priority
	s.AddRoutingRule(RoutingRule{
		Name:        "scheduled-jobs",
		Description: "Route scheduled/cron jobs to low priority queue",
		Priority:    60,
		Conditions: []Condition{
			{Field: "trigger_type", Operator: "equals", Value: "scheduled"},
		},
		TargetQueue: "low-priority",
	})
}

// RegisterQueue registers a queue configuration
func (s *PriorityScheduler) RegisterQueue(config QueueConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.queueConfig[config.Name] = config
	s.logger.WithField("queue", config.Name).Info("Registered queue configuration")
}

// AddRoutingRule adds a routing rule
func (s *PriorityScheduler) AddRoutingRule(rule RoutingRule) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.routingRules = append(s.routingRules, rule)
	// Sort rules by priority (descending)
	s.sortRoutingRules()

	s.logger.WithField("rule", rule.Name).Info("Added routing rule")
}

// sortRoutingRules sorts routing rules by priority
func (s *PriorityScheduler) sortRoutingRules() {
	// Simple bubble sort for small number of rules
	n := len(s.routingRules)
	for i := 0; i < n-1; i++ {
		for j := 0; j < n-i-1; j++ {
			if s.routingRules[j].Priority < s.routingRules[j+1].Priority {
				s.routingRules[j], s.routingRules[j+1] = s.routingRules[j+1], s.routingRules[j]
			}
		}
	}
}

// SubmitJob submits a job with priority-based queue routing
func (s *PriorityScheduler) SubmitJob(ctx context.Context, jobPayload *corndogs.TaskPayload, jobMetadata map[string]interface{}) (*pb.Task, error) {
	// Determine target queue based on routing rules
	targetQueue := s.determineTargetQueue(jobMetadata)

	// Get queue configuration
	s.mu.RLock()
	queueConfig, exists := s.queueConfig[targetQueue]
	s.mu.RUnlock()

	if !exists {
		// Fall back to default queue
		targetQueue = "normal"
		queueConfig = s.queueConfig[targetQueue]
	}

	// Calculate priority based on job metadata and queue config
	priority := s.calculatePriority(jobMetadata, queueConfig)

	// Add queue name to metadata
	jobPayload.Metadata["queue"] = targetQueue
	jobPayload.Metadata["priority"] = priority

	// Set timeout based on queue config
	if timeout, ok := jobPayload.Config["timeout"].(int); !ok || timeout == 0 {
		jobPayload.Config["timeout"] = queueConfig.TimeoutSeconds
	}

	// Add resource limits from queue config
	jobPayload.Config["resource_limits"] = map[string]string{
		"cpu":    queueConfig.ResourceLimits.MaxCPU,
		"memory": queueConfig.ResourceLimits.MaxMemory,
		"disk":   queueConfig.ResourceLimits.MaxDisk,
	}

	// Submit to Corndogs with calculated priority
	task, err := s.corndogsClient.SubmitTask(ctx, jobPayload, priority)
	if err != nil {
		return nil, fmt.Errorf("failed to submit job to queue '%s': %w", targetQueue, err)
	}

	s.logger.WithFields(logrus.Fields{
		"job_id":   jobPayload.JobID,
		"queue":    targetQueue,
		"priority": priority,
		"task_id":  task.Uuid,
	}).Info("Job submitted with priority scheduling")

	return task, nil
}

// determineTargetQueue determines which queue to route a job to
func (s *PriorityScheduler) determineTargetQueue(metadata map[string]interface{}) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check routing rules in order of priority
	for _, rule := range s.routingRules {
		if s.evaluateRule(rule, metadata) {
			s.logger.WithFields(logrus.Fields{
				"rule":  rule.Name,
				"queue": rule.TargetQueue,
			}).Debug("Routing rule matched")
			return rule.TargetQueue
		}
	}

	// Default to normal queue if no rules match
	return "normal"
}

// evaluateRule evaluates if a routing rule matches the job metadata
func (s *PriorityScheduler) evaluateRule(rule RoutingRule, metadata map[string]interface{}) bool {
	// All conditions must match (AND logic)
	for _, condition := range rule.Conditions {
		if !s.evaluateCondition(condition, metadata) {
			return false
		}
	}
	return true
}

// evaluateCondition evaluates a single condition
func (s *PriorityScheduler) evaluateCondition(condition Condition, metadata map[string]interface{}) bool {
	value, exists := metadata[condition.Field]
	if !exists {
		return false
	}

	switch condition.Operator {
	case "equals":
		return fmt.Sprintf("%v", value) == fmt.Sprintf("%v", condition.Value)

	case "contains":
		str := fmt.Sprintf("%v", value)
		substr := fmt.Sprintf("%v", condition.Value)
		return len(str) > 0 && len(substr) > 0 && (str == substr || len(str) > len(substr) && str[:len(substr)] == substr || len(str) > len(substr) && str[len(str)-len(substr):] == substr || containsString(str, substr))

	case "matches":
		// Implement regex matching
		pattern := fmt.Sprintf("%v", condition.Value)
		str := fmt.Sprintf("%v", value)
		matched, err := regexp.MatchString(pattern, str)
		if err != nil {
			s.logger.WithError(err).Warn("Invalid regex pattern")
			return false
		}
		return matched

	case "in":
		// Check if value is in a list
		if list, ok := condition.Value.([]string); ok {
			strValue := fmt.Sprintf("%v", value)
			for _, item := range list {
				if item == strValue {
					return true
				}
			}
		}
		return false

	default:
		return false
	}
}

// containsString checks if a string contains a substring
func containsString(str, substr string) bool {
	if len(substr) > len(str) {
		return false
	}
	for i := 0; i <= len(str)-len(substr); i++ {
		if str[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// calculatePriority calculates the priority for a job
func (s *PriorityScheduler) calculatePriority(metadata map[string]interface{}, queueConfig QueueConfig) int64 {
	// Check if priority is explicitly set
	if priority, ok := metadata["priority"].(float64); ok {
		p := int64(priority)
		// Clamp to queue's priority range
		if p < queueConfig.PriorityRange.Min {
			p = queueConfig.PriorityRange.Min
		}
		if p > queueConfig.PriorityRange.Max {
			p = queueConfig.PriorityRange.Max
		}
		return p
	}

	// Calculate based on job characteristics
	basePriority := (queueConfig.PriorityRange.Min + queueConfig.PriorityRange.Max) / 2

	// Adjust based on job type
	if jobType, ok := metadata["job_type"].(string); ok {
		switch jobType {
		case "rollback":
			basePriority += 20
		case "hotfix":
			basePriority += 15
		case "deploy":
			basePriority += 10
		case "build":
			basePriority += 5
		case "test":
			basePriority += 0
		case "cleanup":
			basePriority -= 10
		}
	}

	// Adjust based on environment
	if env, ok := metadata["environment"].(string); ok {
		switch env {
		case "production":
			basePriority += 10
		case "staging":
			basePriority += 5
		case "development":
			basePriority -= 5
		}
	}

	// Ensure within queue bounds
	if basePriority < queueConfig.PriorityRange.Min {
		basePriority = queueConfig.PriorityRange.Min
	}
	if basePriority > queueConfig.PriorityRange.Max {
		basePriority = queueConfig.PriorityRange.Max
	}

	return basePriority
}

// GetQueueMetrics gets metrics for all queues
func (s *PriorityScheduler) GetQueueMetrics(ctx context.Context) (map[string]QueueMetrics, error) {
	metrics := make(map[string]QueueMetrics)

	// Get task counts from Corndogs
	totalTasks, stateCounts, err := s.corndogsClient.GetTaskStateCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get task state counts: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Build metrics for each queue
	for queueName, config := range s.queueConfig {
		metrics[queueName] = QueueMetrics{
			QueueName:      queueName,
			TotalTasks:     totalTasks,
			MaxConcurrency: config.MaxConcurrency,
			// TODO: Get actual running count from Corndogs by queue
		}
	}

	// stateCounts can be used for more detailed metrics
	_ = stateCounts

	return metrics, nil
}

// QueueMetrics represents metrics for a queue
type QueueMetrics struct {
	QueueName      string `json:"queue_name"`
	TotalTasks     int64  `json:"total_tasks"`
	RunningTasks   int64  `json:"running_tasks"`
	PendingTasks   int64  `json:"pending_tasks"`
	MaxConcurrency int    `json:"max_concurrency"`
	AverageWait    int64  `json:"average_wait_seconds"`
}