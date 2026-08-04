-- +goose Up
ALTER TABLE worker_leases
    ADD COLUMN last_heartbeat_at timestamp;

UPDATE worker_leases
SET last_heartbeat_at = acquired_at
WHERE last_heartbeat_at IS NULL;

ALTER TABLE worker_leases
    ALTER COLUMN last_heartbeat_at SET DEFAULT timezone('utc', now()),
    ALTER COLUMN last_heartbeat_at SET NOT NULL;

DROP INDEX IF EXISTS worker_leases_active_acquired_at_idx;
CREATE INDEX worker_leases_active_heartbeat_idx
    ON worker_leases(last_heartbeat_at)
    WHERE released_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS worker_leases_active_heartbeat_idx;
ALTER TABLE worker_leases DROP COLUMN IF EXISTS last_heartbeat_at;
CREATE INDEX worker_leases_active_acquired_at_idx
    ON worker_leases(acquired_at)
    WHERE released_at IS NULL;
