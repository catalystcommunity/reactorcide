package test

// Postgres-backed coverage for migration 000058 and internal/outboundevents:
// the outbox triggers write in the transaction of the status change, the
// dispatcher fans out to matching subscriptions and submits one keyed task
// per (subscription, workflow) at a time, and the delivery loop retries,
// dead-letters, and replays against an httptest receiver.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/outboundevents"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

type outboundFixture struct {
	orgID, projectID, principalID string
}

func newOutboundFixture(t *testing.T, tx *gorm.DB) outboundFixture {
	t.Helper()
	var f outboundFixture
	name := uniqueName("obev")
	require.NoError(t, tx.Raw(`INSERT INTO organizations (name) VALUES (?) RETURNING org_id`, name).Scan(&f.orgID).Error)
	require.NoError(t, tx.Raw(`INSERT INTO projects (name, repo_url, org_id) VALUES ('app', ?, ?) RETURNING project_id`,
		"github.com/"+name+"/app", f.orgID).Scan(&f.projectID).Error)
	require.NoError(t, tx.Raw(`INSERT INTO principals (kind, org_id, name) VALUES ('service', ?, 'hooks') RETURNING principal_id`,
		f.orgID).Scan(&f.principalID).Error)
	return f
}

// subscribe adds an enabled subscription, so the outbox triggers of the org
// write rows (an org with no enabled subscription writes none).
func (f outboundFixture) subscribe(t *testing.T, tx *gorm.DB) string {
	t.Helper()
	var id string
	require.NoError(t, tx.Raw(`INSERT INTO event_subscriptions (org_id, name, owner_principal_id, scope, sink_type,
signing_secret_ciphertext, signing_secret_key_name) VALUES (?, ?, ?, 'org', 'webhook', '\x00', 'test') RETURNING subscription_id`,
		f.orgID, uniqueName("sub"), f.principalID).Scan(&id).Error)
	return id
}

func (f outboundFixture) insertWorkflow(t *testing.T, tx *gorm.DB, securityID string) string {
	t.Helper()
	var id string
	require.NoError(t, tx.Raw(`INSERT INTO workflow_instances (org_id, project_id, name, workflow_security_id, status, commit_sha)
VALUES (?, ?, 'ci', ?, 'running', 'abc123') RETURNING workflow_id`, f.orgID, f.projectID, securityID).Scan(&id).Error)
	return id
}

func (f outboundFixture) insertJob(t *testing.T, tx *gorm.DB, workflowID *string, status string) string {
	t.Helper()
	var id string
	require.NoError(t, tx.Raw(`INSERT INTO jobs (org_id, project_id, name, job_command, status, workflow_id, source_ref, commit_sha)
VALUES (?, ?, 'build', 'make', ?, ?, 'refs/heads/main', 'abc123') RETURNING job_id`,
		f.orgID, f.projectID, status, workflowID).Scan(&id).Error)
	return id
}

func outboxTypes(t *testing.T, tx *gorm.DB, column, id string) []string {
	t.Helper()
	var types []string
	require.NoError(t, tx.Raw(`SELECT event_type FROM event_outbox WHERE `+column+` = ? ORDER BY outbox_id`, id).Scan(&types).Error)
	return types
}

