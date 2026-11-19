package models

import (
	"time"

	"github.com/lib/pq"
)

// Observability rollups, alert rules, and SLO targets (docs/observability.md,
// coredb/migrations/000062_rollups_alerts_slos.sql).

// Ref class buckets of a rollup row. A job has many ref classes; the bucket
// is the first of these that the job has, else "other".
const (
	RefClassBucketDefault  = "default"
	RefClassBucketTag      = "tag"
	RefClassBucketPR       = "pr"
	RefClassBucketSchedule = "schedule"
	RefClassBucketManual   = "manual"
	RefClassBucketOther    = "other"
)

// RefClassBuckets lists the buckets in precedence order.
var RefClassBuckets = []string{
	RefClassBucketDefault, RefClassBucketTag, RefClassBucketPR, RefClassBucketSchedule, RefClassBucketManual, RefClassBucketOther,
}

// RollupJobFact is one finished job, as the rollup loop reads it.
type RollupJobFact struct {
	JobID                string
	OrgID                string
	ProjectID            *string
	WorkflowSecurityID   string
	OwnerGroup           string
	Lane                 string
	QueueName            string
	RefClasses           pq.StringArray `gorm:"type:text[]"`
	Status               string
	FailureClass         string
	ActorPrincipalID     *string
	ActorKind            string
	CreatedAt            time.Time
	SchedulingStartedAt  *time.Time
	StartedAt            *time.Time
	CompletedAt          time.Time
	SampleCount          *int
	CPUAvgMillicores     *int64
	MemoryAvgBytes       *int64
	CPURequestMillicores *int64
	MemoryLimitBytes     *int64
}

// JobRollup is one row of job_rollups_hourly or job_rollups_daily.
type JobRollup struct {
	BucketStart            time.Time     `gorm:"column:bucket_start" json:"bucket_start"`
	OrgID                  string        `gorm:"column:org_id" json:"org_id"`
	ProjectID              *string       `gorm:"column:project_id" json:"project_id,omitempty"`
	WorkflowSecurityID     string        `gorm:"column:workflow_security_id" json:"workflow_security_id"`
	OwnerGroup             string        `gorm:"column:owner_group" json:"owner_group"`
	Lane                   string        `gorm:"column:lane" json:"lane"`
	RefClassBucket         string        `gorm:"column:ref_class_bucket" json:"ref_class_bucket"`
	ActorKind              string        `gorm:"column:actor_kind" json:"actor_kind"`
	Runs                   int64         `gorm:"column:runs" json:"runs"`
	Successes              int64         `gorm:"column:successes" json:"successes"`
	FailedJob              int64         `gorm:"column:failed_job" json:"failed_job"`
	FailedTimeout          int64         `gorm:"column:failed_timeout" json:"failed_timeout"`
	FailedOOM              int64         `gorm:"column:failed_oom" json:"failed_oom"`
	FailedImagePull        int64         `gorm:"column:failed_image_pull" json:"failed_image_pull"`
	FailedScheduling       int64         `gorm:"column:failed_scheduling" json:"failed_scheduling"`
	FailedWorkerLost       int64         `gorm:"column:failed_worker_lost" json:"failed_worker_lost"`
	FailedPlatform         int64         `gorm:"column:failed_platform" json:"failed_platform"`
	FailedUnknown          int64         `gorm:"column:failed_unknown" json:"failed_unknown"`
	Cancelled              int64         `gorm:"column:cancelled" json:"cancelled"`
	DurationP50Seconds     *float64      `gorm:"column:duration_p50_seconds" json:"duration_p50_seconds,omitempty"`
	DurationP95Seconds     *float64      `gorm:"column:duration_p95_seconds" json:"duration_p95_seconds,omitempty"`
	DurationSumSeconds     float64       `gorm:"column:duration_sum_seconds" json:"duration_sum_seconds"`
	DurationHist           pq.Int64Array `gorm:"column:duration_hist;type:integer[]" json:"duration_hist,omitempty"`
	QueueWaitP50Seconds    *float64      `gorm:"column:queue_wait_p50_seconds" json:"queue_wait_p50_seconds,omitempty"`
	QueueWaitP95Seconds    *float64      `gorm:"column:queue_wait_p95_seconds" json:"queue_wait_p95_seconds,omitempty"`
	QueueWaitSumSeconds    float64       `gorm:"column:queue_wait_sum_seconds" json:"queue_wait_sum_seconds"`
	QueueWaitHist          pq.Int64Array `gorm:"column:queue_wait_hist;type:integer[]" json:"queue_wait_hist,omitempty"`
	MeasuredRuns           int64         `gorm:"column:measured_runs" json:"measured_runs"`
	CPUCoreHours           float64       `gorm:"column:cpu_core_hours" json:"cpu_core_hours"`
	MemoryGiBHours         float64       `gorm:"column:memory_gib_hours" json:"memory_gib_hours"`
	CPUReservedCoreHours   float64       `gorm:"column:cpu_reserved_core_hours" json:"cpu_reserved_core_hours"`
	MemoryReservedGiBHours float64       `gorm:"column:memory_reserved_gib_hours" json:"memory_reserved_gib_hours"`
}

