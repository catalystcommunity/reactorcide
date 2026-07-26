package test

// Postgres-backed integration coverage for queue routing (WORKERS_PLAN.md
// "Queues"): postgres_store/queue_operations.go's FindOrCreateQueueByCharacteristics
// and EnsureDefaultQueue against a real database, plus the end-to-end submit
// path (internal/handlers/job_handler.go's CreateJob) resolving a job's
// characteristics to a queue UUID and submitting the Corndogs task there.
//
// Like visibility_operations_test.go and ui_auth_integration_test.go, these
// writes are NOT wrapped in a rollback-able transaction (RunTransactionalTest):
// the concurrent-insert-race test in particular needs genuinely separate
// connections racing against the real unique constraint on
// characteristics_hash, which a single shared transaction can't exercise
// (statements within one transaction see each other's uncommitted writes, so
// a "concurrent" insert under one tx would just find the first goroutine's
// row instead of hitting the unique-violation-and-reselect path). Every
// characteristics set used here is unique-per-test-run (see
// uniqueTestCharacteristics) so these tests never collide with each other,
// with a previous run against the same long-lived test container, or with
// the real seeded {"os":"linux"} default queue.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

// queueStore is the store surface these tests exercise directly (bypassing
// HTTP), mirroring the narrow queueResolvingStore interfaces declared
// consumer-side in internal/handlers/job_handler.go,
// internal/worker/trigger_processor.go, and internal/jobcontrol/retry.go.
// postgres_store.PostgresStore satisfies this plus the rest of
// postgres_store/queue_operations.go's exported surface directly (it's a
// concrete struct, not just store.Store), so tests call it unqualified.
type queueStore interface {
	FindOrCreateQueueByCharacteristics(ctx context.Context, chars characteristics.Characteristics) (*models.Queue, error)
	EnsureDefaultQueue(ctx context.Context) error
}

var _ queueStore = postgres_store.PostgresStore

// uniqueTestCharacteristics returns a Characteristics set with a
// test-unique custom key, so find-or-create always starts from a
// never-before-seen queue for this test.
func uniqueTestCharacteristics(t *testing.T, os string) characteristics.Characteristics {
	t.Helper()
	chars, err := characteristics.ParseJobCharacteristics(map[string]any{
		"os":             os,
		"test_run_nonce": uniqueName("queue-test"),
	})
	require.NoError(t, err)
	return chars
}

