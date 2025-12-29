-- +goose Up
-- Rename git-specific field names to VCS-agnostic names
-- This supports the DESIGN.md principle of VCS agnosticism -
-- the system should work with git, mercurial, svn, or any source control

-- Rename git_url to source_url (more generic, works for any VCS)
ALTER TABLE jobs RENAME COLUMN git_url TO source_url;

-- Rename git_ref to source_ref (branch, tag, commit, changeset, etc.)
ALTER TABLE jobs RENAME COLUMN git_ref TO source_ref;

-- The source_type enum already supports this abstraction with values:
-- 'git', 'copy', 'none' - and can be extended for 'hg', 'svn', etc.

-- +goose Down
-- Revert to git-specific names
ALTER TABLE jobs RENAME COLUMN source_ref TO git_ref;
ALTER TABLE jobs RENAME COLUMN source_url TO git_url;
