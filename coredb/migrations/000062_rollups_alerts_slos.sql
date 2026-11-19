-- +goose Up
-- WP16: rollups, pipeline owner alerts, and SLOs (plan items F2, F3, F4).
-- See docs/observability.md.
--
-- The rollup loop (internal/observability, lease "observability-rollups")
-- reads finished jobs and CI events and writes the rollup tables. It keeps
-- a watermark for each source in observability_state. When a state row is
-- missing, the loop does a backfill over all existing rows first. Thus this
-- migration copies no data: it only adds tables, columns, indexes, and one
-- trigger. Existing rows do not change.

-- The time of the first commit status that Reactorcide published for a CI
-- event. Eval latency is first_status_at - received_at. checks_published_at
-- holds the LAST publish, so a trigger keeps the first one. Events from before
-- this migration stay NULL: their first publish time is not known.
ALTER TABLE ci_events ADD COLUMN first_status_at timestamp with time zone;

-- +goose StatementBegin
CREATE FUNCTION ci_events_first_status() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF OLD.first_status_at IS NOT NULL THEN
            NEW.first_status_at := OLD.first_status_at;
            RETURN NEW;
        END IF;
        IF NEW.first_status_at IS NULL AND NEW.checks_published_at IS NOT NULL
           AND OLD.checks_published_at IS NULL THEN
            NEW.first_status_at := NEW.checks_published_at;
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.first_status_at IS NULL AND NEW.checks_published_at IS NOT NULL THEN
        NEW.first_status_at := NEW.checks_published_at;
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER ci_events_first_status BEFORE INSERT OR UPDATE ON ci_events
    FOR EACH ROW EXECUTE FUNCTION ci_events_first_status();

CREATE INDEX ci_events_first_status_idx ON ci_events (first_status_at) WHERE first_status_at IS NOT NULL;
-- The rollup loop reads finished jobs by completion time, and summaries by
-- computation time.
CREATE INDEX jobs_completed_at_idx ON jobs (completed_at) WHERE completed_at IS NOT NULL;
CREATE INDEX job_resource_summaries_computed_idx ON job_resource_summaries (computed_at);

-- Some paths finish a job with no completed_at (a cancel before a worker
-- took the job, a submit failure). From now on a trigger sets it. Old rows
-- are not changed; the rollup loop uses updated_at for them, through this
-- small partial index.
-- +goose StatementBegin
CREATE FUNCTION jobs_terminal_completed_at() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.completed_at IS NULL AND NEW.status IN ('completed', 'failed', 'cancelled', 'timeout') THEN
        NEW.completed_at := timezone('utc', now());
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER jobs_terminal_completed_at BEFORE INSERT OR UPDATE ON jobs
    FOR EACH ROW EXECUTE FUNCTION jobs_terminal_completed_at();
CREATE INDEX jobs_terminal_no_completed_idx ON jobs (updated_at)
    WHERE completed_at IS NULL AND status IN ('completed', 'failed', 'cancelled', 'timeout');

