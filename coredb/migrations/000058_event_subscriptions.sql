-- +goose Up
-- Outbound events (plan items E1 and E2, decisions D7, D22, D27).
--
-- 1. Triggers write one event_outbox row for each phase event, in the same
--    transaction as the status change that causes it. Thus a restart cannot
--    lose an event, and every write path is covered, also raw SQL.
-- 2. The dispatcher (one coordinator replica, lease "outbound-events")
--    assigns the per-workflow sequence, builds the payload, and makes one
--    event_deliveries row for each matching subscription.
-- 3. The dispatcher submits one Corndogs task for each delivery on an
--    internal queue. Only one delivery is in flight for each
--    (subscription, order key). A coordinator delivery loop claims the task
--    and calls the sink.
--
-- A trigger never stops a status change: an error in a trigger is logged as
-- a warning and the event is not recorded (the run continues).
--
-- A trigger writes nothing when the organization has no enabled
-- subscription (event_outbox_wanted, one index lookup). Thus an org with no
-- subscriptions adds no outbox rows. The coordinator also deletes
-- undispatched rows that are older than the retention, also when the
-- dispatcher does not run.

CREATE TABLE event_outbox (
    outbox_id bigserial PRIMARY KEY,
    -- The stable event ID (a ULID in UUID form). Receivers drop duplicates
    -- with it.
    event_id uuid NOT NULL DEFAULT generate_ulid() UNIQUE,
    schema_version integer NOT NULL DEFAULT 1,
    event_type text NOT NULL,
    occurred_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    project_id uuid,
    ci_event_id uuid,
    workflow_id uuid,
    workflow_security_id text NOT NULL DEFAULT '',
    node_id uuid,
    job_id uuid,
    -- The status of the entity after the change, and the result of a
    -- finished entity (empty for a phase that has no result).
    status text NOT NULL DEFAULT '',
    result text NOT NULL DEFAULT '',
    failure_class text NOT NULL DEFAULT '',
    actor_principal_id uuid,
    ref text NOT NULL DEFAULT '',
    commit_sha text NOT NULL DEFAULT '',
    ref_classes text[] NOT NULL DEFAULT '{}',
    -- Empty until workflow owners (WP7) exist.
    owner_group text NOT NULL DEFAULT '',
    -- Facts that only one event type has (names, attempt, exit code).
    details jsonb NOT NULL DEFAULT '{}',
    -- "workflow:<id>", "event:<id>", or "job:<id>". Events with the same key
    -- are delivered in sequence order to each subscription.
    order_key text NOT NULL,
    -- Set by the dispatcher.
    sequence bigint,
    payload jsonb,
    dispatched_at timestamp with time zone
);
-- The dispatcher reads undispatched rows in occurrence order.
CREATE INDEX event_outbox_pending_idx ON event_outbox(occurred_at, outbox_id) WHERE dispatched_at IS NULL;
CREATE INDEX event_outbox_occurred_idx ON event_outbox(occurred_at);

-- The last sequence number of each order key. Only the dispatcher writes it.
CREATE TABLE event_outbox_sequences (
    order_key text PRIMARY KEY,
    last_sequence bigint NOT NULL DEFAULT 0,
    updated_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now())
);