func TestOutboxTriggersWriteInTheTransaction(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		f := newOutboundFixture(t, tx)
		f.subscribe(t, tx)
		wf := f.insertWorkflow(t, tx, "sec-build")
		job := f.insertJob(t, tx, &wf, "submitted")
		assert.Empty(t, outboxTypes(t, tx, "job_id", job), "submitted is not a phase")

		require.NoError(t, tx.Exec(`UPDATE jobs SET status = 'queued' WHERE job_id = ?`, job).Error)
		require.NoError(t, tx.Exec(`UPDATE jobs SET status = 'scheduling' WHERE job_id = ?`, job).Error)
		require.NoError(t, tx.Exec(`UPDATE jobs SET status = 'running' WHERE job_id = ?`, job).Error)
		require.NoError(t, tx.Exec(`UPDATE jobs SET status = 'running', notes = 'x' WHERE job_id = ?`, job).Error)
		require.NoError(t, tx.Exec(`UPDATE jobs SET status = 'failed', failure_class = 'job', exit_code = 2 WHERE job_id = ?`, job).Error)
		assert.Equal(t, []string{"node.pre_start", "node.started", "node.post_complete"}, outboxTypes(t, tx, "job_id", job))

		var row models.EventOutbox
		require.NoError(t, tx.Where("job_id = ? AND event_type = 'node.post_complete'", job).First(&row).Error)
		assert.Equal(t, "failed", row.Result)
		assert.Equal(t, "job", row.FailureClass)
		assert.Equal(t, "workflow:"+wf, row.OrderKey)
		assert.Equal(t, "refs/heads/main", row.Ref)
		assert.Equal(t, float64(2), row.Details["exit_code"])
		assert.NotNil(t, row.ActorPrincipalID, "the actor trigger of 000048 gives every job an actor")
		assert.Nil(t, row.DispatchedAt)

		// A job that skips scheduling still gets pre_start before started.
		direct := f.insertJob(t, tx, nil, "queued")
		require.NoError(t, tx.Exec(`UPDATE jobs SET status = 'running' WHERE job_id = ?`, direct).Error)
		assert.Equal(t, []string{"node.pre_start", "node.started"}, outboxTypes(t, tx, "job_id", direct))
		var key string
		require.NoError(t, tx.Raw(`SELECT order_key FROM event_outbox WHERE job_id = ? LIMIT 1`, direct).Scan(&key).Error)
		assert.Equal(t, "job:"+direct, key, "a loose job orders by itself")

		// A rolled-back status change leaves no event: the outbox row is in
		// the same transaction.
		require.NoError(t, tx.SavePoint("before_cancel").Error)
		require.NoError(t, tx.Exec(`UPDATE jobs SET status = 'cancelled' WHERE job_id = ?`, direct).Error)
		assert.Len(t, outboxTypes(t, tx, "job_id", direct), 3)
		require.NoError(t, tx.RollbackTo("before_cancel").Error)
		assert.Len(t, outboxTypes(t, tx, "job_id", direct), 2)

		// Nodes: skipped, and post_complete for a node with no job.
		var skipped, refused string
		require.NoError(t, tx.Raw(`INSERT INTO workflow_nodes (workflow_id, name, display_name, status) VALUES (?, 'docs', 'docs', 'skipped')
RETURNING node_id`, wf).Scan(&skipped).Error)
		require.NoError(t, tx.Raw(`INSERT INTO workflow_nodes (workflow_id, name, display_name, status) VALUES (?, 'deploy', 'deploy', 'pending')
RETURNING node_id`, wf).Scan(&refused).Error)
		require.NoError(t, tx.Exec(`UPDATE workflow_nodes SET status = 'failed', decision_reason = 'workflow not admitted' WHERE node_id = ?`, refused).Error)
		assert.Equal(t, []string{"node.skipped"}, outboxTypes(t, tx, "node_id", skipped))
		assert.Equal(t, []string{"node.post_complete"}, outboxTypes(t, tx, "node_id", refused))
		var nodeRow models.EventOutbox
		require.NoError(t, tx.Where("node_id = ?", skipped).First(&nodeRow).Error)
		assert.Equal(t, "sec-build", nodeRow.WorkflowSecurityID)
		assert.Equal(t, f.orgID, nodeRow.OrgID)

		// Workflow completed.
		require.NoError(t, tx.Exec(`UPDATE workflow_instances SET status = 'failed' WHERE workflow_id = ?`, wf).Error)
		var wfTypes []string
		require.NoError(t, tx.Raw(`SELECT event_type FROM event_outbox WHERE workflow_id = ? AND event_type LIKE 'workflow.%'`, wf).Scan(&wfTypes).Error)
		assert.Equal(t, []string{"workflow.completed"}, wfTypes)

		// Events: evaluated, and the decisions admitted / not_admitted.
		var ev string
		require.NoError(t, tx.Raw(`INSERT INTO ci_events (org_id, project_id, source, provider, event_type, branch, commit_sha, status)
VALUES (?, ?, 'webhook', 'github', 'push', 'main', 'abc123', 'evaluating') RETURNING event_id`, f.orgID, f.projectID).Scan(&ev).Error)
		require.NoError(t, tx.Exec(`UPDATE ci_events SET status = 'evaluated' WHERE event_id = ?`, ev).Error)
		require.NoError(t, tx.Exec(`INSERT INTO ci_event_workflow_decisions (event_id, workflow_security_id, workflow_name, decision)
VALUES (?, 'sec-build', 'ci', 'run'), (?, 'sec-deploy', 'deploy', 'not_admitted'), (?, 'sec-docs', 'docs', 'skipped')`, ev, ev, ev).Error)
		assert.Equal(t, []string{"event.evaluated", "event.admitted", "event.not_admitted"}, outboxTypes(t, tx, "ci_event_id", ev))
		var evRow models.EventOutbox
		require.NoError(t, tx.Where("ci_event_id = ? AND event_type = 'event.evaluated'", ev).First(&evRow).Error)
		assert.Equal(t, "main", evRow.Ref)
		assert.Equal(t, "event:"+ev, evRow.OrderKey)
	})
}

