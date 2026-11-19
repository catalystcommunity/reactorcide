package test

// Postgres-backed coverage for WP16 (migration 000062,
// postgres_store/observability_operations.go, internal/observability): the
// completed_at and first_status_at triggers, the rollup loop backfill,
// watermark, and incremental pass, actor group attribution from membership
// history, alert evaluation into the outbound event outbox, SLO targets and
// heartbeats, and the visibility of the dashboard CSIL operations.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/observability"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func obsStoreT(t *testing.T) store.ObservabilityStore {
	t.Helper()
	st, ok := store.AppStore.(store.ObservabilityStore)
	require.True(t, ok, "store.AppStore must implement store.ObservabilityStore")
	return st
}

// insertObsJob inserts a finished job with explicit times.
func insertObsJob(t *testing.T, db *gorm.DB, org string, project *string, status, class string, created time.Time, wait, run time.Duration, lane, owner string, actor *string) string {
	t.Helper()
	started := created.Add(wait)
	completed := started.Add(run)
	var classValue interface{}
	if class != "" {
		classValue = class
	}
	var id string
	require.NoError(t, db.Raw(`INSERT INTO jobs (org_id, project_id, name, job_command, status, failure_class, created_at, updated_at,
  started_at, completed_at, priority_lane, ref_classes, owner_group, actor_principal_id)
VALUES (?, ?, 'build', 'true', ?, ?, ?::timestamp, ?::timestamp, ?::timestamp, ?::timestamp, ?, ?, nullif(?, ''), ?) RETURNING job_id`,
		org, project, status, classValue, ts(created), ts(completed), ts(started), ts(completed), lane, pq.StringArray{"default"}, owner, actor).Scan(&id).Error)
	return id
}

func ts(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05.999999") }

func TestObservabilityTriggers(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		owner, err := du.CreateUser(DataSetup{})
		require.NoError(t, err)
		var jobID string
		require.NoError(t, tx.Raw(`INSERT INTO jobs (org_id, name, job_command, status) VALUES (?, 'x', 'true', 'queued') RETURNING job_id`, owner.UserID).Scan(&jobID).Error)
		require.NoError(t, tx.Exec(`UPDATE jobs SET status = 'cancelled' WHERE job_id = ?`, jobID).Error)
		var completed *time.Time
		require.NoError(t, tx.Raw(`SELECT completed_at FROM jobs WHERE job_id = ?`, jobID).Scan(&completed).Error)
		require.NotNil(t, completed, "a terminal job always gets completed_at")

		var eventID string
		require.NoError(t, tx.Raw(`INSERT INTO ci_events (org_id, source, event_type) VALUES (?, 'webhook', 'push') RETURNING event_id`, owner.UserID).Scan(&eventID).Error)
		first := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
		require.NoError(t, tx.Exec(`UPDATE ci_events SET checks_published_at = ? WHERE event_id = ?`, first, eventID).Error)
		require.NoError(t, tx.Exec(`UPDATE ci_events SET checks_published_at = ? WHERE event_id = ?`, first.Add(time.Hour), eventID).Error)
		var got time.Time
		require.NoError(t, tx.Raw(`SELECT first_status_at FROM ci_events WHERE event_id = ?`, eventID).Scan(&got).Error)
		assert.True(t, got.Equal(first), "first_status_at keeps the first publish: %v", got)
	})
}

