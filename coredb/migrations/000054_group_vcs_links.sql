-- +goose Up
-- Groups linked to VCS teams (plan item B3, docs/principals.md).
--
-- A group can bind to one or more VCS teams. A sync loop reads the members of
-- each linked team from the VCS and stores them. The effective members of a
-- group are its manual members and the principals of the logins of each
-- linked team. A sync never removes a manual member.
--
-- Storage:
--   group_vcs_links         one row for each linked team, with sync status.
--   group_vcs_link_members  the principals that one linked team has now.
--   group_members.source    'manual' (an admin added it) or 'vcs_sync' (only
--                           a linked team gives it). Every existing row is
--                           manual, so current behavior does not change.
--   group_membership_history  added and removed changes with times, for
--                           attribution of usage over time.

CREATE TABLE group_vcs_links (
    link_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    group_id uuid NOT NULL REFERENCES groups(group_id) ON DELETE CASCADE,
    provider text NOT NULL CHECK (provider IN ('github')),
    -- The VCS organization (GitHub org login), lowercase.
    owner text NOT NULL CHECK (owner <> '' AND owner = lower(owner)),
    -- The team slug, lowercase.
    team_slug text NOT NULL CHECK (team_slug <> '' AND team_slug = lower(team_slug)),
    -- The numeric team ID of the VCS. A rename keeps the ID, so a team
    -- "edited" webhook can find the link after a slug change.
    external_team_id bigint,
    -- The principal that made the link (no foreign key: audit data).
    created_by_principal_id uuid,
    created_at timestamptz NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamptz NOT NULL DEFAULT timezone('utc', now()),
    -- The time of the last successful sync.
    last_synced_at timestamptz,
    -- The time of the last attempt, successful or not.
    last_attempt_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    failure_count integer NOT NULL DEFAULT 0,
    -- The next sync is due at this time. NULL is due now. A webhook sets it
    -- to now; a failure moves it out (backoff or the rate limit reset).
    next_sync_at timestamptz,
    member_count integer NOT NULL DEFAULT 0,
    UNIQUE (group_id, provider, owner, team_slug)
);
CREATE INDEX group_vcs_links_due_idx ON group_vcs_links (next_sync_at);
CREATE INDEX group_vcs_links_team_idx ON group_vcs_links (provider, owner, team_slug);

-- The VCS owners (GitHub org logins, lowercase) for which the sync of this
-- organization may use the GLOBAL VCS credential of the coordinator. Only a
-- global admin sets it. Without an entry, the sync uses only a credential
-- of a project of the organization (project or organization scope), so one
-- tenant cannot read the teams of another tenant with the shared
-- credential. NULL is the same as empty. The Organization model does not
-- map the column, so a whole-struct save cannot reset it.
ALTER TABLE organizations ADD COLUMN vcs_owner_allowlist text[];

CREATE TABLE group_vcs_link_members (
    link_id uuid NOT NULL REFERENCES group_vcs_links(link_id) ON DELETE CASCADE,
    principal_id uuid NOT NULL REFERENCES principals(principal_id) ON DELETE CASCADE,
    -- The VCS login as the VCS returned it.
    vcs_login text NOT NULL,
    added_at timestamptz NOT NULL DEFAULT timezone('utc', now()),
    PRIMARY KEY (link_id, principal_id)
);
CREATE INDEX group_vcs_link_members_principal_idx ON group_vcs_link_members (principal_id);

ALTER TABLE group_members ADD COLUMN source text NOT NULL DEFAULT 'manual'
    CHECK (source IN ('manual', 'vcs_sync'));

-- History of the effective membership. No foreign keys: a row stays after
-- its group or principal is deleted, so attribution of old usage still works.
CREATE TABLE group_membership_history (
    history_id bigserial PRIMARY KEY,
    group_id uuid NOT NULL,
    org_id uuid,
    group_name text NOT NULL DEFAULT '',
    principal_id uuid NOT NULL,
    change text NOT NULL CHECK (change IN ('added', 'removed')),
    source text NOT NULL DEFAULT 'manual',
    changed_at timestamptz NOT NULL DEFAULT timezone('utc', now())
);
CREATE INDEX group_membership_history_principal_idx ON group_membership_history (principal_id, changed_at);
CREATE INDEX group_membership_history_group_idx ON group_membership_history (group_id, changed_at);

