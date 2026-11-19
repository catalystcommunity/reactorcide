-- +goose Up
-- WP17: diagnosis entries from principals (plan item F6). See
-- docs/diagnosis.md.
--
-- A diagnosis tool is a service principal. It reads the logs of a failed
-- job with its own grants and posts one diagnosis entry for the job or the
-- workflow. One principal has at most one entry for each target: a new post
-- replaces the entry of that principal. Reactorcide shows the entries on the
-- job and workflow pages and in the rolling pull request comment.
--
-- This migration adds a new table and one new column with an empty default.
-- It changes no existing row and no existing behavior.
CREATE TABLE diagnosis_reports (
    diagnosis_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    project_id uuid REFERENCES projects(project_id) ON DELETE CASCADE,
    -- 'job': the entry is about one job (also a workflow node: node_name is
    -- then set). 'workflow': the entry is about a whole workflow.
    target_type text NOT NULL CHECK (target_type IN ('job', 'workflow')),
    job_id uuid REFERENCES jobs(job_id) ON DELETE CASCADE,
    workflow_id uuid REFERENCES workflow_instances(workflow_id) ON DELETE CASCADE,
    node_name text NOT NULL DEFAULT '',
    author_principal_id uuid NOT NULL REFERENCES principals(principal_id) ON DELETE CASCADE,
    -- The kind and the display name of the author when it wrote the entry.
    author_kind text NOT NULL CHECK (author_kind IN ('human', 'service', 'system')),
    author_display_name text NOT NULL DEFAULT '',
    title text NOT NULL,
    -- Markdown, cleaned of control characters. The coordinator sanitizes it
    -- for the pull request comment; the UI shows it as plain text.
    body text NOT NULL DEFAULT '',
    confidence text NOT NULL DEFAULT '' CHECK (confidence IN ('', 'low', 'medium', 'high')),
    category text NOT NULL DEFAULT '',
    -- [{"name": "...", "url": "https://..."}]
    links jsonb NOT NULL DEFAULT '[]',
    -- Validated run shifts (internal/runshifts), or NULL.
    suggested_shifts jsonb,
    revision integer NOT NULL DEFAULT 1,
    created_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    CONSTRAINT diagnosis_reports_target_fields_check CHECK (
        (target_type = 'job' AND job_id IS NOT NULL)
        OR (target_type = 'workflow' AND workflow_id IS NOT NULL AND job_id IS NULL AND node_name = '')
    ),
    CONSTRAINT diagnosis_reports_title_length_check CHECK (char_length(title) BETWEEN 1 AND 200),
    CONSTRAINT diagnosis_reports_body_length_check CHECK (octet_length(body) <= 16384)
);
CREATE UNIQUE INDEX diagnosis_reports_job_author_idx
    ON diagnosis_reports (job_id, author_principal_id) WHERE target_type = 'job';
CREATE UNIQUE INDEX diagnosis_reports_workflow_author_idx
    ON diagnosis_reports (workflow_id, author_principal_id) WHERE target_type = 'workflow';
CREATE INDEX diagnosis_reports_workflow_idx ON diagnosis_reports (workflow_id) WHERE workflow_id IS NOT NULL;

-- A subscription filter on the failure class of node events (for example
-- job, timeout, oom). Empty matches every event, as before.
ALTER TABLE event_subscriptions ADD COLUMN failure_classes text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE event_subscriptions DROP COLUMN failure_classes;
DROP TABLE diagnosis_reports;
