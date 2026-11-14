-- +goose Up
-- A named lease that at most one coordinator replica holds at a time. A
-- holder renews it before expires_at; another replica takes it over only
-- after it expires. Times use the database clock, so replica clock skew does
-- not matter.
CREATE TABLE coordinator_leases (
    name TEXT PRIMARY KEY,
    holder TEXT NOT NULL,
    acquired_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    renewed_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL
);

-- A coordinator-managed schedule. It runs one workflow definition, held in
-- the repository on `branch`, at the times the cron expression gives in
-- `timezone`. The definition is not copied here: a fire resolves it on the
-- branch head. A missing definition fails that fire; it does not delete the
-- schedule.
CREATE TABLE ci_schedules (
    schedule_id UUID PRIMARY KEY DEFAULT generate_ulid(),
    org_id UUID NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    branch TEXT NOT NULL,
    -- An explicit workflow ID, or a workflow file path under
    -- .reactorcide/workflows/.
    workflow_ref TEXT NOT NULL,
    cron TEXT NOT NULL,
    timezone TEXT NOT NULL DEFAULT 'UTC',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    -- The next fire time. NULL while disabled.
    next_fire_at TIMESTAMP WITH TIME ZONE,
    last_fire_at TIMESTAMP WITH TIME ZONE,
    -- The ci_events row of the last fire. No foreign key: event retention
    -- can delete the event and the schedule must stay.
    last_event_id UUID,
    last_error TEXT NOT NULL DEFAULT '',
    created_by UUID REFERENCES users(user_id) ON DELETE SET NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

CREATE INDEX idx_ci_schedules_project ON ci_schedules(project_id);
-- The scheduler loop reads due, enabled schedules.
CREATE INDEX idx_ci_schedules_due ON ci_schedules(next_fire_at) WHERE enabled;

-- One row per fire. The unique key makes a fire exactly-once, even when two
-- replicas both believe they hold the scheduler lease for a moment.
CREATE TABLE ci_schedule_fires (
    fire_id UUID PRIMARY KEY DEFAULT generate_ulid(),
    schedule_id UUID NOT NULL REFERENCES ci_schedules(schedule_id) ON DELETE CASCADE,
    fire_time TIMESTAMP WITH TIME ZONE NOT NULL,
    -- firing, fired, failed
    status TEXT NOT NULL DEFAULT 'firing' CHECK (status IN ('firing', 'fired', 'failed')),
    event_id UUID,
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    completed_at TIMESTAMP WITH TIME ZONE,
    UNIQUE (schedule_id, fire_time)
);

CREATE INDEX idx_ci_schedule_fires_schedule_time ON ci_schedule_fires(schedule_id, fire_time DESC);

-- +goose Down
DROP TABLE ci_schedule_fires;
DROP TABLE ci_schedules;
DROP TABLE coordinator_leases;
