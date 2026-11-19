package postgres_store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Rollups, alert rules, and SLO targets (docs/observability.md, migration
// 000062).

// jobs timestamps are "timestamp without time zone" in UTC. A parameter is
// sent as UTC text and cast, so the session time zone never changes it.
func utcTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05.999999")
}

func jobRollupTable(granularity string) (string, error) {
	switch granularity {
	case "hour":
		return "job_rollups_hourly", nil
	case "day":
		return "job_rollups_daily", nil
	}
	return "", fmt.Errorf("%w: granularity must be hour or day", store.ErrInvalidInput)
}

func evalRollupTable(granularity string) (string, error) {
	switch granularity {
	case "hour":
		return "eval_rollups_hourly", nil
	case "day":
		return "eval_rollups_daily", nil
	}
	return "", fmt.Errorf("%w: granularity must be hour or day", store.ErrInvalidInput)
}

func (ps PostgresDbStore) GetObservabilityState(ctx context.Context, name string) (*models.ObservabilityState, error) {
	var state models.ObservabilityState
	err := ps.getDB(ctx).Where("name = ?", name).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read observability state: %w", err)
	}
	return &state, nil
}

func (ps PostgresDbStore) SaveObservabilityState(ctx context.Context, state *models.ObservabilityState) error {
	state.UpdatedAt = time.Now().UTC()
	err := ps.getDB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"watermark", "backfilled_at", "last_run_at", "last_error", "updated_at"}),
	}).Create(state).Error
	if err != nil {
		return fmt.Errorf("failed to save observability state: %w", err)
	}
	return nil
}

const terminalJobStatusesSQL = "('completed', 'failed', 'cancelled', 'timeout')"

func (ps PostgresDbStore) ListChangedJobBuckets(ctx context.Context, from, summaryFrom, to time.Time) ([]time.Time, error) {
	var rows []struct{ Bucket time.Time }
	err := ps.getDB(ctx).Raw(`
SELECT DISTINCT date_trunc('hour', j.completed_at) AS bucket FROM jobs j
 WHERE j.completed_at > ?::timestamp AND j.completed_at <= ?::timestamp AND j.status IN `+terminalJobStatusesSQL+`
UNION
SELECT DISTINCT date_trunc('hour', j.updated_at) FROM jobs j
 WHERE j.completed_at IS NULL AND j.status IN `+terminalJobStatusesSQL+`
   AND j.updated_at > ?::timestamp AND j.updated_at <= ?::timestamp
UNION
SELECT DISTINCT date_trunc('hour', coalesce(j.completed_at, j.updated_at)) FROM job_resource_summaries s
  JOIN jobs j ON j.job_id = s.job_id
 WHERE s.computed_at > ? AND s.computed_at <= ? AND j.status IN `+terminalJobStatusesSQL+`
ORDER BY 1`,
		utcTimestamp(from), utcTimestamp(to), utcTimestamp(from), utcTimestamp(to), summaryFrom.UTC(), to.UTC()).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list changed job buckets: %w", err)
	}
	out := make([]time.Time, 0, len(rows))
	for _, r := range rows {
		out = append(out, asUTC(r.Bucket))
	}
	return out, nil
}

