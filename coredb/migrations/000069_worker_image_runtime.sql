-- +goose Up
-- Worker image sets (docs/image-aliases.md, VM_IMAGES_PLAN.md "Worker image
-- sets").
--
-- image_runtime is the job runner backend that the worker sent at Register
-- (WorkerInfo.runtime): vm, docker, containerd, or kubernetes. The
-- coordinator uses it to select the alias kind of the image set (vm or
-- container). It is NULL for an older worker; such a worker gets no image
-- set.
ALTER TABLE workers
    ADD COLUMN image_runtime text,
    ADD CONSTRAINT workers_image_runtime_check CHECK (image_runtime IS NULL OR image_runtime IN ('vm', 'docker', 'containerd', 'kubernetes'));

-- The image set of a worker keeps each older digest of an alias while a job
-- that is not terminal uses it (rollout drain). This partial index holds
-- only such jobs, so the drain query at each image set computation reads a
-- small index.
CREATE INDEX jobs_image_alias_active_idx ON jobs (org_id, image_alias)
    WHERE image_alias IS NOT NULL AND status IN ('submitted', 'queued', 'scheduling', 'running', 'cancelling');

-- +goose Down
DROP INDEX IF EXISTS jobs_image_alias_active_idx;
ALTER TABLE workers
    DROP CONSTRAINT workers_image_runtime_check,
    DROP COLUMN image_runtime;