func TestObservabilityRollupLoop(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		st := obsStoreT(t)
		// Start from no watermark (a backfill), also when a coordinator in
		// another test already ran the loop.
		require.NoError(t, tx.Exec(`DELETE FROM observability_state`).Error)
		du := &DataUtils{db: tx}
		owner, err := du.CreateUser(DataSetup{})
		require.NoError(t, err)
		org := owner.UserID
		project := &models.Project{UserID: &org, OrgID: org, Name: uniqueName("obs"), RepoURL: "https://example.invalid/" + uniqueName("r"),
			Enabled: true, TargetBranches: pq.StringArray{"main"}, AllowedEventTypes: pq.StringArray{"push"}}
		require.NoError(t, tx.Create(project).Error)

		// An actor (a service principal) in group team-a from the start.
		var actor string
		require.NoError(t, tx.Raw(`INSERT INTO principals (kind, org_id, name, display_name) VALUES ('service', ?, ?, 'svc') RETURNING principal_id`,
			org, uniqueName("svc")).Scan(&actor).Error)
		groupID := "0193a6f0-0000-7000-8000-00000000a001"
		day := time.Now().UTC().Add(-72 * time.Hour).Truncate(24 * time.Hour)
		require.NoError(t, tx.Exec(`INSERT INTO group_membership_history (group_id, org_id, group_name, principal_id, change, changed_at)
VALUES (?, ?, 'team-a', ?, 'added', ?)`, groupID, org, actor, day.Add(-time.Hour)).Error)

		pid := &project.ProjectID
		j1 := insertObsJob(t, tx, org, pid, "completed", "", day.Add(time.Hour), 10*time.Second, 100*time.Second, "default", "team-a", &actor)
		insertObsJob(t, tx, org, pid, "completed", "", day.Add(time.Hour+time.Minute), 20*time.Second, 200*time.Second, "default", "team-a", &actor)
		insertObsJob(t, tx, org, pid, "failed", "job", day.Add(time.Hour+2*time.Minute), 30*time.Second, 300*time.Second, "default", "team-a", nil)
		insertObsJob(t, tx, org, pid, "failed", "infra.worker_lost", day.Add(time.Hour+3*time.Minute), 40*time.Second, 50*time.Second, "pr", "", nil)
		cpu, samples := int64(2000), 4
		require.NoError(t, tx.Exec(`INSERT INTO job_resource_summaries (job_id, summary_version, sample_count, cpu_avg_millicores, cpu_request_millicores)
VALUES (?, 1, ?, ?, 4000)`, j1, samples, cpu).Error)

		loop := &observability.Loop{Store: st, Now: func() time.Time { return time.Now().UTC().Add(time.Hour) }}
		require.NoError(t, loop.Tick(ctx))

		state, err := st.GetObservabilityState(ctx, observability.StateJobs)
		require.NoError(t, err)
		require.NotNil(t, state)
		require.NotNil(t, state.BackfilledAt, "the first pass is a backfill")

		from, to := day, day.Add(24*time.Hour)
		filter := store.RollupFilter{OrgID: org, From: from, To: to, AllProjects: true}
		daily, err := st.ListJobRollups(ctx, "day", filter)
		require.NoError(t, err)
		agg := observability.AggregateRows(daily)
		assert.Equal(t, int64(4), agg.Runs)
		assert.Equal(t, int64(2), agg.Successes)
		assert.Equal(t, int64(1), agg.PipelineFailures)
		assert.Equal(t, int64(1), agg.InfraFailures)
		rate, ok := agg.PassRate()
		require.True(t, ok)
		assert.InDelta(t, 2.0/3.0, rate, 1e-9, "infra failures do not count in the pass rate")
		// 2 cores x 100 s.
		assert.InDelta(t, 2.0*100/3600, agg.CPUCoreHours, 1e-6)
		hourly, err := st.ListJobRollups(ctx, "hour", filter)
		require.NoError(t, err)
		assert.Equal(t, int64(4), observability.AggregateRows(hourly).Runs)
		lane := "pr"
		prOnly, err := st.ListJobRollups(ctx, "day", store.RollupFilter{OrgID: org, From: from, To: to, AllProjects: true, Lane: &lane})
		require.NoError(t, err)
		assert.Equal(t, int64(1), observability.AggregateRows(prOnly).Runs)
		var exact *float64
		for _, r := range daily {
			if r.Lane == "default" && r.ActorKind == "service" {
				exact = r.DurationP50Seconds
			}
		}
		require.NotNil(t, exact)
		assert.InDelta(t, 150, *exact, 0.001, "the runs of the service actor: 100 s and 200 s")

		groups, err := st.ListActorGroupRollups(ctx, org, from, to)
		require.NoError(t, err)
		require.Len(t, groups, 1)
		assert.Equal(t, "team-a", groups[0].GroupName)
		assert.Equal(t, int64(2), groups[0].Runs, "only the jobs of the actor in the group")

		// Incremental: a new job after the watermark is added on the next pass.
		insertObsJob(t, tx, org, pid, "completed", "", day.Add(2*time.Hour), time.Second, time.Second, "default", "team-a", nil)
		require.NoError(t, tx.Exec(`UPDATE observability_state SET watermark = ? WHERE name = 'jobs'`, day.Add(90*time.Minute)).Error)
		require.NoError(t, loop.Tick(ctx))
		daily, err = st.ListJobRollups(ctx, "day", filter)
		require.NoError(t, err)
		assert.Equal(t, int64(5), observability.AggregateRows(daily).Runs)

		// Alerts: pass rate 2/3 < 0.9 on the default branch fires at once
		// with fire_after 1, and writes an outbox event.
		rule := &models.AlertRule{OrgID: org, Name: "build-pass", Scope: models.AlertScopeProject, ProjectID: pid,
			Condition: models.AlertConditionPassRateBelow, Threshold: 0.9, RefClassBucket: "default", WindowHours: 720, MinRuns: 2,
			FireAfter: 1, ResolveAfter: 1, ResolveMargin: 0.05, Enabled: true}
		observability.ApplyAlertDefaults(rule)
		require.NoError(t, observability.ValidateAlertRule(rule))
		require.NoError(t, st.CreateAlertRule(ctx, rule))
		require.Error(t, st.CreateAlertRule(ctx, &models.AlertRule{OrgID: org, Name: "build-pass", Scope: "project", ProjectID: pid,
			Condition: "pass_rate_below", Threshold: 0.9, WindowHours: 24, BaselineDays: 7, MinRuns: 1, FireAfter: 1, ResolveAfter: 1, Factor: 1.5}))
		stored, err := st.GetAlertRule(ctx, org, "build-pass")
		require.NoError(t, err)
		ev := &observability.Evaluator{Store: st, Owners: store.AppStore}
		// Receivers: with no subscription the loop does not evaluate the
		// rule; a project subscription for alert events receives it.
		subs, err := store.AppStore.(store.OutboundEventStore).ListEventSubscriptions(ctx, org, nil)
		require.NoError(t, err)
		assert.False(t, ev.HasReceiver(ctx, stored, subs, time.Now().UTC()), "no subscription")
		require.NoError(t, tx.Exec(`INSERT INTO event_subscriptions (org_id, name, owner_principal_id, scope, project_id, event_types, sink_type, sink_config, signing_secret_ciphertext, signing_secret_key_name)
VALUES (?, ?, ?, 'project', ?, '{alert.firing}', 'webhook', '{}', '\x00', 'test')`, org, uniqueName("alerts"), actor, *pid).Error)
		subs, err = store.AppStore.(store.OutboundEventStore).ListEventSubscriptions(ctx, org, nil)
		require.NoError(t, err)
		require.Len(t, subs, 1)
		assert.True(t, ev.HasReceiver(ctx, stored, subs, time.Now().UTC()), "a project subscription for alert.firing")
		require.NoError(t, ev.Evaluate(ctx, stored, time.Now().UTC()))
		assert.Equal(t, models.AlertStateFiring, stored.State)
		transitions, err := st.ListAlertTransitions(ctx, stored.RuleID, 10)
		require.NoError(t, err)
		require.Len(t, transitions, 1)
		require.NotNil(t, transitions[0].EventID)
		var row struct {
			EventType string
			OrderKey  string
			Status    string
		}
		require.NoError(t, tx.Raw(`SELECT event_type, order_key, status FROM event_outbox WHERE event_id = ?`, *transitions[0].EventID).Scan(&row).Error)
		assert.Equal(t, models.OutboundEventAlertFiring, row.EventType)
		assert.Equal(t, "alert:"+stored.RuleID, row.OrderKey)
		pending, err := store.AppStore.(store.OutboundEventStore).ListUndispatchedOutbox(ctx, 1000)
		require.NoError(t, err)
		found := false
		for _, p := range pending {
			if p.EventType == models.OutboundEventAlertFiring && p.OrgID == org {
				found = true
				assert.NotNil(t, p.Details["alert"])
			}
		}
		assert.True(t, found, "the dispatcher reads the alert event")

		// SLO targets and heartbeats.
		require.NoError(t, st.UpsertSLOTarget(ctx, &models.SLOTarget{OrgID: org, SLI: models.SLIQueueWait, Lane: "pr", Objective: 0.9, ThresholdSeconds: 30, WindowDays: 30}))
		require.Error(t, st.UpsertSLOTarget(ctx, &models.SLOTarget{OrgID: org, SLI: models.SLIAvailability, Lane: "pr", Objective: 0.9, WindowDays: 30}),
			"availability has no lane target")
		targets, err := st.ListSLOTargets(ctx, org)
		require.NoError(t, err)
		require.Len(t, targets, 1)
		now := time.Now().UTC()
		require.NoError(t, st.RecordHeartbeat(ctx, now.Add(-2*time.Minute)))
		require.NoError(t, st.RecordHeartbeat(ctx, now.Add(-time.Minute)))
		statuses, err := observability.SLOStatuses(ctx, st, store.RollupFilter{OrgID: org, AllProjects: true}, now)
		require.NoError(t, err)
		bySLI := map[string]observability.SLOStatus{}
		for _, s := range statuses {
			bySLI[s.SLI+"/"+s.Lane] = s
		}
		assert.Equal(t, 30.0, bySLI["queue_wait/pr"].ThresholdSeconds)
		assert.Equal(t, 0.0, bySLI["queue_wait/pr"].Good, "the pr job waited 40 s")
		infra := bySLI["infra_failure_rate/pr"]
		assert.Equal(t, 1.0, infra.Total)
		assert.Equal(t, observability.BudgetExhausted, infra.BudgetState)
		assert.Greater(t, bySLI["availability/"].Good, 0.0)
		require.NoError(t, st.DeleteSLOTarget(ctx, org, models.SLIQueueWait, "pr"))
		require.NoError(t, st.DeleteRollupsBefore(ctx, to.Add(48*time.Hour), from))
		hourly, err = st.ListJobRollups(ctx, "hour", filter)
		require.NoError(t, err)
		assert.Empty(t, hourly, "hourly retention")
	})
}