// asUTC reads a "timestamp without time zone" value as UTC.
func asUTC(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

func (ps PostgresDbStore) ListRollupJobFacts(ctx context.Context, from, to time.Time) ([]models.RollupJobFact, error) {
	var rows []models.RollupJobFact
	err := ps.getDB(ctx).Raw(`
SELECT j.job_id, j.org_id, j.project_id,
       coalesce(w.workflow_security_id, '') AS workflow_security_id,
       coalesce(j.owner_group, '') AS owner_group,
       coalesce(j.priority_lane, '') AS lane,
       j.queue_name, j.ref_classes, j.status,
       coalesce(j.failure_class, '') AS failure_class,
       j.actor_principal_id,
       coalesce(pm.kind, pr.kind, '') AS actor_kind,
       j.created_at, j.scheduling_started_at, j.started_at,
       coalesce(j.completed_at, j.updated_at) AS completed_at,
       s.sample_count, s.cpu_avg_millicores, s.memory_avg_bytes, s.cpu_request_millicores, s.memory_limit_bytes
  FROM jobs j
  LEFT JOIN workflow_instances w ON w.workflow_id = j.workflow_id
  LEFT JOIN principals pr ON pr.principal_id = j.actor_principal_id
  LEFT JOIN principals pm ON pm.principal_id = pr.merged_into
  LEFT JOIN job_resource_summaries s ON s.job_id = j.job_id
 WHERE j.status IN `+terminalJobStatusesSQL+`
   AND ((j.completed_at >= ?::timestamp AND j.completed_at < ?::timestamp)
     OR (j.completed_at IS NULL AND j.updated_at >= ?::timestamp AND j.updated_at < ?::timestamp))`,
		utcTimestamp(from), utcTimestamp(to), utcTimestamp(from), utcTimestamp(to)).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list rollup job facts: %w", err)
	}
	for i := range rows {
		rows[i].CreatedAt = asUTC(rows[i].CreatedAt)
		rows[i].CompletedAt = asUTC(rows[i].CompletedAt)
		if rows[i].StartedAt != nil {
			v := asUTC(*rows[i].StartedAt)
			rows[i].StartedAt = &v
		}
	}
	return rows, nil
}

func (ps PostgresDbStore) ReplaceJobRollups(ctx context.Context, granularity string, from, to time.Time, rows []models.JobRollup) error {
	table, err := jobRollupTable(granularity)
	if err != nil {
		return err
	}
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM "+table+" WHERE bucket_start >= ? AND bucket_start < ?", from.UTC(), to.UTC()).Error; err != nil {
			return fmt.Errorf("failed to delete %s: %w", table, err)
		}
		if len(rows) == 0 {
			return nil
		}
		if err := tx.Table(table).Omit("computed_at").CreateInBatches(rows, 500).Error; err != nil {
			return fmt.Errorf("failed to insert %s: %w", table, err)
		}
		return nil
	})
}

func (ps PostgresDbStore) ReplaceActorGroupRollups(ctx context.Context, from, to time.Time, rows []models.ActorGroupRollup) error {
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM job_actor_group_rollups_daily WHERE bucket_start >= ? AND bucket_start < ?", from.UTC(), to.UTC()).Error; err != nil {
			return fmt.Errorf("failed to delete actor group rollups: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		if err := tx.Table("job_actor_group_rollups_daily").Omit("computed_at").CreateInBatches(rows, 500).Error; err != nil {
			return fmt.Errorf("failed to insert actor group rollups: %w", err)
		}
		return nil
	})
}

func (ps PostgresDbStore) ListGroupMembershipChanges(ctx context.Context, principalIDs []string, until time.Time) ([]models.GroupMembershipChange, error) {
	ids := make([]string, 0, len(principalIDs))
	for _, id := range principalIDs {
		if isValidUUID(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []models.GroupMembershipChange
	err := ps.getDB(ctx).Raw(`
SELECT history_id, group_id, org_id, group_name, principal_id, change, source, changed_at
  FROM group_membership_history
 WHERE principal_id = ANY(?::uuid[]) AND changed_at <= ?
 ORDER BY changed_at, history_id`, pq.StringArray(ids), until.UTC()).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list group membership history: %w", err)
	}
	return rows, nil
}

func (ps PostgresDbStore) ListChangedEventBuckets(ctx context.Context, from, to time.Time) ([]time.Time, error) {
	var rows []struct{ Bucket time.Time }
	err := ps.getDB(ctx).Raw(`
SELECT DISTINCT date_trunc('hour', first_status_at AT TIME ZONE 'UTC') AS bucket FROM ci_events
 WHERE first_status_at > ? AND first_status_at <= ? ORDER BY 1`, from.UTC(), to.UTC()).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list changed event buckets: %w", err)
	}
	out := make([]time.Time, 0, len(rows))
	for _, r := range rows {
		out = append(out, asUTC(r.Bucket))
	}
	return out, nil
}

