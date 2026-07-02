-- +goose Up
-- Make secret grants name-addressable and pattern based.
ALTER TABLE secret_grants ADD COLUMN name text;
ALTER TABLE secret_grants ADD COLUMN secret_path_match text NOT NULL DEFAULT 'prefix';
ALTER TABLE secret_grants ADD COLUMN secret_path_pattern text;
ALTER TABLE secret_grants ADD COLUMN job_name_match text NOT NULL DEFAULT 'any';
ALTER TABLE secret_grants ADD COLUMN job_name_pattern text NOT NULL DEFAULT '';

UPDATE secret_grants
SET
  name = 'grant-' || grant_id::text,
  secret_path_pattern = secret_path_prefix,
  job_name_match = CASE WHEN coalesce(job_name, '') = '' THEN 'any' ELSE 'exact' END,
  job_name_pattern = coalesce(job_name, '');

ALTER TABLE secret_grants ALTER COLUMN name SET NOT NULL;
ALTER TABLE secret_grants ALTER COLUMN secret_path_pattern SET NOT NULL;

ALTER TABLE secret_grants
  ADD CONSTRAINT secret_grants_secret_path_match_check
  CHECK (secret_path_match IN ('exact', 'prefix', 'glob', 'regex'));

ALTER TABLE secret_grants
  ADD CONSTRAINT secret_grants_job_name_match_check
  CHECK (job_name_match IN ('any', 'exact', 'prefix', 'glob', 'regex'));

CREATE UNIQUE INDEX secret_grants_user_project_name_unique
  ON secret_grants (user_id, coalesce(project_id, '00000000-0000-0000-0000-000000000000'::uuid), name);

DROP INDEX IF EXISTS secret_grants_lookup_idx;
CREATE INDEX secret_grants_lookup_idx
  ON secret_grants (user_id, project_id, secret_path_pattern);

ALTER TABLE secret_grants DROP COLUMN job_file;
ALTER TABLE secret_grants DROP COLUMN secret_path_prefix;
ALTER TABLE secret_grants DROP COLUMN job_name;

-- +goose Down
ALTER TABLE secret_grants ADD COLUMN secret_path_prefix text;
ALTER TABLE secret_grants ADD COLUMN job_name text;
ALTER TABLE secret_grants ADD COLUMN job_file text;

UPDATE secret_grants
SET
  secret_path_prefix = secret_path_pattern,
  job_name = CASE WHEN job_name_match = 'any' THEN '' ELSE job_name_pattern END,
  job_file = '';

ALTER TABLE secret_grants ALTER COLUMN secret_path_prefix SET NOT NULL;

DROP INDEX IF EXISTS secret_grants_lookup_idx;
DROP INDEX IF EXISTS secret_grants_user_project_name_unique;
ALTER TABLE secret_grants DROP CONSTRAINT IF EXISTS secret_grants_job_name_match_check;
ALTER TABLE secret_grants DROP CONSTRAINT IF EXISTS secret_grants_secret_path_match_check;

ALTER TABLE secret_grants DROP COLUMN job_name_pattern;
ALTER TABLE secret_grants DROP COLUMN job_name_match;
ALTER TABLE secret_grants DROP COLUMN secret_path_pattern;
ALTER TABLE secret_grants DROP COLUMN secret_path_match;
ALTER TABLE secret_grants DROP COLUMN name;

CREATE INDEX secret_grants_lookup_idx ON secret_grants(user_id, project_id, secret_path_prefix);
