-- +goose Up
-- Approvals become append-only actions on a pull request head. A person
-- approves or revokes one of three kinds:
--   run      /reactorcide approve-ci   satisfies the run gate (trusted base CI)
--   head_ci  /reactorcide override-ci  lets the head's own CI run
--   accept   /reactorcide accept       counts toward required reviews
-- An approval binds to the head SHA only. The state is calculated from the
-- rows: for each role, the last action wins. Workflow scope, execution
-- profile, policy revision, and base SHA are no longer part of an approval;
-- the columns stay for rows made before this migration.
--
-- An approval made before this migration names a workflow, a profile, and a
-- policy revision. The new admission does not read those, so each old row is
-- invalidated instead of being given a meaning it never had.
UPDATE ci_approvals SET invalidated_at = timezone('utc', now()) WHERE invalidated_at IS NULL;

-- +goose StatementBegin
DO $$
DECLARE constraint_name text;
BEGIN
    SELECT conname INTO constraint_name FROM pg_constraint
     WHERE conrelid = 'ci_approvals'::regclass AND contype = 'u';
    IF constraint_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE ci_approvals DROP CONSTRAINT %I', constraint_name);
    END IF;
END $$;
-- +goose StatementEnd
DROP INDEX IF EXISTS ci_approvals_lookup_idx;

ALTER TABLE ci_approvals ALTER COLUMN base_sha SET DEFAULT '';
ALTER TABLE ci_approvals ALTER COLUMN policy_revision SET DEFAULT '';
ALTER TABLE ci_approvals ALTER COLUMN workflow_scope SET DEFAULT '';
ALTER TABLE ci_approvals ALTER COLUMN execution_profile SET DEFAULT '';
ALTER TABLE ci_approvals ALTER COLUMN approver_subject SET DEFAULT '';

ALTER TABLE ci_approvals ADD COLUMN kind text NOT NULL DEFAULT 'head_ci'
    CHECK (kind IN ('run', 'head_ci', 'accept'));
ALTER TABLE ci_approvals ADD COLUMN action text NOT NULL DEFAULT 'approved'
    CHECK (action IN ('approved', 'revoked'));
-- An optional label of an accept. Empty means every required review that
-- the person can satisfy.
ALTER TABLE ci_approvals ADD COLUMN label text NOT NULL DEFAULT '';
-- The person: "PROVIDER/LOGIN" for a VCS account, "user:USER_ID" for a
-- Reactorcide user with no VCS account.
ALTER TABLE ci_approvals ADD COLUMN person text NOT NULL DEFAULT '';
-- Every approver subject the person had when they acted.
ALTER TABLE ci_approvals ADD COLUMN subjects text[] NOT NULL DEFAULT '{}';
-- Where the action came from: comment, review, api, ui.
ALTER TABLE ci_approvals ADD COLUMN channel text NOT NULL DEFAULT 'api';
-- The provider object that made the action (for example github:comment:ID).
-- A redelivery of the same object makes no second action.
ALTER TABLE ci_approvals ADD COLUMN source_object text;
CREATE UNIQUE INDEX ci_approvals_source_object_idx ON ci_approvals(project_id, source_object)
    WHERE source_object IS NOT NULL;
CREATE INDEX ci_approvals_head_idx ON ci_approvals(project_id, pr_number, head_sha, created_at);