func (ps PostgresDbStore) ListRollupEventFacts(ctx context.Context, from, to time.Time) ([]models.RollupEventFact, error) {
	var rows []models.RollupEventFact
	err := ps.getDB(ctx).Raw(`
SELECT event_id, org_id, project_id, source, received_at, first_status_at FROM ci_events
 WHERE first_status_at >= ? AND first_status_at < ?`, from.UTC(), to.UTC()).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list rollup event facts: %w", err)
	}
	for i := range rows {
		rows[i].ReceivedAt = rows[i].ReceivedAt.UTC()
		rows[i].FirstStatusAt = rows[i].FirstStatusAt.UTC()
	}
	return rows, nil
}

func (ps PostgresDbStore) ReplaceEvalRollups(ctx context.Context, granularity string, from, to time.Time, rows []models.EvalRollup) error {
	table, err := evalRollupTable(granularity)
	if err != nil {
		return err
	}
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM "+table+" WHERE bucket_start >= ? AND bucket_start < ?", from.UTC(), to.UTC()).Error; err != nil {
			return fmt.Errorf("failed to delete %s: %w", table, err)
		}
		if len(rows) == 0 {
			return nil
		}
		if err := tx.Table(table).Omit("computed_at").CreateInBatches(rows, 500).Error; err != nil {
			return fmt.Errorf("failed to insert %s: %w", table, err)
		}
		return nil
	})
}

func (ps PostgresDbStore) RecordHeartbeat(ctx context.Context, t time.Time) error {
	t = t.UTC()
	hour := t.Truncate(time.Hour)
	bit := int64(1) << uint(t.Minute())
	err := ps.getDB(ctx).Exec(`
INSERT INTO control_plane_heartbeats (hour_start, up_mask, updated_at) VALUES (?, ?, timezone('utc', now()))
ON CONFLICT (hour_start) DO UPDATE SET up_mask = control_plane_heartbeats.up_mask | EXCLUDED.up_mask, updated_at = EXCLUDED.updated_at`,
		hour, bit).Error
	if err != nil {
		return fmt.Errorf("failed to record heartbeat: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) ListHeartbeats(ctx context.Context, from, to time.Time) ([]models.HeartbeatHour, error) {
	var rows []models.HeartbeatHour
	err := ps.getDB(ctx).Raw(`SELECT hour_start, up_mask FROM control_plane_heartbeats
 WHERE hour_start >= ? AND hour_start < ? ORDER BY hour_start`, from.UTC().Truncate(time.Hour), to.UTC()).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list heartbeats: %w", err)
	}
	for i := range rows {
		rows[i].HourStart = rows[i].HourStart.UTC()
	}
	return rows, nil
}

func (ps PostgresDbStore) DeleteRollupsBefore(ctx context.Context, hourlyBefore, dailyBefore time.Time) error {
	db := ps.getDB(ctx)
	for _, stmt := range []struct {
		sql string
		at  time.Time
	}{
		{"DELETE FROM job_rollups_hourly WHERE bucket_start < ?", hourlyBefore},
		{"DELETE FROM eval_rollups_hourly WHERE bucket_start < ?", hourlyBefore},
		{"DELETE FROM job_rollups_daily WHERE bucket_start < ?", dailyBefore},
		{"DELETE FROM eval_rollups_daily WHERE bucket_start < ?", dailyBefore},
		{"DELETE FROM job_actor_group_rollups_daily WHERE bucket_start < ?", dailyBefore},
		{"DELETE FROM control_plane_heartbeats WHERE hour_start < ?", dailyBefore},
		{"DELETE FROM alert_transitions WHERE occurred_at < ?", dailyBefore},
	} {
		if err := db.Exec(stmt.sql, stmt.at.UTC()).Error; err != nil {
			return fmt.Errorf("failed to apply rollup retention: %w", err)
		}
	}
	return nil
}

// rollupScope applies the organization, time, and project visibility parts
// of a filter.
func rollupScope(db *gorm.DB, filter store.RollupFilter) *gorm.DB {
	db = db.Where("org_id = ? AND bucket_start >= ? AND bucket_start < ?", filter.OrgID, filter.From.UTC(), filter.To.UTC())
	if !filter.AllProjects {
		ids := make([]string, 0, len(filter.ProjectIDs))
		for _, id := range filter.ProjectIDs {
			if isValidUUID(id) {
				ids = append(ids, id)
			}
		}
		switch {
		case len(ids) > 0 && filter.NoProject:
			db = db.Where("(project_id = ANY(?::uuid[]) OR project_id IS NULL)", pq.StringArray(ids))
		case len(ids) > 0:
			db = db.Where("project_id = ANY(?::uuid[])", pq.StringArray(ids))
		case filter.NoProject:
			db = db.Where("project_id IS NULL")
		default:
			db = db.Where("false")
		}
	}
	return db
}

func (ps PostgresDbStore) ListJobRollups(ctx context.Context, granularity string, filter store.RollupFilter) ([]models.JobRollup, error) {
	table, err := jobRollupTable(granularity)
	if err != nil {
		return nil, err
	}
	if !isValidUUID(filter.OrgID) {
		return []models.JobRollup{}, nil
	}
	db := rollupScope(ps.getDB(ctx).Table(table), filter)
	if filter.WorkflowSecurityID != "" {
		db = db.Where("workflow_security_id = ?", filter.WorkflowSecurityID)
	}
	if filter.OwnerGroup != "" {
		db = db.Where("owner_group = ?", filter.OwnerGroup)
	}
	if filter.Lane != nil {
		db = db.Where("lane = ?", *filter.Lane)
	}
	if filter.RefClassBucket != "" {
		db = db.Where("ref_class_bucket = ?", filter.RefClassBucket)
	}
	var rows []models.JobRollup
	if err := db.Order("bucket_start").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list job rollups: %w", err)
	}
	for i := range rows {
		rows[i].BucketStart = rows[i].BucketStart.UTC()
	}
	return rows, nil
}

