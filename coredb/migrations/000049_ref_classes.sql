-- +goose Up
-- WP3: ref classes (docs/ref-classes.md).
--
-- The coordinator classifies the ref of each event with labels ("classes"),
-- for example default, tag, pr, fork, schedule, manual, and the classes that
-- an operator adds with rules (production, release, test). Classes are
-- metadata. Any policy can read them. Workflow code cannot set them.
--
-- Nothing restricts by class until an operator configures a consumer, so
-- this migration does not change the behavior of existing rows.

-- Operator rules. A rule with project_id NULL is an organization rule. A rule
-- adds its classes when all of its configured match facts are true. A rule
-- never removes a class: the built-in classes and the classes of other
-- rules stay.
CREATE TABLE ref_class_rules (
    rule_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    project_id uuid REFERENCES projects(project_id) ON DELETE CASCADE,
    name text NOT NULL,
    position integer NOT NULL DEFAULT 0,
    description text NOT NULL DEFAULT '',
    event_types text[] NOT NULL DEFAULT '{}',
    branch_glob text NOT NULL DEFAULT '',
    tag_glob text NOT NULL DEFAULT '',
    base_branch_glob text NOT NULL DEFAULT '',
    is_pr boolean,
    is_fork boolean,
    is_default_branch boolean,
    classes text[] NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    CHECK (cardinality(classes) > 0)
);
CREATE UNIQUE INDEX ref_class_rules_org_name_idx ON ref_class_rules (org_id, name) WHERE project_id IS NULL;
CREATE UNIQUE INDEX ref_class_rules_project_name_idx ON ref_class_rules (project_id, name) WHERE project_id IS NOT NULL;
CREATE INDEX ref_class_rules_scope_idx ON ref_class_rules (org_id, project_id, position);

-- The classes of an event, and the facts and reasons that gave them.
ALTER TABLE ci_events ADD COLUMN ref_classes text[] NOT NULL DEFAULT '{}';
ALTER TABLE ci_events ADD COLUMN ref_class_facts jsonb NOT NULL DEFAULT '{}';
ALTER TABLE ci_events ADD COLUMN ref_class_reasons jsonb NOT NULL DEFAULT '[]';
ALTER TABLE workflow_instances ADD COLUMN ref_classes text[] NOT NULL DEFAULT '{}';
ALTER TABLE jobs ADD COLUMN ref_classes text[] NOT NULL DEFAULT '{}';

-- Consumers. Empty lists (the default) put no condition on a job.
-- A secret grant with ref_classes_any applies only to a job that has one of
-- the classes. A grant with ref_classes_none never applies to a job that has
-- one of the classes.
ALTER TABLE secret_grants ADD COLUMN ref_classes_any text[];
ALTER TABLE secret_grants ADD COLUMN ref_classes_none text[];
-- A worker class with ref_classes_any admits a workflow only when the
-- classes of the workflow have one of the listed classes.
ALTER TABLE worker_classes ADD COLUMN ref_classes_any text[];

-- Backfill with the built-in defaults, from the facts that the rows store.
-- The default branch of a repository was not stored, so an old push gets no
-- "default" class. A fork pull request is known from the eval job.
UPDATE ci_events e SET ref_classes = classes.value
  FROM (
    SELECT event_id, coalesce(array_agg(DISTINCT class ORDER BY class) FILTER (WHERE class IS NOT NULL), '{}') AS value
      FROM (
        SELECT e2.event_id, unnest(ARRAY[
            CASE WHEN e2.source = 'schedule' OR e2.event_type = 'schedule' THEN 'schedule' END,
            CASE WHEN e2.source = 'manual' THEN 'manual' END,
            CASE WHEN e2.event_type = 'tag_created' AND e2.source <> 'manual' THEN 'tag' END,
            CASE WHEN e2.event_type LIKE 'pull\_request\_%' THEN 'pr' END,
            CASE WHEN e2.event_type LIKE 'pull\_request\_%' AND EXISTS (
                SELECT 1 FROM jobs j WHERE j.event_id = e2.event_id
                   AND j.job_env_vars ->> 'REACTORCIDE_IS_FORK_PR' = 'true') THEN 'fork' END
        ]) AS class
          FROM ci_events e2
      ) expanded
     GROUP BY event_id
  ) classes
 WHERE e.event_id = classes.event_id AND cardinality(classes.value) > 0;

UPDATE workflow_instances w SET ref_classes = e.ref_classes
  FROM ci_events e
 WHERE w.event_id = e.event_id AND cardinality(e.ref_classes) > 0;

UPDATE jobs j SET ref_classes = e.ref_classes
  FROM ci_events e
 WHERE j.event_id = e.event_id AND cardinality(e.ref_classes) > 0;

UPDATE jobs j SET ref_classes = w.ref_classes
  FROM workflow_instances w
 WHERE j.event_id IS NULL AND j.workflow_id = w.workflow_id AND cardinality(w.ref_classes) > 0;

