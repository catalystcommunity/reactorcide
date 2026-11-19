-- +goose Up
-- Resource-aware admission and backlog signals (docs/workers.md).
--
-- workers.admission holds the newest admission report of a worker in
-- "resources" mode: mode, capacity, headroom, and the report time. It is
-- NULL for a worker in the default "concurrency" mode, so every existing
-- row keeps its current meaning. No backfill is necessary.
ALTER TABLE workers ADD COLUMN admission jsonb;

-- The waiting jobs of each queue in Corndogs order (highest priority, then
-- oldest). The resources-mode claim reads the head job of each candidate
-- queue, and the backlog signal reads every waiting job. The partial index
-- holds only jobs that wait for a worker, so it stays small.
CREATE INDEX jobs_waiting_queue_idx ON jobs (queue_name, priority DESC, created_at)
    WHERE status IN ('submitted', 'queued');

-- +goose Down
DROP INDEX IF EXISTS jobs_waiting_queue_idx;
ALTER TABLE workers DROP COLUMN IF EXISTS admission;