func (ps PostgresDbStore) ListEvalRollups(ctx context.Context, granularity string, filter store.RollupFilter) ([]models.EvalRollup, error) {
	table, err := evalRollupTable(granularity)
	if err != nil {
		return nil, err
	}
	if !isValidUUID(filter.OrgID) {
		return []models.EvalRollup{}, nil
	}
	var rows []models.EvalRollup
	if err := rollupScope(ps.getDB(ctx).Table(table), filter).Order("bucket_start").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list eval rollups: %w", err)
	}
	for i := range rows {
		rows[i].BucketStart = rows[i].BucketStart.UTC()
	}
	return rows, nil
}

func (ps PostgresDbStore) ListActorGroupRollups(ctx context.Context, orgID string, from, to time.Time) ([]models.ActorGroupRollup, error) {
	if !isValidUUID(orgID) {
		return []models.ActorGroupRollup{}, nil
	}
	var rows []models.ActorGroupRollup
	err := ps.getDB(ctx).Table("job_actor_group_rollups_daily").
		Where("org_id = ? AND bucket_start >= ? AND bucket_start < ?", orgID, from.UTC(), to.UTC()).
		Order("bucket_start, group_name").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list actor group rollups: %w", err)
	}
	for i := range rows {
		rows[i].BucketStart = rows[i].BucketStart.UTC()
	}
	return rows, nil
}