// TestFindOrCreateQueueByCharacteristics_Idempotent verifies that resolving
// the same characteristics twice returns the identical queue row (same
// QueueID and QueueUUID) rather than creating a second one.
func TestFindOrCreateQueueByCharacteristics_Idempotent(t *testing.T) {
	ctx := context.Background()
	chars := uniqueTestCharacteristics(t, "windows")

	first, err := postgres_store.PostgresStore.FindOrCreateQueueByCharacteristics(ctx, chars)
	require.NoError(t, err)
	require.NotEmpty(t, first.QueueUUID)

	second, err := postgres_store.PostgresStore.FindOrCreateQueueByCharacteristics(ctx, chars)
	require.NoError(t, err)

	assert.Equal(t, first.QueueID, second.QueueID)
	assert.Equal(t, first.QueueUUID, second.QueueUUID)

	// Exactly one row exists for this hash.
	var count int64
	hash := characteristics.Hash(chars)
	require.NoError(t, postgres_store.PostgresStore.GetDB().
		Model(&models.Queue{}).Where("characteristics_hash = ?", hash).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// TestFindOrCreateQueueByCharacteristics_ConcurrentRace verifies that many
// goroutines racing to resolve the SAME never-before-seen characteristics
// converge on exactly one queue row: the loser(s) of the real
// characteristics_hash unique-constraint race re-select and return the
// winner's row rather than erroring (see
// postgres_store.FindOrCreateQueueByCharacteristics's race-handling
// comment).
func TestFindOrCreateQueueByCharacteristics_ConcurrentRace(t *testing.T) {
	ctx := context.Background()
	chars := uniqueTestCharacteristics(t, "macos")

	const n = 20
	var wg sync.WaitGroup
	results := make([]*models.Queue, n)
	errs := make([]error, n)
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // line every goroutine up before any of them call the DB
			q, err := postgres_store.PostgresStore.FindOrCreateQueueByCharacteristics(ctx, chars)
			results[i] = q
			errs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	var wantUUID string
	for i := 0; i < n; i++ {
		require.NoErrorf(t, errs[i], "goroutine %d", i)
		require.NotNilf(t, results[i], "goroutine %d", i)
		if i == 0 {
			wantUUID = results[i].QueueUUID
			continue
		}
		assert.Equalf(t, wantUUID, results[i].QueueUUID, "goroutine %d resolved a different queue UUID", i)
	}

	var count int64
	hash := characteristics.Hash(chars)
	require.NoError(t, postgres_store.PostgresStore.GetDB().
		Model(&models.Queue{}).Where("characteristics_hash = ?", hash).Count(&count).Error)
	assert.Equal(t, int64(1), count, "expected exactly one queue row despite %d concurrent find-or-create calls", n)
}

// TestEnsureDefaultQueue_Idempotent verifies EnsureDefaultQueue seeds
// exactly one {"os":"linux"} queue marked is_default, and calling it again
// (simulating a coordinator restart, or a second pod starting concurrently)
// is a no-op that resolves to the same row.
func TestEnsureDefaultQueue_Idempotent(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, postgres_store.PostgresStore.EnsureDefaultQueue(ctx))
	require.NoError(t, postgres_store.PostgresStore.EnsureDefaultQueue(ctx))
	require.NoError(t, postgres_store.PostgresStore.EnsureDefaultQueue(ctx))

	wantChars, err := characteristics.ParseJobCharacteristics(nil)
	require.NoError(t, err)
	require.Equal(t, "linux", string(wantChars["os"].(characteristics.StringValue)))

	var queues []models.Queue
	hash := characteristics.Hash(wantChars)
	require.NoError(t, postgres_store.PostgresStore.GetDB().
		Where("characteristics_hash = ?", hash).Find(&queues).Error)

	require.Len(t, queues, 1, "expected exactly one default queue row after 3 EnsureDefaultQueue calls")
	assert.True(t, queues[0].IsDefault)
	assert.Equal(t, postgres_store.DefaultQueueDisplayName, queues[0].DisplayName)

	// A direct find-or-create for the same (defaulted) characteristics
	// resolves to the identically same row -- the seeded hash and the
	// runtime-computed hash for a bare job never disagree.
	resolved, err := postgres_store.PostgresStore.FindOrCreateQueueByCharacteristics(ctx, wantChars)
	require.NoError(t, err)
	assert.Equal(t, queues[0].QueueUUID, resolved.QueueUUID)
}

// createTestJobHandler builds a JobHandler wired to the real Postgres store
// (so its queueResolvingStore type assertion succeeds against the genuine
// postgres_store/queue_operations.go implementation, not a test mock) and a
// mock Corndogs client, for exercising the actual submit-path wiring
// end-to-end without going through the HTTP mux/router singleton.
func createTestJobHandler() (*handlers.JobHandler, *corndogs.MockClient) {
	mockCorndogs := corndogs.NewMockClient()
	return handlers.NewJobHandler(store.AppStore, mockCorndogs), mockCorndogs
}

// submitTestJob POSTs a CreateJobRequest-shaped body directly to
// JobHandler.CreateJob (bypassing HTTP routing/auth middleware, like
// job_handler_test.go's unit tests do) as userID, carrying ctx (which, in
// these tests, wraps the RunTransactionalTest transaction -- see the two
// tests below) so the job row created by the real store is rolled back with
// everything else and never leaks into other tests sharing this container.
func submitTestJob(t *testing.T, ctx context.Context, h *handlers.JobHandler, userID string, reqBody map[string]interface{}) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	body, err := json.Marshal(reqBody)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/jobs", bytes.NewReader(body))
	require.NoError(t, err)
	user := &models.User{UserID: userID}
	req = req.WithContext(checkauth.SetUserContext(req.Context(), user))

	w := httptest.NewRecorder()
	h.CreateJob(w, req)

	var resp map[string]interface{}
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	}
	return w, resp
}