CREATE TABLE event_subscriptions (
    subscription_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    -- The principal that owns the subscription. A disabled owner receives
    -- nothing.
    owner_principal_id uuid NOT NULL REFERENCES principals(principal_id) ON DELETE CASCADE,
    scope text NOT NULL CHECK (scope IN ('org', 'project', 'workflow', 'owner_group')),
    project_id uuid REFERENCES projects(project_id) ON DELETE CASCADE,
    workflow_security_id text NOT NULL DEFAULT '',
    owner_group text NOT NULL DEFAULT '',
    -- Filters. An empty list matches everything.
    event_types text[] NOT NULL DEFAULT '{}',
    ref_classes_any text[] NOT NULL DEFAULT '{}',
    results text[] NOT NULL DEFAULT '{}',
    actor_kinds text[] NOT NULL DEFAULT '{}',
    sink_type text NOT NULL CHECK (sink_type IN ('webhook')),
    sink_config jsonb NOT NULL DEFAULT '{}',
    -- The HMAC signing secret, Fernet-encrypted under a master key.
    signing_secret_ciphertext bytea NOT NULL,
    signing_secret_key_name text NOT NULL,
    -- 0 uses the coordinator default.
    max_attempts integer NOT NULL DEFAULT 0 CHECK (max_attempts >= 0 AND max_attempts <= 100),
    enabled boolean NOT NULL DEFAULT true,
    created_by uuid,
    created_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    UNIQUE (org_id, name),
    CONSTRAINT event_subscriptions_scope_fields_check CHECK (
        (scope = 'org' AND project_id IS NULL AND workflow_security_id = '' AND owner_group = '')
        OR (scope = 'project' AND project_id IS NOT NULL AND workflow_security_id = '' AND owner_group = '')
        OR (scope = 'workflow' AND project_id IS NOT NULL AND workflow_security_id <> '' AND owner_group = '')
        OR (scope = 'owner_group' AND owner_group <> '' AND workflow_security_id = ''))
);
CREATE INDEX event_subscriptions_org_idx ON event_subscriptions(org_id) WHERE enabled;

CREATE TABLE event_deliveries (
    delivery_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    subscription_id uuid NOT NULL REFERENCES event_subscriptions(subscription_id) ON DELETE CASCADE,
    outbox_id bigint NOT NULL REFERENCES event_outbox(outbox_id) ON DELETE CASCADE,
    event_id uuid NOT NULL,
    event_type text NOT NULL,
    order_key text NOT NULL,
    sequence bigint NOT NULL,
    -- pending: waits for its turn or for next_attempt_at. queued: a Corndogs
    -- task exists. sending: the delivery loop calls the sink. delivered and
    -- dead_letter are final until a replay.
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'queued', 'sending', 'delivered', 'dead_letter')),
    attempts integer NOT NULL DEFAULT 0,
    -- Each submit has a new key: "<delivery_id>:<generation>". A task with an
    -- older key is stale, and the delivery loop completes it with no send.
    generation integer NOT NULL DEFAULT 0,
    task_key text NOT NULL DEFAULT '',
    task_id text NOT NULL DEFAULT '',
    next_attempt_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    last_http_status integer,
    last_latency_ms integer,
    last_error text NOT NULL DEFAULT '',
    queued_at timestamp with time zone,
    delivered_at timestamp with time zone,
    dead_lettered_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    UNIQUE (subscription_id, event_id)
);
CREATE INDEX event_deliveries_open_idx ON event_deliveries(subscription_id, order_key, sequence)
    WHERE status IN ('pending', 'queued', 'sending');
CREATE INDEX event_deliveries_subscription_idx ON event_deliveries(subscription_id, created_at DESC);
CREATE INDEX event_deliveries_inflight_idx ON event_deliveries(updated_at)
    WHERE status IN ('queued', 'sending');

-- One row for each send to the sink.
CREATE TABLE event_delivery_attempts (
    attempt_id bigserial PRIMARY KEY,
    delivery_id uuid NOT NULL REFERENCES event_deliveries(delivery_id) ON DELETE CASCADE,
    attempt integer NOT NULL,
    started_at timestamp with time zone NOT NULL,
    finished_at timestamp with time zone NOT NULL,
    result text NOT NULL CHECK (result IN ('delivered', 'failed', 'refused')),
    http_status integer,
    latency_ms integer NOT NULL DEFAULT 0,
    error text NOT NULL DEFAULT ''
);
CREATE INDEX event_delivery_attempts_delivery_idx ON event_delivery_attempts(delivery_id, attempt_id);

