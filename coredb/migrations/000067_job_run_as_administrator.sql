-- +goose Up
-- Job run_as.administrator. See docs/runtime-behavior.md "Run Identity".
--
-- This value applies only to Windows VM jobs. When the guest image has no
-- job account, the in-guest daemon creates it. This column says whether the
-- new account goes in Administrators. NULL means that the job did not set
-- the value, and the guest uses its default (administrator).
--
-- This migration adds one nullable column with no default value. Existing
-- jobs keep NULL. v0.12.x does not write the column, so it continues to work.
ALTER TABLE jobs
    ADD COLUMN run_as_administrator boolean;

-- +goose Down
ALTER TABLE jobs DROP COLUMN run_as_administrator;
