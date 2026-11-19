-- +goose Up
-- Retry and rerun after an alias update (docs/image-aliases.md "Retry and
-- rerun"). A retry or a rerun resolves the image again through an alias and
-- uses the current active pin of that alias. The old digest leaves the
-- worker image sets after the drain, so a job that keeps it cannot run.
--
-- image_pin_alias is the name of the alias that pinned the digest of a job
-- that named a pinned reference directly (image_alias is NULL for that
-- job). A retry of that job resolves through this alias. NULL for a job
-- that named an alias, a container job with a plain reference, and a job
-- created before this migration (a retry then looks for an alias that pins
-- the digest now).
--
-- image_retry_of is the image of the earlier run, in the display form
-- ("alias (ref)" or "ref"), when a retry or a rerun got another image than
-- the earlier run. NULL when the image did not change.
ALTER TABLE jobs
    ADD COLUMN image_pin_alias text,
    ADD COLUMN image_retry_of text;

-- The image_pin_alias of the job of the earlier run, which a workflow retry
-- or re-run copies to its new node (with carried_image_*).
ALTER TABLE workflow_nodes
    ADD COLUMN carried_image_pin_alias text;

-- All new columns are nullable. Older builds do not read or write them.

-- +goose Down
ALTER TABLE workflow_nodes DROP COLUMN IF EXISTS carried_image_pin_alias;
ALTER TABLE jobs
    DROP COLUMN IF EXISTS image_retry_of,
    DROP COLUMN IF EXISTS image_pin_alias;
