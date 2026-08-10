-- +goose Up
-- Workers: registration, auth, protocol ( "Workers --
-- registration, auth, protocol", "Schema"). Five tables land together here:
-- worker_pools (admin grouping only -- pools do NOT
-- participate in characteristic matching), pool_enrollment_tokens (the only
-- human-handled worker credential, hash-only per checkauth.HashAPIToken),
-- workers (one row per enrolled worker process, upserted by worker_key),
-- worker_sessions (ephemeral, process-lifetime, hash-only, mirrors
-- ui_sessions), and worker_leases (a display/audit ledger whose lifetime
-- tracks the corndogs task timeout -- no invented lease TTL/deadline
-- column).
--
-- Numbered 000022: 000020 (queues) and 000021 (job resources) already exist
-- already exist; goose.WithAllowMissing() (see cmd/migrate.go) tolerates this
-- landing after other in-flight branches, so the number only needs to be
-- unique and greater than what's already applied.

CREATE TABLE worker_pools (
    pool_id uuid DEFAULT generate_ulid() PRIMARY KEY,
    org_id uuid REFERENCES users(user_id),
    name text NOT NULL,
    description text,
    created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
    updated_at timestamp DEFAULT timezone('utc', now()) NOT NULL
);

-- NULLs are distinct for uniqueness purposes in Postgres, so this does NOT
-- prevent two global (org_id IS NULL) pools from sharing a name -- only
-- same-org name collisions are rejected. Acceptable: global pools are an
-- admin-only surface expected to be few and deliberately named.
CREATE UNIQUE INDEX worker_pools_org_id_name_idx ON worker_pools(org_id, name);

CREATE TABLE pool_enrollment_tokens (
    token_id uuid DEFAULT generate_ulid() PRIMARY KEY,
    pool_id uuid NOT NULL REFERENCES worker_pools(pool_id) ON DELETE CASCADE,
    name text,
    token_hash bytea NOT NULL,
    is_active boolean NOT NULL DEFAULT true,
    last_used_at timestamp,
    created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
    deactivated_at timestamp
);

-- Doubles as the lookup index (Validate hashes the presented token and
-- selects by exact match) and the uniqueness guarantee -- one unique index
-- serves both, same style as queues_characteristics_hash_idx (000020).
CREATE UNIQUE INDEX pool_enrollment_tokens_token_hash_idx ON pool_enrollment_tokens(token_hash);
CREATE INDEX pool_enrollment_tokens_pool_id_idx ON pool_enrollment_tokens(pool_id);

CREATE TABLE workers (
    worker_id uuid DEFAULT generate_ulid() PRIMARY KEY,
    pool_id uuid NOT NULL REFERENCES worker_pools(pool_id),
    worker_key text NOT NULL,
    hostname text,
    os text NOT NULL,
    arch text NOT NULL,
    characteristics jsonb NOT NULL DEFAULT '{}',
    worker_version text,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'quarantined', 'disabled')),
    last_seen_at timestamp,
    enrolled_at timestamp,
    created_at timestamp DEFAULT timezone('utc', now()) NOT NULL
);

-- worker_key is a stable, worker-provided identifier (generated + persisted
-- locally by the worker, like a host GUID) that Register upserts by -- see
-- postgres_store.UpsertWorkerByKey.
CREATE UNIQUE INDEX workers_worker_key_idx ON workers(worker_key);
CREATE INDEX workers_pool_id_idx ON workers(pool_id);

CREATE TABLE worker_sessions (
    session_id uuid DEFAULT generate_ulid() PRIMARY KEY,
    worker_id uuid NOT NULL REFERENCES workers(worker_id) ON DELETE CASCADE,
    token_hash bytea NOT NULL,
    expires_at timestamp NOT NULL,
    last_seen_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
    created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
    revoked_at timestamp
);

CREATE UNIQUE INDEX worker_sessions_token_hash_idx ON worker_sessions(token_hash);
CREATE INDEX worker_sessions_worker_id_idx ON worker_sessions(worker_id);

CREATE TABLE worker_leases (
    lease_id uuid DEFAULT generate_ulid() PRIMARY KEY,
    worker_id uuid NOT NULL REFERENCES workers(worker_id),
    job_id uuid NOT NULL REFERENCES jobs(job_id),
    queue_uuid uuid,
    acquired_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
    released_at timestamp,
    outcome text
);

CREATE INDEX worker_leases_worker_id_released_at_idx ON worker_leases(worker_id, released_at);
-- Partial index supporting the future reaper's stale-active-lease scan
-- (ListStaleActiveLeases): only still-open leases are ever queried by age.
CREATE INDEX worker_leases_active_acquired_at_idx ON worker_leases(acquired_at) WHERE released_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS worker_leases_active_acquired_at_idx;
DROP INDEX IF EXISTS worker_leases_worker_id_released_at_idx;
DROP TABLE IF EXISTS worker_leases;

DROP INDEX IF EXISTS worker_sessions_worker_id_idx;
DROP INDEX IF EXISTS worker_sessions_token_hash_idx;
DROP TABLE IF EXISTS worker_sessions;

DROP INDEX IF EXISTS workers_pool_id_idx;
DROP INDEX IF EXISTS workers_worker_key_idx;
DROP TABLE IF EXISTS workers;

DROP INDEX IF EXISTS pool_enrollment_tokens_pool_id_idx;
DROP INDEX IF EXISTS pool_enrollment_tokens_token_hash_idx;
DROP TABLE IF EXISTS pool_enrollment_tokens;

DROP INDEX IF EXISTS worker_pools_org_id_name_idx;
DROP TABLE IF EXISTS worker_pools;
