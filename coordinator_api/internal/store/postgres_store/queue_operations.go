package postgres_store

import (
	"context"
	"errors"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// pgUniqueViolationCode is the Postgres SQLSTATE for a unique constraint
// violation (23505). Detected directly from *pgconn.PgError rather than
// relying on GORM's error translation (gorm.Config.TranslateError is not
// enabled for this store's connection -- see postgres_store.go's
// gorm.Open), so this is the one place that has to know the raw code.
const pgUniqueViolationCode = "23505"

// isUniqueViolation reports whether err is a Postgres unique constraint
// violation (SQLSTATE 23505), unwrapping through GORM/pgx's error chain.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolationCode
}

// GetQueueByID retrieves a queue by its row primary key (queue_id, a ULID
// -- distinct from QueueUUID, the Corndogs routing identifier).
func (ps PostgresDbStore) GetQueueByID(ctx context.Context, queueID string) (*models.Queue, error) {
	if !isValidUUID(queueID) {
		return nil, store.ErrNotFound
	}

	var q models.Queue
	if err := ps.getDB(ctx).Where("queue_id = ?", queueID).First(&q).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get queue %s: %w", queueID, err)
	}
	return &q, nil
}

// GetQueueByUUID retrieves a queue by its QueueUUID -- the literal Corndogs
// queue name. Used at pull time (worker matching) and by admin surfaces
// that reference a queue by its routing identifier.
func (ps PostgresDbStore) GetQueueByUUID(ctx context.Context, queueUUID string) (*models.Queue, error) {
	if !isValidUUID(queueUUID) {
		return nil, store.ErrNotFound
	}

	var q models.Queue
	if err := ps.getDB(ctx).Where("queue_uuid = ?", queueUUID).First(&q).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get queue by uuid %s: %w", queueUUID, err)
	}
	return &q, nil
}

// getQueueByHash retrieves a queue by its characteristics_hash. Unexported:
// callers outside this file only ever want find-or-create-by-characteristics
// (FindOrCreateQueueByCharacteristics) rather than an already-computed hash.
func (ps PostgresDbStore) getQueueByHash(ctx context.Context, hash string) (*models.Queue, error) {
	var q models.Queue
	if err := ps.getDB(ctx).Where("characteristics_hash = ?", hash).First(&q).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get queue by hash: %w", err)
	}
	return &q, nil
}

// ListQueues retrieves queues with pagination, newest first. The queue
// table is small (operator/coordinator-created, not per-job -- see
// WORKERS_PLAN.md "Matching at pull"), so this is mainly for admin listing.
func (ps PostgresDbStore) ListQueues(ctx context.Context, limit, offset int) ([]models.Queue, error) {
	var queues []models.Queue
	query := ps.getDB(ctx).Order("created_at DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&queues).Error; err != nil {
		return nil, fmt.Errorf("failed to list queues: %w", err)
	}
	return queues, nil
}

// FindOrCreateQueueByCharacteristics resolves the queue for a job's
// (already-defaulted) characteristics: canonicalize -> hash -> look up by
// hash; on a miss, create a new queue row with a fresh QueueUUID. This is
// the submit path's routing primitive (WORKERS_PLAN.md "Find-or-create at
// submit") -- callers set job.QueueName to the returned Queue.QueueUUID
// before submitting to Corndogs.
//
// Concurrent-insert race: characteristics_hash is UNIQUE, so two submits
// racing to create the same never-before-seen queue will have one INSERT
// succeed and the other fail with a unique violation. On that failure this
// re-selects by hash rather than erroring -- the row now exists (created by
// the winner), so the loser simply returns it, exactly as if its own SELECT
// had found it first.
func (ps PostgresDbStore) FindOrCreateQueueByCharacteristics(ctx context.Context, chars characteristics.Characteristics) (*models.Queue, error) {
	hash := characteristics.Hash(chars)

	existing, err := ps.getQueueByHash(ctx, hash)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	q := &models.Queue{
		QueueUUID:           uuid.NewString(),
		Characteristics:     chars,
		CharacteristicsHash: hash,
	}
	if err := ps.getDB(ctx).Create(q).Error; err != nil {
		if isUniqueViolation(err) {
			// Lost the race to a concurrent submit that created this exact
			// queue between our SELECT and our INSERT. Re-select rather
			// than surfacing a spurious error -- the desired end state
			// (exactly one queue row for these characteristics) holds
			// either way.
			winner, selErr := ps.getQueueByHash(ctx, hash)
			if selErr != nil {
				return nil, fmt.Errorf("queue create lost race but re-select by hash failed: %w", selErr)
			}
			return winner, nil
		}
		return nil, fmt.Errorf("failed to create queue: %w", err)
	}
	return q, nil
}

// CreateQueue lets an operator pre-create a queue (define characteristics +
// display name up front, before any job needs it) -- WORKERS_PLAN.md
// "Operator control (admin UI)". Unlike FindOrCreateQueueByCharacteristics,
// a hash collision here is an error (characteristics are immutable and a
// queue for this exact set already exists), not silently resolved to the
// existing row.
func (ps PostgresDbStore) CreateQueue(ctx context.Context, chars characteristics.Characteristics, displayName string) (*models.Queue, error) {
	hash := characteristics.Hash(chars)

	q := &models.Queue{
		QueueUUID:           uuid.NewString(),
		Characteristics:     chars,
		CharacteristicsHash: hash,
		DisplayName:         displayName,
	}
	if err := ps.getDB(ctx).Create(q).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("a queue with these characteristics already exists: %w", store.ErrAlreadyExists)
		}
		return nil, fmt.Errorf("failed to create queue: %w", err)
	}
	return q, nil
}