-- Run gate, override, and required-review settings. One row for an
-- organization (project_id NULL) and at most one row for each project. A NULL
-- column in a project row inherits the organization value. Required reviews
-- add: organization reviews apply to each project, and a project adds its own.
CREATE TABLE ci_trust_settings (
    settings_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    project_id uuid REFERENCES projects(project_id) ON DELETE CASCADE,
    -- Conditions that hold a pull request until approve-ci: forks,
    -- first_time_contributors, outside_contributors.
    run_gate text[],
    gated_run_authority text CHECK (gated_run_authority IN ('no_secrets', 'normal')),
    run_approvers text[],
    override_approvers text[],
    override_profile text,
    required_reviews jsonb,
    updated_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    updated_by text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX ci_trust_settings_org_idx ON ci_trust_settings(org_id) WHERE project_id IS NULL;
CREATE UNIQUE INDEX ci_trust_settings_project_idx ON ci_trust_settings(project_id) WHERE project_id IS NOT NULL;

-- A coordinator-made event: re-evaluation after an approval, or a rerun.
ALTER TABLE ci_events DROP CONSTRAINT ci_events_source_check;
ALTER TABLE ci_events ADD CONSTRAINT ci_events_source_check
    CHECK (source IN ('webhook', 'schedule', 'manual', 'retry', 'approval', 'rerun'));
-- A pull request held by the run gate has no eval job until approve-ci.
ALTER TABLE ci_events DROP CONSTRAINT ci_events_status_check;
ALTER TABLE ci_events ADD CONSTRAINT ci_events_status_check
    CHECK (status IN ('received', 'evaluating', 'evaluated', 'failed', 'duplicate', 'awaiting_approval'));

-- A re-evaluation after an approval keeps a workflow whose decision did not
-- change: the decision is 'carried' and points at the earlier workflow.
ALTER TABLE ci_event_workflow_decisions DROP CONSTRAINT ci_event_workflow_decisions_decision_check;
ALTER TABLE ci_event_workflow_decisions ADD CONSTRAINT ci_event_workflow_decisions_decision_check
    CHECK (decision IN ('run', 'skipped', 'invalid', 'not_admitted', 'carried'));

-- +goose Down
DELETE FROM ci_event_workflow_decisions WHERE decision = 'carried';
ALTER TABLE ci_event_workflow_decisions DROP CONSTRAINT ci_event_workflow_decisions_decision_check;
ALTER TABLE ci_event_workflow_decisions ADD CONSTRAINT ci_event_workflow_decisions_decision_check
    CHECK (decision IN ('run', 'skipped', 'invalid', 'not_admitted'));
UPDATE ci_events SET status = 'failed' WHERE status = 'awaiting_approval';
ALTER TABLE ci_events DROP CONSTRAINT ci_events_status_check;
ALTER TABLE ci_events ADD CONSTRAINT ci_events_status_check
    CHECK (status IN ('received', 'evaluating', 'evaluated', 'failed', 'duplicate'));
UPDATE ci_events SET source = 'manual' WHERE source IN ('approval', 'rerun');
ALTER TABLE ci_events DROP CONSTRAINT ci_events_source_check;
ALTER TABLE ci_events ADD CONSTRAINT ci_events_source_check
    CHECK (source IN ('webhook', 'schedule', 'manual', 'retry'));

DROP TABLE ci_trust_settings;

-- Actions made by this migration's model have no meaning to the old model.
UPDATE jobs SET approval_id = NULL WHERE approval_id IN (SELECT approval_id FROM ci_approvals WHERE person <> '');
UPDATE workflow_instances SET approval_id = NULL WHERE approval_id IN (SELECT approval_id FROM ci_approvals WHERE person <> '');
DELETE FROM ci_approvals WHERE person <> '';
DROP INDEX ci_approvals_head_idx;
DROP INDEX ci_approvals_source_object_idx;
ALTER TABLE ci_approvals DROP COLUMN source_object;
ALTER TABLE ci_approvals DROP COLUMN channel;
ALTER TABLE ci_approvals DROP COLUMN subjects;
ALTER TABLE ci_approvals DROP COLUMN person;
ALTER TABLE ci_approvals DROP COLUMN label;
ALTER TABLE ci_approvals DROP COLUMN action;
ALTER TABLE ci_approvals DROP COLUMN kind;
ALTER TABLE ci_approvals ALTER COLUMN base_sha DROP DEFAULT;
ALTER TABLE ci_approvals ALTER COLUMN policy_revision DROP DEFAULT;
ALTER TABLE ci_approvals ALTER COLUMN workflow_scope DROP DEFAULT;
ALTER TABLE ci_approvals ALTER COLUMN execution_profile DROP DEFAULT;
ALTER TABLE ci_approvals ALTER COLUMN approver_subject DROP DEFAULT;
ALTER TABLE ci_approvals ADD UNIQUE (project_id, pr_number, head_repository, head_sha, base_sha,
    policy_revision, workflow_scope, execution_profile, approver_subject);
CREATE INDEX ci_approvals_lookup_idx ON ci_approvals(
    project_id, pr_number, head_sha, base_sha, policy_revision, workflow_scope
) WHERE invalidated_at IS NULL;