-- Inheritance. A new job without classes inherits them from its parent job,
-- then its workflow, then its event. A new workflow inherits from its parent
-- job, its parent workflow, then its event. Thus child jobs, retries, and
-- re-runs keep the classes of the original run, also on a code path that
-- does not set them.
--
-- Classes are immutable after insert. An UPDATE never changes them: it keeps
-- the stored classes (and, for an event, the facts and the reasons), also
-- when it writes NULL or '{}' (an in-memory row saved with every column).
-- Otherwise a save of a stale row could clear a "fork" class and remove a
-- consumer condition. No code path changes classes after insert.
-- +goose StatementBegin
CREATE FUNCTION ref_classes_default_job() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        NEW.ref_classes := OLD.ref_classes;
        RETURN NEW;
    END IF;
    IF coalesce(cardinality(NEW.ref_classes), 0) = 0 AND NEW.parent_job_id IS NOT NULL THEN
        SELECT ref_classes INTO NEW.ref_classes FROM jobs WHERE job_id = NEW.parent_job_id;
    END IF;
    IF coalesce(cardinality(NEW.ref_classes), 0) = 0 AND NEW.workflow_id IS NOT NULL THEN
        SELECT ref_classes INTO NEW.ref_classes FROM workflow_instances WHERE workflow_id = NEW.workflow_id;
    END IF;
    IF coalesce(cardinality(NEW.ref_classes), 0) = 0 AND NEW.event_id IS NOT NULL THEN
        SELECT ref_classes INTO NEW.ref_classes FROM ci_events WHERE event_id = NEW.event_id;
    END IF;
    NEW.ref_classes := coalesce(NEW.ref_classes, '{}');
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER ref_classes_job BEFORE INSERT OR UPDATE OF ref_classes ON jobs
    FOR EACH ROW EXECUTE FUNCTION ref_classes_default_job();

-- +goose StatementBegin
CREATE FUNCTION ref_classes_default_workflow() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        NEW.ref_classes := OLD.ref_classes;
        RETURN NEW;
    END IF;
    IF coalesce(cardinality(NEW.ref_classes), 0) = 0 AND NEW.parent_job_id IS NOT NULL THEN
        SELECT ref_classes INTO NEW.ref_classes FROM jobs WHERE job_id = NEW.parent_job_id;
    END IF;
    IF coalesce(cardinality(NEW.ref_classes), 0) = 0 AND NEW.parent_workflow_id IS NOT NULL THEN
        SELECT ref_classes INTO NEW.ref_classes FROM workflow_instances WHERE workflow_id = NEW.parent_workflow_id;
    END IF;
    IF coalesce(cardinality(NEW.ref_classes), 0) = 0 AND NEW.event_id IS NOT NULL THEN
        SELECT ref_classes INTO NEW.ref_classes FROM ci_events WHERE event_id = NEW.event_id;
    END IF;
    NEW.ref_classes := coalesce(NEW.ref_classes, '{}');
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER ref_classes_workflow BEFORE INSERT OR UPDATE OF ref_classes ON workflow_instances
    FOR EACH ROW EXECUTE FUNCTION ref_classes_default_workflow();

-- +goose StatementBegin
CREATE FUNCTION ref_classes_default_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        NEW.ref_classes := OLD.ref_classes;
        NEW.ref_class_facts := OLD.ref_class_facts;
        NEW.ref_class_reasons := OLD.ref_class_reasons;
        RETURN NEW;
    END IF;
    NEW.ref_classes := coalesce(NEW.ref_classes, '{}');
    NEW.ref_class_facts := coalesce(NEW.ref_class_facts, '{}');
    NEW.ref_class_reasons := coalesce(NEW.ref_class_reasons, '[]');
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER ref_classes_event BEFORE INSERT OR UPDATE OF ref_classes, ref_class_facts, ref_class_reasons ON ci_events
    FOR EACH ROW EXECUTE FUNCTION ref_classes_default_event();

-- +goose Down
DROP TRIGGER IF EXISTS ref_classes_event ON ci_events;
DROP TRIGGER IF EXISTS ref_classes_workflow ON workflow_instances;
DROP TRIGGER IF EXISTS ref_classes_job ON jobs;
DROP FUNCTION IF EXISTS ref_classes_default_event();
DROP FUNCTION IF EXISTS ref_classes_default_workflow();
DROP FUNCTION IF EXISTS ref_classes_default_job();
ALTER TABLE worker_classes DROP COLUMN IF EXISTS ref_classes_any;
ALTER TABLE secret_grants DROP COLUMN IF EXISTS ref_classes_none;
ALTER TABLE secret_grants DROP COLUMN IF EXISTS ref_classes_any;
ALTER TABLE jobs DROP COLUMN IF EXISTS ref_classes;
ALTER TABLE workflow_instances DROP COLUMN IF EXISTS ref_classes;
ALTER TABLE ci_events DROP COLUMN IF EXISTS ref_class_reasons;
ALTER TABLE ci_events DROP COLUMN IF EXISTS ref_class_facts;
ALTER TABLE ci_events DROP COLUMN IF EXISTS ref_classes;
DROP TABLE IF EXISTS ref_class_rules;