-- Backfill: each existing member was added at its added_at.
INSERT INTO group_membership_history (group_id, org_id, group_name, principal_id, change, source, changed_at)
SELECT gm.group_id, g.org_id, g.name, gm.principal_id, 'added', 'manual', gm.added_at
  FROM group_members gm JOIN groups g ON g.group_id = gm.group_id;

-- A trigger records each change, so every code path (REST, UI, principal
-- merge, sync) writes history. clock_timestamp(), not now(): two changes in
-- one transaction keep their order in time.
-- +goose StatementBegin
CREATE FUNCTION group_members_history() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    g_org uuid;
    g_name text;
BEGIN
    IF TG_OP = 'INSERT' THEN
        SELECT org_id, name INTO g_org, g_name FROM groups WHERE group_id = NEW.group_id;
        INSERT INTO group_membership_history (group_id, org_id, group_name, principal_id, change, source, changed_at)
        VALUES (NEW.group_id, g_org, coalesce(g_name, ''), NEW.principal_id, 'added', NEW.source, timezone('utc', clock_timestamp()));
        RETURN NEW;
    END IF;
    SELECT org_id, name INTO g_org, g_name FROM groups WHERE group_id = OLD.group_id;
    IF NOT FOUND THEN
        -- The group is being deleted. groups_delete_history recorded the
        -- removals before the cascade, with the group name.
        RETURN OLD;
    END IF;
    INSERT INTO group_membership_history (group_id, org_id, group_name, principal_id, change, source, changed_at)
    VALUES (OLD.group_id, g_org, g_name, OLD.principal_id, 'removed', OLD.source, timezone('utc', clock_timestamp()));
    RETURN OLD;
END $$;
-- +goose StatementEnd
CREATE TRIGGER group_members_history AFTER INSERT OR DELETE ON group_members
    FOR EACH ROW EXECUTE FUNCTION group_members_history();

-- +goose StatementBegin
CREATE FUNCTION groups_delete_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO group_membership_history (group_id, org_id, group_name, principal_id, change, source, changed_at)
    SELECT OLD.group_id, OLD.org_id, OLD.name, gm.principal_id, 'removed', gm.source, timezone('utc', clock_timestamp())
      FROM group_members gm WHERE gm.group_id = OLD.group_id;
    RETURN OLD;
END $$;
-- +goose StatementEnd
CREATE TRIGGER groups_delete_history BEFORE DELETE ON groups
    FOR EACH ROW EXECUTE FUNCTION groups_delete_history();

-- A merge (a webhook-only VCS principal linked to a user) keeps the source of
-- each membership and moves the synced team memberships. Without this, a
-- synced membership became manual on the target, and a later team removal
-- did not remove it.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION principals_merge(source uuid, target uuid) RETURNS void LANGUAGE plpgsql AS $$
-- group_members has a column "source" now: name the parameters with the
-- function name.
BEGIN
    IF principals_merge.source IS NULL OR principals_merge.target IS NULL
       OR principals_merge.source = principals_merge.target THEN
        RETURN;
    END IF;
    UPDATE principal_identities pi SET principal_id = principals_merge.target
     WHERE pi.principal_id = principals_merge.source;
    INSERT INTO group_vcs_link_members (link_id, principal_id, vcs_login, added_at)
    SELECT lm.link_id, principals_merge.target, lm.vcs_login, lm.added_at
      FROM group_vcs_link_members lm WHERE lm.principal_id = principals_merge.source
    ON CONFLICT DO NOTHING;
    DELETE FROM group_vcs_link_members lm WHERE lm.principal_id = principals_merge.source;
    INSERT INTO group_members (group_id, principal_id, user_id, added_at, source)
    SELECT gm.group_id, principals_merge.target,
           (SELECT u.user_id FROM users u WHERE u.user_id = principals_merge.target), gm.added_at, gm.source
      FROM group_members gm WHERE gm.principal_id = principals_merge.source
    ON CONFLICT DO NOTHING;
    -- A manual membership of the source makes the target membership manual.
    UPDATE group_members tm SET source = 'manual'
     WHERE tm.principal_id = principals_merge.target AND tm.source = 'vcs_sync'
       AND tm.group_id IN (SELECT gm.group_id FROM group_members gm
                            WHERE gm.principal_id = principals_merge.source AND gm.source = 'manual');
    DELETE FROM group_members gm WHERE gm.principal_id = principals_merge.source;
    UPDATE role_assignments ra SET principal_id = principals_merge.target
     WHERE ra.principal_type = 'principal' AND ra.principal_id = principals_merge.source
       AND NOT EXISTS (SELECT 1 FROM role_assignments other
                        WHERE other.principal_type = 'principal' AND other.principal_id = principals_merge.target
                          AND other.scope_type = ra.scope_type
                          AND other.scope_id IS NOT DISTINCT FROM ra.scope_id
                          AND other.role = ra.role);
    DELETE FROM role_assignments ra WHERE ra.principal_type = 'principal' AND ra.principal_id = principals_merge.source;
    UPDATE principals p SET merged_into = principals_merge.target, status = 'disabled', updated_at = timezone('utc', now())
     WHERE p.principal_id = principals_merge.source;