// TestCreateJob_BareCharacteristics_ResolvesDefaultQueueAndSubmits verifies
// submitting a job with no `characteristics` block resolves to the seeded
// default {"os":"linux"} queue and that the Corndogs task is submitted to
// that queue's UUID -- WORKERS_PLAN.md "Default queue" / "Find-or-create at
// submit". Wrapped in RunTransactionalTest (like jobs_test.go's own
// submit-path tests) so the job/user rows it creates roll back instead of
// leaking into other tests' unscoped ListJobs queries.
func TestCreateJob_BareCharacteristics_ResolvesDefaultQueueAndSubmits(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		require.NoError(t, postgres_store.PostgresStore.EnsureDefaultQueue(ctx))

		defaultChars, err := characteristics.ParseJobCharacteristics(nil)
		require.NoError(t, err)
		defaultQueue, err := postgres_store.PostgresStore.FindOrCreateQueueByCharacteristics(ctx, defaultChars)
		require.NoError(t, err)

		dataUtils := &DataUtils{db: tx}
		user, err := dataUtils.CreateUser(DataSetup{})
		require.NoError(t, err)

		h, mockCorndogs := createTestJobHandler()

		w, resp := submitTestJob(t, ctx, h, user.UserID, map[string]interface{}{
			"name":        "bare job " + uniqueName("job"),
			"job_command": "echo hi",
			"source_type": "copy",
			"source_path": "./",
		})

		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		assert.Equal(t, defaultQueue.QueueUUID, resp["queue_name"],
			"expected a bare job's queue_name to be the default queue's UUID")

		require.Len(t, mockCorndogs.SubmitTaskToQueueCalls, 1)
		assert.Equal(t, defaultQueue.QueueUUID, mockCorndogs.SubmitTaskToQueueCalls[0].Queue,
			"expected the Corndogs task to be submitted to the default queue's UUID")
	})
}

// TestCreateJob_WindowsCharacteristics_CreatesNewQueueAndSubmits verifies
// submitting a job with an {"os":"windows"} characteristics block creates a
// brand-new queue row (distinct from the default) and that the Corndogs
// task is submitted to ITS uuid. Wrapped in RunTransactionalTest for the
// same leak-avoidance reason as the bare-job test above.
func TestCreateJob_WindowsCharacteristics_CreatesNewQueueAndSubmits(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		nonce := uniqueName("windows-submit")
		wantChars, err := characteristics.ParseJobCharacteristics(map[string]any{"os": "windows", "test_run_nonce": nonce})
		require.NoError(t, err)

		// Sanity: this exact characteristics set (with its unique nonce) has
		// never been resolved before, so the handler's find-or-create call
		// is guaranteed to create a fresh row, not find a pre-existing one.
		// Queried through tx (not PostgresStore.GetDB(), which is the
		// global connection and can't see this transaction's uncommitted
		// writes) so pre/post counts are consistent with what CreateJob
		// itself sees via ctx.
		var preCount int64
		require.NoError(t, tx.
			Model(&models.Queue{}).Where("characteristics_hash = ?", characteristics.Hash(wantChars)).Count(&preCount).Error)
		require.Zero(t, preCount)

		dataUtils := &DataUtils{db: tx}
		user, err := dataUtils.CreateUser(DataSetup{})
		require.NoError(t, err)

		h, mockCorndogs := createTestJobHandler()

		w, resp := submitTestJob(t, ctx, h, user.UserID, map[string]interface{}{
			"name":            "windows job " + uniqueName("job"),
			"job_command":     "echo hi",
			"source_type":     "copy",
			"source_path":     "./",
			"characteristics": map[string]interface{}{"os": "windows", "test_run_nonce": nonce},
		})

		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

		newQueue, err := postgres_store.PostgresStore.FindOrCreateQueueByCharacteristics(ctx, wantChars)
		require.NoError(t, err)

		queueName, _ := resp["queue_name"].(string)
		assert.Equal(t, newQueue.QueueUUID, queueName)

		require.Len(t, mockCorndogs.SubmitTaskToQueueCalls, 1)
		assert.Equal(t, newQueue.QueueUUID, mockCorndogs.SubmitTaskToQueueCalls[0].Queue,
			"expected the Corndogs task to be submitted to the new queue's UUID")

		var postCount int64
		require.NoError(t, tx.
			Model(&models.Queue{}).Where("characteristics_hash = ?", characteristics.Hash(wantChars)).Count(&postCount).Error)
		assert.Equal(t, int64(1), postCount)
	})
}
