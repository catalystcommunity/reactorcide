-- +goose Up
-- WP10: report modes, the agreement report, the code owners import, and owner
-- acceptance of path dependencies (CI_REQUIREMENTS_PLAN.md A3, A2, A7;
-- docs/reporting-and-ownership.md).
--
-- The settings live in tables of their own, not in columns of projects and
-- organizations, so code that saves a whole Project struct (GORM Save) cannot
-- reset them. No row means the built-in defaults, which keep the behavior from
-- before this migration:
--
--   report_mode       full   (statuses and the pull request comment)
--   agreement_contexts {}    (no agreement report)
--   codeowners_import off    (VCS native code owners enforcement only)
--   path_acceptance   off    (any workflow can claim any path)
--
-- Nothing is backfilled: every existing organization and project gets the
-- defaults.

CREATE TABLE org_reporting_settings (
    org_id uuid PRIMARY KEY REFERENCES organizations(org_id) ON DELETE CASCADE,
    report_mode text NOT NULL DEFAULT 'full',
    agreement_contexts text[] NOT NULL DEFAULT '{}',
    codeowners_import text NOT NULL DEFAULT 'off',
    codeowners_cache_ttl_seconds integer NOT NULL DEFAULT 600,
    path_acceptance text NOT NULL DEFAULT 'off',
    path_owners jsonb NOT NULL DEFAULT '[]',
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    CHECK (report_mode IN ('full', 'statuses_only', 'silent')),
    CHECK (codeowners_import IN ('off', 'on')),
    CHECK (codeowners_cache_ttl_seconds BETWEEN 10 AND 86400),
    CHECK (path_acceptance IN ('off', 'warn', 'enforce')),
    CHECK (jsonb_typeof(path_owners) = 'array')
);

-- A NULL column inherits the organization value.
CREATE TABLE project_reporting_settings (
    project_id uuid PRIMARY KEY REFERENCES projects(project_id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    report_mode text,
    agreement_contexts text[],
    codeowners_import text,
    codeowners_cache_ttl_seconds integer,
    path_acceptance text,
    path_owners jsonb,
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    CHECK (report_mode IS NULL OR report_mode IN ('full', 'statuses_only', 'silent')),
    CHECK (codeowners_import IS NULL OR codeowners_import IN ('off', 'on')),
    CHECK (codeowners_cache_ttl_seconds IS NULL OR codeowners_cache_ttl_seconds BETWEEN 10 AND 86400),
    CHECK (path_acceptance IS NULL OR path_acceptance IN ('off', 'warn', 'enforce')),
    CHECK (path_owners IS NULL OR jsonb_typeof(path_owners) = 'array')
);

-- The agreement report: the Reactorcide result of a commit compared with the
-- results of another CI system on the same commit (VCS commit statuses and
-- check runs). One row per (project, commit). A later Reactorcide event of
-- the same commit replaces the Reactorcide result and starts the comparison
-- again.
--
-- status: pending  - waiting for the other CI (next_check_at is the next read)
--         complete - every configured context that exists has a final result
--         no_other - the other CI reported nothing before the window closed
CREATE TABLE ci_agreement (
    project_id uuid NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    commit_sha text NOT NULL,
    event_id uuid,
    provider text NOT NULL DEFAULT '',
    repository text NOT NULL DEFAULT '',
    pr_number integer,
    branch text NOT NULL DEFAULT '',
    reactorcide_result text NOT NULL,
    reactorcide_description text NOT NULL DEFAULT '',
    other_results jsonb NOT NULL DEFAULT '[]',
    other_result text NOT NULL DEFAULT '',
    agree boolean,
    status text NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    next_check_at timestamp,
    last_error text NOT NULL DEFAULT '',
    reactorcide_completed_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    observed_at timestamp,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    PRIMARY KEY (project_id, commit_sha),
    CHECK (reactorcide_result IN ('success', 'failure')),
    CHECK (other_result IN ('', 'success', 'failure')),
    CHECK (status IN ('pending', 'complete', 'no_other')),
    CHECK (jsonb_typeof(other_results) = 'array')
);
CREATE INDEX ci_agreement_due_idx ON ci_agreement (next_check_at) WHERE status = 'pending';
CREATE INDEX ci_agreement_project_idx ON ci_agreement (project_id, reactorcide_completed_at DESC);

-- Owner acceptance of path dependencies (A7). A workflow owner requests that
-- the owner of a path area accepts a path dependency of a workflow; the owner
-- accepts (or rejects); either side can revoke. Only an accepted row that is
-- not revoked counts at admission. The coordinator never deletes a row, so the
-- history stays.
CREATE TABLE path_acceptances (
    acceptance_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    project_id uuid NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    workflow_security_id text NOT NULL,
    path_pattern text NOT NULL,
    owner_subject text NOT NULL,
    state text NOT NULL DEFAULT 'requested',
    reason text NOT NULL DEFAULT '',
    requested_by uuid,
    requested_at timestamp,
    accepted_by uuid,
    accepted_at timestamp,
    revoked_by uuid,
    revoked_at timestamp,
    decision_reason text NOT NULL DEFAULT '',
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    CHECK (state IN ('requested', 'accepted', 'rejected', 'revoked')),
    CHECK (workflow_security_id <> '' AND path_pattern <> '' AND owner_subject <> '')
);
-- One open row (requested or accepted) per workflow, pattern, and owner.
CREATE UNIQUE INDEX path_acceptances_open_idx
    ON path_acceptances (project_id, workflow_security_id, path_pattern, owner_subject)
    WHERE state IN ('requested', 'accepted');
CREATE INDEX path_acceptances_project_idx ON path_acceptances (project_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS path_acceptances;
DROP TABLE IF EXISTS ci_agreement;
DROP TABLE IF EXISTS project_reporting_settings;
DROP TABLE IF EXISTS org_reporting_settings;