END $$;
-- +goose StatementEnd

-- A synced membership does not stop a merge: the VCS gave it, not an admin,
-- and the merge moves it with its source. A manual membership still stops it.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION principals_webhook_only(candidate uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (SELECT 1 FROM principals p
                    WHERE p.principal_id = candidate AND p.kind = 'human' AND p.org_id IS NULL)
       AND NOT EXISTS (SELECT 1 FROM principal_identities pi
                        WHERE pi.principal_id = candidate AND pi.identity_type IN ('user', 'token', 'federated'))
       AND NOT EXISTS (SELECT 1 FROM group_members gm WHERE gm.principal_id = candidate AND gm.source = 'manual')
       AND NOT EXISTS (SELECT 1 FROM role_assignments ra WHERE ra.principal_id = candidate)
$$;
-- +goose StatementEnd

-- +goose Down
-- Restore the 000048 functions.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION principals_webhook_only(candidate uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (SELECT 1 FROM principals p
                    WHERE p.principal_id = candidate AND p.kind = 'human' AND p.org_id IS NULL)
       AND NOT EXISTS (SELECT 1 FROM principal_identities pi
                        WHERE pi.principal_id = candidate AND pi.identity_type IN ('user', 'token', 'federated'))
       AND NOT EXISTS (SELECT 1 FROM group_members gm WHERE gm.principal_id = candidate)
       AND NOT EXISTS (SELECT 1 FROM role_assignments ra WHERE ra.principal_id = candidate)
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION principals_merge(source uuid, target uuid) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    IF source IS NULL OR target IS NULL OR source = target THEN
        RETURN;
    END IF;
    UPDATE principal_identities SET principal_id = target WHERE principal_id = source;
    INSERT INTO group_members (group_id, principal_id, user_id, added_at)
    SELECT gm.group_id, target, (SELECT u.user_id FROM users u WHERE u.user_id = target), gm.added_at
      FROM group_members gm WHERE gm.principal_id = source
    ON CONFLICT DO NOTHING;
    DELETE FROM group_members WHERE principal_id = source;
    UPDATE role_assignments SET principal_id = target
     WHERE principal_type = 'principal' AND principal_id = source
       AND NOT EXISTS (SELECT 1 FROM role_assignments other
                        WHERE other.principal_type = 'principal' AND other.principal_id = target
                          AND other.scope_type = role_assignments.scope_type
                          AND other.scope_id IS NOT DISTINCT FROM role_assignments.scope_id
                          AND other.role = role_assignments.role);
    DELETE FROM role_assignments WHERE principal_type = 'principal' AND principal_id = source;
    UPDATE principals SET merged_into = target, status = 'disabled', updated_at = timezone('utc', now())
     WHERE principal_id = source;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS groups_delete_history ON groups;
DROP FUNCTION IF EXISTS groups_delete_history();
DROP TRIGGER IF EXISTS group_members_history ON group_members;
DROP FUNCTION IF EXISTS group_members_history();
DROP TABLE IF EXISTS group_membership_history;
-- Memberships that only a linked team gave go away with the links: without
-- the link tables nothing can remove them later.
DELETE FROM group_members WHERE source = 'vcs_sync';
ALTER TABLE group_members DROP COLUMN IF EXISTS source;
DROP TABLE IF EXISTS group_vcs_link_members;
DROP TABLE IF EXISTS group_vcs_links;
ALTER TABLE organizations DROP COLUMN IF EXISTS vcs_owner_allowlist;
