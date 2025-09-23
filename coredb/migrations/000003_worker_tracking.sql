-- +goose Up
-- Add worker tracking and notes fields to jobs table for worker lifecycle management
ALTER TABLE jobs ADD COLUMN worker_id text; -- ID of the worker processing this job
ALTER TABLE jobs ADD COLUMN notes text; -- Notes for job recovery, errors, etc.

-- Add retry tracking fields
ALTER TABLE jobs ADD COLUMN retry_count integer DEFAULT 0; -- Number of retry attempts
ALTER TABLE jobs ADD COLUMN last_error text; -- Last error message from execution

-- Add indexes for efficient worker queries
CREATE INDEX jobs_worker_id_idx ON jobs(worker_id);
CREATE INDEX jobs_status_worker_id_idx ON jobs(status, worker_id);

-- +goose Down
-- Remove indexes
DROP INDEX IF EXISTS jobs_status_worker_id_idx;
DROP INDEX IF EXISTS jobs_worker_id_idx;

-- Remove columns
ALTER TABLE jobs DROP COLUMN IF EXISTS last_error;
ALTER TABLE jobs DROP COLUMN IF EXISTS retry_count;
ALTER TABLE jobs DROP COLUMN IF EXISTS notes;
ALTER TABLE jobs DROP COLUMN IF EXISTS worker_id;