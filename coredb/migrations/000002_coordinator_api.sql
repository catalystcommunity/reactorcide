-- +goose Up
-- Fix syntax error from baseline migration
ALTER TABLE tasks ALTER COLUMN task_name SET DEFAULT 'task';

-- Add new roles for coordinator API
ALTER TYPE user_role ADD VALUE 'support';
ALTER TYPE user_role ADD VALUE 'admin';

-- Enhance users table for coordinator API
ALTER TABLE users ADD COLUMN username text;
ALTER TABLE users ADD COLUMN email text;
ALTER TABLE users ADD COLUMN password bytea;
ALTER TABLE users ADD COLUMN salt bytea;

-- Add unique constraints for data integrity
ALTER TABLE users ADD CONSTRAINT users_email_unique UNIQUE (email);
ALTER TABLE users ADD CONSTRAINT users_username_unique UNIQUE (username);

-- Update default roles to include 'user'
ALTER TABLE users ALTER COLUMN roles SET DEFAULT ARRAY['user'::user_role];

-- API tokens for authentication
CREATE TABLE api_tokens (
    token_id uuid DEFAULT generate_ulid() PRIMARY KEY,
    created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
    updated_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
    user_id uuid NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    token_hash bytea NOT NULL, -- SHA256 hash of the actual token
    name text NOT NULL, -- Human-readable name for the token
    expires_at timestamp, -- NULL for no expiration
    last_used_at timestamp,
    is_active boolean DEFAULT true NOT NULL
);

CREATE INDEX api_tokens_user_id_idx ON api_tokens(user_id);
CREATE INDEX api_tokens_token_hash_idx ON api_tokens(token_hash);
CREATE INDEX api_tokens_is_active_idx ON api_tokens(is_active);

-- Jobs table for coordinator API
CREATE TABLE jobs (
    job_id uuid DEFAULT generate_ulid() PRIMARY KEY,
    created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
    updated_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
    user_id uuid NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    
    -- Job metadata
    name text NOT NULL,
    description text,
    
    -- Source configuration
    git_url text, -- Git repository URL
    git_ref text, -- Branch, tag, or commit hash
    source_type text NOT NULL CHECK (source_type IN ('git', 'copy')), -- 'git' or 'copy'
    source_path text, -- For copy operations, local path
    
    -- Runnerlib configuration
    code_dir text NOT NULL DEFAULT '/job/src',
    job_dir text NOT NULL DEFAULT '/job/src', 
    job_command text NOT NULL, -- Command to execute
    runner_image text NOT NULL DEFAULT 'quay.io/catalystcommunity/reactorcide_runner',
    job_env_vars jsonb, -- Environment variables as JSON object
    job_env_file text, -- Path to environment file within job directory
    
    -- Job execution settings
    timeout_seconds integer DEFAULT 3600, -- 1 hour default timeout
    priority integer DEFAULT 0, -- Higher numbers = higher priority
    
    -- Queue integration
    queue_name text NOT NULL DEFAULT 'reactorcide-jobs',
    auto_target_state text DEFAULT 'running',
    
    -- Current state
    status text NOT NULL DEFAULT 'submitted' CHECK (status IN (
        'submitted', 'queued', 'running', 'completed', 'failed', 'cancelled', 'timeout'
    )),
    corndogs_task_id uuid, -- Reference to corndogs task
    
    -- Execution metadata
    started_at timestamp,
    completed_at timestamp,
    exit_code integer,
    
    -- Object store references for logs and artifacts
    logs_object_key text, -- Object store key for logs
    artifacts_object_key text -- Object store key for artifacts/outputs
);

-- Indexes for efficient querying
CREATE INDEX jobs_user_id_idx ON jobs(user_id);
CREATE INDEX jobs_status_idx ON jobs(status);
CREATE INDEX jobs_created_at_idx ON jobs(created_at);
CREATE INDEX jobs_queue_name_idx ON jobs(queue_name);
CREATE INDEX jobs_corndogs_task_id_idx ON jobs(corndogs_task_id);
CREATE INDEX jobs_name_idx ON jobs(name);

-- +goose Down
-- Drop jobs table and related indexes
DROP TABLE IF EXISTS jobs;

-- Drop api_tokens table and related indexes
DROP TABLE IF EXISTS api_tokens;

-- Remove constraints and columns from users table
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_email_unique;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_username_unique;
ALTER TABLE users DROP COLUMN IF EXISTS username;
ALTER TABLE users DROP COLUMN IF EXISTS email;
ALTER TABLE users DROP COLUMN IF EXISTS password;
ALTER TABLE users DROP COLUMN IF EXISTS salt;

-- Note: Cannot easily remove enum values in PostgreSQL, so we leave the roles as-is
-- Also cannot easily revert the tasks table fix