-- +goose Up
-- Dedicated cancel-mode column for graceful-cancel vs kill routing.
--
-- Previously, jobs.last_error doubled as the cancel/kill marker: jobcontrol
-- wrote the sentinel string "kill requested" into last_error alongside
-- status='cancelling', and job_processor.go's pollForCancel string-compared
-- against it to decide between a graceful JobRunner.Stop and an immediate
-- forced Cleanup. That meant a job mid-cancel showed a fake "kill
-- requested" error to anyone viewing it, and a graceful cancel had to
-- explicitly blank last_error to avoid leaving a stale value behind. This
-- column replaces that scheme: cancel_mode carries the request type, and
-- last_error is reserved for genuine terminal error text again.
ALTER TABLE jobs ADD COLUMN cancel_mode text;
-- '' is allowed alongside NULL because the Go model stores this as a plain
-- string whose zero value ('' = "no cancel requested") is what GORM inserts
-- for every new job.
ALTER TABLE jobs ADD CONSTRAINT jobs_cancel_mode_check CHECK (cancel_mode IS NULL OR cancel_mode IN ('', 'cancel', 'kill'));

-- +goose Down
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_cancel_mode_check;
ALTER TABLE jobs DROP COLUMN IF EXISTS cancel_mode;
