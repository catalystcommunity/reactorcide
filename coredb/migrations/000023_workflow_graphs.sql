-- +goose Up
ALTER TABLE workflow_instances
  ADD COLUMN root_workflow_id uuid REFERENCES workflow_instances(workflow_id) ON DELETE CASCADE,
  ADD COLUMN parent_workflow_id uuid REFERENCES workflow_instances(workflow_id) ON DELETE CASCADE,
  ADD COLUMN origin_job_id uuid REFERENCES jobs(job_id) ON DELETE SET NULL,
  ADD COLUMN origin_type text,
  ADD COLUMN trigger_operation_id text,
  ADD COLUMN trigger_type text NOT NULL DEFAULT 'runnerlib';

CREATE INDEX workflow_instances_root_idx ON workflow_instances(root_workflow_id);
CREATE INDEX workflow_instances_parent_idx ON workflow_instances(parent_workflow_id);
CREATE UNIQUE INDEX workflow_instances_trigger_operation_idx
  ON workflow_instances(parent_job_id, trigger_operation_id, name)
  WHERE trigger_operation_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS workflow_instances_trigger_operation_idx;
DROP INDEX IF EXISTS workflow_instances_parent_idx;
DROP INDEX IF EXISTS workflow_instances_root_idx;
ALTER TABLE workflow_instances
  DROP COLUMN IF EXISTS trigger_type,
  DROP COLUMN IF EXISTS trigger_operation_id,
  DROP COLUMN IF EXISTS origin_type,
  DROP COLUMN IF EXISTS origin_job_id,
  DROP COLUMN IF EXISTS parent_workflow_id,
  DROP COLUMN IF EXISTS root_workflow_id;
