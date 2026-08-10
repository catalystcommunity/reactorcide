-- +goose Up
CREATE TABLE vcs_report_targets (
    report_target_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    project_id uuid REFERENCES projects(project_id) ON DELETE CASCADE,
    provider text NOT NULL,
    repository text NOT NULL,
    target_type text NOT NULL,
    external_target_id text NOT NULL,
    root_marker text NOT NULL,
    provider_comment_id text,
    current_generation bigint NOT NULL DEFAULT 1,
    generation_key text,
    generation_complete boolean NOT NULL DEFAULT true,
    desired_revision bigint NOT NULL DEFAULT 0,
    rendered_revision bigint NOT NULL DEFAULT 0,
    dirty boolean NOT NULL DEFAULT false,
    last_error text,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    UNIQUE (provider, repository, target_type, external_target_id, root_marker)
);
CREATE INDEX vcs_report_targets_dirty_idx ON vcs_report_targets(dirty, updated_at) WHERE dirty;

CREATE TABLE vcs_report_entries (
    report_target_id uuid NOT NULL REFERENCES vcs_report_targets(report_target_id) ON DELETE CASCADE,
    entry_key text NOT NULL,
    workflow_id uuid REFERENCES workflow_instances(workflow_id) ON DELETE SET NULL,
    generation bigint NOT NULL,
    status text NOT NULL,
    structured_state jsonb NOT NULL DEFAULT '{}',
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    PRIMARY KEY (report_target_id, entry_key)
);

-- +goose Down
DROP TABLE vcs_report_entries;
DROP TABLE vcs_report_targets;
