-- +goose Up
-- WP13b: priority lanes and group priority shifts (plan item D1, owner
-- decision D10: priority is the only capacity mechanism; groups shift
-- priority). See docs/priority.md.
--
-- A lane is a coordinator rule. It matches trusted facts of a job (ref
-- classes, event type, draft state, job kind, retry) and gives the job a
-- base priority. The built-in lanes are in Go (internal/priority). A row in
-- this table adds a lane, or replaces the built-in lane or the organization
-- lane with the same name. project_id NULL is an organization lane.
--
-- Existing jobs keep their stored priority. Corndogs already has their
-- tasks, so this migration does not change them.
CREATE TABLE priority_lanes (
    lane_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    project_id uuid REFERENCES projects(project_id) ON DELETE CASCADE,
    name text NOT NULL,
    -- Lower positions are evaluated first. The first lane that matches wins.
    position integer NOT NULL DEFAULT 0,
    description text NOT NULL DEFAULT '',
    base integer NOT NULL,
    -- Match facts. An empty list or NULL is no condition.
    classes_any text[] NOT NULL DEFAULT '{}',
    classes_none text[] NOT NULL DEFAULT '{}',
    event_types text[] NOT NULL DEFAULT '{}',
    draft boolean,
    -- '' any job, 'eval' an eval job, 'work' a job that is not an eval.
    job_kind text NOT NULL DEFAULT '' CHECK (job_kind IN ('', 'eval', 'work')),
    -- true: the lane applies only to retries. A retry keeps the lane of the
    -- original job unless a retry lane matches.
    retry boolean NOT NULL DEFAULT false,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    CHECK (base BETWEEN -500 AND 500),
    CHECK (position BETWEEN -100000 AND 100000)
);
CREATE UNIQUE INDEX priority_lanes_org_name_idx ON priority_lanes (org_id, name) WHERE project_id IS NULL;
CREATE UNIQUE INDEX priority_lanes_project_name_idx ON priority_lanes (project_id, name) WHERE project_id IS NOT NULL;
CREATE INDEX priority_lanes_scope_idx ON priority_lanes (org_id, project_id);

-- A group shifts the priority of the work of its members. 0 is no shift.
ALTER TABLE groups ADD COLUMN priority_shift integer NOT NULL DEFAULT 0
    CHECK (priority_shift BETWEEN -50 AND 50);

-- The lane of a job and the structured reason
-- {kind: "priority", lane, base, shifts, final}. '' and NULL: the job was
-- made before lanes, and its priority is the stored value.
ALTER TABLE jobs ADD COLUMN priority_lane text NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN priority_reason jsonb;

-- +goose Down
ALTER TABLE jobs DROP COLUMN priority_reason;
ALTER TABLE jobs DROP COLUMN priority_lane;
ALTER TABLE groups DROP COLUMN priority_shift;
DROP TABLE priority_lanes;
