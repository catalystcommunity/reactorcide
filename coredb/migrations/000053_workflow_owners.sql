-- +goose Up
-- WP7: workflow owners and contacts, owner approvers, workflow-scoped roles
-- (CI_REQUIREMENTS_PLAN.md B2, A6, B5; docs/workflow-design.md).
--
-- A workflow definition can name an owner group and a contact. The
-- coordinator reads them from the TRUSTED CI definition only, and stores a
-- snapshot on each workflow instance and each job when it creates the row.
-- The snapshot does not change later, so attribution stays correct after a
-- team changes.
--
-- NULL means "unknown": rows that predate this migration, and runs that had
-- no trusted definition to read. An empty owner_group ('') means that the
-- trusted definition declares no owner. Nothing is backfilled.

ALTER TABLE workflow_instances ADD COLUMN owner_group text;
ALTER TABLE workflow_instances ADD COLUMN owner_subjects text[];
ALTER TABLE workflow_instances ADD COLUMN owner_contact jsonb;
-- Where the snapshot came from: ci (the trusted CI definition at
-- owner_ci_sha), prior (an earlier trusted snapshot of the same workflow,
-- because the CI of this run was the event's own content), parent (the
-- parent workflow), retry (the retried instance).
ALTER TABLE workflow_instances ADD COLUMN owner_source text;
ALTER TABLE workflow_instances ADD COLUMN owner_ci_sha text;
ALTER TABLE workflow_instances ADD CONSTRAINT workflow_instances_owner_source_check
    CHECK (owner_source IS NULL OR owner_source IN ('ci', 'prior', 'parent', 'retry'));
-- An owner snapshot is trusted only when the CI definition came from a ref
-- that the pusher of the event cannot write freely: the default branch (ref
-- class default), a pull request whose base is the default branch, a tag,
-- or a separate CI repository. owner_ref_classes are the ref classes of the
-- run that wrote the snapshot. Only trusted snapshots give authority, and
-- only trusted 'ci' snapshots are the "latest owner" of a workflow.
ALTER TABLE workflow_instances ADD COLUMN owner_trusted boolean NOT NULL DEFAULT false;
ALTER TABLE workflow_instances ADD COLUMN owner_ref_classes text[];

ALTER TABLE jobs ADD COLUMN owner_group text;
ALTER TABLE jobs ADD COLUMN owner_subjects text[];
ALTER TABLE jobs ADD COLUMN owner_contact jsonb;

-- The latest trusted snapshot of a workflow (owners.ForWorkflow).
CREATE INDEX workflow_instances_owner_lookup_idx
    ON workflow_instances (project_id, workflow_security_id, created_at DESC)
    WHERE owner_group IS NOT NULL AND owner_trusted AND owner_source = 'ci';

-- A child workflow gets the owner of its parent, and a job gets the owner of
-- its workflow, when the creator does not set one. The snapshot is set at
-- insert only: an update never changes it (a Go struct that did not read the
-- columns back must not erase them).
-- +goose StatementBegin
CREATE FUNCTION workflow_owner_snapshot_workflow() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        NEW.owner_group := OLD.owner_group;
        NEW.owner_subjects := OLD.owner_subjects;
        NEW.owner_contact := OLD.owner_contact;
        NEW.owner_source := OLD.owner_source;
        NEW.owner_ci_sha := OLD.owner_ci_sha;
        NEW.owner_trusted := OLD.owner_trusted;
        NEW.owner_ref_classes := OLD.owner_ref_classes;
        RETURN NEW;
    END IF;
    IF NEW.owner_group IS NULL AND NEW.parent_workflow_id IS NOT NULL THEN
        SELECT owner_group, owner_subjects, owner_contact, owner_ci_sha, owner_trusted, owner_ref_classes
          INTO NEW.owner_group, NEW.owner_subjects, NEW.owner_contact, NEW.owner_ci_sha, NEW.owner_trusted, NEW.owner_ref_classes
          FROM workflow_instances WHERE workflow_id = NEW.parent_workflow_id;
        IF NEW.owner_group IS NOT NULL THEN
            NEW.owner_source := 'parent';
        END IF;
    END IF;
    IF NEW.owner_group IS NULL THEN
        NEW.owner_subjects := NULL;
        NEW.owner_contact := NULL;
        NEW.owner_source := NULL;
        NEW.owner_ci_sha := NULL;
        NEW.owner_trusted := false;
        NEW.owner_ref_classes := NULL;
    END IF;
    -- Only the trusted CI definition can write source 'ci'.
    IF NEW.owner_source = 'ci' AND NOT coalesce(NEW.owner_trusted, false) THEN
        NEW.owner_group := NULL;
        NEW.owner_subjects := NULL;
        NEW.owner_contact := NULL;
        NEW.owner_source := NULL;
        NEW.owner_ci_sha := NULL;
        NEW.owner_ref_classes := NULL;
        NEW.owner_trusted := false;
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER workflow_owner_snapshot BEFORE INSERT OR UPDATE ON workflow_instances
    FOR EACH ROW EXECUTE FUNCTION workflow_owner_snapshot_workflow();

