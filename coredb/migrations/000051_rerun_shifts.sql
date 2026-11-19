-- +goose Up
-- Re-run with shifts (owner decisions D5, D18, D25). A retry or a re-run can
-- change only the CPU, the memory, the ephemeral storage and the timeout of a
-- job. The new run keeps the authority of the original run and records its
-- shifts. Existing rows get no shifts and no storage request or limit, which
-- is the current behavior.

-- Ephemeral (scratch) storage of the job container. Empty is "not set".
ALTER TABLE jobs ADD COLUMN resource_storage_request text NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN resource_storage_limit text NOT NULL DEFAULT '';

-- The shifts of a retry or a re-run. NULL is "no shifts".
ALTER TABLE jobs ADD COLUMN shifts jsonb;
ALTER TABLE workflow_instances ADD COLUMN shifts jsonb;

-- The largest values a shift can set. NULL on an organization row is "no
-- limit beyond the worker class, the queue and the execution profile". NULL
-- on a project row inherits the organization value.
ALTER TABLE ci_trust_settings ADD COLUMN rerun_shift_limits jsonb;

-- A retry or a re-run of a workflow is a new instance of the same CI event.
-- It records the first instance of its chain in rerun_of_workflow_id. The
-- "Reactorcide Checks" aggregate counts only the newest instance of each
-- chain.
--
-- A root re-run instance keeps its event in rerun_event_id and has a NULL
-- event_id. Thus workflow_instances_event_security_idx (migration 000041)
-- stays EXACTLY as it is: one root instance for each (event, security ID).
-- Do not change that index. Release v0.12.13 and older infer it in
-- "ON CONFLICT (event_id, workflow_security_id) WHERE event_id IS NOT NULL
-- AND parent_workflow_id IS NULL". A changed predicate stops that inference
-- during a rolling deploy and after a rollback. The CI event of a workflow
-- is coalesce(event_id, rerun_event_id). A child workflow keeps event_id.
ALTER TABLE workflow_instances ADD COLUMN rerun_of_workflow_id uuid
    REFERENCES workflow_instances(workflow_id) ON DELETE SET NULL;
CREATE INDEX workflow_instances_rerun_of_idx ON workflow_instances(rerun_of_workflow_id)
    WHERE rerun_of_workflow_id IS NOT NULL;
ALTER TABLE workflow_instances ADD COLUMN rerun_event_id uuid
    REFERENCES ci_events(event_id) ON DELETE SET NULL;
CREATE INDEX workflow_instances_rerun_event_idx ON workflow_instances(rerun_event_id)
    WHERE rerun_event_id IS NOT NULL;

-- +goose Down
-- Re-run instances lose their event link (rerun_event_id). The identity
-- index of migration 000041 is not changed by the Up step.
DROP INDEX IF EXISTS workflow_instances_rerun_event_idx;
ALTER TABLE workflow_instances DROP COLUMN rerun_event_id;
DROP INDEX IF EXISTS workflow_instances_rerun_of_idx;
ALTER TABLE workflow_instances DROP COLUMN rerun_of_workflow_id;
ALTER TABLE ci_trust_settings DROP COLUMN rerun_shift_limits;
ALTER TABLE workflow_instances DROP COLUMN shifts;
ALTER TABLE jobs DROP COLUMN shifts;
ALTER TABLE jobs DROP COLUMN resource_storage_limit;
ALTER TABLE jobs DROP COLUMN resource_storage_request;