// fakeTaskQueue is an in-memory Corndogs internal queue with keyed submit
// semantics: a repeat of a key returns the first task.
type fakeTaskQueue struct {
	mu        sync.Mutex
	byKey     map[string]string
	pending   []*corndogs.ClaimedTask
	submits   int
	discarded []string
	failNext  bool
}

func newFakeTaskQueue() *fakeTaskQueue { return &fakeTaskQueue{byKey: map[string]string{}} }

func (q *fakeTaskQueue) SubmitKeyedTask(_ context.Context, queue, key string, payload []byte) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failNext {
		q.failNext = false
		return "", fmt.Errorf("corndogs unavailable")
	}
	if !corndogs.IsInternalQueue(queue) {
		return "", fmt.Errorf("not an internal queue")
	}
	if id, ok := q.byKey[key]; ok {
		return id, nil
	}
	q.submits++
	id := fmt.Sprintf("task-%d", q.submits)
	q.byKey[key] = id
	q.pending = append(q.pending, &corndogs.ClaimedTask{ID: id, Queue: queue, State: "submitted-working", Guarded: true, Payload: payload})
	return id, nil
}

func (q *fakeTaskQueue) Claim(_ context.Context, queue string) (*corndogs.ClaimedTask, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return nil, nil
	}
	task := q.pending[0]
	q.pending = q.pending[1:]
	return task, nil
}

func (q *fakeTaskQueue) Complete(context.Context, *corndogs.ClaimedTask) error { return nil }

func (q *fakeTaskQueue) Discard(_ context.Context, _ string, taskID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.discarded = append(q.discarded, taskID)
	return nil
}

func (q *fakeTaskQueue) queued() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// fakeCrypto stands in for the master key manager.
type fakeCrypto struct{}

func (fakeCrypto) EncryptWithPrimary(plaintext []byte) (string, []byte, error) {
	return "test-key", append([]byte("enc:"), plaintext...), nil
}

func (fakeCrypto) DecryptWithKey(keyName string, ciphertext []byte) ([]byte, error) {
	if keyName != "test-key" || len(ciphertext) < 4 {
		return nil, fmt.Errorf("bad key")
	}
	return ciphertext[4:], nil
}

type receivedEvent struct {
	ID, Type, Signature, Timestamp string
	Body                           []byte
	Sequence                       int64
}

type receiver struct {
	mu     sync.Mutex
	events []receivedEvent
	status int
}

func (r *receiver) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var payload struct {
			Sequence int64 `json:"sequence"`
		}
		_ = json.Unmarshal(body, &payload)
		r.mu.Lock()
		r.events = append(r.events, receivedEvent{ID: req.Header.Get(outboundevents.HeaderEventID), Type: req.Header.Get(outboundevents.HeaderEventType),
			Signature: req.Header.Get(outboundevents.HeaderSignature), Timestamp: req.Header.Get(outboundevents.HeaderTimestamp),
			Body: body, Sequence: payload.Sequence})
		status := r.status
		r.mu.Unlock()
		if status == 0 {
			status = http.StatusNoContent
		}
		w.WriteHeader(status)
	})
}

func (r *receiver) setStatus(status int) {
	r.mu.Lock()
	r.status = status
	r.mu.Unlock()
}

func (r *receiver) received() []receivedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedEvent(nil), r.events...)
}

