-- +goose Up
-- Add per-project VCS token secret reference for multi-org deployments.
-- Stores a "path:key" reference into the secrets store.
ALTER TABLE projects ADD COLUMN vcs_token_secret text;

-- +goose Down
ALTER TABLE projects DROP COLUMN IF EXISTS vcs_token_secret;