// PipelineFailures is the count of failures that a pipeline owner owns: the
// classes job, timeout, and oom.
func (r *JobRollup) PipelineFailures() int64 { return r.FailedJob + r.FailedTimeout + r.FailedOOM }

// InfraFailures is the count of failures of the infra.* classes.
func (r *JobRollup) InfraFailures() int64 {
	return r.FailedImagePull + r.FailedScheduling + r.FailedWorkerLost + r.FailedPlatform
}

// ActorGroupRollup is one row of job_actor_group_rollups_daily.
type ActorGroupRollup struct {
	BucketStart          time.Time `gorm:"column:bucket_start" json:"bucket_start"`
	OrgID                string    `gorm:"column:org_id" json:"org_id"`
	GroupID              string    `gorm:"column:group_id" json:"group_id"`
	GroupName            string    `gorm:"column:group_name" json:"group_name"`
	Runs                 int64     `gorm:"column:runs" json:"runs"`
	Successes            int64     `gorm:"column:successes" json:"successes"`
	PipelineFailures     int64     `gorm:"column:pipeline_failures" json:"pipeline_failures"`
	InfraFailures        int64     `gorm:"column:infra_failures" json:"infra_failures"`
	DurationSumSeconds   float64   `gorm:"column:duration_sum_seconds" json:"duration_sum_seconds"`
	CPUCoreHours         float64   `gorm:"column:cpu_core_hours" json:"cpu_core_hours"`
	MemoryGiBHours       float64   `gorm:"column:memory_gib_hours" json:"memory_gib_hours"`
	CPUReservedCoreHours float64   `gorm:"column:cpu_reserved_core_hours" json:"cpu_reserved_core_hours"`
}

// Group membership history uses GroupMembershipChange (group_vcs.go, WP8).

// RollupEventFact is one CI event with a first status.
type RollupEventFact struct {
	EventID       string
	OrgID         string
	ProjectID     *string
	Source        string
	ReceivedAt    time.Time
	FirstStatusAt time.Time
}

// EvalRollup is one row of eval_rollups_hourly or eval_rollups_daily.
type EvalRollup struct {
	BucketStart       time.Time     `gorm:"column:bucket_start" json:"bucket_start"`
	OrgID             string        `gorm:"column:org_id" json:"org_id"`
	ProjectID         *string       `gorm:"column:project_id" json:"project_id,omitempty"`
	Source            string        `gorm:"column:source" json:"source"`
	Events            int64         `gorm:"column:events" json:"events"`
	LatencyP50Seconds *float64      `gorm:"column:latency_p50_seconds" json:"latency_p50_seconds,omitempty"`
	LatencyP95Seconds *float64      `gorm:"column:latency_p95_seconds" json:"latency_p95_seconds,omitempty"`
	LatencySumSeconds float64       `gorm:"column:latency_sum_seconds" json:"latency_sum_seconds"`
	LatencyHist       pq.Int64Array `gorm:"column:latency_hist;type:integer[]" json:"latency_hist,omitempty"`
}

