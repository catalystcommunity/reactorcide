-- +goose Up
-- Update default allowed_event_types to use generic (VCS-agnostic) event type names.
-- Old default: ARRAY['push', 'pull_request']
-- New default: ARRAY['push', 'pull_request_opened', 'pull_request_updated', 'tag_created']
ALTER TABLE projects ALTER COLUMN allowed_event_types SET DEFAULT ARRAY['push', 'pull_request_opened', 'pull_request_updated', 'tag_created'];

-- Migrate existing rows that still have the old default values.
-- Only update rows whose allowed_event_types exactly match the old default so we don't
-- clobber any custom configuration.
UPDATE projects
SET allowed_event_types = ARRAY['push', 'pull_request_opened', 'pull_request_updated', 'tag_created'],
    updated_at = timezone('utc', now())
WHERE allowed_event_types = ARRAY['push', 'pull_request'];

-- +goose Down
ALTER TABLE projects ALTER COLUMN allowed_event_types SET DEFAULT ARRAY['push', 'pull_request'];

UPDATE projects
SET allowed_event_types = ARRAY['push', 'pull_request'],
    updated_at = timezone('utc', now())
WHERE allowed_event_types = ARRAY['push', 'pull_request_opened', 'pull_request_updated', 'tag_created'];
