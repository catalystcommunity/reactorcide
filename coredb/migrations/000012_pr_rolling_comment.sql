-- +goose Up
-- Denormalize VCS metadata onto jobs so we can efficiently query
-- "all jobs for a given (repo, pr_number, commit_sha)" without parsing
-- the Notes JSON. Notes stays the source of truth; these columns are a
-- query-performance denormalization populated at job-creation time.
ALTER TABLE jobs ADD COLUMN vcs_repo text;
ALTER TABLE jobs ADD COLUMN pr_number integer;
ALTER TABLE jobs ADD COLUMN commit_sha text;

CREATE INDEX jobs_pr_idx ON jobs (vcs_repo, pr_number, commit_sha)
  WHERE pr_number IS NOT NULL;

-- Tracks merged PRs so the status-update flow can switch from the
-- per-(PR, commit) rolling comment to per-job result comments.
CREATE TABLE pr_merged (
  repo        text        NOT NULL,
  pr_number   integer     NOT NULL,
  merged_at   timestamptz NOT NULL DEFAULT timezone('utc', now()),
  PRIMARY KEY (repo, pr_number)
);

-- +goose Down
DROP TABLE IF EXISTS pr_merged;
DROP INDEX IF EXISTS jobs_pr_idx;
ALTER TABLE jobs DROP COLUMN IF EXISTS commit_sha;
ALTER TABLE jobs DROP COLUMN IF EXISTS pr_number;
ALTER TABLE jobs DROP COLUMN IF EXISTS vcs_repo;
