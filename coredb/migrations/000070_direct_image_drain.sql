-- +goose Up
-- Drain of direct pinned VM references (docs/image-aliases.md "Rollout of a
-- new pin").
--
-- A VM job can name a pinned reference directly (no image_alias). The job
-- has the characteristic image_digest. The image set of each worker that
-- could run the job keeps that digest until the job ends, also after the
-- alias that pinned the digest changes or is deleted. This partial index
-- holds only such jobs, so the drain query at each image set computation
-- reads a small index. The status list must agree with
-- jobs_image_alias_active_idx (migration 000069).
CREATE INDEX jobs_image_digest_direct_active_idx ON jobs (org_id)
    WHERE image_alias IS NULL
      AND (characteristics -> 'image_digest') IS NOT NULL
      AND status IN ('submitted', 'queued', 'scheduling', 'running', 'cancelling');

-- +goose Down
DROP INDEX IF EXISTS jobs_image_digest_direct_active_idx;