// TestObservabilityDashboardVisibility drives the dashboard CSIL operations:
// an anonymous caller sees the public project only, the admin token sees the
// private project too. These calls commit, so the test removes its rows.
func TestObservabilityDashboardVisibility(t *testing.T) {
	initTestDB()
	require.NoError(t, initErr)
	st := obsStoreT(t)
	ctx := context.Background()
	du := &DataUtils{db: testDB}
	owner, err := du.CreateUser(DataSetup{})
	require.NoError(t, err)
	org := owner.UserID
	public := createTestProject(t, org, "https://example.invalid/"+uniqueName("pub"), false)
	private := createTestProject(t, org, "https://example.invalid/"+uniqueName("priv"), true)
	adminValue := uniqueName("obs-admin-token")
	adminToken, err := du.CreateAPIToken(DataSetup{"Name": "obs-admin", "TokenHash": checkauth.HashAPIToken(adminValue),
		"IsActive": true, "SubjectType": "instance_token", "AllOrganizations": true, "AllCapabilities": true})
	require.NoError(t, err)
	t.Cleanup(func() {
		testDB.Exec(`DELETE FROM job_rollups_daily WHERE org_id = ?`, org)
		testDB.Exec(`DELETE FROM projects WHERE project_id IN (?, ?)`, public.ProjectID, private.ProjectID)
		testDB.Exec(`DELETE FROM api_tokens WHERE token_id = ?`, adminToken.TokenID)
		testDB.Exec(`DELETE FROM principals WHERE principal_id = ?`, adminToken.TokenID)
		testDB.Exec(`DELETE FROM users WHERE user_id = ?`, adminToken.UserID)
	})
	today := observability.BucketStart(time.Now(), observability.Daily)
	for _, p := range []*models.Project{public, private} {
		id := p.ProjectID
		row := models.JobRollup{BucketStart: today, OrgID: org, ProjectID: &id, WorkflowSecurityID: "ci", RefClassBucket: "default",
			ActorKind: "human", Runs: 2, Successes: 1, FailedJob: 1, DurationHist: pq.Int64Array(observability.NewHistogram()),
			QueueWaitHist: pq.Int64Array(observability.NewHistogram())}
		if p == private {
			row.WorkflowSecurityID = "secret-deploy"
		}
		require.NoError(t, st.ReplaceJobRollups(ctx, "day", today, today.Add(24*time.Hour), append(existingRows(t, st, org, today), row)))
	}

	handlers.ResetAppMux()
	defer handlers.ResetAppMux()
	mux := handlers.GetAppMux()
	call := func(token string, project *string) (csilapi.GetPipelineDashboardResponse, *csilapi.ServiceError) {
		return csilCall(t, mux, "ReactorcideUi", "get-pipeline-dashboard", csilapi.EncodeGetPipelineDashboardRequest,
			csilapi.DecodeGetPipelineDashboardResponse, csilapi.GetPipelineDashboardRequest{OrgId: org, ProjectId: project}, token)
	}
	anon, svcErr := call("", nil)
	require.Nil(t, svcErr, "%+v", svcErr)
	require.Len(t, anon.Workflows, 1)
	assert.Equal(t, "ci", anon.Workflows[0].WorkflowSecurityId)
	assert.Equal(t, int64(2), anon.Totals.Runs)
	_, svcErr = call("", &private.ProjectID)
	require.NotNil(t, svcErr)
	assert.Equal(t, "not_found", svcErr.Code)

	admin, svcErr := call(adminValue, nil)
	require.Nil(t, svcErr, "%+v", svcErr)
	assert.Len(t, admin.Workflows, 2)
	assert.Equal(t, int64(4), admin.Totals.Runs)

	infra, svcErr := csilCall(t, mux, "ReactorcideUi", "get-infra-dashboard", csilapi.EncodeGetInfraDashboardRequest,
		csilapi.DecodeGetInfraDashboardResponse, csilapi.GetInfraDashboardRequest{OrgId: org}, "")
	require.Nil(t, svcErr, "%+v", svcErr)
	assert.Equal(t, int64(2), infra.Totals.Runs)
	assert.NotEmpty(t, infra.Slos)
	assert.Empty(t, infra.Capacity.Queues, "queue names need every project")

	_, svcErr = csilCall(t, mux, "ReactorcideUi", "get-usage-dashboard", csilapi.EncodeGetUsageDashboardRequest,
		csilapi.DecodeGetUsageDashboardResponse, csilapi.GetUsageDashboardRequest{OrgId: org, Attribution: strPtrObs("actor")}, "")
	require.NotNil(t, svcErr)
	assert.Equal(t, "permission_denied", svcErr.Code, "actor attribution needs every project")
	usage, svcErr := csilCall(t, mux, "ReactorcideUi", "get-usage-dashboard", csilapi.EncodeGetUsageDashboardRequest,
		csilapi.DecodeGetUsageDashboardResponse, csilapi.GetUsageDashboardRequest{OrgId: org, Attribution: strPtrObs("actor")}, adminValue)
	require.Nil(t, svcErr, "%+v", svcErr)
	assert.Equal(t, "actor", usage.Attribution)

	// REST with the admin token (no logins).
	var orgName string
	require.NoError(t, testDB.Raw(`SELECT name FROM organizations WHERE org_id = ?`, org).Scan(&orgName).Error)
	base := "/api/v1/organizations/" + orgName
	rest := func(method, path, body, token string) (int, map[string]interface{}) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		out := map[string]interface{}{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, _ := rest(http.MethodGet, base+"/slos/status", "", "")
	assert.Equal(t, http.StatusUnauthorized, code)
	code, body := rest(http.MethodGet, base+"/slos/status", "", adminValue)
	require.Equal(t, http.StatusOK, code, "%v", body)
	assert.Len(t, body["slos"], 4, "one row each: eval, queue wait (no lanes), infra rate (no lanes), availability")
	code, body = rest(http.MethodPost, base+"/alert-rules", `{"name":"ci-pass","scope":"workflow","project":"`+public.ProjectID+
		`","workflow_security_id":"ci","condition":"pass_rate_below","threshold":0.8}`, adminValue)
	require.Equal(t, http.StatusCreated, code, "%v", body)
	t.Cleanup(func() { testDB.Exec(`DELETE FROM alert_rules WHERE org_id = ?`, org) })
	assert.Equal(t, "default", body["ref_class_bucket"])
	// The test configuration rolls back each request, so store the rule
	// for the next requests.
	pid := public.ProjectID
	seed := &models.AlertRule{OrgID: org, Name: "ci-pass", Scope: models.AlertScopeWorkflow, ProjectID: &pid, WorkflowSecurityID: "ci",
		Condition: models.AlertConditionPassRateBelow, Threshold: 0.8, RefClassBucket: "default", Enabled: true, ResolveMargin: 0.05}
	observability.ApplyAlertDefaults(seed)
	require.NoError(t, st.CreateAlertRule(ctx, seed))
	code, body = rest(http.MethodPatch, base+"/alert-rules/ci-pass", `{"threshold":0.7}`, adminValue)
	require.Equal(t, http.StatusOK, code, "%v", body)
	assert.Equal(t, 0.7, body["threshold"])
	code, body = rest(http.MethodPatch, base+"/alert-rules/ci-pass", `{"owner_group":"x"}`, adminValue)
	assert.Equal(t, http.StatusBadRequest, code, "the scope cannot change: %v", body)
	code, body = rest(http.MethodPut, base+"/slos/queue_wait?lane=pr", `{"objective":0.9,"threshold_seconds":600}`, adminValue)
	require.Equal(t, http.StatusOK, code, "%v", body)
	t.Cleanup(func() { testDB.Exec(`DELETE FROM slo_targets WHERE org_id = ?`, org) })
	code, _ = rest(http.MethodPut, base+"/slos/availability?lane=pr", `{"objective":0.9}`, adminValue)
	assert.Equal(t, http.StatusBadRequest, code)
	code, body = rest(http.MethodGet, base+"/observability/pipeline?days=7", "", adminValue)
	require.Equal(t, http.StatusOK, code, "%v", body)
	assert.Len(t, body["workflows"], 2)
	code, _ = rest(http.MethodGet, base+"/observability/rollups?granularity=week", "", adminValue)
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = rest(http.MethodDelete, base+"/alert-rules/ci-pass", "", adminValue)
	assert.Equal(t, http.StatusNoContent, code)
}

func strPtrObs(s string) *string { return &s }

func existingRows(t *testing.T, st store.ObservabilityStore, org string, day time.Time) []models.JobRollup {
	rows, err := st.ListJobRollups(context.Background(), "day", store.RollupFilter{OrgID: org, From: day, To: day.Add(24 * time.Hour), AllProjects: true})
	require.NoError(t, err)
	return rows
}

// TestObservabilityMigrationUpDown applies 000062 on a database with a
// finished job, removes it, and applies it again.
func TestObservabilityMigrationUpDown(t *testing.T) {
	db := newScratchDBAt(t, "wp16_migration", 61)
	require.NoError(t, goose.UpTo(db, "migrations", 62))
	var tables int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_name IN
		('job_rollups_hourly', 'job_rollups_daily', 'job_actor_group_rollups_daily', 'eval_rollups_hourly', 'eval_rollups_daily',
		 'observability_state', 'control_plane_heartbeats', 'alert_rules', 'alert_transitions', 'slo_targets')`).Scan(&tables))
	require.Equal(t, 10, tables)
	require.NoError(t, goose.DownTo(db, "migrations", 61))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_name IN ('job_rollups_hourly', 'alert_rules', 'slo_targets')`).Scan(&tables))
	require.Zero(t, tables)
	var columns int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'ci_events' AND column_name = 'first_status_at'`).Scan(&columns))
	require.Zero(t, columns)
	require.NoError(t, goose.UpTo(db, "migrations", 62))
}
