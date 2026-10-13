-- +goose Up
-- The reconciler finds submitted and running nodes before it reads their jobs.
-- Index only those nodes to avoid a scan of all completed nodes on each pass.
-- The query uses these fixed statuses so generic prepared plans can use the index.
CREATE INDEX workflow_nodes_open_job_idx ON workflow_nodes(job_id)
    WHERE status IN ('submitted', 'running');

-- +goose Down
DROP INDEX IF EXISTS workflow_nodes_open_job_idx;
