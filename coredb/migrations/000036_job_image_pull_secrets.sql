-- +goose Up
-- Kubernetes Secret NAMES a job may use to pull its image. Names only,
-- never credential values. The worker enforces its operator allowlist
-- before it creates a Kubernetes Job.
ALTER TABLE jobs ADD COLUMN image_pull_secrets text[];

-- +goose Down
ALTER TABLE jobs DROP COLUMN image_pull_secrets;
