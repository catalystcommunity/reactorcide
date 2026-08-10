-- +goose Up
CREATE TABLE vcs_identity_links (
    link_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    provider text NOT NULL,
    external_subject text NOT NULL,
    user_id uuid NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    verified_by text NOT NULL CHECK (verified_by IN ('admin', 'linkkeys')),
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    UNIQUE (provider, external_subject)
);
CREATE INDEX vcs_identity_links_user_id_idx ON vcs_identity_links(user_id);

CREATE TABLE ci_approvals (
    approval_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    project_id uuid NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    pr_number integer NOT NULL,
    head_repository text NOT NULL,
    head_sha text NOT NULL,
    base_sha text NOT NULL,
    policy_revision text NOT NULL,
    workflow_scope text NOT NULL,
    execution_profile text NOT NULL,
    approver_user_id uuid REFERENCES users(user_id) ON DELETE SET NULL,
    approver_provider text,
    approver_subject text NOT NULL,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    expires_at timestamp,
    invalidated_at timestamp,
    UNIQUE (project_id, pr_number, head_repository, head_sha, base_sha,
            policy_revision, workflow_scope, execution_profile, approver_subject)
);
CREATE INDEX ci_approvals_lookup_idx ON ci_approvals(
    project_id, pr_number, head_sha, base_sha, policy_revision, workflow_scope
) WHERE invalidated_at IS NULL;

ALTER TABLE workflow_instances ADD CONSTRAINT workflow_instances_approval_id_fkey
    FOREIGN KEY (approval_id) REFERENCES ci_approvals(approval_id) ON DELETE SET NULL;
ALTER TABLE jobs ADD CONSTRAINT jobs_approval_id_fkey
    FOREIGN KEY (approval_id) REFERENCES ci_approvals(approval_id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE jobs DROP CONSTRAINT jobs_approval_id_fkey;
ALTER TABLE workflow_instances DROP CONSTRAINT workflow_instances_approval_id_fkey;
DROP TABLE ci_approvals;
DROP TABLE vcs_identity_links;

