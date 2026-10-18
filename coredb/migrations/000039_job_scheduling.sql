-- +goose Up
ALTER TABLE jobs DROP CONSTRAINT jobs_status_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_status_check CHECK (status IN (
    'submitted', 'queued', 'scheduling', 'running', 'cancelling',
    'completed', 'failed', 'cancelled', 'timeout'
));
ALTER TABLE jobs ADD COLUMN scheduling_started_at timestamp with time zone;

-- +goose Down
-- Restore the old claim-time behavior before removing the scheduling state.
UPDATE jobs SET status = 'running', started_at = COALESCE(started_at, scheduling_started_at)
    WHERE status = 'scheduling';
ALTER TABLE jobs DROP COLUMN scheduling_started_at;
ALTER TABLE jobs DROP CONSTRAINT jobs_status_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_status_check CHECK (status IN (
    'submitted', 'queued', 'running', 'cancelling',
    'completed', 'failed', 'cancelled', 'timeout'
));
