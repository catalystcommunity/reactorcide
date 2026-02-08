-- +goose Up
-- Add event metadata and parent job tracking to support eval job → triggered job relationships.
ALTER TABLE jobs ADD COLUMN event_metadata jsonb;
ALTER TABLE jobs ADD COLUMN parent_job_id uuid REFERENCES jobs(job_id) ON DELETE SET NULL;
CREATE INDEX jobs_parent_job_id_idx ON jobs(parent_job_id);

-- +goose Down
DROP INDEX IF EXISTS jobs_parent_job_id_idx;
ALTER TABLE jobs DROP COLUMN IF EXISTS parent_job_id;
ALTER TABLE jobs DROP COLUMN IF EXISTS event_metadata;
