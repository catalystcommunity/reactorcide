-- +goose Up
-- Structured reasons for a node decision: the job's `if:` predicates and the
-- dependency outcome. decision_reason stays as the human summary.
ALTER TABLE workflow_nodes ADD COLUMN decision_reasons jsonb;

-- +goose Down
ALTER TABLE workflow_nodes DROP COLUMN decision_reasons;
