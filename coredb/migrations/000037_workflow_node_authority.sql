-- +goose Up
-- Per-node authority overrides for mixed-trust workflows. Empty values mean
-- that the node inherits the workflow-level authority.
ALTER TABLE workflow_nodes
    ADD COLUMN ci_origin text CHECK (ci_origin IS NULL OR ci_origin = '' OR ci_origin IN ('base', 'head')),
    ADD COLUMN ci_repository text,
    ADD COLUMN ci_sha text,
    ADD COLUMN execution_profile text,
    ADD COLUMN worker_class text,
    ADD COLUMN policy_revision text,
    ADD COLUMN policy_rule_id text,
    ADD COLUMN approval_id uuid;

-- +goose Down
ALTER TABLE workflow_nodes DROP COLUMN approval_id, DROP COLUMN policy_rule_id,
    DROP COLUMN policy_revision, DROP COLUMN worker_class,
    DROP COLUMN execution_profile, DROP COLUMN ci_sha, DROP COLUMN ci_repository,
    DROP COLUMN ci_origin;
