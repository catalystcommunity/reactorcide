-- +goose Up
-- One "Reactorcide Checks" commit status per CI event. The last published
-- state is kept on the event so a recompute that finds the same state does
-- not post again, and so the aggregate is read from the database, not from
-- the memory of one coordinator replica.
ALTER TABLE ci_events ADD COLUMN checks_state text NOT NULL DEFAULT '';
ALTER TABLE ci_events ADD COLUMN checks_description text NOT NULL DEFAULT '';
ALTER TABLE ci_events ADD COLUMN checks_target_url text NOT NULL DEFAULT '';
ALTER TABLE ci_events ADD COLUMN checks_published_at timestamp with time zone;
-- CI files the coordinator policy refused for this event. A refusal fails
-- the event's checks; base CI still runs.
ALTER TABLE ci_events ADD COLUMN policy_violation_count integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE ci_events DROP COLUMN policy_violation_count;
ALTER TABLE ci_events DROP COLUMN checks_published_at;
ALTER TABLE ci_events DROP COLUMN checks_target_url;
ALTER TABLE ci_events DROP COLUMN checks_description;
ALTER TABLE ci_events DROP COLUMN checks_state;
