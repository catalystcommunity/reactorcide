-- +goose Up
-- Secure defaults, draft pull requests, and shed levels (plan items A5, G3,
-- D4, owner decision D15).
--
-- defaults_profile records which defaults an organization uses:
--   legacy: the behavior before this migration. A project with no stored CI
--           policy runs the CI of a same-repository pull request head.
--   secure: run_gate outside_contributors, dynamic_jobs deny, and a built-in
--           CI policy for a project with no stored policy (CI from base; head
--           CI only through override-ci or a head_ci rule).
-- Every existing organization becomes legacy, so its behavior does not
-- change. The coordinator writes secure when it creates a new organization.
ALTER TABLE organizations ADD COLUMN defaults_profile text NOT NULL DEFAULT 'legacy'
    CHECK (defaults_profile IN ('legacy', 'secure'));

-- What a draft pull request does. run is the previous behavior.
ALTER TABLE organizations ADD COLUMN draft_prs text NOT NULL DEFAULT 'run'
    CHECK (draft_prs IN ('run', 'skip', 'skip_when_shedding'));
ALTER TABLE projects ADD COLUMN draft_prs text NOT NULL DEFAULT 'inherit'
    CHECK (draft_prs IN ('inherit', 'run', 'skip', 'skip_when_shedding'));

-- The shed level of the organization. The level in force is the higher of
-- this level and the global level (global_settings key shed_level).
ALTER TABLE organizations ADD COLUMN shed_level text NOT NULL DEFAULT 'normal'
    CHECK (shed_level IN ('normal', 'degraded', 'critical'));
-- The actions of each shed level for this organization. NULL uses the
-- built-in actions.
ALTER TABLE organizations ADD COLUMN shed_actions jsonb;

-- A webhook delivery over the rate limit is recorded as rate_limited, so a
-- redelivery can evaluate it. A draft pull request or shed work that the
-- coordinator did not evaluate is skipped; the summary holds the reason.
ALTER TABLE ci_events DROP CONSTRAINT ci_events_status_check;
ALTER TABLE ci_events ADD CONSTRAINT ci_events_status_check
    CHECK (status IN ('received', 'evaluating', 'evaluated', 'failed', 'duplicate', 'awaiting_approval',
                      'rate_limited', 'skipped'));

-- +goose Down
UPDATE ci_events SET status = 'failed' WHERE status IN ('rate_limited', 'skipped');
ALTER TABLE ci_events DROP CONSTRAINT ci_events_status_check;
ALTER TABLE ci_events ADD CONSTRAINT ci_events_status_check
    CHECK (status IN ('received', 'evaluating', 'evaluated', 'failed', 'duplicate', 'awaiting_approval'));
ALTER TABLE organizations DROP COLUMN shed_actions;
ALTER TABLE organizations DROP COLUMN shed_level;
ALTER TABLE projects DROP COLUMN draft_prs;
ALTER TABLE organizations DROP COLUMN draft_prs;
ALTER TABLE organizations DROP COLUMN defaults_profile;