// UpdateQueueDisplayName updates a queue's mutable display name.
// Characteristics are immutable once created (WORKERS_PLAN.md "Operator
// control") and have no update path.
func (ps PostgresDbStore) UpdateQueueDisplayName(ctx context.Context, queueID, displayName string) error {
	if !isValidUUID(queueID) {
		return store.ErrNotFound
	}

	result := ps.getDB(ctx).Model(&models.Queue{}).
		Where("queue_id = ?", queueID).
		Updates(map[string]interface{}{
			"display_name": displayName,
			"updated_at":   gorm.Expr("timezone('utc', now())"),
		})
	if result.Error != nil {
		return fmt.Errorf("failed to update queue %s display name: %w", queueID, result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// DefaultQueueDisplayName is the display name stamped on the seeded
// {"os":"linux"} queue the first time EnsureDefaultQueue creates it.
const DefaultQueueDisplayName = "Default (linux)"

// EnsureDefaultQueue finds or creates the default {"os":"linux"} queue,
// mirroring EnsureDefaultUser's idempotent-seed shape. It deliberately does
// NOT hardcode a SQL hash literal (see migration 000020_queues.sql's
// comment): the characteristics come from
// characteristics.ParseJobCharacteristics(map[string]any{}), the exact same
// function every submit path uses to default an empty/omitted
// `characteristics` block, so the seeded row's hash can never disagree with
// a hash computed for a real bare job at submit time. Safe to call on every
// startup (existing row -> no-op); safe under concurrent coordinator pods
// starting simultaneously (unique characteristics_hash resolves the race
// the same way FindOrCreateQueueByCharacteristics does).
func (ps PostgresDbStore) EnsureDefaultQueue(ctx context.Context) error {
	chars, err := characteristics.ParseJobCharacteristics(map[string]any{})
	if err != nil {
		return fmt.Errorf("failed to build default queue characteristics: %w", err)
	}
	hash := characteristics.Hash(chars)

	if _, err := ps.getQueueByHash(ctx, hash); err == nil {
		// Already seeded (by this process on a previous startup, or by
		// another coordinator pod) -- nothing to do.
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("failed to check for default queue: %w", err)
	}

	q := &models.Queue{
		QueueUUID:           uuid.NewString(),
		Characteristics:     chars,
		CharacteristicsHash: hash,
		DisplayName:         DefaultQueueDisplayName,
		IsDefault:           true,
	}
	if err := ps.getDB(ctx).Create(q).Error; err != nil {
		if isUniqueViolation(err) {
			// Another coordinator pod won the race to seed it first; the
			// desired end state (exactly one default queue row) holds
			// either way.
			return nil
		}
		return fmt.Errorf("failed to create default queue: %w", err)
	}
	return nil
}

// nonTerminalJobStatuses are every status a job can be cancelled from
// (WORKERS_PLAN.md Wave-4 P4's delete-queue cancel-in-flight admin op):
// submitted/queued/running (not yet claimed or currently executing) plus
// cancelling (a cancel already in flight but not yet confirmed) — mirrors
// models.Job.CanBeCancelled/IsCancelling/IsCompleted's status set rather
// than redefining it, so this list can't silently drift from the
// cancel/kill state machine those methods encode.
var nonTerminalJobStatuses = []string{"submitted", "queued", "running", "cancelling"}

// ListNonTerminalJobsByQueue lists every job routed to queueUUID (the
// Corndogs queue name, i.e. models.Job.QueueName) that is not yet in a
// terminal state -- the set delete-queue's admin op must cancel before
// removing the queue row (WORKERS_PLAN.md "Operator control": "Deleting a
// queue cancels its in-flight corndogs tasks"). Ordered oldest first purely
// for deterministic test/admin-log output; delete-queue processes the whole
// set regardless of order.
func (ps PostgresDbStore) ListNonTerminalJobsByQueue(ctx context.Context, queueUUID string) ([]models.Job, error) {
	var jobs []models.Job
	if err := ps.getDB(ctx).
		Where("queue_name = ? AND status IN ?", queueUUID, nonTerminalJobStatuses).
		Order("created_at ASC").
		Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("failed to list non-terminal jobs for queue %s: %w", queueUUID, err)
	}
	return jobs, nil
}

// DeleteQueue deletes a queue row only. It does NOT cancel or otherwise
// touch any in-flight Corndogs tasks/jobs routed to this queue's UUID --
// per WORKERS_PLAN.md "Operator control", deleting a queue is supposed to
// cancel its in-flight tasks, but that cancel-in-flight orchestration
// (finding affected jobs, driving them through the existing cancel path)
// belongs to the P4 admin operation that calls this, not to the row-level
// store primitive. Callers that need the full delete-cancels-in-flight
// behavior must do that orchestration themselves before/around calling
// this.
func (ps PostgresDbStore) DeleteQueue(ctx context.Context, queueID string) error {
	if !isValidUUID(queueID) {
		return store.ErrNotFound
	}

	result := ps.getDB(ctx).Where("queue_id = ?", queueID).Delete(&models.Queue{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete queue %s: %w", queueID, result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}