-- Job rollups. One row for each bucket and dimension set. Dimensions:
-- project (NULL for a job with no project), workflow security ID ('' for a
-- job with no workflow), owner group ('' for none or unknown), priority lane
-- ('' for a job from before lanes), ref class bucket (default, tag, pr,
-- schedule, manual, other), and actor kind (human, service, system).
--
-- Counts: runs = every finished job. successes = completed. failed_* are
-- failures by failure class; failed_unknown is a failed job with no class.
-- The duration histogram has only pipeline runs (completed, job, timeout,
-- oom), so an infrastructure failure never changes a pipeline duration.
-- The queue wait histogram has every run that got a worker.
-- Histograms use the fixed bounds of internal/observability (Bounds): a
-- histogram has len(Bounds)+1 counts, the last one is +Inf.
CREATE TABLE job_rollups_hourly (
    bucket_start timestamp with time zone NOT NULL,
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    project_id uuid REFERENCES projects(project_id) ON DELETE CASCADE,
    workflow_security_id text NOT NULL DEFAULT '',
    owner_group text NOT NULL DEFAULT '',
    lane text NOT NULL DEFAULT '',
    ref_class_bucket text NOT NULL DEFAULT 'other',
    actor_kind text NOT NULL DEFAULT 'system',
    runs integer NOT NULL DEFAULT 0,
    successes integer NOT NULL DEFAULT 0,
    failed_job integer NOT NULL DEFAULT 0,
    failed_timeout integer NOT NULL DEFAULT 0,
    failed_oom integer NOT NULL DEFAULT 0,
    failed_image_pull integer NOT NULL DEFAULT 0,
    failed_scheduling integer NOT NULL DEFAULT 0,
    failed_worker_lost integer NOT NULL DEFAULT 0,
    failed_platform integer NOT NULL DEFAULT 0,
    failed_unknown integer NOT NULL DEFAULT 0,
    cancelled integer NOT NULL DEFAULT 0,
    duration_p50_seconds double precision,
    duration_p95_seconds double precision,
    duration_sum_seconds double precision NOT NULL DEFAULT 0,
    duration_hist integer[] NOT NULL DEFAULT '{}',
    queue_wait_p50_seconds double precision,
    queue_wait_p95_seconds double precision,
    queue_wait_sum_seconds double precision NOT NULL DEFAULT 0,
    queue_wait_hist integer[] NOT NULL DEFAULT '{}',
    -- Resources from job_resource_summaries: usage (average x run time) and
    -- reservation (CPU request, memory limit x run time). measured_runs is
    -- the count of runs that have a summary with samples.
    measured_runs integer NOT NULL DEFAULT 0,
    cpu_core_hours double precision NOT NULL DEFAULT 0,
    memory_gib_hours double precision NOT NULL DEFAULT 0,
    cpu_reserved_core_hours double precision NOT NULL DEFAULT 0,
    memory_reserved_gib_hours double precision NOT NULL DEFAULT 0,
    computed_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now())
);
CREATE UNIQUE INDEX job_rollups_hourly_key_idx ON job_rollups_hourly (
    bucket_start, org_id, coalesce(project_id, '00000000-0000-0000-0000-000000000000'::uuid),
    workflow_security_id, owner_group, lane, ref_class_bucket, actor_kind);
CREATE INDEX job_rollups_hourly_org_idx ON job_rollups_hourly (org_id, bucket_start);
CREATE INDEX job_rollups_hourly_project_idx ON job_rollups_hourly (project_id, workflow_security_id, bucket_start);

CREATE TABLE job_rollups_daily (LIKE job_rollups_hourly INCLUDING DEFAULTS INCLUDING CONSTRAINTS);
ALTER TABLE job_rollups_daily ADD CONSTRAINT job_rollups_daily_org_fk
    FOREIGN KEY (org_id) REFERENCES organizations(org_id) ON DELETE CASCADE;
ALTER TABLE job_rollups_daily ADD CONSTRAINT job_rollups_daily_project_fk
    FOREIGN KEY (project_id) REFERENCES projects(project_id) ON DELETE CASCADE;
CREATE UNIQUE INDEX job_rollups_daily_key_idx ON job_rollups_daily (
    bucket_start, org_id, coalesce(project_id, '00000000-0000-0000-0000-000000000000'::uuid),
    workflow_security_id, owner_group, lane, ref_class_bucket, actor_kind);
CREATE INDEX job_rollups_daily_org_idx ON job_rollups_daily (org_id, bucket_start);
CREATE INDEX job_rollups_daily_project_idx ON job_rollups_daily (project_id, workflow_security_id, bucket_start);

-- Usage by the groups of the actor at the time of the run (from
-- group_membership_history). A run of an actor in two groups counts in
-- both. Daily only.
CREATE TABLE job_actor_group_rollups_daily (
    bucket_start timestamp with time zone NOT NULL,
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    group_id uuid NOT NULL,
    group_name text NOT NULL DEFAULT '',
    runs integer NOT NULL DEFAULT 0,
    successes integer NOT NULL DEFAULT 0,
    pipeline_failures integer NOT NULL DEFAULT 0,
    infra_failures integer NOT NULL DEFAULT 0,
    duration_sum_seconds double precision NOT NULL DEFAULT 0,
    cpu_core_hours double precision NOT NULL DEFAULT 0,
    memory_gib_hours double precision NOT NULL DEFAULT 0,
    cpu_reserved_core_hours double precision NOT NULL DEFAULT 0,
    computed_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    PRIMARY KEY (bucket_start, org_id, group_id)
);
CREATE INDEX job_actor_group_rollups_org_idx ON job_actor_group_rollups_daily (org_id, bucket_start);