// ObservabilityState is one watermark of the rollup loop.
type ObservabilityState struct {
	Name         string     `gorm:"column:name;primaryKey" json:"name"`
	Watermark    time.Time  `gorm:"column:watermark" json:"watermark"`
	BackfilledAt *time.Time `gorm:"column:backfilled_at" json:"backfilled_at,omitempty"`
	LastRunAt    *time.Time `gorm:"column:last_run_at" json:"last_run_at,omitempty"`
	LastError    string     `gorm:"column:last_error" json:"last_error,omitempty"`
	UpdatedAt    time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (ObservabilityState) TableName() string { return "observability_state" }

// Alert rule scopes, conditions, and states.
const (
	AlertScopeWorkflow   = "workflow"
	AlertScopeProject    = "project"
	AlertScopeOwnerGroup = "owner_group"

	AlertConditionPassRateBelow      = "pass_rate_below"
	AlertConditionDurationRegression = "duration_regression"

	AlertStateOK     = "ok"
	AlertStateFiring = "firing"

	AlertTransitionFiring   = "firing"
	AlertTransitionResolved = "resolved"
)

// Outbound event types of alert transitions (docs/outbound-events.md).
const (
	OutboundEventAlertFiring   = "alert.firing"
	OutboundEventAlertResolved = "alert.resolved"
)

// AlertRule is one row of alert_rules.
type AlertRule struct {
	RuleID             string     `gorm:"column:rule_id;primaryKey;type:uuid;default:generate_ulid()" json:"rule_id"`
	OrgID              string     `gorm:"column:org_id;type:uuid" json:"org_id"`
	Name               string     `gorm:"column:name" json:"name"`
	Description        string     `gorm:"column:description" json:"description"`
	Scope              string     `gorm:"column:scope" json:"scope"`
	ProjectID          *string    `gorm:"column:project_id;type:uuid" json:"project_id,omitempty"`
	WorkflowSecurityID string     `gorm:"column:workflow_security_id" json:"workflow_security_id,omitempty"`
	OwnerGroup         string     `gorm:"column:owner_group" json:"owner_group,omitempty"`
	Condition          string     `gorm:"column:condition" json:"condition"`
	RefClassBucket     string     `gorm:"column:ref_class_bucket" json:"ref_class_bucket"`
	Threshold          float64    `gorm:"column:threshold" json:"threshold"`
	Factor             float64    `gorm:"column:factor" json:"factor"`
	WindowHours        int        `gorm:"column:window_hours" json:"window_hours"`
	BaselineDays       int        `gorm:"column:baseline_days" json:"baseline_days"`
	MinRuns            int        `gorm:"column:min_runs" json:"min_runs"`
	ResolveMargin      float64    `gorm:"column:resolve_margin" json:"resolve_margin"`
	FireAfter          int        `gorm:"column:fire_after" json:"fire_after"`
	ResolveAfter       int        `gorm:"column:resolve_after" json:"resolve_after"`
	Enabled            bool       `gorm:"column:enabled" json:"enabled"`
	State              string     `gorm:"column:state" json:"state"`
	BreachStreak       int        `gorm:"column:breach_streak" json:"breach_streak"`
	RecoverStreak      int        `gorm:"column:recover_streak" json:"recover_streak"`
	LastValue          *float64   `gorm:"column:last_value" json:"last_value,omitempty"`
	LastBaseline       *float64   `gorm:"column:last_baseline" json:"last_baseline,omitempty"`
	LastRuns           int        `gorm:"column:last_runs" json:"last_runs"`
	LastEvaluatedAt    *time.Time `gorm:"column:last_evaluated_at" json:"last_evaluated_at,omitempty"`
	StateChangedAt     *time.Time `gorm:"column:state_changed_at" json:"state_changed_at,omitempty"`
	CreatedBy          *string    `gorm:"column:created_by;type:uuid" json:"created_by,omitempty"`
	CreatedAt          time.Time  `gorm:"column:created_at;autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at;autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (AlertRule) TableName() string { return "alert_rules" }

// AlertTransition is one row of alert_transitions.
type AlertTransition struct {
	TransitionID int64     `gorm:"column:transition_id;primaryKey;autoIncrement" json:"transition_id"`
	RuleID       string    `gorm:"column:rule_id;type:uuid" json:"rule_id"`
	OrgID        string    `gorm:"column:org_id;type:uuid" json:"org_id"`
	State        string    `gorm:"column:state" json:"state"`
	Value        *float64  `gorm:"column:value" json:"value,omitempty"`
	Baseline     *float64  `gorm:"column:baseline" json:"baseline,omitempty"`
	Threshold    *float64  `gorm:"column:threshold" json:"threshold,omitempty"`
	Runs         int       `gorm:"column:runs" json:"runs"`
	EventID      *string   `gorm:"column:event_id;type:uuid" json:"event_id,omitempty"`
	OccurredAt   time.Time `gorm:"column:occurred_at" json:"occurred_at"`
}

func (AlertTransition) TableName() string { return "alert_transitions" }

// AlertOutboxEvent is the outbox row of an alert transition.
type AlertOutboxEvent struct {
	EventType          string
	OrgID              string
	ProjectID          *string
	WorkflowSecurityID string
	OwnerGroup         string
	Status             string
	OccurredAt         time.Time
	Details            JSONB
}

// SLI names.
const (
	SLIEvalLatency      = "eval_latency"
	SLIQueueWait        = "queue_wait"
	SLIInfraFailureRate = "infra_failure_rate"
	SLIAvailability     = "availability"
)

// SLIs lists the SLIs in display order.
var SLIs = []string{SLIEvalLatency, SLIQueueWait, SLIInfraFailureRate, SLIAvailability}

// SLIHasLanes reports whether an SLI has a target for each lane.
func SLIHasLanes(sli string) bool { return sli == SLIQueueWait || sli == SLIInfraFailureRate }

// SLOTarget is one row of slo_targets.
type SLOTarget struct {
	OrgID            string    `gorm:"column:org_id;primaryKey;type:uuid" json:"org_id"`
	SLI              string    `gorm:"column:sli;primaryKey" json:"sli"`
	Lane             string    `gorm:"column:lane;primaryKey" json:"lane"`
	Objective        float64   `gorm:"column:objective" json:"objective"`
	ThresholdSeconds float64   `gorm:"column:threshold_seconds" json:"threshold_seconds"`
	WindowDays       int       `gorm:"column:window_days" json:"window_days"`
	UpdatedBy        *string   `gorm:"column:updated_by;type:uuid" json:"updated_by,omitempty"`
	UpdatedAt        time.Time `gorm:"column:updated_at;autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
	// Builtin is true for a default that has no stored row.
	Builtin bool `gorm:"-" json:"builtin"`
}

func (SLOTarget) TableName() string { return "slo_targets" }

// HeartbeatHour is one row of control_plane_heartbeats.
type HeartbeatHour struct {
	HourStart time.Time `gorm:"column:hour_start"`
	UpMask    int64     `gorm:"column:up_mask"`
}

// FlakyTestSummary is one flaky test of a project (WP15 test_flakiness).
type FlakyTestSummary struct {
	TestID      string     `json:"test_id"`
	Suite       string     `json:"suite"`
	Classname   string     `json:"classname"`
	Name        string     `json:"name"`
	FlakyCount  int64      `json:"flaky_count"`
	TotalRuns   int64      `json:"total_runs"`
	LastFlakyAt *time.Time `json:"last_flaky_at,omitempty"`
}
