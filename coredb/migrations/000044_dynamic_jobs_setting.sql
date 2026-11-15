-- +goose Up
-- Whether a running job may create jobs at runtime (runnerlib trigger). The
-- eval result of an event is not a dynamic job and is not affected. The
-- project value inherits the organization value unless set.
ALTER TABLE organizations ADD COLUMN dynamic_jobs text NOT NULL DEFAULT 'allow'
    CHECK (dynamic_jobs IN ('allow', 'deny'));
ALTER TABLE projects ADD COLUMN dynamic_jobs text NOT NULL DEFAULT 'inherit'
    CHECK (dynamic_jobs IN ('inherit', 'allow', 'deny'));

-- +goose Down
ALTER TABLE projects DROP COLUMN dynamic_jobs;
ALTER TABLE organizations DROP COLUMN dynamic_jobs;