-- Eval latency rollups: CI events by the hour (or day) of their first
-- status. source is the event source (webhook, schedule, manual, ...).
CREATE TABLE eval_rollups_hourly (
    bucket_start timestamp with time zone NOT NULL,
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    project_id uuid REFERENCES projects(project_id) ON DELETE CASCADE,
    source text NOT NULL DEFAULT '',
    events integer NOT NULL DEFAULT 0,
    latency_p50_seconds double precision,
    latency_p95_seconds double precision,
    latency_sum_seconds double precision NOT NULL DEFAULT 0,
    latency_hist integer[] NOT NULL DEFAULT '{}',
    computed_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now())
);
CREATE UNIQUE INDEX eval_rollups_hourly_key_idx ON eval_rollups_hourly (
    bucket_start, org_id, coalesce(project_id, '00000000-0000-0000-0000-000000000000'::uuid), source);
CREATE INDEX eval_rollups_hourly_org_idx ON eval_rollups_hourly (org_id, bucket_start);

CREATE TABLE eval_rollups_daily (LIKE eval_rollups_hourly INCLUDING DEFAULTS INCLUDING CONSTRAINTS);
ALTER TABLE eval_rollups_daily ADD CONSTRAINT eval_rollups_daily_org_fk
    FOREIGN KEY (org_id) REFERENCES organizations(org_id) ON DELETE CASCADE;
ALTER TABLE eval_rollups_daily ADD CONSTRAINT eval_rollups_daily_project_fk
    FOREIGN KEY (project_id) REFERENCES projects(project_id) ON DELETE CASCADE;
CREATE UNIQUE INDEX eval_rollups_daily_key_idx ON eval_rollups_daily (
    bucket_start, org_id, coalesce(project_id, '00000000-0000-0000-0000-000000000000'::uuid), source);
CREATE INDEX eval_rollups_daily_org_idx ON eval_rollups_daily (org_id, bucket_start);

-- Watermarks of the rollup loop: 'jobs' (completed_at), 'summaries'
-- (job_resource_summaries.computed_at), 'events' (first_status_at).
CREATE TABLE observability_state (
    name text PRIMARY KEY,
    watermark timestamp with time zone NOT NULL,
    backfilled_at timestamp with time zone,
    last_run_at timestamp with time zone,
    last_error text NOT NULL DEFAULT '',
    updated_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now())
);

-- Control-plane availability: the lease holder of the rollup loop sets one
-- bit for each minute in which it ran and could write to the database. Bit
-- N of up_mask is minute N of the hour.
CREATE TABLE control_plane_heartbeats (
    hour_start timestamp with time zone PRIMARY KEY,
    up_mask bigint NOT NULL DEFAULT 0,
    updated_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now())
);

-- Pipeline owner alert rules (F3). Only the failure classes job, timeout,
-- and oom count: an infrastructure failure never pages a pipeline owner.
CREATE TABLE alert_rules (
    rule_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    scope text NOT NULL,
    project_id uuid REFERENCES projects(project_id) ON DELETE CASCADE,
    workflow_security_id text NOT NULL DEFAULT '',
    owner_group text NOT NULL DEFAULT '',
    -- pass_rate_below: pass rate < threshold over window_hours.
    -- duration_regression: p95 duration > baseline p95 x factor; the
    -- baseline is the baseline_days before the window.
    condition text NOT NULL,
    -- '' is every ref class bucket.
    ref_class_bucket text NOT NULL DEFAULT 'default',
    threshold double precision NOT NULL DEFAULT 0.9,
    factor double precision NOT NULL DEFAULT 1.5,
    window_hours integer NOT NULL DEFAULT 24,
    baseline_days integer NOT NULL DEFAULT 7,
    min_runs integer NOT NULL DEFAULT 5,
    -- Hysteresis: a firing rule resolves only when the value is better than
    -- the threshold by this margin, for resolve_after evaluations in a row.
    resolve_margin double precision NOT NULL DEFAULT 0.05,
    fire_after integer NOT NULL DEFAULT 2,
    resolve_after integer NOT NULL DEFAULT 2,
    enabled boolean NOT NULL DEFAULT true,
    state text NOT NULL DEFAULT 'ok',
    breach_streak integer NOT NULL DEFAULT 0,
    recover_streak integer NOT NULL DEFAULT 0,
    last_value double precision,
    last_baseline double precision,
    last_runs integer NOT NULL DEFAULT 0,
    last_evaluated_at timestamp with time zone,
    state_changed_at timestamp with time zone,
    created_by uuid,
    created_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    UNIQUE (org_id, name),
    CONSTRAINT alert_rules_name_format CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    CONSTRAINT alert_rules_scope_values CHECK (scope IN ('workflow', 'project', 'owner_group')),
    CONSTRAINT alert_rules_condition_values CHECK (condition IN ('pass_rate_below', 'duration_regression')),
    CONSTRAINT alert_rules_state_values CHECK (state IN ('ok', 'firing')),
    CONSTRAINT alert_rules_scope_fields CHECK (
        (scope = 'workflow' AND project_id IS NOT NULL AND workflow_security_id <> '' AND owner_group = '')
        OR (scope = 'project' AND project_id IS NOT NULL AND workflow_security_id = '' AND owner_group = '')
        OR (scope = 'owner_group' AND owner_group <> '' AND workflow_security_id = '')),
    CONSTRAINT alert_rules_bounds CHECK (
        threshold >= 0 AND threshold <= 1 AND factor >= 1 AND factor <= 100
        AND window_hours BETWEEN 1 AND 720 AND baseline_days BETWEEN 1 AND 90
        AND min_runs BETWEEN 1 AND 100000 AND resolve_margin >= 0 AND resolve_margin < 1
        AND fire_after BETWEEN 1 AND 100 AND resolve_after BETWEEN 1 AND 100)
);
CREATE INDEX alert_rules_enabled_idx ON alert_rules (org_id) WHERE enabled;

