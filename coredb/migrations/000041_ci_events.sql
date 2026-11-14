-- +goose Up
-- An incoming event (webhook, schedule, manual submit) is a first-class row.
-- The eval job of the event answers "what must run for this event?".
CREATE TABLE ci_events (
    event_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL,
    project_id uuid REFERENCES projects(project_id) ON DELETE CASCADE,
    source text NOT NULL CHECK (source IN ('webhook', 'schedule', 'manual', 'retry')),
    provider text NOT NULL DEFAULT '',
    delivery_id text NOT NULL DEFAULT '',
    event_type text NOT NULL,
    repo text NOT NULL DEFAULT '',
    ref text NOT NULL DEFAULT '',
    branch text NOT NULL DEFAULT '',
    commit_sha text NOT NULL DEFAULT '',
    base_sha text NOT NULL DEFAULT '',
    pr_number integer,
    actor text NOT NULL DEFAULT '',
    schedule_id uuid,
    received_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    status text NOT NULL DEFAULT 'received'
        CHECK (status IN ('received', 'evaluating', 'evaluated', 'failed', 'duplicate')),
    eval_job_id uuid REFERENCES jobs(job_id) ON DELETE SET NULL,
    duplicate_of_event_id uuid REFERENCES ci_events(event_id) ON DELETE SET NULL,
    -- A redelivery of the same provider delivery is the same event. NULL when
    -- the source has no delivery identity (NULLs do not collide).
    idempotency_key text UNIQUE,
    -- provider:repo:event_type:ref:sha:pr. Two deliveries with different IDs
    -- and the same semantic key are the same change; the duplicate_events
    -- setting decides whether the second one runs.
    semantic_key text NOT NULL DEFAULT '',
    -- Display-safe facts only (changed-file preview and count). Never secrets
    -- or raw provider payloads.
    summary jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX ci_events_semantic_idx ON ci_events(project_id, semantic_key, received_at)
    WHERE semantic_key <> '';
CREATE INDEX ci_events_project_received_idx ON ci_events(project_id, received_at DESC);
CREATE INDEX ci_events_eval_job_idx ON ci_events(eval_job_id);

-- One row per workflow definition the eval considered, run or not. The
-- reasons say why.
CREATE TABLE ci_event_workflow_decisions (
    event_id uuid NOT NULL REFERENCES ci_events(event_id) ON DELETE CASCADE,
    workflow_security_id text NOT NULL,
    workflow_name text NOT NULL DEFAULT '',
    source_file text NOT NULL DEFAULT '',
    decision text NOT NULL CHECK (decision IN ('run', 'skipped', 'invalid', 'not_admitted')),
    reasons jsonb NOT NULL DEFAULT '[]',
    workflow_id uuid REFERENCES workflow_instances(workflow_id) ON DELETE SET NULL,
    created_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    PRIMARY KEY (event_id, workflow_security_id)
);
CREATE INDEX ci_event_workflow_decisions_workflow_idx ON ci_event_workflow_decisions(workflow_id)
    WHERE workflow_id IS NOT NULL;

ALTER TABLE jobs ADD COLUMN event_id uuid REFERENCES ci_events(event_id) ON DELETE SET NULL;
CREATE INDEX jobs_event_idx ON jobs(event_id) WHERE event_id IS NOT NULL;

ALTER TABLE workflow_instances ADD COLUMN event_id uuid REFERENCES ci_events(event_id) ON DELETE SET NULL;
CREATE INDEX workflow_instances_event_idx ON workflow_instances(event_id) WHERE event_id IS NOT NULL;
-- A retried eval of the same event finds the workflow the first eval made
-- instead of making a second copy. Root workflows only: child workflows from
-- dynamic triggers are keyed by their parent job.
CREATE UNIQUE INDEX workflow_instances_event_security_idx
    ON workflow_instances(event_id, workflow_security_id)
    WHERE event_id IS NOT NULL AND parent_workflow_id IS NULL;

-- Whether a second delivery of the same change runs again. The project value
-- inherits the organization value unless set.
ALTER TABLE organizations ADD COLUMN duplicate_events text NOT NULL DEFAULT 'run'
    CHECK (duplicate_events IN ('run', 'skip'));
ALTER TABLE projects ADD COLUMN duplicate_events text NOT NULL DEFAULT 'inherit'
    CHECK (duplicate_events IN ('inherit', 'run', 'skip'));

-- +goose Down
ALTER TABLE projects DROP COLUMN duplicate_events;
ALTER TABLE organizations DROP COLUMN duplicate_events;
DROP INDEX IF EXISTS workflow_instances_event_security_idx;
DROP INDEX IF EXISTS workflow_instances_event_idx;
ALTER TABLE workflow_instances DROP COLUMN event_id;
DROP INDEX IF EXISTS jobs_event_idx;
ALTER TABLE jobs DROP COLUMN event_id;
DROP TABLE ci_event_workflow_decisions;
DROP TABLE ci_events;
