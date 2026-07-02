-- +goose Up
-- Private VCS and job secret grant plumbing.
--
-- Existing users act as orgs until first-class org membership exists.
ALTER TABLE users ADD COLUMN vcs_token_secrets jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE users ADD COLUMN webhook_secrets jsonb NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE projects ADD COLUMN user_id uuid REFERENCES users(user_id) ON DELETE SET NULL;
ALTER TABLE projects ADD COLUMN vcs_token_secrets jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE projects ADD COLUMN webhook_secrets jsonb NOT NULL DEFAULT '{}'::jsonb;
CREATE INDEX projects_user_id_idx ON projects(user_id);

ALTER TABLE jobs ADD COLUMN job_file text;

CREATE TABLE secret_grants (
  grant_id uuid DEFAULT generate_ulid() PRIMARY KEY,
  created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  updated_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  user_id uuid NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  project_id uuid REFERENCES projects(project_id) ON DELETE CASCADE,
  secret_path_prefix text NOT NULL,
  job_name text,
  job_file text,
  description text
);

CREATE INDEX secret_grants_user_id_idx ON secret_grants(user_id);
CREATE INDEX secret_grants_project_id_idx ON secret_grants(project_id);
CREATE INDEX secret_grants_lookup_idx ON secret_grants(user_id, project_id, secret_path_prefix);

-- +goose Down
DROP INDEX IF EXISTS secret_grants_lookup_idx;
DROP INDEX IF EXISTS secret_grants_project_id_idx;
DROP INDEX IF EXISTS secret_grants_user_id_idx;
DROP TABLE IF EXISTS secret_grants;
ALTER TABLE jobs DROP COLUMN IF EXISTS job_file;
DROP INDEX IF EXISTS projects_user_id_idx;
ALTER TABLE projects DROP COLUMN IF EXISTS webhook_secrets;
ALTER TABLE projects DROP COLUMN IF EXISTS vcs_token_secrets;
ALTER TABLE projects DROP COLUMN IF EXISTS user_id;
ALTER TABLE users DROP COLUMN IF EXISTS webhook_secrets;
ALTER TABLE users DROP COLUMN IF EXISTS vcs_token_secrets;