func TestOutboundDispatchOrderRetryDeadLetterReplay(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		events := store.OutboundEventStore(postgres_store.PostgresStore)
		f := newOutboundFixture(t, tx)

		recv := &receiver{}
		server := httptest.NewTLSServer(recv.handler())
		defer server.Close()
		sink := &outboundevents.WebhookSink{
			Policy:    outboundevents.DestinationPolicy{AllowedNetworks: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}},
			Transport: server.Client().Transport,
		}
		sinks := map[string]outboundevents.Sink{models.SinkTypeWebhook: sink}
		manager := &outboundevents.Manager{Store: events, Principals: postgres_store.PostgresStore, Crypto: fakeCrypto{}, Sinks: sinks}

		// A project subscription for post_complete and workflow.completed,
		// and an org subscription filtered to another project (no match).
		sub, secret, err := manager.Create(ctx, f.orgID, outboundevents.SubscriptionInput{
			Name: "diagnosis", Scope: models.SubscriptionScopeProject, ProjectID: &f.projectID,
			EventTypes: []string{models.OutboundEventNodePostComplete, models.OutboundEventNodeStarted},
			WebhookURL: server.URL + "/hook", MaxAttempts: 2,
		}, f.principalID)
		require.NoError(t, err)
		require.NotEmpty(t, secret)
		assert.Equal(t, f.principalID, sub.OwnerPrincipalID)
		_, _, err = manager.Create(ctx, f.orgID, outboundevents.SubscriptionInput{
			Name: "refused", Scope: models.SubscriptionScopeOrg, WebhookURL: "https://10.0.0.1/hook",
		}, f.principalID)
		require.Error(t, err, "a private destination is refused at create")
		assert.True(t, outboundevents.IsValidationError(err))

		wfA := f.insertWorkflow(t, tx, "sec-a")
		wfB := f.insertWorkflow(t, tx, "sec-b")
		jobA := f.insertJob(t, tx, &wfA, "scheduling")
		jobB := f.insertJob(t, tx, &wfB, "scheduling")
		for _, step := range []string{"running", "completed"} {
			require.NoError(t, tx.Exec(`UPDATE jobs SET status = ? WHERE job_id = ?`, step, jobA).Error)
			require.NoError(t, tx.Exec(`UPDATE jobs SET status = ? WHERE job_id = ?`, step, jobB).Error)
		}

		queue := newFakeTaskQueue()
		clock := time.Now().UTC()
		now := func() time.Time { return clock }
		dispatcher := &outboundevents.Dispatcher{Store: events, Queue: queue, UIBaseURL: "https://ci.example.com", Now: now, Lag: -1}
		loop := &outboundevents.DeliveryLoop{Store: events, Queue: queue, Sinks: sinks, Secrets: fakeCrypto{}, Now: now,
			Backoff: outboundevents.Backoff{Base: time.Minute, Max: time.Hour}}

		require.NoError(t, dispatcher.FanOut(ctx))
		deliveries, err := events.ListEventDeliveries(ctx, sub.SubscriptionID, "", 50)
		require.NoError(t, err)
		require.Len(t, deliveries, 4, "started and post_complete of two workflows; pre_start is filtered")

		// One task per workflow; a second submit pass adds nothing.
		require.NoError(t, dispatcher.SubmitDue(ctx))
		require.NoError(t, dispatcher.SubmitDue(ctx))
		assert.Equal(t, 2, queue.queued(), "one in-flight delivery per (subscription, workflow)")
		assert.Equal(t, 2, queue.submits)

		for i := 0; i < 2; i++ {
			worked, err := loop.RunOnce(ctx)
			require.NoError(t, err)
			require.True(t, worked)
		}
		require.NoError(t, dispatcher.SubmitDue(ctx))
		assert.Equal(t, 2, queue.queued(), "the next event of each workflow follows the first")
		for i := 0; i < 2; i++ {
			_, err := loop.RunOnce(ctx)
			require.NoError(t, err)
		}
		got := recv.received()
		require.Len(t, got, 4)
		perWorkflow := map[string][]string{}
		for _, ev := range got {
			assert.True(t, outboundevents.Verify([]byte(secret), ev.Timestamp, ev.Body, ev.Signature), "signed with the subscription secret")
			var p outboundevents.Payload
			require.NoError(t, json.Unmarshal(ev.Body, &p))
			perWorkflow[p.Workflow.ID] = append(perWorkflow[p.Workflow.ID], p.Type)
			assert.Equal(t, f.projectID, p.Project.ID)
			assert.Equal(t, "https://ci.example.com/app/workflows/"+p.Workflow.ID, p.Links.UI)
			assert.NotEmpty(t, p.Actor.Kind, "every run has an actor")
		}
		assert.Equal(t, []string{"node.started", "node.post_complete"}, perWorkflow[wfA], "order per workflow")
		assert.Equal(t, []string{"node.started", "node.post_complete"}, perWorkflow[wfB])

		// Retries with backoff, then dead_letter after max attempts (2).
		recv.setStatus(http.StatusInternalServerError)
		jobC := f.insertJob(t, tx, &wfA, "running")
		require.NoError(t, tx.Exec(`UPDATE jobs SET status = 'failed' WHERE job_id = ?`, jobC).Error)
		require.NoError(t, dispatcher.FanOut(ctx))
		require.NoError(t, dispatcher.SubmitDue(ctx))
		require.Equal(t, 1, queue.queued(), "started of jobC")
		_, err = loop.RunOnce(ctx)
		require.NoError(t, err)
		first := findDelivery(ctx, t, events, sub.SubscriptionID, models.OutboundEventNodeStarted, jobDeliveryFilter(t, tx, jobC, "node.started"))
		assert.Equal(t, models.DeliveryStatusPending, first.Status)
		assert.Equal(t, 1, first.Attempts)
		require.NotNil(t, first.LastHTTPStatus)
		assert.Equal(t, 500, *first.LastHTTPStatus)
		assert.True(t, first.NextAttemptAt.After(clock), "backoff moves the next attempt")

		require.NoError(t, dispatcher.SubmitDue(ctx))
		assert.Equal(t, 0, queue.queued(), "not due before the backoff ends, and the next event waits")
		clock = clock.Add(2 * time.Hour)
		require.NoError(t, dispatcher.SubmitDue(ctx))
		require.Equal(t, 1, queue.queued())
		_, err = loop.RunOnce(ctx)
		require.NoError(t, err)
		dead, err := events.GetEventDelivery(ctx, first.DeliveryID)
		require.NoError(t, err)
		assert.Equal(t, models.DeliveryStatusDeadLetter, dead.Status)
		assert.Equal(t, 2, dead.Attempts)
		attempts, err := events.ListEventDeliveryAttempts(ctx, dead.DeliveryID)
		require.NoError(t, err)
		require.Len(t, attempts, 2)
		assert.Equal(t, models.DeliveryAttemptFailed, attempts[1].Result)

		// A dead letter does not block the workflow: the next event goes.
		recv.setStatus(http.StatusOK)
		require.NoError(t, dispatcher.SubmitDue(ctx))
		require.Equal(t, 1, queue.queued())
		_, err = loop.RunOnce(ctx)
		require.NoError(t, err)

		// Replay delivers the dead letter with a fresh task key.
		replayed, err := events.ReplayEventDelivery(ctx, dead.DeliveryID)
		require.NoError(t, err)
		assert.Equal(t, models.DeliveryStatusPending, replayed.Status)
		assert.Equal(t, 0, replayed.Attempts)
		submitsBefore := queue.submits
		require.NoError(t, dispatcher.SubmitDue(ctx))
		assert.Equal(t, submitsBefore+1, queue.submits, "a replay is a new keyed submit, not a swallowed repeat")
		_, err = loop.RunOnce(ctx)
		require.NoError(t, err)
		final, err := events.GetEventDelivery(ctx, dead.DeliveryID)
		require.NoError(t, err)
		assert.Equal(t, models.DeliveryStatusDelivered, final.Status)
		_, err = events.ReplayEventDelivery(ctx, wfA)
		assert.Error(t, err)

		// A stale task (its delivery was reset and resubmitted) sends nothing.
		jobD := f.insertJob(t, tx, &wfB, "running")
		require.NoError(t, dispatcher.FanOut(ctx))
		require.NoError(t, dispatcher.SubmitDue(ctx))
		require.Equal(t, 1, queue.queued())
		require.NoError(t, tx.Exec(`UPDATE event_deliveries SET updated_at = now() - interval '1 hour' WHERE status = 'queued' AND subscription_id = ?`,
			sub.SubscriptionID).Error)
		dispatcher.StaleAfter = time.Minute
		dispatcher.Now = func() time.Time { return time.Now().UTC() }
		dispatcher.ReapStale(ctx)
		assert.Len(t, queue.discarded, 1)
		require.NoError(t, dispatcher.SubmitDue(ctx))
		require.Equal(t, 2, queue.queued(), "the old task and the new one")
		before := len(recv.received())
		_, err = loop.RunOnce(ctx) // the old task: stale
		require.NoError(t, err)
		assert.Equal(t, before, len(recv.received()), "a stale task does not send")
		_, err = loop.RunOnce(ctx)
		require.NoError(t, err)
		assert.Equal(t, before+1, len(recv.received()))
		_ = jobD
	})
}

