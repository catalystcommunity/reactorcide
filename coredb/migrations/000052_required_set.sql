-- +goose Up
-- WP6: the required set, advisory workflows, promotion approvals, and
-- quarantine (docs/required-set.md).
--
-- The required set of a project names the workflow security IDs that count
-- toward "Reactorcide Checks". A workflow that is not in the set is advisory:
-- it runs, it reports in the pull request comment, and it never fails the
-- aggregate status. The set is OFF for every existing project (no row in
-- project_required_set_settings), so every workflow blocks, as before.
--
-- The settings live in tables of their own, not in columns of projects and
-- organizations. Code that saves a whole Project struct (GORM Save) can then
-- not reset them.

-- Organization defaults. approvers NULL or empty: the org admins approve.
CREATE TABLE org_required_set_settings (
    org_id uuid PRIMARY KEY REFERENCES organizations(org_id) ON DELETE CASCADE,
    approvers text[],
    approver_rules jsonb NOT NULL DEFAULT '[]',
    max_quarantine_days integer NOT NULL DEFAULT 30,
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    CHECK (max_quarantine_days BETWEEN 1 AND 365),
    CHECK (jsonb_typeof(approver_rules) = 'array')
);

-- Project settings. approvers NULL and approver_rules NULL inherit the
-- organization values.
CREATE TABLE project_required_set_settings (
    project_id uuid PRIMARY KEY REFERENCES projects(project_id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    enabled boolean NOT NULL DEFAULT false,
    approvers text[],
    approver_rules jsonb,
    enabled_at timestamp,
    enabled_by uuid,
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    CHECK (approver_rules IS NULL OR jsonb_typeof(approver_rules) = 'array')
);

-- The members of the set. branch_pattern '' means every target branch; a
-- glob limits the entry to pull requests into (and pushes to) matching
-- branches. A quarantine is a time-limited removal: while quarantined_until
-- is in the future, the entry does not make the workflow required.
CREATE TABLE project_required_workflows (
    project_id uuid NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    workflow_security_id text NOT NULL,
    branch_pattern text NOT NULL DEFAULT '',
    source text NOT NULL DEFAULT 'promotion',
    added_by uuid,
    added_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    reason text NOT NULL DEFAULT '',
    proposal_id uuid,
    quarantined_until timestamp,
    quarantine_reason text NOT NULL DEFAULT '',
    quarantined_by uuid,
    quarantined_at timestamp,
    PRIMARY KEY (project_id, workflow_security_id, branch_pattern),
    CHECK (source IN ('seed', 'promotion')),
    CHECK (workflow_security_id <> '')
);

-- Promotions (into the set) and demotions (out of the set). A proposal is
-- pending until an approver approves or rejects it, or the proposer
-- withdraws it.
CREATE TABLE required_set_proposals (
    proposal_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    project_id uuid NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    workflow_security_id text NOT NULL,
    branch_pattern text NOT NULL DEFAULT '',
    action text NOT NULL,
    state text NOT NULL DEFAULT 'pending',
    reason text NOT NULL DEFAULT '',
    proposed_by uuid,
    proposed_identity text NOT NULL DEFAULT '',
    proposed_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    decided_by uuid,
    decided_at timestamp,
    decision_reason text NOT NULL DEFAULT '',
    CHECK (action IN ('promote', 'demote')),
    CHECK (state IN ('pending', 'approved', 'rejected', 'withdrawn')),
    CHECK (workflow_security_id <> '')
);
CREATE UNIQUE INDEX required_set_proposals_pending_idx
    ON required_set_proposals (project_id, workflow_security_id, branch_pattern, action)
    WHERE state = 'pending';
CREATE INDEX required_set_proposals_project_idx ON required_set_proposals (project_id, proposed_at DESC);

-- +goose Down
DROP TABLE IF EXISTS required_set_proposals;
DROP TABLE IF EXISTS project_required_workflows;
DROP TABLE IF EXISTS project_required_set_settings;
DROP TABLE IF EXISTS org_required_set_settings;
