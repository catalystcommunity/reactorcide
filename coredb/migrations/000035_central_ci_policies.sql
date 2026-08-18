-- +goose Up
-- CI admission policy is coordinator state. Repository content cannot change
-- the policy that decides if repository CI content is trusted.
CREATE TABLE ci_policies (
    policy_id UUID PRIMARY KEY DEFAULT generate_ulid(),
    org_id UUID NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    project_id UUID NOT NULL UNIQUE REFERENCES projects(project_id) ON DELETE CASCADE,
    document JSONB NOT NULL,
    revision TEXT NOT NULL,
    updated_by UUID REFERENCES users(user_id) ON DELETE SET NULL,
    created_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT timezone('utc', now()),
    updated_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT timezone('utc', now())
);

CREATE INDEX idx_ci_policies_org_id ON ci_policies(org_id);

-- +goose Down
DROP TABLE ci_policies;