func jobDeliveryFilter(t *testing.T, tx *gorm.DB, jobID, eventType string) string {
	t.Helper()
	var eventID string
	require.NoError(t, tx.Raw(`SELECT event_id FROM event_outbox WHERE job_id = ? AND event_type = ?`, jobID, eventType).Scan(&eventID).Error)
	return eventID
}

func findDelivery(ctx context.Context, t *testing.T, events store.OutboundEventStore, subscriptionID, eventType, eventID string) models.EventDelivery {
	t.Helper()
	deliveries, err := events.ListEventDeliveries(ctx, subscriptionID, "", 200)
	require.NoError(t, err)
	for _, d := range deliveries {
		if d.EventType == eventType && d.EventID == eventID {
			return d
		}
	}
	t.Fatalf("no %s delivery for event %s", eventType, eventID)
	return models.EventDelivery{}
}

func TestSubscriptionRESTWithAdminToken(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		admin, err := du.CreateUser(DataSetup{"Username": "sub-admin", "Email": "sub@example.com"})
		require.NoError(t, err)
		adminAuth, err := createTokenAuthHeader(ctx, tx, admin.UserID)
		require.NoError(t, err)
		newPrincipalOrg(t, ctx, "sub-org")
		base := "/api/v1/organizations/sub-org/subscriptions"

		rr := principalRequest(t, ctx, http.MethodPost, base, adminAuth, map[string]any{
			"name": "ops", "scope": "org", "webhook_url": "https://10.1.2.3/hook"})
		assert.Equal(t, http.StatusBadRequest, rr.Code, "a private destination is refused: %s", rr.Body.String())

		rr = principalRequest(t, ctx, http.MethodPost, base, adminAuth, map[string]any{
			"name": "ops", "scope": "org", "webhook_url": "https://hooks.example.com/rc?team=ops",
			"event_types": []string{"node.post_complete"}, "results": []string{"failed"}})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		var created struct {
			SubscriptionID string   `json:"subscription_id"`
			SigningSecret  string   `json:"signing_secret"`
			EventTypes     []string `json:"event_types"`
			OwnerPrincipal string   `json:"owner_principal_id"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
		assert.NotEmpty(t, created.SigningSecret, "the secret is shown on create")
		assert.NotEmpty(t, created.OwnerPrincipal)
		assert.NotContains(t, rr.Body.String(), "ciphertext")

		rr = principalRequest(t, ctx, http.MethodGet, base, adminAuth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), created.SubscriptionID)
		assert.NotContains(t, rr.Body.String(), created.SigningSecret, "the secret is never listed")

		rr = principalRequest(t, ctx, http.MethodPatch, base+"/"+created.SubscriptionID, adminAuth, map[string]any{
			"enabled": false, "results": []string{}})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), `"enabled":false`)
		assert.Contains(t, rr.Body.String(), `"results":[]`)

		rr = principalRequest(t, ctx, http.MethodGet, base+"/"+created.SubscriptionID+"/deliveries", adminAuth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.JSONEq(t, `{"deliveries":[]}`, rr.Body.String())

		var audits int64
		require.NoError(t, tx.Raw(`SELECT count(*) FROM audit_events WHERE subject_id = ? AND action LIKE 'subscription.%'`,
			created.SubscriptionID).Scan(&audits).Error)
		assert.Equal(t, int64(2), audits, "create and update are audited")
		var details string
		require.NoError(t, tx.Raw(`SELECT details::text FROM audit_events WHERE subject_id = ? LIMIT 1`, created.SubscriptionID).Scan(&details).Error)
		assert.NotContains(t, details, "team=ops", "the URL query is not in the audit record")

		rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/organizations/sub-org/subscriptions/"+created.SubscriptionID, "", nil)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)

		rr = principalRequest(t, ctx, http.MethodDelete, base+"/"+created.SubscriptionID, adminAuth, nil)
		require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodGet, base+"/"+created.SubscriptionID, adminAuth, nil)
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})
}

// An org with no enabled subscription writes no outbox rows; the cost of a
// 3000-row status UPDATE is measured with and without a subscription.
func TestOutboxSkipsOrgsWithoutSubscriptionsAndPrunes(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		f := newOutboundFixture(t, tx)
		const rows = 3000
		require.NoError(t, tx.Exec(`INSERT INTO jobs (org_id, project_id, name, job_command, status)
SELECT ?, ?, 'bulk-' || g, 'make', 'queued' FROM generate_series(1, ?) g`, f.orgID, f.projectID, rows).Error)
		count := func() int64 {
			var n int64
			require.NoError(t, tx.Raw(`SELECT count(*) FROM event_outbox WHERE org_id = ?`, f.orgID).Scan(&n).Error)
			return n
		}
		update := func(status string) time.Duration {
			start := time.Now()
			require.NoError(t, tx.Exec(`UPDATE jobs SET status = ? WHERE org_id = ? AND name LIKE 'bulk-%'`, status, f.orgID).Error)
			return time.Since(start)
		}
		without := update("running")
		assert.Equal(t, int64(0), count(), "an org with no subscription writes no outbox rows")

		sub := f.subscribe(t, tx)
		with := update("completed")
		assert.Equal(t, int64(rows), count(), "an enabled subscription records every phase event")
		t.Logf("3000-row job status UPDATE: %v with no subscription (skip), %v with a subscription (insert)", without, with)

		// A disabled subscription is the same as none.
		require.NoError(t, tx.Exec(`UPDATE event_subscriptions SET enabled = false WHERE subscription_id = ?`, sub).Error)
		update("failed")
		assert.Equal(t, int64(rows), count())

		// Undispatched rows older than the retention go, also with no
		// dispatcher; newer rows stay.
		require.NoError(t, tx.Exec(`UPDATE event_outbox SET occurred_at = occurred_at - interval '30 days'
WHERE outbox_id IN (SELECT outbox_id FROM event_outbox WHERE org_id = ? ORDER BY outbox_id LIMIT 1000)`, f.orgID).Error)
		outboundevents.PruneOutbox(ctx, postgres_store.PostgresStore, time.Now().UTC().Add(-14*24*time.Hour))
		assert.Equal(t, int64(rows-1000), count())
	})
}

// The dispatcher reads rows in occurrence order and waits for the lag: a
// row of a transaction that started earlier (smaller occurred_at) is
// delivered first, also when its outbox_id is larger.
func TestOutboxDispatchOrderAndLag(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		events := store.OutboundEventStore(postgres_store.PostgresStore)
		f := newOutboundFixture(t, tx)
		sub := f.subscribe(t, tx)
		wf := f.insertWorkflow(t, tx, "sec-order")
		job := f.insertJob(t, tx, &wf, "scheduling")
		require.NoError(t, tx.Exec(`UPDATE jobs SET status = 'running' WHERE job_id = ?`, job).Error)
		var ids []int64
		require.NoError(t, tx.Raw(`SELECT outbox_id FROM event_outbox WHERE job_id = ? ORDER BY outbox_id`, job).Scan(&ids).Error)
		require.Len(t, ids, 2)
		// The later row (started) occurred first.
		require.NoError(t, tx.Exec(`UPDATE event_outbox SET occurred_at = occurred_at - interval '1 minute' WHERE outbox_id = ?`, ids[1]).Error)

		clock := time.Now().UTC()
		dispatcher := &outboundevents.Dispatcher{Store: events, Queue: newFakeTaskQueue(), Now: func() time.Time { return clock }}
		var txStart time.Time
		require.NoError(t, tx.Raw(`SELECT now()`).Scan(&txStart).Error)
		// Inside the lag only the row that occurred a minute earlier is read.
		clock = txStart.Add(time.Second)
		require.NoError(t, dispatcher.FanOut(ctx))
		var dispatched []int64
		require.NoError(t, tx.Raw(`SELECT outbox_id FROM event_outbox WHERE job_id = ? AND dispatched_at IS NOT NULL`, job).Scan(&dispatched).Error)
		assert.Equal(t, []int64{ids[1]}, dispatched)
		// After the lag the other row follows, with the next sequence.
		clock = txStart.Add(outboundevents.DefaultOutboxLag + time.Second)
		require.NoError(t, dispatcher.FanOut(ctx))
		var sequences []struct {
			OutboxID int64
			Sequence int64
		}
		require.NoError(t, tx.Raw(`SELECT outbox_id, sequence FROM event_outbox WHERE job_id = ? ORDER BY sequence`, job).Scan(&sequences).Error)
		require.Len(t, sequences, 2)
		assert.Equal(t, ids[1], sequences[0].OutboxID, "the row that occurred first gets the first sequence")
		assert.Equal(t, ids[0], sequences[1].OutboxID)
		_ = sub
	})
}
