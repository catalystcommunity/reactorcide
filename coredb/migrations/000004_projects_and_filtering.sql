-- +goose Up
-- Create source type enum for consistent typing across projects and jobs
CREATE TYPE source_type AS ENUM ('git', 'copy', 'none');

-- Create projects table for repository configuration and event filtering
CREATE TABLE projects (
    project_id uuid DEFAULT generate_ulid() PRIMARY KEY,
    created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
    updated_at timestamp DEFAULT timezone('utc', now()) NOT NULL,

    -- Project identification
    name text NOT NULL,
    description text,
    -- Repository URL in canonical form: github.com/org/repo (no protocol, no .git suffix)
    -- Webhook URLs will be normalized in code to match this format
    repo_url text NOT NULL,

    -- Event filtering configuration
    enabled boolean DEFAULT true NOT NULL,
    target_branches text[] DEFAULT ARRAY['main', 'master', 'develop'], -- Branches to trigger CI on
    allowed_event_types text[] DEFAULT ARRAY['push', 'pull_request'], -- Event types to process

    -- Default CI source configuration (trusted CI code)
    default_ci_source_type source_type DEFAULT 'git',
    default_ci_source_url text, -- Default CI pipeline code repository (canonical form)
    default_ci_source_ref text DEFAULT 'main', -- Default branch/tag for CI code

    -- Job defaults
    default_runner_image text DEFAULT 'quay.io/catalystcommunity/reactorcide_runner',
    default_job_command text, -- Default command if not specified
    default_timeout_seconds integer DEFAULT 3600,
    default_queue_name text DEFAULT 'reactorcide-jobs',

    CONSTRAINT projects_repo_url_unique UNIQUE (repo_url)
);

CREATE INDEX projects_repo_url_idx ON projects(repo_url);
CREATE INDEX projects_enabled_idx ON projects(enabled);

-- Add project reference and CI source fields to jobs table
ALTER TABLE jobs ADD COLUMN project_id uuid REFERENCES projects(project_id) ON DELETE SET NULL;

-- Add CI source fields (trusted CI pipeline code, separate from source under test)
ALTER TABLE jobs ADD COLUMN ci_source_type source_type;
ALTER TABLE jobs ADD COLUMN ci_source_url text;
ALTER TABLE jobs ADD COLUMN ci_source_ref text;

-- Make existing source fields nullable (source checkout is now optional)
ALTER TABLE jobs ALTER COLUMN git_url DROP NOT NULL;
ALTER TABLE jobs ALTER COLUMN git_ref DROP NOT NULL;
ALTER TABLE jobs ALTER COLUMN source_path DROP NOT NULL;

-- Update source_type column to use the new enum type
-- First, drop the check constraint
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_source_type_check;
-- Convert the column to the enum type
ALTER TABLE jobs ALTER COLUMN source_type TYPE source_type USING source_type::source_type;
-- Now it can be nullable
ALTER TABLE jobs ALTER COLUMN source_type DROP NOT NULL;

-- Add container image field to jobs (allows custom images per job)
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS container_image text;

-- Add worker tracking fields (some may already exist from previous migrations)
-- Using ALTER TABLE IF NOT EXISTS is not supported in older PostgreSQL, so we use individual statements
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS worker_id text;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS notes text;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS retry_count integer DEFAULT 0;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS last_error text;

-- Create index on project_id for efficient queries
CREATE INDEX jobs_project_id_idx ON jobs(project_id);

-- +goose Down
-- Remove indexes
DROP INDEX IF EXISTS jobs_project_id_idx;
DROP INDEX IF EXISTS projects_enabled_idx;
DROP INDEX IF EXISTS projects_repo_url_idx;

-- Remove columns from jobs table
ALTER TABLE jobs DROP COLUMN IF EXISTS last_error;
ALTER TABLE jobs DROP COLUMN IF EXISTS retry_count;
ALTER TABLE jobs DROP COLUMN IF EXISTS notes;
ALTER TABLE jobs DROP COLUMN IF EXISTS worker_id;
ALTER TABLE jobs DROP COLUMN IF EXISTS container_image;
ALTER TABLE jobs DROP COLUMN IF EXISTS ci_source_ref;
ALTER TABLE jobs DROP COLUMN IF EXISTS ci_source_url;
ALTER TABLE jobs DROP COLUMN IF EXISTS ci_source_type;
ALTER TABLE jobs DROP COLUMN IF EXISTS project_id;

-- Restore the check constraint and NOT NULL on source_type
ALTER TABLE jobs ALTER COLUMN source_type SET NOT NULL;
ALTER TABLE jobs ADD CONSTRAINT jobs_source_type_check CHECK (source_type IN ('git', 'copy'));

-- Restore NOT NULL constraints on other source fields
ALTER TABLE jobs ALTER COLUMN source_path SET NOT NULL;
ALTER TABLE jobs ALTER COLUMN git_ref SET NOT NULL;
ALTER TABLE jobs ALTER COLUMN git_url SET NOT NULL;

-- Drop projects table
DROP TABLE IF EXISTS projects;

-- Drop the source_type enum
DROP TYPE IF EXISTS source_type;
