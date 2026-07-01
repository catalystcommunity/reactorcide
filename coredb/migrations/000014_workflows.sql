-- +goose Up
CREATE TABLE workflow_instances (
  workflow_id uuid DEFAULT generate_ulid() PRIMARY KEY,
  created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  updated_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  user_id uuid NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  project_id uuid REFERENCES projects(project_id) ON DELETE SET NULL,
  parent_job_id uuid REFERENCES jobs(job_id) ON DELETE SET NULL,
  name text NOT NULL,
  status text NOT NULL DEFAULT 'evaluating',
  queue_name text NOT NULL DEFAULT 'reactorcide-jobs',
  vcs_provider text,
  vcs_repo text,
  pr_number integer,
  commit_sha text,
  status_context text NOT NULL DEFAULT 'Reactorcide Jobs',
  comment_marker text,
  completed_at timestamp,
  last_error text
);

CREATE INDEX workflow_instances_status_idx ON workflow_instances(status);
CREATE INDEX workflow_instances_pr_idx ON workflow_instances(vcs_repo, pr_number, commit_sha)
  WHERE pr_number IS NOT NULL;

CREATE TABLE workflow_nodes (
  node_id uuid DEFAULT generate_ulid() PRIMARY KEY,
  created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  updated_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  workflow_id uuid NOT NULL REFERENCES workflow_instances(workflow_id) ON DELETE CASCADE,
  name text NOT NULL,
  display_name text NOT NULL,
  status text NOT NULL DEFAULT 'pending',
  depends_on text[],
  condition text NOT NULL DEFAULT 'all_success',
  job_id uuid REFERENCES jobs(job_id) ON DELETE SET NULL,
  job_spec jsonb,
  item_index integer,
  item_value jsonb,
  item_var text,
  decision_reason text,
  completed_at timestamp,
  last_successful_duration_ms bigint
);

CREATE INDEX workflow_nodes_workflow_idx ON workflow_nodes(workflow_id);
CREATE INDEX workflow_nodes_job_idx ON workflow_nodes(job_id);
CREATE INDEX workflow_nodes_name_idx ON workflow_nodes(workflow_id, name);

CREATE TABLE workflow_vars (
  workflow_id uuid NOT NULL REFERENCES workflow_instances(workflow_id) ON DELETE CASCADE,
  key text NOT NULL,
  value jsonb,
  value_hash text NOT NULL,
  source_node_id uuid REFERENCES workflow_nodes(node_id) ON DELETE SET NULL,
  source_job_id uuid REFERENCES jobs(job_id) ON DELETE SET NULL,
  created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  updated_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  PRIMARY KEY (workflow_id, key)
);

CREATE TABLE workflow_events (
  event_id uuid DEFAULT generate_ulid() PRIMARY KEY,
  created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  workflow_id uuid NOT NULL REFERENCES workflow_instances(workflow_id) ON DELETE CASCADE,
  node_id uuid REFERENCES workflow_nodes(node_id) ON DELETE SET NULL,
  job_id uuid REFERENCES jobs(job_id) ON DELETE SET NULL,
  event_type text NOT NULL,
  reason text,
  details jsonb
);

CREATE INDEX workflow_events_workflow_idx ON workflow_events(workflow_id, created_at);
CREATE INDEX workflow_events_node_idx ON workflow_events(node_id);

ALTER TABLE jobs ADD COLUMN workflow_id uuid REFERENCES workflow_instances(workflow_id) ON DELETE SET NULL;
ALTER TABLE jobs ADD COLUMN workflow_node_id uuid REFERENCES workflow_nodes(node_id) ON DELETE SET NULL;
ALTER TABLE jobs ADD COLUMN workflow_run_id uuid;
ALTER TABLE jobs ADD COLUMN workflow_node_name text;

CREATE INDEX jobs_workflow_idx ON jobs(workflow_id);
CREATE INDEX jobs_workflow_node_idx ON jobs(workflow_node_id);

-- +goose Down
DROP INDEX IF EXISTS jobs_workflow_node_idx;
DROP INDEX IF EXISTS jobs_workflow_idx;
ALTER TABLE jobs DROP COLUMN IF EXISTS workflow_node_name;
ALTER TABLE jobs DROP COLUMN IF EXISTS workflow_run_id;
ALTER TABLE jobs DROP COLUMN IF EXISTS workflow_node_id;
ALTER TABLE jobs DROP COLUMN IF EXISTS workflow_id;
DROP TABLE IF EXISTS workflow_events;
DROP TABLE IF EXISTS workflow_vars;
DROP TABLE IF EXISTS workflow_nodes;
DROP TABLE IF EXISTS workflow_instances;
