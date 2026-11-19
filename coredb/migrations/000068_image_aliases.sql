-- +goose Up
-- Image aliases and worker image state. See VM_IMAGES_PLAN.md "Contracts"
-- and docs/job-definitions.md "Image aliases".
--
-- An image alias gives a name to one pinned image reference. A job that
-- names an alias gets the pinned reference when the coordinator creates the
-- job. An organization alias has no project_id. A project alias with the
-- same name replaces the organization alias for that project.
--
-- The pending_* columns hold the next pin during a rollout. The coordinator
-- moves them to active_* when every worker that serves the alias has the
-- new image.
--
-- org_id and project_id are uuid with foreign keys, as in the other
-- organization tables, so that the deletion of an organization or a project
-- also deletes its aliases. The unique index uses
-- coalesce(project_id::text, '') to give one name per scope.
CREATE TABLE image_aliases (
    alias_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    project_id uuid REFERENCES projects(project_id) ON DELETE CASCADE,
    name text NOT NULL,
    kind text NOT NULL,
    os text NOT NULL,
    arch text NOT NULL,
    characteristics jsonb NOT NULL DEFAULT '{}',
    active_ref text NOT NULL,
    active_digest text NOT NULL,
    pending_ref text,
    pending_digest text,
    pending_since timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by text NOT NULL DEFAULT '',
    CONSTRAINT image_aliases_kind_check CHECK (kind IN ('vm', 'container')),
    CONSTRAINT image_aliases_name_check CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    CONSTRAINT image_aliases_active_digest_check CHECK (active_digest ~ '^sha256:[a-f0-9]{64}$'),
    CONSTRAINT image_aliases_pending_digest_check CHECK (pending_digest IS NULL OR pending_digest ~ '^sha256:[a-f0-9]{64}$'),
    CONSTRAINT image_aliases_pending_pair_check CHECK ((pending_ref IS NULL) = (pending_digest IS NULL))
);
CREATE UNIQUE INDEX image_aliases_scope_name_idx ON image_aliases (org_id, coalesce(project_id::text, ''), name);
CREATE INDEX image_aliases_active_digest_idx ON image_aliases (org_id, active_digest);
CREATE INDEX image_aliases_pending_digest_idx ON image_aliases (org_id, pending_digest) WHERE pending_digest IS NOT NULL;

-- The alias name that a job used. NULL when the job named a reference. The
-- pinned reference goes in the image field of the job (container_image when
-- the job sets it, else runner_image).
ALTER TABLE jobs
    ADD COLUMN image_alias text;

-- The resolved image of a workflow node that a workflow retry or re-run
-- copies from the job of the earlier run. The node job then gets this
-- reference, alias, and image_digest characteristic, and the coordinator
-- does not resolve the alias again. NULL for a node of a new workflow.
ALTER TABLE workflow_nodes
    ADD COLUMN carried_image_ref text,
    ADD COLUMN carried_image_alias text,
    ADD COLUMN carried_image_digest text;

-- The image state of a worker. image_set_version is the image set version
-- that the coordinator wants the worker to hold. The image_state_* columns
-- and image_ready_digests are from the newest heartbeat image_set_report.
-- They stay NULL for a worker that sends no report.
ALTER TABLE workers
    ADD COLUMN image_set_version bigint NOT NULL DEFAULT 0,
    ADD COLUMN image_set_hash text,
    ADD COLUMN image_state text,
    ADD COLUMN image_state_version bigint,
    ADD COLUMN image_state_detail text,
    ADD COLUMN image_ready_digests jsonb,
    ADD CONSTRAINT workers_image_state_check CHECK (image_state IS NULL OR image_state IN ('updating', 'updated', 'failed'));

-- All new columns are nullable or have a default value. v0.12.x and earlier
-- 0.13 builds do not read or write them, so they continue to work.

-- +goose Down
ALTER TABLE workers
    DROP CONSTRAINT workers_image_state_check,
    DROP COLUMN image_ready_digests,
    DROP COLUMN image_state_detail,
    DROP COLUMN image_state_version,
    DROP COLUMN image_state,
    DROP COLUMN image_set_hash,
    DROP COLUMN image_set_version;
ALTER TABLE workflow_nodes
    DROP COLUMN carried_image_digest,
    DROP COLUMN carried_image_alias,
    DROP COLUMN carried_image_ref;
ALTER TABLE jobs DROP COLUMN image_alias;
DROP TABLE image_aliases;
