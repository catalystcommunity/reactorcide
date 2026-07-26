-- +goose Up
-- Per-job compute resource columns (WORKERS_PLAN.md "Resources").
--
-- Values are Kubernetes-style quantity strings ("1", "2", "500m" for CPU;
-- "4Gi", "512Mi" for memory) so they map directly onto k8s
-- requests/limits and are convertible for the Docker runner (nanocpus /
-- bytes). Memory is limit-only -- no memory request column -- mirroring the
-- JobConfig/runner shape (see internal/worker/interfaces.go JobConfig).
--
-- The NOT NULL DEFAULTs mean any row inserted without explicitly setting
-- these columns already lands on the reactorcide defaults (cpu.request=1,
-- cpu.limit=2, memory.limit=4Gi) at the DB layer; explicit values supplied
-- via a job spec's `resources` block are parsed/validated by
-- internal/resources.ParseResources before being written here.
ALTER TABLE jobs ADD COLUMN resource_cpu_request text NOT NULL DEFAULT '1';
ALTER TABLE jobs ADD COLUMN resource_cpu_limit text NOT NULL DEFAULT '2';
ALTER TABLE jobs ADD COLUMN resource_memory_limit text NOT NULL DEFAULT '4Gi';

-- +goose Down
ALTER TABLE jobs DROP COLUMN IF EXISTS resource_memory_limit;
ALTER TABLE jobs DROP COLUMN IF EXISTS resource_cpu_limit;
ALTER TABLE jobs DROP COLUMN IF EXISTS resource_cpu_request;