func (ps PostgresDbStore) ListFlakyTestsForProjects(ctx context.Context, projectIDs []string, since time.Time, limit int) ([]models.FlakyTestSummary, error) {
	ids := make([]string, 0, len(projectIDs))
	for _, id := range projectIDs {
		if isValidUUID(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return []models.FlakyTestSummary{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var exists bool
	if err := ps.getDB(ctx).Raw(`SELECT to_regclass('public.test_flakiness') IS NOT NULL`).Scan(&exists).Error; err != nil || !exists {
		return []models.FlakyTestSummary{}, nil
	}
	var rows []models.FlakyTestSummary
	err := ps.getDB(ctx).Raw(`
SELECT test_id, suite, classname, name, flaky_count, total_runs, last_flaky_at FROM test_flakiness
 WHERE project_id = ANY(?::uuid[]) AND flaky_count > 0 AND last_flaky_at >= ?::timestamp
 ORDER BY flaky_count DESC, last_flaky_at DESC LIMIT ?`, pq.StringArray(ids), utcTimestamp(since), limit).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list flaky tests: %w", err)
	}
	return rows, nil
}

// --- alert rules ---

func (ps PostgresDbStore) ListAlertRules(ctx context.Context, orgID string) ([]models.AlertRule, error) {
	if !isValidUUID(orgID) {
		return []models.AlertRule{}, nil
	}
	var rules []models.AlertRule
	if err := ps.getDB(ctx).Where("org_id = ?", orgID).Order("name").Find(&rules).Error; err != nil {
		return nil, fmt.Errorf("failed to list alert rules: %w", err)
	}
	return rules, nil
}

func (ps PostgresDbStore) ListEnabledAlertRules(ctx context.Context) ([]models.AlertRule, error) {
	var rules []models.AlertRule
	if err := ps.getDB(ctx).Where("enabled").Order("org_id, name").Find(&rules).Error; err != nil {
		return nil, fmt.Errorf("failed to list enabled alert rules: %w", err)
	}
	return rules, nil
}

func (ps PostgresDbStore) GetAlertRule(ctx context.Context, orgID, name string) (*models.AlertRule, error) {
	if !isValidUUID(orgID) {
		return nil, store.ErrNotFound
	}
	var rule models.AlertRule
	db := ps.getDB(ctx).Where("org_id = ?", orgID)
	if isValidUUID(name) {
		db = db.Where("name = ? OR rule_id = ?::uuid", name, name)
	} else {
		db = db.Where("name = ?", name)
	}
	if err := db.First(&rule).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get alert rule: %w", err)
	}
	return &rule, nil
}

func alertRuleError(err error) error {
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: an alert rule with this name exists", store.ErrAlreadyExists)
	}
	if strings.Contains(err.Error(), "SQLSTATE 23514") {
		return fmt.Errorf("%w: %v", store.ErrInvalidInput, err)
	}
	return fmt.Errorf("failed to save alert rule: %w", err)
}

func (ps PostgresDbStore) CreateAlertRule(ctx context.Context, rule *models.AlertRule) error {
	if rule.State == "" {
		rule.State = models.AlertStateOK
	}
	// A savepoint: a duplicate name does not abort the caller's transaction.
	return alertRuleError(ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Omit("rule_id", "created_at", "updated_at").Create(rule).Error
	}))
}

var alertRuleConfigColumns = []string{
	"description", "condition", "ref_class_bucket", "threshold", "factor", "window_hours", "baseline_days",
	"min_runs", "resolve_margin", "fire_after", "resolve_after", "enabled",
}

