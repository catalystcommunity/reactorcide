-- +goose Up
ALTER TABLE jobs
ADD COLUMN run_as_user TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE jobs
DROP COLUMN IF EXISTS run_as_user;
