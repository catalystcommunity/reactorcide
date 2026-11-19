-- +goose Up
-- WP15: job artifacts, test results, flaky tests, test quarantine, and
-- signed provenance (docs/artifacts-and-tests.md, docs/provenance.md).
--
-- Every table is new. No existing row changes, so current jobs, projects,
-- and organizations keep their behavior. A job that writes no files to its
-- artifacts directory has no rows here.

-- The files that a job publishes. The worker uploads them through the
-- coordinator in chunks; the coordinator writes the chunks to the object
-- store under storage_prefix and computes the size and the SHA-256 digest
-- itself. hash_state holds the SHA-256 state between chunks, so that any
-- replica can take the next chunk. kind:
--   file         a normal file
--   test_report  a JUnit XML report (by name convention or declaration)
--   provenance   the signed provenance envelope that the coordinator writes
CREATE TABLE job_artifacts (
    artifact_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    job_id uuid NOT NULL REFERENCES jobs(job_id) ON DELETE CASCADE,
    name text NOT NULL,
    path text NOT NULL,
    kind text NOT NULL DEFAULT 'file' CHECK (kind IN ('file', 'test_report', 'provenance')),
    media_type text NOT NULL DEFAULT 'application/octet-stream',
    size_bytes bigint NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    sha256 text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'uploading' CHECK (status IN ('uploading', 'complete', 'failed')),
    status_reason text NOT NULL DEFAULT '',
    artifact_namespace text NOT NULL DEFAULT '',
    storage_prefix text NOT NULL,
    chunk_count integer NOT NULL DEFAULT 0 CHECK (chunk_count >= 0),
    hash_state bytea,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    completed_at timestamp,
    CHECK (char_length(path) BETWEEN 1 AND 512),
    CHECK (char_length(name) BETWEEN 1 AND 512),
    CHECK (status <> 'complete' OR sha256 ~ '^[0-9a-f]{64}$')
);
CREATE UNIQUE INDEX job_artifacts_job_kind_path_idx ON job_artifacts (job_id, kind, path);
CREATE INDEX job_artifacts_job_idx ON job_artifacts (job_id, created_at);

-- The results of each test case in the JUnit reports of a job. test_id is a
-- stable ID: a hash of the project, the suite, the class, and the name.
CREATE TABLE test_results (
    result_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    job_id uuid NOT NULL REFERENCES jobs(job_id) ON DELETE CASCADE,
    project_id uuid REFERENCES projects(project_id) ON DELETE CASCADE,
    artifact_id uuid REFERENCES job_artifacts(artifact_id) ON DELETE SET NULL,
    test_id text NOT NULL,
    suite text NOT NULL DEFAULT '',
    classname text NOT NULL DEFAULT '',
    name text NOT NULL,
    status text NOT NULL CHECK (status IN ('pass', 'fail', 'error', 'skip')),
    duration_ms bigint NOT NULL DEFAULT 0,
    message text NOT NULL DEFAULT '',
    file text NOT NULL DEFAULT '',
    line integer,
    commit_sha text NOT NULL DEFAULT '',
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    CHECK (char_length(message) <= 4096)
);
CREATE INDEX test_results_job_idx ON test_results (job_id, status);
CREATE INDEX test_results_project_sha_idx ON test_results (project_id, commit_sha, test_id);
CREATE INDEX test_results_project_test_idx ON test_results (project_id, test_id, created_at DESC);

-- The test summary of a job, computed when the job result arrives.
-- quarantine_override is true when the job failed, every failed test of its
-- reports is quarantined, and its workflow node has
-- quarantine_policy: ignore_quarantined. The job status stays "failed"; only
-- the workflow node counts as a success.
CREATE TABLE job_test_summaries (
    job_id uuid PRIMARY KEY REFERENCES jobs(job_id) ON DELETE CASCADE,
    reports integer NOT NULL DEFAULT 0,
    total integer NOT NULL DEFAULT 0,
    passed integer NOT NULL DEFAULT 0,
    failed integer NOT NULL DEFAULT 0,
    errored integer NOT NULL DEFAULT 0,
    skipped integer NOT NULL DEFAULT 0,
    quarantined_failures integer NOT NULL DEFAULT 0,
    flaky integer NOT NULL DEFAULT 0,
    truncated boolean NOT NULL DEFAULT false,
    parse_errors jsonb NOT NULL DEFAULT '[]'::jsonb,
    quarantine_policy text NOT NULL DEFAULT '',
    quarantine_override boolean NOT NULL DEFAULT false,
    -- True when the workflow output of the job declared test_exit: true
    -- (the job states that its non-zero exit came from the test runner).
    -- The quarantine override applies only then.
    test_exit boolean NOT NULL DEFAULT false,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now())
);