-- +goose StatementBegin
CREATE FUNCTION workflow_owner_snapshot_job() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        NEW.owner_group := OLD.owner_group;
        NEW.owner_subjects := OLD.owner_subjects;
        NEW.owner_contact := OLD.owner_contact;
        RETURN NEW;
    END IF;
    IF NEW.owner_group IS NULL AND NEW.workflow_id IS NOT NULL THEN
        SELECT owner_group, owner_subjects, owner_contact
          INTO NEW.owner_group, NEW.owner_subjects, NEW.owner_contact
          FROM workflow_instances WHERE workflow_id = NEW.workflow_id;
    END IF;
    IF NEW.owner_group IS NULL THEN
        NEW.owner_subjects := NULL;
        NEW.owner_contact := NULL;
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER workflow_owner_snapshot BEFORE INSERT OR UPDATE ON jobs
    FOR EACH ROW EXECUTE FUNCTION workflow_owner_snapshot_job();

-- Workflow-scoped roles (B5). scope_id is the project; scope_workflow is the
-- workflow security ID in that project. The only role is owner: cancel,
-- retry and re-run, quarantine, and required-set proposals for that one
-- workflow. A workflow role never widens a project or org scope.
ALTER TABLE role_assignments ADD COLUMN scope_workflow text;
ALTER TABLE role_assignments DROP CONSTRAINT role_assignments_scope_type_check;
ALTER TABLE role_assignments ADD CONSTRAINT role_assignments_scope_type_check
    CHECK (scope_type IN ('global', 'org', 'project', 'workflow'));
ALTER TABLE role_assignments ADD CONSTRAINT role_assignments_scope_workflow_check
    CHECK ((scope_type = 'workflow' AND scope_id IS NOT NULL AND role = 'owner'
            AND scope_workflow IS NOT NULL AND scope_workflow ~ '^[a-z0-9][a-z0-9-]{0,62}$')
        OR (scope_type <> 'workflow' AND scope_workflow IS NULL));
DROP INDEX IF EXISTS role_assignments_unique_idx;
CREATE UNIQUE INDEX role_assignments_unique_idx ON role_assignments (
    principal_type,
    principal_id,
    scope_type,
    coalesce(scope_id, '00000000-0000-0000-0000-000000000000'::uuid),
    coalesce(scope_workflow, ''),
    role
);

-- +goose Down
DELETE FROM role_assignments WHERE scope_type = 'workflow';
DROP INDEX IF EXISTS role_assignments_unique_idx;
CREATE UNIQUE INDEX role_assignments_unique_idx ON role_assignments (
    principal_type,
    principal_id,
    scope_type,
    coalesce(scope_id, '00000000-0000-0000-0000-000000000000'::uuid),
    role
);
ALTER TABLE role_assignments DROP CONSTRAINT IF EXISTS role_assignments_scope_workflow_check;
ALTER TABLE role_assignments DROP CONSTRAINT IF EXISTS role_assignments_scope_type_check;
ALTER TABLE role_assignments ADD CONSTRAINT role_assignments_scope_type_check
    CHECK (scope_type IN ('global', 'org', 'project'));
ALTER TABLE role_assignments DROP COLUMN IF EXISTS scope_workflow;

DROP TRIGGER IF EXISTS workflow_owner_snapshot ON jobs;
DROP TRIGGER IF EXISTS workflow_owner_snapshot ON workflow_instances;
DROP FUNCTION IF EXISTS workflow_owner_snapshot_job();
DROP FUNCTION IF EXISTS workflow_owner_snapshot_workflow();
DROP INDEX IF EXISTS workflow_instances_owner_lookup_idx;
ALTER TABLE jobs DROP COLUMN IF EXISTS owner_contact;
ALTER TABLE jobs DROP COLUMN IF EXISTS owner_subjects;
ALTER TABLE jobs DROP COLUMN IF EXISTS owner_group;
ALTER TABLE workflow_instances DROP COLUMN IF EXISTS owner_ref_classes;
ALTER TABLE workflow_instances DROP COLUMN IF EXISTS owner_trusted;
ALTER TABLE workflow_instances DROP CONSTRAINT IF EXISTS workflow_instances_owner_source_check;
ALTER TABLE workflow_instances DROP COLUMN IF EXISTS owner_ci_sha;
ALTER TABLE workflow_instances DROP COLUMN IF EXISTS owner_source;
ALTER TABLE workflow_instances DROP COLUMN IF EXISTS owner_contact;
ALTER TABLE workflow_instances DROP COLUMN IF EXISTS owner_subjects;
ALTER TABLE workflow_instances DROP COLUMN IF EXISTS owner_group;