-- Whether an organization has an enabled subscription. The triggers call it
-- before they build a row, so an organization with no subscription pays one
-- lookup on event_subscriptions_org_idx and writes nothing.
-- +goose StatementBegin
CREATE FUNCTION event_outbox_wanted(org uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT org IS NOT NULL AND EXISTS (SELECT 1 FROM event_subscriptions s WHERE s.org_id = org AND s.enabled)
$$;
-- +goose StatementEnd

-- The order key of an event: its workflow, else its CI event, else its job.
-- +goose StatementBegin
CREATE FUNCTION event_outbox_order_key(workflow uuid, ci_event uuid, job uuid) RETURNS text
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE
        WHEN workflow IS NOT NULL THEN 'workflow:' || workflow::text
        WHEN ci_event IS NOT NULL THEN 'event:' || ci_event::text
        ELSE 'job:' || coalesce(job::text, '')
    END
$$;
-- +goose StatementEnd

-- The owner group of a row. Workflow owners (WP7) replace this function when
-- they add the column; until then it reads an "owner_group" value if one is
-- present and is empty otherwise.
-- +goose StatementBegin
CREATE FUNCTION event_outbox_owner_group(row_value jsonb) RETURNS text
LANGUAGE sql IMMUTABLE AS $$
    SELECT coalesce(row_value->>'owner_group', '')
$$;
-- +goose StatementEnd

-- Jobs: pre_start (scheduling: admitted, the container did not start),
-- started (running), post_complete (a final status). A job that goes to
-- running without scheduling gets pre_start and started.
-- +goose StatementBegin
CREATE FUNCTION event_outbox_on_job() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    old_status text;
    event_types text[];
    event_type_value text;
    job_result text := '';
BEGIN
    IF TG_OP = 'UPDATE' THEN
        old_status := OLD.status;
    END IF;
    IF old_status IS NOT DISTINCT FROM NEW.status THEN
        RETURN NULL;
    END IF;
    IF NEW.status = 'scheduling' THEN
        event_types := ARRAY['node.pre_start'];
    ELSIF NEW.status = 'running' THEN
        IF old_status = 'scheduling' THEN
            event_types := ARRAY['node.started'];
        ELSIF old_status = 'cancelling' THEN
            RETURN NULL;
        ELSE
            event_types := ARRAY['node.pre_start', 'node.started'];
        END IF;
    ELSIF NEW.status IN ('completed', 'failed', 'cancelled', 'timeout') THEN
        event_types := ARRAY['node.post_complete'];
        job_result := NEW.status;
    ELSE
        RETURN NULL;
    END IF;
    BEGIN
        IF NOT event_outbox_wanted(NEW.org_id) THEN
            RETURN NULL;
        END IF;
        FOREACH event_type_value IN ARRAY event_types LOOP
            INSERT INTO event_outbox (event_type, org_id, project_id, ci_event_id, workflow_id, node_id, job_id,
                status, result, failure_class, actor_principal_id, ref, commit_sha, ref_classes, owner_group,
                details, order_key)
            VALUES (event_type_value, NEW.org_id, NEW.project_id, NEW.event_id, NEW.workflow_id,
                NEW.workflow_node_id, NEW.job_id, NEW.status, job_result, coalesce(NEW.failure_class, ''),
                NEW.actor_principal_id, coalesce(NEW.source_ref, ''), coalesce(NEW.commit_sha, ''),
                coalesce(NEW.ref_classes, '{}'), event_outbox_owner_group(to_jsonb(NEW)),
                jsonb_build_object('job_name', NEW.name, 'node_name', coalesce(NEW.workflow_node_name, ''),
                    'attempt', coalesce(NEW.retry_count, 0) + 1, 'exit_code', NEW.exit_code),
                event_outbox_order_key(NEW.workflow_id, NEW.event_id, NEW.job_id));
        END LOOP;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'event outbox: event for job % not recorded: %', NEW.job_id, SQLERRM;
    END;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER event_outbox_job AFTER INSERT OR UPDATE OF status ON jobs
    FOR EACH ROW EXECUTE FUNCTION event_outbox_on_job();

-- Workflow nodes: skipped, and post_complete for a node that finished with
-- no job (for example not admitted, or cancelled before it started). A node
-- with a job gets its phase events from the job.
-- +goose StatementBegin
CREATE FUNCTION event_outbox_on_workflow_node() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    old_status text;
    event_type_value text;
    node_result text;
    wf record;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        old_status := OLD.status;
    END IF;
    IF old_status IS NOT DISTINCT FROM NEW.status THEN
        RETURN NULL;
    END IF;
    IF NEW.status = 'skipped' THEN
        event_type_value := 'node.skipped';
        node_result := 'skipped';
    ELSIF NEW.status IN ('failed', 'cancelled', 'timeout') AND NEW.job_id IS NULL THEN
        event_type_value := 'node.post_complete';
        node_result := NEW.status;
    ELSE
        RETURN NULL;
    END IF;
    BEGIN
        SELECT org_id, project_id, event_id, workflow_security_id, actor_principal_id, commit_sha,
               ref_classes, event_outbox_owner_group(to_jsonb(w)) AS owner_group
          INTO wf FROM workflow_instances w WHERE w.workflow_id = NEW.workflow_id;
        IF NOT FOUND OR NOT event_outbox_wanted(wf.org_id) THEN
            RETURN NULL;
        END IF;
        INSERT INTO event_outbox (event_type, org_id, project_id, ci_event_id, workflow_id, workflow_security_id,
            node_id, status, result, actor_principal_id, commit_sha, ref_classes, owner_group, details, order_key)
        VALUES (event_type_value, wf.org_id, wf.project_id, wf.event_id, NEW.workflow_id, wf.workflow_security_id,
            NEW.node_id, NEW.status, node_result, wf.actor_principal_id, coalesce(wf.commit_sha, ''),
            coalesce(wf.ref_classes, '{}'), wf.owner_group,
            jsonb_build_object('node_name', NEW.name, 'reason', coalesce(NEW.decision_reason, '')),
            event_outbox_order_key(NEW.workflow_id, NULL, NULL));
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'event outbox: event for workflow node % not recorded: %', NEW.node_id, SQLERRM;
    END;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER event_outbox_workflow_node AFTER INSERT OR UPDATE OF status ON workflow_nodes
    FOR EACH ROW EXECUTE FUNCTION event_outbox_on_workflow_node();

-- Workflows: completed, when the workflow gets a final status.
-- +goose StatementBegin
CREATE FUNCTION event_outbox_on_workflow() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    old_status text;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        old_status := OLD.status;
    END IF;
    IF old_status IS NOT DISTINCT FROM NEW.status
        OR NEW.status NOT IN ('success', 'failed', 'cancelled', 'skipped', 'not_admitted') THEN
        RETURN NULL;
    END IF;
    BEGIN
        IF NOT event_outbox_wanted(NEW.org_id) THEN
            RETURN NULL;
        END IF;
        INSERT INTO event_outbox (event_type, org_id, project_id, ci_event_id, workflow_id, workflow_security_id,
            status, result, actor_principal_id, commit_sha, ref_classes, owner_group, details, order_key)
        VALUES ('workflow.completed', NEW.org_id, NEW.project_id, NEW.event_id, NEW.workflow_id,
            NEW.workflow_security_id, NEW.status, NEW.status, NEW.actor_principal_id, coalesce(NEW.commit_sha, ''),
            coalesce(NEW.ref_classes, '{}'), event_outbox_owner_group(to_jsonb(NEW)),
            jsonb_build_object('workflow_name', NEW.name, 'error', coalesce(NEW.last_error, '')),
            event_outbox_order_key(NEW.workflow_id, NULL, NULL));
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'event outbox: event for workflow % not recorded: %', NEW.workflow_id, SQLERRM;
    END;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER event_outbox_workflow AFTER INSERT OR UPDATE OF status ON workflow_instances
    FOR EACH ROW EXECUTE FUNCTION event_outbox_on_workflow();

-- CI events: evaluated, when the eval of the event ends (evaluated or
-- failed).
-- +goose StatementBegin
CREATE FUNCTION event_outbox_on_ci_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    old_status text;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        old_status := OLD.status;
    END IF;
    IF old_status IS NOT DISTINCT FROM NEW.status OR NEW.status NOT IN ('evaluated', 'failed') THEN
        RETURN NULL;
    END IF;
    BEGIN
        IF NOT event_outbox_wanted(NEW.org_id) THEN
            RETURN NULL;
        END IF;
        INSERT INTO event_outbox (event_type, org_id, project_id, ci_event_id, job_id, status, result,
            actor_principal_id, ref, commit_sha, ref_classes, owner_group, details, order_key)
        VALUES ('event.evaluated', NEW.org_id, NEW.project_id, NEW.event_id, NEW.eval_job_id, NEW.status,
            NEW.status, NEW.actor_principal_id, coalesce(nullif(NEW.ref, ''), NEW.branch), NEW.commit_sha,
            coalesce(NEW.ref_classes, '{}'), event_outbox_owner_group(to_jsonb(NEW)),
            jsonb_build_object('source', NEW.source, 'event_type', NEW.event_type, 'pr_number', NEW.pr_number),
            event_outbox_order_key(NULL, NEW.event_id, NULL));
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'event outbox: event for CI event % not recorded: %', NEW.event_id, SQLERRM;
    END;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER event_outbox_ci_event AFTER INSERT OR UPDATE OF status ON ci_events
    FOR EACH ROW EXECUTE FUNCTION event_outbox_on_ci_event();

-- Workflow decisions of an event: admitted (decision run) and not_admitted.
-- +goose StatementBegin
CREATE FUNCTION event_outbox_on_workflow_decision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    event_type_value text;
    ev record;
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.decision IS NOT DISTINCT FROM NEW.decision THEN
        RETURN NULL;
    END IF;
    IF NEW.decision = 'run' THEN
        event_type_value := 'event.admitted';
    ELSIF NEW.decision = 'not_admitted' THEN
        event_type_value := 'event.not_admitted';
    ELSE
        RETURN NULL;
    END IF;
    BEGIN
        SELECT org_id, project_id, actor_principal_id, coalesce(nullif(ref, ''), branch) AS ref, commit_sha,
               ref_classes, event_outbox_owner_group(to_jsonb(e)) AS owner_group
          INTO ev FROM ci_events e WHERE e.event_id = NEW.event_id;
        IF NOT FOUND OR NOT event_outbox_wanted(ev.org_id) THEN
            RETURN NULL;
        END IF;
        INSERT INTO event_outbox (event_type, org_id, project_id, ci_event_id, workflow_id, workflow_security_id,
            status, result, actor_principal_id, ref, commit_sha, ref_classes, owner_group, details, order_key)
        VALUES (event_type_value, ev.org_id, ev.project_id, NEW.event_id, NEW.workflow_id, NEW.workflow_security_id,
            NEW.decision, NEW.decision, ev.actor_principal_id, ev.ref, ev.commit_sha, coalesce(ev.ref_classes, '{}'),
            ev.owner_group,
            jsonb_build_object('workflow_name', NEW.workflow_name, 'source_file', NEW.source_file),
            event_outbox_order_key(NEW.workflow_id, NEW.event_id, NULL));
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'event outbox: decision event for CI event % not recorded: %', NEW.event_id, SQLERRM;
    END;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER event_outbox_workflow_decision AFTER INSERT OR UPDATE OF decision ON ci_event_workflow_decisions
    FOR EACH ROW EXECUTE FUNCTION event_outbox_on_workflow_decision();

-- +goose Down
DROP TRIGGER IF EXISTS event_outbox_workflow_decision ON ci_event_workflow_decisions;
DROP TRIGGER IF EXISTS event_outbox_ci_event ON ci_events;
DROP TRIGGER IF EXISTS event_outbox_workflow ON workflow_instances;
DROP TRIGGER IF EXISTS event_outbox_workflow_node ON workflow_nodes;
DROP TRIGGER IF EXISTS event_outbox_job ON jobs;
DROP FUNCTION IF EXISTS event_outbox_on_workflow_decision();
DROP FUNCTION IF EXISTS event_outbox_on_ci_event();
DROP FUNCTION IF EXISTS event_outbox_on_workflow();
DROP FUNCTION IF EXISTS event_outbox_on_workflow_node();
DROP FUNCTION IF EXISTS event_outbox_on_job();
DROP FUNCTION IF EXISTS event_outbox_owner_group(jsonb);
DROP FUNCTION IF EXISTS event_outbox_order_key(uuid, uuid, uuid);
DROP FUNCTION IF EXISTS event_outbox_wanted(uuid);
DROP TABLE IF EXISTS event_delivery_attempts;
DROP TABLE IF EXISTS event_deliveries;
DROP TABLE IF EXISTS event_subscriptions;
DROP TABLE IF EXISTS event_outbox_sequences;
DROP TABLE IF EXISTS event_outbox;
