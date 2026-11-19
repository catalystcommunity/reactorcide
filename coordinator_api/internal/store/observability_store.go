package store

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// RollupFilter selects rollup rows. From is inclusive and To exclusive (of
// bucket_start).
type RollupFilter struct {
	OrgID string
	From  time.Time
	To    time.Time
	// AllProjects false limits rows to ProjectIDs, and to rows with no
	// project when NoProject is true. The dashboards use it for visibility.
	AllProjects bool
	ProjectIDs  []string
	NoProject   bool
	// Optional dimension filters. Empty is no condition; Lane nil is no
	// condition (a lane can be '').
	WorkflowSecurityID string
	OwnerGroup         string
	Lane               *string
	RefClassBucket     string
}

// ObservabilityStore stores rollups, alert rules, and SLO targets
// (docs/observability.md).
//
// It is separate from Store, so the many full Store fakes do not each need
// these methods. The Postgres store implements it; a caller type-asserts.
type ObservabilityStore interface {
	// Rollup loop state.
	GetObservabilityState(ctx context.Context, name string) (*models.ObservabilityState, error)
	SaveObservabilityState(ctx context.Context, state *models.ObservabilityState) error

	// ListChangedJobBuckets returns the distinct UTC hours (of completion)
	// of the finished jobs that completed in (from, to], and of the jobs
	// whose resource summary was computed in (summaryFrom, to].
	ListChangedJobBuckets(ctx context.Context, from, summaryFrom, to time.Time) ([]time.Time, error)
	// ListRollupJobFacts returns the finished jobs that completed in
	// [from, to). A finished job with no completed_at uses its updated_at.
	ListRollupJobFacts(ctx context.Context, from, to time.Time) ([]models.RollupJobFact, error)
	// ReplaceJobRollups deletes the rows of granularity in [from, to) and
	// inserts rows, in one transaction.
	ReplaceJobRollups(ctx context.Context, granularity string, from, to time.Time, rows []models.JobRollup) error
	ReplaceActorGroupRollups(ctx context.Context, from, to time.Time, rows []models.ActorGroupRollup) error
	// ListGroupMembershipChanges returns the membership history of these
	// principals up to until, oldest first.
	ListGroupMembershipChanges(ctx context.Context, principalIDs []string, until time.Time) ([]models.GroupMembershipChange, error)
	ListChangedEventBuckets(ctx context.Context, from, to time.Time) ([]time.Time, error)
	ListRollupEventFacts(ctx context.Context, from, to time.Time) ([]models.RollupEventFact, error)
	ReplaceEvalRollups(ctx context.Context, granularity string, from, to time.Time, rows []models.EvalRollup) error
	// RecordHeartbeat sets the bit of the minute of t.
	RecordHeartbeat(ctx context.Context, t time.Time) error
	ListHeartbeats(ctx context.Context, from, to time.Time) ([]models.HeartbeatHour, error)
	// DeleteRollupsBefore removes hourly rows before hourlyBefore and daily
	// rows, heartbeats, and alert transitions before dailyBefore.
	DeleteRollupsBefore(ctx context.Context, hourlyBefore, dailyBefore time.Time) error

	// Reads.
	ListJobRollups(ctx context.Context, granularity string, filter RollupFilter) ([]models.JobRollup, error)
	ListEvalRollups(ctx context.Context, granularity string, filter RollupFilter) ([]models.EvalRollup, error)
	ListActorGroupRollups(ctx context.Context, orgID string, from, to time.Time) ([]models.ActorGroupRollup, error)
	// ListFlakyTestsForProjects returns the tests of these projects with a flaky
	// commit since the time, most flaky first. A store without test
	// results returns none.
	ListFlakyTestsForProjects(ctx context.Context, projectIDs []string, since time.Time, limit int) ([]models.FlakyTestSummary, error)

	// Alert rules.
	ListAlertRules(ctx context.Context, orgID string) ([]models.AlertRule, error)
	ListEnabledAlertRules(ctx context.Context) ([]models.AlertRule, error)
	GetAlertRule(ctx context.Context, orgID, name string) (*models.AlertRule, error)
	CreateAlertRule(ctx context.Context, rule *models.AlertRule) error
	// UpdateAlertRuleConfig saves the configuration fields of a rule. A
	// change of the condition resets the state to ok.
	UpdateAlertRuleConfig(ctx context.Context, rule *models.AlertRule, resetState bool) error
	DeleteAlertRule(ctx context.Context, orgID, name string) error
	// ApplyAlertEvaluation saves the evaluation state of a rule. With a
	// transition, it also writes the transition and its outbox event, in
	// the same transaction.
	ApplyAlertEvaluation(ctx context.Context, rule *models.AlertRule, transition *models.AlertTransition, event *models.AlertOutboxEvent) error
	ListAlertTransitions(ctx context.Context, ruleID string, limit int) ([]models.AlertTransition, error)

	// SLO targets.
	ListSLOTargets(ctx context.Context, orgID string) ([]models.SLOTarget, error)
	UpsertSLOTarget(ctx context.Context, target *models.SLOTarget) error
	DeleteSLOTarget(ctx context.Context, orgID, sli, lane string) error
	// ListRollupLanes returns the distinct lanes of the daily job rollups
	// of an organization in [from, to).
	ListRollupLanes(ctx context.Context, orgID string, from, to time.Time) ([]string, error)
}
