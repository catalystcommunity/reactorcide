-- +goose Up
-- Add per-project webhook signing secret reference for multi-tenant webhook validation.
-- Stores a "path:key" reference into the secrets store.
ALTER TABLE projects ADD COLUMN webhook_secret text;

-- +goose Down
ALTER TABLE projects DROP COLUMN IF EXISTS webhook_secret;