-- Flakiness of one test in one project. A test is flaky on a commit when it
-- passed and failed (or errored) on that commit, also across retries and
-- re-runs, within the detection window. test_flaky_commits records each
-- such commit one time, so flaky_count counts commits, not results.
CREATE TABLE test_flakiness (
    project_id uuid NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    test_id text NOT NULL,
    suite text NOT NULL DEFAULT '',
    classname text NOT NULL DEFAULT '',
    name text NOT NULL DEFAULT '',
    flaky_count integer NOT NULL DEFAULT 0,
    total_runs integer NOT NULL DEFAULT 0,
    last_flaky_at timestamp,
    last_flaky_sha text NOT NULL DEFAULT '',
    last_seen_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    PRIMARY KEY (project_id, test_id)
);
CREATE INDEX test_flakiness_last_flaky_idx ON test_flakiness (project_id, last_flaky_at DESC) WHERE flaky_count > 0;

CREATE TABLE test_flaky_commits (
    project_id uuid NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    test_id text NOT NULL,
    commit_sha text NOT NULL,
    detected_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    PRIMARY KEY (project_id, test_id, commit_sha)
);

-- Test quarantine (plan item A4). A quarantine has an owner principal, a
-- reason, and a required expiry. At the expiry it ends with no action. A
-- quarantined failure does not count in the PR comment test summary, and it
-- does not fail a workflow node that has quarantine_policy:
-- ignore_quarantined. The job exit code is not changed.
CREATE TABLE test_quarantines (
    quarantine_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    project_id uuid NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    test_id text NOT NULL,
    suite text NOT NULL DEFAULT '',
    classname text NOT NULL DEFAULT '',
    name text NOT NULL DEFAULT '',
    owner_principal_id uuid NOT NULL REFERENCES principals(principal_id),
    reason text NOT NULL CHECK (char_length(btrim(reason)) BETWEEN 1 AND 1000),
    expires_at timestamp NOT NULL,
    created_by_principal_id uuid REFERENCES principals(principal_id) ON DELETE SET NULL,
    created_by text NOT NULL DEFAULT '',
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    removed_at timestamp,
    removed_by text NOT NULL DEFAULT '',
    CHECK (expires_at > created_at)
);
CREATE UNIQUE INDEX test_quarantines_active_idx ON test_quarantines (project_id, test_id) WHERE removed_at IS NULL;
CREATE INDEX test_quarantines_project_idx ON test_quarantines (project_id, expires_at);

-- The provenance signing keys. Same shape as oidc_signing_keys, separate
-- key purpose: a provenance key never signs an OIDC token, and an OIDC key
-- never signs provenance.
CREATE TABLE provenance_signing_keys (
    kid text PRIMARY KEY,
    algorithm text NOT NULL CHECK (algorithm IN ('ES256', 'RS256')),
    public_jwk jsonb NOT NULL,
    master_key_id uuid NOT NULL REFERENCES master_keys(key_id),
    encrypted_private_key bytea NOT NULL,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    activates_at timestamp NOT NULL,
    retires_at timestamp
);
CREATE INDEX provenance_signing_keys_activates_idx ON provenance_signing_keys (activates_at);

-- +goose Down
DROP TABLE provenance_signing_keys;
DROP TABLE test_quarantines;
DROP TABLE test_flaky_commits;
DROP TABLE test_flakiness;
DROP TABLE job_test_summaries;
DROP TABLE test_results;
DROP TABLE job_artifacts;
