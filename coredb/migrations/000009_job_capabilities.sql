-- +goose Up
-- Add capabilities field to jobs for runtime requirements (e.g., docker, gpu).
ALTER TABLE jobs ADD COLUMN capabilities text[];

-- +goose Down
ALTER TABLE jobs DROP COLUMN IF EXISTS capabilities;
