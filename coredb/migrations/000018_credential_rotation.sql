-- +goose Up
-- Per-project webhook secret / VCS credential rotation tracking.
-- Legacy jsonb/text columns on projects remain as fallback in
-- credential_refs.go resolution order; these tables allow N rows per
-- (project, provider) so a new credential can be added and verified before
-- the old one is deactivated.

CREATE TABLE project_webhook_secrets (
  id uuid DEFAULT generate_ulid() PRIMARY KEY,
  project_id uuid NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
  provider text NOT NULL,
  name text NOT NULL,
  secret_ref text NOT NULL,
  is_active boolean NOT NULL DEFAULT true,
  last_used_at timestamp,
  created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  deactivated_at timestamp,
  UNIQUE (project_id, provider, name)
);

CREATE INDEX project_webhook_secrets_project_id_idx ON project_webhook_secrets(project_id);
CREATE INDEX project_webhook_secrets_lookup_idx ON project_webhook_secrets(project_id, provider, is_active);

CREATE TABLE project_vcs_credentials (
  id uuid DEFAULT generate_ulid() PRIMARY KEY,
  project_id uuid NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
  provider text NOT NULL,
  name text NOT NULL,
  secret_ref text NOT NULL,
  is_active boolean NOT NULL DEFAULT true,
  last_used_at timestamp,
  created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  deactivated_at timestamp,
  UNIQUE (project_id, provider, name)
);

CREATE INDEX project_vcs_credentials_project_id_idx ON project_vcs_credentials(project_id);
CREATE INDEX project_vcs_credentials_lookup_idx ON project_vcs_credentials(project_id, provider, is_active);

-- +goose Down
DROP INDEX IF EXISTS project_vcs_credentials_lookup_idx;
DROP INDEX IF EXISTS project_vcs_credentials_project_id_idx;
DROP TABLE IF EXISTS project_vcs_credentials;

DROP INDEX IF EXISTS project_webhook_secrets_lookup_idx;
DROP INDEX IF EXISTS project_webhook_secrets_project_id_idx;
DROP TABLE IF EXISTS project_webhook_secrets;