func (ps PostgresDbStore) UpdateAlertRuleConfig(ctx context.Context, rule *models.AlertRule, resetState bool) error {
	updates := map[string]interface{}{"updated_at": time.Now().UTC()}
	values := map[string]interface{}{
		"description": rule.Description, "condition": rule.Condition, "ref_class_bucket": rule.RefClassBucket,
		"threshold": rule.Threshold, "factor": rule.Factor, "window_hours": rule.WindowHours, "baseline_days": rule.BaselineDays,
		"min_runs": rule.MinRuns, "resolve_margin": rule.ResolveMargin, "fire_after": rule.FireAfter,
		"resolve_after": rule.ResolveAfter, "enabled": rule.Enabled,
	}
	for _, c := range alertRuleConfigColumns {
		updates[c] = values[c]
	}
	if resetState {
		updates["state"] = models.AlertStateOK
		updates["breach_streak"] = 0
		updates["recover_streak"] = 0
	}
	result := ps.getDB(ctx).Model(&models.AlertRule{}).Where("rule_id = ?", rule.RuleID).Updates(updates)
	if result.Error != nil {
		return alertRuleError(result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) DeleteAlertRule(ctx context.Context, orgID, name string) error {
	rule, err := ps.GetAlertRule(ctx, orgID, name)
	if err != nil {
		return err
	}
	if err := ps.getDB(ctx).Exec("DELETE FROM alert_rules WHERE rule_id = ?", rule.RuleID).Error; err != nil {
		return fmt.Errorf("failed to delete alert rule: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) ApplyAlertEvaluation(ctx context.Context, rule *models.AlertRule, transition *models.AlertTransition, event *models.AlertOutboxEvent) error {
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]interface{}{
			"state": rule.State, "breach_streak": rule.BreachStreak, "recover_streak": rule.RecoverStreak,
			"last_value": rule.LastValue, "last_baseline": rule.LastBaseline, "last_runs": rule.LastRuns,
			"last_evaluated_at": rule.LastEvaluatedAt, "state_changed_at": rule.StateChangedAt,
		}
		if err := tx.Model(&models.AlertRule{}).Where("rule_id = ?", rule.RuleID).Updates(updates).Error; err != nil {
			return fmt.Errorf("failed to save alert state: %w", err)
		}
		if transition == nil {
			return nil
		}
		if event != nil {
			var eventID string
			err := tx.Raw(`
INSERT INTO event_outbox (event_type, occurred_at, org_id, project_id, workflow_security_id, status, result,
                          owner_group, details, order_key)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING event_id`,
				event.EventType, event.OccurredAt.UTC(), event.OrgID, event.ProjectID, event.WorkflowSecurityID,
				event.Status, event.Status, event.OwnerGroup, event.Details, "alert:"+rule.RuleID).Scan(&eventID).Error
			if err != nil {
				return fmt.Errorf("failed to write alert event: %w", err)
			}
			transition.EventID = &eventID
		}
		if err := tx.Omit("transition_id").Create(transition).Error; err != nil {
			return fmt.Errorf("failed to write alert transition: %w", err)
		}
		return nil
	})
}

func (ps PostgresDbStore) ListAlertTransitions(ctx context.Context, ruleID string, limit int) ([]models.AlertTransition, error) {
	if !isValidUUID(ruleID) {
		return []models.AlertTransition{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	var rows []models.AlertTransition
	if err := ps.getDB(ctx).Where("rule_id = ?", ruleID).Order("occurred_at DESC, transition_id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list alert transitions: %w", err)
	}
	return rows, nil
}

// --- SLO targets ---

func (ps PostgresDbStore) ListSLOTargets(ctx context.Context, orgID string) ([]models.SLOTarget, error) {
	if !isValidUUID(orgID) {
		return []models.SLOTarget{}, nil
	}
	var rows []models.SLOTarget
	if err := ps.getDB(ctx).Where("org_id = ?", orgID).Order("sli, lane").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list SLO targets: %w", err)
	}
	return rows, nil
}

func (ps PostgresDbStore) UpsertSLOTarget(ctx context.Context, target *models.SLOTarget) error {
	target.UpdatedAt = time.Now().UTC()
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "org_id"}, {Name: "sli"}, {Name: "lane"}},
			DoUpdates: clause.AssignmentColumns([]string{"objective", "threshold_seconds", "window_days", "updated_by", "updated_at"}),
		}).Create(target).Error
	})
	if err != nil {
		if strings.Contains(err.Error(), "SQLSTATE 23514") {
			return fmt.Errorf("%w: %v", store.ErrInvalidInput, err)
		}
		return fmt.Errorf("failed to save SLO target: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) DeleteSLOTarget(ctx context.Context, orgID, sli, lane string) error {
	if !isValidUUID(orgID) {
		return store.ErrNotFound
	}
	result := ps.getDB(ctx).Exec("DELETE FROM slo_targets WHERE org_id = ? AND sli = ? AND lane = ?", orgID, sli, lane)
	if result.Error != nil {
		return fmt.Errorf("failed to delete SLO target: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) ListRollupLanes(ctx context.Context, orgID string, from, to time.Time) ([]string, error) {
	if !isValidUUID(orgID) {
		return []string{}, nil
	}
	var lanes []string
	err := ps.getDB(ctx).Raw(`SELECT DISTINCT lane FROM job_rollups_daily
 WHERE org_id = ? AND bucket_start >= ? AND bucket_start < ? ORDER BY lane`, orgID, from.UTC(), to.UTC()).Scan(&lanes).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list rollup lanes: %w", err)
	}
	return lanes, nil
}
