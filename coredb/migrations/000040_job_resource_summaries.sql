-- +goose Up
-- One row per finished job: a resource summary computed from the job's stored
-- telemetry (the job roll-up series only). The UI reads these rows to chart
-- the history of one job across its runs. Run metadata (status, times, ref)
-- stays on jobs; this table holds only measurements.
--
-- Every measurement column is nullable: NULL means "not measured for this
-- run", which is different from a measured zero. A run with no telemetry
-- still gets a row with sample_count 0, so it is not computed again.
-- summary_version records the algorithm. A row with a lower version than the
-- coordinator's is computed again on read.
CREATE TABLE job_resource_summaries (
    job_id uuid PRIMARY KEY REFERENCES jobs(job_id) ON DELETE CASCADE,
    summary_version integer NOT NULL,
    computed_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    sample_count integer NOT NULL DEFAULT 0,
    cpu_avg_millicores bigint,
    cpu_p95_millicores bigint,
    cpu_max_millicores bigint,
    cpu_peak_at timestamp with time zone,
    cpu_request_millicores bigint,
    cpu_limit_millicores bigint,
    memory_avg_bytes bigint,
    memory_p95_bytes bigint,
    memory_max_bytes bigint,
    memory_peak_at timestamp with time zone,
    memory_limit_bytes bigint,
    storage_max_bytes bigint
);

-- The history read: every run of one job (same project and name), newest
-- first. jobs_name_idx alone scans every run of a common name in every project.
CREATE INDEX jobs_project_name_created_idx ON jobs(project_id, name, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS jobs_project_name_created_idx;
DROP TABLE IF EXISTS job_resource_summaries;
