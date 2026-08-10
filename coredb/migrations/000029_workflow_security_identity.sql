-- +goose Up
ALTER TABLE workflow_instances
    ADD COLUMN workflow_security_id text,
    ADD COLUMN source_file text,
    ADD COLUMN ci_origin text CHECK (ci_origin IS NULL OR ci_origin = '' OR ci_origin IN ('base', 'head')),
    ADD COLUMN ci_repository text,
    ADD COLUMN ci_sha text,
    ADD COLUMN execution_profile text,
    ADD COLUMN worker_class text,
    ADD COLUMN policy_revision text,
    ADD COLUMN policy_rule_id text,
    ADD COLUMN approval_id uuid;
UPDATE workflow_instances SET workflow_security_id = name WHERE workflow_security_id IS NULL;
ALTER TABLE workflow_instances ALTER COLUMN workflow_security_id SET NOT NULL;
DROP INDEX workflow_instances_trigger_operation_idx;
CREATE UNIQUE INDEX workflow_instances_trigger_security_idx
  ON workflow_instances(parent_job_id, trigger_operation_id, workflow_security_id)
  WHERE trigger_operation_id IS NOT NULL AND trigger_operation_id <> '';

ALTER TABLE jobs
    ADD COLUMN ci_origin text CHECK (ci_origin IS NULL OR ci_origin = '' OR ci_origin IN ('base', 'head')),
    ADD COLUMN ci_repository text,
    ADD COLUMN ci_sha text,
    ADD COLUMN execution_profile text,
    ADD COLUMN policy_revision text,
    ADD COLUMN policy_rule_id text,
    ADD COLUMN approval_id uuid;

-- +goose Down
ALTER TABLE jobs DROP COLUMN approval_id, DROP COLUMN policy_rule_id,
    DROP COLUMN policy_revision, DROP COLUMN execution_profile,
    DROP COLUMN ci_sha, DROP COLUMN ci_repository, DROP COLUMN ci_origin;
DROP INDEX workflow_instances_trigger_security_idx;
CREATE UNIQUE INDEX workflow_instances_trigger_operation_idx
  ON workflow_instances(parent_job_id, trigger_operation_id, name)
  WHERE trigger_operation_id IS NOT NULL;
ALTER TABLE workflow_instances DROP COLUMN approval_id, DROP COLUMN policy_rule_id,
    DROP COLUMN policy_revision, DROP COLUMN worker_class,
    DROP COLUMN execution_profile, DROP COLUMN ci_sha, DROP COLUMN ci_repository,
    DROP COLUMN ci_origin, DROP COLUMN source_file, DROP COLUMN workflow_security_id;
