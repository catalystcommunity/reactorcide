# Advanced Corndogs Features - Priority 5.2

This document describes the advanced Corndogs features implemented for Reactorcide, including workflow support, priority scheduling, custom state machines, and queue-based routing.

## Table of Contents
1. [Overview](#overview)
2. [Workflow Support](#workflow-support)
3. [Priority Scheduling](#priority-scheduling)
4. [Queue-Based Routing](#queue-based-routing)
5. [Custom State Machines](#custom-state-machines)
6. [Usage Examples](#usage-examples)
7. [API Reference](#api-reference)

## Overview

The Priority 5.2 implementation adds sophisticated job orchestration capabilities to Reactorcide by leveraging Corndogs' queue and state management features. These enhancements enable:

- **Complex Workflows**: Multi-step workflows with conditional logic and parallel execution
- **Priority-Based Scheduling**: Intelligent job prioritization and queue routing
- **Custom State Machines**: Flexible state transitions for complex deployment pipelines
- **Dynamic Queue Management**: Automatic routing based on job characteristics

## Workflow Support

### Workflow Engine

The workflow engine (`internal/workflows/engine.go`) manages complex multi-step workflows using Corndogs for state management.

#### Key Features:

- **State-Based Execution**: Each workflow consists of states with defined transitions
- **Action Types**: Support for various action types:
  - `run_job`: Execute a job via Corndogs
  - `notify`: Send notifications (Slack, email, etc.)
  - `wait`: Pause execution for a duration
  - `parallel`: Execute multiple jobs concurrently
  - `conditional`: Execute actions based on conditions

- **Timeout Handling**: Automatic state timeouts with configurable timeout states
- **Retry Policies**: Configurable retry behavior per state
- **State History**: Complete audit trail of state transitions

### Predefined Workflows

#### Simple CI Workflow
```go
workflow := workflows.PredefinedWorkflows["simple-ci"]
```

States:
- `pending` → `building` → `testing` → `completed/failed`

Features:
- Sequential build and test stages
- Automatic failure handling
- Notifications on completion

#### Deploy Pipeline Workflow
```go
workflow := workflows.PredefinedWorkflows["deploy-pipeline"]
```

States:
- Build → Test → Deploy Staging → Validation → Approval → Deploy Production
- Includes rollback states for failure recovery
- Manual approval gate for production deployment

### Creating Custom Workflows

```go
workflow := workflows.WorkflowDefinition{
    Name:         "custom-workflow",
    Description:  "Custom deployment workflow",
    Version:      "1.0.0",
    InitialState: "start",
    States: map[string]workflows.WorkflowState{
        "start": {
            Name: "start",
            Transitions: map[string]string{
                "begin": "processing",
            },
            OnEnter: []workflows.Action{
                {
                    Type: "run_job",
                    Name: "init",
                    Parameters: map[string]interface{}{
                        "command": "make init",
                    },
                    OnSuccess: "begin",
                },
            },
        },
        "processing": {
            Name: "processing",
            Transitions: map[string]string{
                "complete": "done",
                "error":    "failed",
            },
            TimeoutSeconds: 3600,
            TimeoutState:   "failed",
            RetryPolicy: &workflows.RetryPolicy{
                MaxAttempts:     3,
                BackoffStrategy: "exponential",
                InitialDelay:    30 * time.Second,
            },
        },
        "done": {
            Name:       "done",
            IsTerminal: true,
        },
        "failed": {
            Name:       "failed",
            IsTerminal: true,
        },
    },
}
```

## Priority Scheduling

### Priority Scheduler

The priority scheduler (`internal/scheduler/priority_scheduler.go`) manages job prioritization and intelligent queue routing.

#### Default Queue Configuration

| Queue Name | Priority Range | Max Concurrency | Timeout | Use Case |
|------------|---------------|-----------------|---------|----------|
| critical | 90-100 | 10 | 5 min | Production emergencies, rollbacks |
| high-priority | 70-89 | 20 | 30 min | Deployments, releases |
| normal | 30-69 | 50 | 60 min | CI builds, regular tests |
| low-priority | 0-29 | 30 | 120 min | Scheduled jobs, maintenance |

#### Priority Calculation

Priority is calculated based on:
1. Explicit priority value (if provided)
2. Job type (rollback > hotfix > deploy > build > test)
3. Environment (production > staging > development)
4. Queue configuration limits

Example:
```go
// Production rollback gets highest priority
metadata := map[string]interface{}{
    "job_type":    "rollback",
    "environment": "production",
}
// Calculated priority: 95+ (critical queue)
```

## Queue-Based Routing

### Routing Rules

Routing rules determine which queue a job is sent to based on job metadata.

#### Default Routing Rules

1. **Rollbacks** → Critical Queue
   - Condition: job_type contains "rollback"
   - Priority: 110

2. **Production Deployments** → High Priority Queue
   - Conditions: job_type="deploy" AND environment="production"
   - Priority: 100

3. **Staging Deployments** → Normal Queue
   - Conditions: job_type="deploy" AND environment="staging"
   - Priority: 90

4. **Main Branch Builds** → Normal Queue
   - Conditions: job_type="build" AND branch IN ["main", "master", "develop"]
   - Priority: 80

5. **Feature Branch Builds** → Low Priority Queue
   - Conditions: job_type="build" AND branch MATCHES "^(feature|bugfix|hotfix)/.*"
   - Priority: 70

### Custom Routing Rules

```go
scheduler.AddRoutingRule(scheduler.RoutingRule{
    Name:        "security-scans",
    Description: "Route security scans to high priority",
    Priority:    105,
    Conditions: []scheduler.Condition{
        {Field: "job_type", Operator: "equals", Value: "security_scan"},
    },
    TargetQueue: "high-priority",
})
```

### Condition Operators

- `equals`: Exact match
- `contains`: Substring match
- `matches`: Regular expression match
- `in`: Value in list

## Custom State Machines

### State Machine Features

Each workflow state supports:

- **Multiple Transitions**: Different events trigger different state changes
- **Entry/Exit Actions**: Actions executed when entering or leaving a state
- **Timeout Handling**: Automatic transition on timeout
- **Retry Policies**: Automatic retry with backoff strategies
- **Terminal States**: Mark workflow completion

### State Transition Example

```go
state := workflows.WorkflowState{
    Name:        "deploying",
    Description: "Deploying to environment",
    Transitions: map[string]string{
        "deploy_success":  "validating",
        "deploy_failed":   "rollback",
        "timeout":         "rollback",
    },
    OnEnter: []workflows.Action{
        {
            Type: "run_job",
            Name: "deploy",
            Parameters: map[string]interface{}{
                "command": "kubectl apply -f manifests/",
            },
            OnSuccess: "deploy_success",
            OnFailure: "deploy_failed",
        },
    },
    OnExit: []workflows.Action{
        {
            Type: "notify",
            Name: "deployment_status",
            Parameters: map[string]interface{}{
                "channel": "slack",
            },
        },
    },
    TimeoutSeconds: 600,
    TimeoutState:   "rollback",
    RetryPolicy: &workflows.RetryPolicy{
        MaxAttempts:     3,
        BackoffStrategy: "exponential",
        InitialDelay:    30 * time.Second,
    },
}
```

## Usage Examples

### Starting a Workflow

```go
// Initialize workflow engine
engine := workflows.NewEngine(corndogsClient, logger)
engine.LoadPredefinedWorkflows()

// Start a workflow instance
parameters := map[string]interface{}{
    "environment": "staging",
    "version":     "v1.2.3",
}

instance, err := engine.StartWorkflow(ctx, "deploy-pipeline", parameters)
if err != nil {
    log.Fatalf("Failed to start workflow: %v", err)
}

fmt.Printf("Workflow started: %s\n", instance.InstanceID)
```

### Submitting a Job with Priority

```go
// Initialize priority scheduler
scheduler := scheduler.NewPriorityScheduler(corndogsClient, logger)

// Prepare job payload
jobPayload := &corndogs.TaskPayload{
    JobID:   uuid.New().String(),
    JobType: "deploy",
    Config:  map[string]interface{}{
        "command": "make deploy",
    },
    Metadata: make(map[string]interface{}),
}

// Set job metadata for routing
jobMetadata := map[string]interface{}{
    "job_type":    "deploy",
    "environment": "production",
    "priority":    85,
}

// Submit with automatic routing and priority
task, err := scheduler.SubmitJob(ctx, jobPayload, jobMetadata)
if err != nil {
    log.Fatalf("Failed to submit job: %v", err)
}

fmt.Printf("Job submitted to queue with priority %d\n", task.Priority)
```

### Custom Workflow with Conditional Logic

```go
workflow := workflows.WorkflowDefinition{
    Name:         "conditional-deploy",
    InitialState: "check_tests",
    States: map[string]workflows.WorkflowState{
        "check_tests": {
            Name: "check_tests",
            OnEnter: []workflows.Action{
                {
                    Type: "conditional",
                    Name: "test_check",
                    Parameters: map[string]interface{}{
                        "condition": "tests_passed == true",
                        "then": map[string]interface{}{
                            "type": "run_job",
                            "name": "deploy",
                            "parameters": map[string]interface{}{
                                "command": "make deploy",
                            },
                        },
                        "else": map[string]interface{}{
                            "type": "notify",
                            "name": "test_failure",
                            "parameters": map[string]interface{}{
                                "message": "Tests failed, deployment cancelled",
                            },
                        },
                    },
                },
            },
            Transitions: map[string]string{
                "deployed": "completed",
                "skipped":  "cancelled",
            },
        },
        "completed": {
            Name:       "completed",
            IsTerminal: true,
        },
        "cancelled": {
            Name:       "cancelled",
            IsTerminal: true,
        },
    },
}
```

## API Reference

### Workflow Engine API

```go
// Create new engine
engine := workflows.NewEngine(corndogsClient, logger)

// Register workflow
err := engine.RegisterWorkflow(workflow)

// Start workflow
instance, err := engine.StartWorkflow(ctx, workflowName, parameters)

// Get workflow instance
instance, err := engine.GetInstance(instanceID)

// List all instances
instances := engine.ListInstances()

// Process Corndogs task (for state transitions)
err := engine.ProcessCornDogsTask(ctx, task)
```

### Priority Scheduler API

```go
// Create scheduler
scheduler := scheduler.NewPriorityScheduler(corndogsClient, logger)

// Register custom queue
scheduler.RegisterQueue(queueConfig)

// Add routing rule
scheduler.AddRoutingRule(rule)

// Submit job with routing
task, err := scheduler.SubmitJob(ctx, jobPayload, jobMetadata)

// Get queue metrics
metrics, err := scheduler.GetQueueMetrics(ctx)
```

### Workflow Definition Structure

```go
type WorkflowDefinition struct {
    Name         string                   // Unique workflow name
    Description  string                   // Human-readable description
    Version      string                   // Workflow version
    InitialState string                   // Starting state
    States       map[string]WorkflowState // State definitions
    Parameters   map[string]ParameterDef  // Input parameters
    Metadata     map[string]interface{}   // Additional metadata
}

type WorkflowState struct {
    Name            string            // State name
    Description     string            // State description
    Transitions     map[string]string // Event → next state mapping
    OnEnter         []Action          // Actions on state entry
    OnExit          []Action          // Actions on state exit
    TimeoutSeconds  int               // Timeout duration
    TimeoutState    string            // State on timeout
    IsTerminal      bool              // Terminal state flag
    RetryPolicy     *RetryPolicy      // Retry configuration
}
```

## Integration with Coordinator API

To integrate these features with the existing Coordinator API:

1. **Add Workflow Endpoints**:
   ```go
   // POST /api/v1/workflows
   // GET /api/v1/workflows/{workflow_id}
   // POST /api/v1/workflows/{workflow_name}/start
   ```

2. **Update Job Creation** to use priority scheduler:
   ```go
   // In job_handler.go
   if scheduler != nil {
       task, err := scheduler.SubmitJob(ctx, jobPayload, jobMetadata)
   }
   ```

3. **Add Queue Management Endpoints**:
   ```go
   // GET /api/v1/queues
   // GET /api/v1/queues/{queue_name}/metrics
   // POST /api/v1/routing-rules
   ```

## Best Practices

1. **Workflow Design**:
   - Keep states focused and single-purpose
   - Always define timeout states for long-running operations
   - Include proper error handling and rollback states
   - Use terminal states to clearly mark completion

2. **Priority Management**:
   - Reserve critical queue for true emergencies
   - Use explicit priorities sparingly
   - Let routing rules handle most prioritization
   - Monitor queue depths and adjust concurrency limits

3. **Queue Routing**:
   - Order routing rules by priority (highest first)
   - Use specific conditions to avoid ambiguity
   - Test routing rules with various job metadata
   - Document custom routing rules

4. **State Transitions**:
   - Make transitions explicit and predictable
   - Use meaningful event names
   - Log all state transitions for debugging
   - Handle timeouts gracefully

## Testing

All features include comprehensive test coverage:

- `internal/workflows/workflow_test.go`: Workflow validation and state machine tests
- `internal/scheduler/priority_scheduler_test.go`: Priority calculation and routing tests

Run tests:
```bash
go test ./internal/workflows -v
go test ./internal/scheduler -v
```

## Future Enhancements

- **Workflow Templates**: Parameterized workflow templates for common patterns
- **Dynamic Priority Adjustment**: Adjust priorities based on queue depth and wait times
- **Workflow Composition**: Combine smaller workflows into larger pipelines
- **Visual Workflow Editor**: UI for creating and managing workflows
- **Advanced Metrics**: Detailed analytics on workflow performance and queue efficiency