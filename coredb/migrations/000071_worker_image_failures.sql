-- +goose Up
-- Per-image pull failures of a worker image set report
-- (docs/image-aliases.md "Worker image states"). A worker pulls each image
-- independently and reports each image that failed: its digest, whether
-- the failure is permanent, and the reason. A list of
-- {"digest", "permanent", "detail"} objects, or NULL when the newest report
-- has no failures (or the worker is older and sends no list).
ALTER TABLE workers ADD COLUMN image_failed jsonb;

-- +goose Down
ALTER TABLE workers DROP COLUMN IF EXISTS image_failed;