-- Each firing and each resolve of a rule. event_id is the outbox event.
CREATE TABLE alert_transitions (
    transition_id bigserial PRIMARY KEY,
    rule_id uuid NOT NULL REFERENCES alert_rules(rule_id) ON DELETE CASCADE,
    org_id uuid NOT NULL,
    state text NOT NULL CHECK (state IN ('firing', 'resolved')),
    value double precision,
    baseline double precision,
    threshold double precision,
    runs integer NOT NULL DEFAULT 0,
    event_id uuid,
    occurred_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now())
);
CREATE INDEX alert_transitions_rule_idx ON alert_transitions (rule_id, occurred_at DESC);

-- SLO targets (F4). A missing row uses the built-in default
-- (internal/observability DefaultTargets). lane '' is the target of every
-- lane that has no row of its own; only queue_wait and infra_failure_rate
-- have lane targets.
CREATE TABLE slo_targets (
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    sli text NOT NULL,
    lane text NOT NULL DEFAULT '',
    objective double precision NOT NULL,
    threshold_seconds double precision NOT NULL DEFAULT 0,
    window_days integer NOT NULL DEFAULT 30,
    updated_by uuid,
    updated_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    PRIMARY KEY (org_id, sli, lane),
    CONSTRAINT slo_targets_sli_values CHECK (sli IN ('eval_latency', 'queue_wait', 'infra_failure_rate', 'availability')),
    CONSTRAINT slo_targets_lane_scope CHECK (lane = '' OR sli IN ('queue_wait', 'infra_failure_rate')),
    CONSTRAINT slo_targets_bounds CHECK (objective > 0 AND objective < 1 AND threshold_seconds >= 0
        AND window_days BETWEEN 1 AND 90)
);

-- +goose Down
DROP TABLE IF EXISTS slo_targets;
DROP TABLE IF EXISTS alert_transitions;
DROP TABLE IF EXISTS alert_rules;
DROP TABLE IF EXISTS control_plane_heartbeats;
DROP TABLE IF EXISTS observability_state;
DROP TABLE IF EXISTS eval_rollups_daily;
DROP TABLE IF EXISTS eval_rollups_hourly;
DROP TABLE IF EXISTS job_actor_group_rollups_daily;
DROP TABLE IF EXISTS job_rollups_daily;
DROP TABLE IF EXISTS job_rollups_hourly;
DROP INDEX IF EXISTS job_resource_summaries_computed_idx;
DROP INDEX IF EXISTS jobs_terminal_no_completed_idx;
DROP TRIGGER IF EXISTS jobs_terminal_completed_at ON jobs;
DROP FUNCTION IF EXISTS jobs_terminal_completed_at();
DROP INDEX IF EXISTS jobs_completed_at_idx;
DROP INDEX IF EXISTS ci_events_first_status_idx;
DROP TRIGGER IF EXISTS ci_events_first_status ON ci_events;
DROP FUNCTION IF EXISTS ci_events_first_status();
ALTER TABLE ci_events DROP COLUMN IF EXISTS first_status_at;
