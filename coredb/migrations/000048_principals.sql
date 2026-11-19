-- +goose Up
-- Principals: anything that can act in Reactorcide. A principal is a human, a
-- service, or the system principal "Reactorcide". A principal has one or more
-- identities: a Reactorcide user, a VCS login, a LinkKeys identity, an API
-- token, a VCSStar identity, or a federated subject.
--
-- Compatibility rules of this migration:
--   * A human principal of a users row has principal_id = users.user_id. Role
--     assignments, group members, and visibility queries that use user_id
--     keep working without change.
--   * A service principal made from an existing token has principal_id =
--     api_tokens.token_id. Existing tokens keep their current authority:
--     intersect_roles is false for them, so their capabilities alone apply,
--     as before. Only tokens issued for a principal (intersect_roles = true)
--     intersect their capabilities with the roles of the principal.
--   * Every actor column is nullable. Rows with no derivable actor get the
--     system principal, which has no privileges.
--
-- Triggers keep the principal tables in step with the tables that hold the
-- source identities (users, auth_identities, vcs_identity_links, api_tokens)
-- and give each new job, workflow, event, approval, and audit record an actor
-- when the application did not set one. The application sets the actor where
-- it knows a better one (API submit, webhook sender, retry, rerun).

CREATE TABLE principals (
    principal_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    kind text NOT NULL CHECK (kind IN ('human', 'service', 'system')),
    -- Service principals belong to an organization. A service principal with
    -- no organization is an instance service principal (an instance token).
    -- Humans and the system principal have no organization.
    org_id uuid REFERENCES organizations(org_id) ON DELETE CASCADE,
    name text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    -- A principal that was merged into another principal (for example a VCS
    -- login seen in a webhook, later linked to a user). The resolver follows
    -- this link, so old actor references resolve to the merged principal.
    merged_into uuid REFERENCES principals(principal_id) ON DELETE SET NULL,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    CONSTRAINT principals_org_kind_check CHECK (kind = 'service' OR org_id IS NULL)
);
CREATE UNIQUE INDEX principals_service_name_idx
    ON principals (coalesce(org_id, '00000000-0000-0000-0000-000000000000'::uuid), name)
    WHERE kind = 'service';
CREATE INDEX principals_org_idx ON principals(org_id) WHERE org_id IS NOT NULL;

CREATE TABLE principal_identities (
    identity_type text NOT NULL
        CHECK (identity_type IN ('user', 'vcs', 'vcsstar', 'linkkeys', 'token', 'federated')),
    -- user: users.user_id. vcs: provider/login (lowercase). linkkeys:
    -- subject@domain or handle@domain (lowercase). token: api_tokens.token_id.
    -- vcsstar: the VCSStar identity. federated: issuer|subject.
    identity_value text NOT NULL,
    principal_id uuid NOT NULL REFERENCES principals(principal_id) ON DELETE CASCADE,
    verified_by text NOT NULL DEFAULT 'system'
        CHECK (verified_by IN ('system', 'admin', 'linkkeys', 'webhook', 'federation', 'migration')),
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    PRIMARY KEY (identity_type, identity_value)
);
CREATE INDEX principal_identities_principal_idx ON principal_identities(principal_id);

-- The system principal. It is the actor when no other actor exists, for
-- example a schedule fire. It has no role assignments and no privileges.
INSERT INTO principals (principal_id, kind, name, display_name)
VALUES ('00000000-0000-0000-0000-000000000001', 'system', 'reactorcide', 'Reactorcide');

-- A safe name part for a service principal made from a token name.
-- +goose StatementBegin
CREATE FUNCTION principal_slug(value text) RETURNS text LANGUAGE sql IMMUTABLE AS $$
    SELECT coalesce(nullif(left(trim(both '-' FROM lower(regexp_replace(coalesce(value, ''), '[^a-zA-Z0-9-]+', '-', 'g'))), 48), ''), 'service')
$$;
-- +goose StatementEnd

-- One human principal for each user, with the same UUID.
INSERT INTO principals (principal_id, kind, name, display_name, status, created_at, updated_at)
SELECT user_id, 'human', coalesce(nullif(username, ''), user_id::text), coalesce(username, ''),
       CASE WHEN status = 'active' THEN 'active' ELSE 'disabled' END, created_at, updated_at
  FROM users;
INSERT INTO principal_identities (identity_type, identity_value, principal_id, verified_by)
SELECT 'user', user_id::text, user_id, 'system' FROM users;

-- LinkKeys identities: the subject form and the handle form.
INSERT INTO principal_identities (identity_type, identity_value, principal_id, verified_by)
SELECT 'linkkeys', lower(subject), user_id, 'linkkeys' FROM auth_identities
ON CONFLICT DO NOTHING;
INSERT INTO principal_identities (identity_type, identity_value, principal_id, verified_by)
SELECT 'linkkeys', lower(handle || '@' || domain), user_id, 'linkkeys' FROM auth_identities
 WHERE coalesce(handle, '') <> '' AND domain <> ''
ON CONFLICT DO NOTHING;

-- VCS logins that an admin or LinkKeys linked to a user.
INSERT INTO principal_identities (identity_type, identity_value, principal_id, verified_by)
SELECT 'vcs', lower(provider || '/' || external_subject), user_id, verified_by FROM vcs_identity_links
ON CONFLICT DO NOTHING;

-- One human principal for each other VCS login seen in an event or an
-- approval. The CTE is materialized, so each login gets one UUID.
WITH logins AS (
    SELECT DISTINCT lower(provider || '/' || actor) AS value
      FROM ci_events
     WHERE source = 'webhook' AND provider <> '' AND actor <> ''
    UNION
    SELECT DISTINCT lower(person) AS value
      FROM ci_approvals
     WHERE person LIKE '%/%' AND person NOT LIKE 'user:%'
), missing AS (
    SELECT value, generate_ulid() AS principal_id
      FROM logins
     WHERE NOT EXISTS (SELECT 1 FROM principal_identities pi
                        WHERE pi.identity_type = 'vcs' AND pi.identity_value = logins.value)
), created AS (
    INSERT INTO principals (principal_id, kind, name, display_name)
    SELECT principal_id, 'human', value, split_part(value, '/', 2) FROM missing
    RETURNING principal_id
)
INSERT INTO principal_identities (identity_type, identity_value, principal_id, verified_by)
SELECT 'vcs', missing.value, missing.principal_id, 'migration'
  FROM missing JOIN created ON created.principal_id = missing.principal_id;

-- API tokens bind to a principal. intersect_roles = true makes the effective
-- authority of the token its capabilities intersected with the roles and
-- groups of the principal, the same as a user token. It is false for every
-- token made before this migration, so those tokens keep their authority.
ALTER TABLE api_tokens ADD COLUMN principal_id uuid REFERENCES principals(principal_id) ON DELETE SET NULL;
ALTER TABLE api_tokens ADD COLUMN intersect_roles boolean NOT NULL DEFAULT false;
CREATE INDEX api_tokens_principal_idx ON api_tokens(principal_id) WHERE principal_id IS NOT NULL;

-- One service principal for each existing service or instance token, with
-- the same UUID as the token. A name collision in one organization gets a
-- suffix from the token UUID.
WITH named AS (
    SELECT token_id, owner_org_id, name,
           principal_slug(name) AS slug,
           row_number() OVER (PARTITION BY owner_org_id, principal_slug(name) ORDER BY created_at, token_id) AS position
      FROM api_tokens
     WHERE subject_type IN ('service_token', 'instance_token')
)
INSERT INTO principals (principal_id, kind, org_id, name, display_name)
SELECT token_id, 'service', owner_org_id,
       CASE WHEN position = 1 THEN slug ELSE slug || '-' || right(replace(token_id::text, '-', ''), 8) END,
       name
  FROM named;
UPDATE api_tokens SET principal_id = token_id WHERE subject_type IN ('service_token', 'instance_token');
UPDATE api_tokens SET principal_id = user_id
 WHERE subject_type = 'user_token' AND user_id IS NOT NULL
   AND EXISTS (SELECT 1 FROM principals p WHERE p.principal_id = api_tokens.user_id);
INSERT INTO principal_identities (identity_type, identity_value, principal_id, verified_by)
SELECT 'token', token_id::text, principal_id, 'system' FROM api_tokens WHERE principal_id IS NOT NULL
ON CONFLICT DO NOTHING;

-- Actor columns. actor_identity is "TYPE:VALUE" of the identity the actor
-- used (for example "vcs:github/octocat" or "token:UUID"). It is empty when
-- the actor was not recorded (rows made before this migration).
ALTER TABLE ci_events ADD COLUMN actor_principal_id uuid REFERENCES principals(principal_id) ON DELETE SET NULL;
ALTER TABLE ci_events ADD COLUMN actor_identity text NOT NULL DEFAULT '';
ALTER TABLE workflow_instances ADD COLUMN actor_principal_id uuid REFERENCES principals(principal_id) ON DELETE SET NULL;
ALTER TABLE workflow_instances ADD COLUMN actor_identity text NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN actor_principal_id uuid REFERENCES principals(principal_id) ON DELETE SET NULL;
ALTER TABLE jobs ADD COLUMN actor_identity text NOT NULL DEFAULT '';
ALTER TABLE ci_approvals ADD COLUMN actor_principal_id uuid REFERENCES principals(principal_id) ON DELETE SET NULL;
ALTER TABLE audit_events ADD COLUMN actor_principal_id uuid REFERENCES principals(principal_id) ON DELETE SET NULL;

UPDATE ci_events e
   SET actor_principal_id = pi.principal_id,
       actor_identity = 'vcs:' || pi.identity_value
  FROM principal_identities pi
 WHERE e.source = 'webhook' AND e.provider <> '' AND e.actor <> ''
   AND pi.identity_type = 'vcs' AND pi.identity_value = lower(e.provider || '/' || e.actor);
UPDATE ci_events SET actor_principal_id = '00000000-0000-0000-0000-000000000001'
 WHERE actor_principal_id IS NULL;

UPDATE workflow_instances w
   SET actor_principal_id = e.actor_principal_id, actor_identity = e.actor_identity
  FROM ci_events e
 WHERE w.event_id = e.event_id;
UPDATE workflow_instances SET actor_principal_id = '00000000-0000-0000-0000-000000000001'
 WHERE actor_principal_id IS NULL;

UPDATE jobs j
   SET actor_principal_id = e.actor_principal_id, actor_identity = e.actor_identity
  FROM ci_events e
 WHERE j.event_id = e.event_id;
UPDATE jobs SET actor_principal_id = '00000000-0000-0000-0000-000000000001'
 WHERE actor_principal_id IS NULL;

UPDATE ci_approvals a
   SET actor_principal_id = pi.principal_id
  FROM principal_identities pi
 WHERE a.person NOT LIKE 'user:%'
   AND pi.identity_type = 'vcs' AND pi.identity_value = lower(a.person);
UPDATE ci_approvals a
   SET actor_principal_id = p.principal_id
  FROM principals p
 WHERE a.actor_principal_id IS NULL AND a.person LIKE 'user:%'
   AND p.principal_id::text = substr(a.person, 6);
UPDATE ci_approvals a
   SET actor_principal_id = a.approver_user_id
 WHERE a.actor_principal_id IS NULL AND a.approver_user_id IS NOT NULL
   AND EXISTS (SELECT 1 FROM principals p WHERE p.principal_id = a.approver_user_id);
UPDATE ci_approvals SET actor_principal_id = '00000000-0000-0000-0000-000000000001'
 WHERE actor_principal_id IS NULL;

UPDATE audit_events a
   SET actor_principal_id = a.actor_user_id
 WHERE a.actor_user_id IS NOT NULL
   AND EXISTS (SELECT 1 FROM principals p WHERE p.principal_id = a.actor_user_id);
UPDATE audit_events a
   SET actor_principal_id = t.principal_id
  FROM api_tokens t
 WHERE a.actor_principal_id IS NULL AND a.actor_credential_id = t.token_id AND t.principal_id IS NOT NULL;
UPDATE audit_events SET actor_principal_id = '00000000-0000-0000-0000-000000000001'
 WHERE actor_principal_id IS NULL;

CREATE INDEX jobs_actor_principal_idx ON jobs(actor_principal_id);
CREATE INDEX workflow_instances_actor_principal_idx ON workflow_instances(actor_principal_id);
CREATE INDEX ci_events_actor_principal_idx ON ci_events(actor_principal_id);
CREATE INDEX ci_approvals_actor_principal_idx ON ci_approvals(actor_principal_id);
CREATE INDEX audit_events_actor_principal_idx ON audit_events(actor_principal_id);

-- Group members can be any principal. user_id stays for compatibility: it
-- is set for a principal that is a user, and visibility queries still read
-- it. principal_id is the member key now.
ALTER TABLE group_members ADD COLUMN principal_id uuid REFERENCES principals(principal_id) ON DELETE CASCADE;
UPDATE group_members SET principal_id = user_id;
ALTER TABLE group_members ALTER COLUMN principal_id SET NOT NULL;
ALTER TABLE group_members DROP CONSTRAINT group_members_pkey;
ALTER TABLE group_members ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE group_members ADD CONSTRAINT group_members_pkey PRIMARY KEY (group_id, principal_id);
CREATE UNIQUE INDEX group_members_group_user_idx ON group_members(group_id, user_id) WHERE user_id IS NOT NULL;
CREATE INDEX group_members_principal_idx ON group_members(principal_id);

-- Role assignments can name a principal directly. 'user' and 'group' keep
-- working. A 'principal' assignment to a human of a user has the user UUID.
ALTER TABLE role_assignments DROP CONSTRAINT role_assignments_principal_type_check;
ALTER TABLE role_assignments ADD CONSTRAINT role_assignments_principal_type_check
    CHECK (principal_type IN ('user', 'group', 'principal'));

-- ---------------------------------------------------------------------------
-- Triggers that keep principals in step with their source tables.
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE FUNCTION principals_on_user_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO principals (principal_id, kind, name, display_name)
    VALUES (NEW.user_id, 'human', coalesce(nullif(NEW.username, ''), NEW.user_id::text), coalesce(NEW.username, ''))
    ON CONFLICT (principal_id) DO NOTHING;
    INSERT INTO principal_identities (identity_type, identity_value, principal_id, verified_by)
    VALUES ('user', NEW.user_id::text, NEW.user_id, 'system')
    ON CONFLICT DO NOTHING;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER principals_user_insert AFTER INSERT ON users
    FOR EACH ROW EXECUTE FUNCTION principals_on_user_insert();

-- +goose StatementBegin
CREATE FUNCTION principals_on_user_status() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE principals
       SET status = CASE WHEN NEW.status = 'active' THEN 'active' ELSE 'disabled' END,
           updated_at = timezone('utc', now())
     WHERE principal_id = NEW.user_id AND kind = 'human';
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER principals_user_status AFTER UPDATE OF status ON users
    FOR EACH ROW EXECUTE FUNCTION principals_on_user_status();

-- +goose StatementBegin
CREATE FUNCTION principals_on_auth_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        DELETE FROM principal_identities
         WHERE identity_type = 'linkkeys' AND principal_id = OLD.user_id AND verified_by = 'linkkeys'
           AND identity_value IN (lower(OLD.subject), lower(coalesce(OLD.handle, '') || '@' || OLD.domain));
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        INSERT INTO principal_identities (identity_type, identity_value, principal_id, verified_by)
        VALUES ('linkkeys', lower(NEW.subject), NEW.user_id, 'linkkeys')
        ON CONFLICT (identity_type, identity_value)
        DO UPDATE SET principal_id = EXCLUDED.principal_id, verified_by = 'linkkeys';
        IF coalesce(NEW.handle, '') <> '' AND NEW.domain <> '' THEN
            INSERT INTO principal_identities (identity_type, identity_value, principal_id, verified_by)
            VALUES ('linkkeys', lower(NEW.handle || '@' || NEW.domain), NEW.user_id, 'linkkeys')
            ON CONFLICT (identity_type, identity_value)
            DO UPDATE SET principal_id = EXCLUDED.principal_id, verified_by = 'linkkeys';
        END IF;
        RETURN NEW;
    END IF;
    RETURN OLD;
END $$;
-- +goose StatementEnd
CREATE TRIGGER principals_auth_identity AFTER INSERT OR DELETE OR UPDATE OF subject, handle, domain, user_id
    ON auth_identities FOR EACH ROW EXECUTE FUNCTION principals_on_auth_identity();

-- Move everything of one principal to another and mark it merged. Used when
-- a VCS login that Reactorcide saw first in a webhook is linked to a user.
-- +goose StatementBegin
CREATE FUNCTION principals_merge(source uuid, target uuid) RETURNS void LANGUAGE plpgsql AS $$
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

-- A principal that only a webhook made: a human with no organization, no
-- user, token, or federated identity, no group membership, and no role
-- assignment. Only such a principal can be merged into another principal.
-- Any other owner of an identity keeps it: a merge would move its roles and
-- memberships (for example a service principal of another organization)
-- to the target. The Go store (LinkPrincipalIdentity) uses this rule too.
-- +goose StatementBegin
CREATE FUNCTION principals_webhook_only(candidate uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (SELECT 1 FROM principals p
                    WHERE p.principal_id = candidate AND p.kind = 'human' AND p.org_id IS NULL)
       AND NOT EXISTS (SELECT 1 FROM principal_identities pi
                        WHERE pi.principal_id = candidate AND pi.identity_type IN ('user', 'token', 'federated'))
       AND NOT EXISTS (SELECT 1 FROM group_members gm WHERE gm.principal_id = candidate)
       AND NOT EXISTS (SELECT 1 FROM role_assignments ra WHERE ra.principal_id = candidate)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION principals_on_vcs_identity_link() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    linked_value text;
    previous uuid;
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        DELETE FROM principal_identities
         WHERE identity_type = 'vcs' AND principal_id = OLD.user_id
           AND identity_value = lower(OLD.provider || '/' || OLD.external_subject);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        linked_value := lower(NEW.provider || '/' || NEW.external_subject);
        SELECT principal_id INTO previous FROM principal_identities
         WHERE identity_type = 'vcs' AND identity_value = linked_value;
        -- A login first seen in a webhook has its own principal with no user.
        -- Merge it into the user, so its history follows. An identity of
        -- any other principal is not moved: the link is refused.
        IF previous IS NOT NULL AND previous <> NEW.user_id THEN
            IF NOT principals_webhook_only(previous) THEN
                RAISE EXCEPTION 'VCS identity % belongs to another principal', linked_value
                    USING ERRCODE = 'unique_violation';
            END IF;
            PERFORM principals_merge(previous, NEW.user_id);
        END IF;
        INSERT INTO principal_identities (identity_type, identity_value, principal_id, verified_by)
        VALUES ('vcs', linked_value, NEW.user_id, NEW.verified_by)
        ON CONFLICT (identity_type, identity_value)
        DO UPDATE SET verified_by = EXCLUDED.verified_by
        WHERE principal_identities.principal_id = EXCLUDED.principal_id;
        IF NOT FOUND THEN
            -- Another transaction gave the identity to another principal.
            RAISE EXCEPTION 'VCS identity % belongs to another principal', linked_value
                USING ERRCODE = 'unique_violation';
        END IF;
        RETURN NEW;
    END IF;
    RETURN OLD;
END $$;
-- +goose StatementEnd
CREATE TRIGGER principals_vcs_identity_link AFTER INSERT OR DELETE OR UPDATE ON vcs_identity_links
    FOR EACH ROW EXECUTE FUNCTION principals_on_vcs_identity_link();

-- A token gets a principal: a user token the principal of its user; a
-- service or instance token without a principal a new service principal
-- with the token UUID. A job token has no principal: it acts for its job.
-- +goose StatementBegin
CREATE FUNCTION principals_on_api_token_insert() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    candidate text;
BEGIN
    IF NEW.subject_type = 'job_token' THEN
        RETURN NEW;
    END IF;
    IF NEW.principal_id IS NULL THEN
        IF NEW.subject_type = 'user_token' THEN
            IF NEW.user_id IS NOT NULL AND EXISTS (SELECT 1 FROM principals WHERE principal_id = NEW.user_id) THEN
                NEW.principal_id := NEW.user_id;
            END IF;
        ELSIF NEW.subject_type IN ('service_token', 'instance_token') THEN
            candidate := principal_slug(NEW.name);
            IF EXISTS (SELECT 1 FROM principals
                        WHERE kind = 'service' AND org_id IS NOT DISTINCT FROM NEW.owner_org_id AND name = candidate) THEN
                candidate := candidate || '-' || right(replace(NEW.token_id::text, '-', ''), 8);
            END IF;
            INSERT INTO principals (principal_id, kind, org_id, name, display_name)
            VALUES (NEW.token_id, 'service', NEW.owner_org_id, candidate, NEW.name);
            NEW.principal_id := NEW.token_id;
        END IF;
    END IF;
    IF NEW.principal_id IS NOT NULL THEN
        INSERT INTO principal_identities (identity_type, identity_value, principal_id, verified_by)
        VALUES ('token', NEW.token_id::text, NEW.principal_id, 'system')
        ON CONFLICT DO NOTHING;
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER principals_api_token_insert BEFORE INSERT ON api_tokens
    FOR EACH ROW EXECUTE FUNCTION principals_on_api_token_insert();

-- A group member row from code that knows only the user gets the principal;
-- a row for a principal that is a user gets the user_id too.
-- +goose StatementBegin
CREATE FUNCTION principals_on_group_member() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.principal_id IS NULL THEN
        NEW.principal_id := NEW.user_id;
    END IF;
    IF NEW.user_id IS NULL THEN
        SELECT u.user_id INTO NEW.user_id FROM users u WHERE u.user_id = NEW.principal_id;
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER principals_group_member BEFORE INSERT ON group_members
    FOR EACH ROW EXECUTE FUNCTION principals_on_group_member();

-- Default actors. A job inherits from its parent job, then its workflow,
-- then its event. A workflow inherits from its parent job, its parent
-- workflow, then its event. Otherwise the actor is the system principal.
-- +goose StatementBegin
CREATE FUNCTION principals_default_job_actor() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.actor_principal_id IS NULL AND NEW.parent_job_id IS NOT NULL THEN
        SELECT actor_principal_id, actor_identity INTO NEW.actor_principal_id, NEW.actor_identity
          FROM jobs WHERE job_id = NEW.parent_job_id;
    END IF;
    IF NEW.actor_principal_id IS NULL AND NEW.workflow_id IS NOT NULL THEN
        SELECT actor_principal_id, actor_identity INTO NEW.actor_principal_id, NEW.actor_identity
          FROM workflow_instances WHERE workflow_id = NEW.workflow_id;
    END IF;
    IF NEW.actor_principal_id IS NULL AND NEW.event_id IS NOT NULL THEN
        SELECT actor_principal_id, actor_identity INTO NEW.actor_principal_id, NEW.actor_identity
          FROM ci_events WHERE event_id = NEW.event_id;
    END IF;
    IF NEW.actor_principal_id IS NULL THEN
        NEW.actor_principal_id := '00000000-0000-0000-0000-000000000001';
    END IF;
    NEW.actor_identity := coalesce(NEW.actor_identity, '');
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER principals_job_actor BEFORE INSERT ON jobs
    FOR EACH ROW EXECUTE FUNCTION principals_default_job_actor();

-- +goose StatementBegin
CREATE FUNCTION principals_default_workflow_actor() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.actor_principal_id IS NULL AND NEW.parent_job_id IS NOT NULL THEN
        SELECT actor_principal_id, actor_identity INTO NEW.actor_principal_id, NEW.actor_identity
          FROM jobs WHERE job_id = NEW.parent_job_id;
    END IF;
    IF NEW.actor_principal_id IS NULL AND NEW.parent_workflow_id IS NOT NULL THEN
        SELECT actor_principal_id, actor_identity INTO NEW.actor_principal_id, NEW.actor_identity
          FROM workflow_instances WHERE workflow_id = NEW.parent_workflow_id;
    END IF;
    IF NEW.actor_principal_id IS NULL AND NEW.event_id IS NOT NULL THEN
        SELECT actor_principal_id, actor_identity INTO NEW.actor_principal_id, NEW.actor_identity
          FROM ci_events WHERE event_id = NEW.event_id;
    END IF;
    IF NEW.actor_principal_id IS NULL THEN
        NEW.actor_principal_id := '00000000-0000-0000-0000-000000000001';
    END IF;
    NEW.actor_identity := coalesce(NEW.actor_identity, '');
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER principals_workflow_actor BEFORE INSERT ON workflow_instances
    FOR EACH ROW EXECUTE FUNCTION principals_default_workflow_actor();

-- +goose StatementBegin
CREATE FUNCTION principals_default_event_actor() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.actor_principal_id IS NULL AND NEW.source = 'webhook' AND NEW.provider <> '' AND NEW.actor <> '' THEN
        SELECT principal_id, 'vcs:' || identity_value INTO NEW.actor_principal_id, NEW.actor_identity
          FROM principal_identities
         WHERE identity_type = 'vcs' AND identity_value = lower(NEW.provider || '/' || NEW.actor);
    END IF;
    IF NEW.actor_principal_id IS NULL THEN
        NEW.actor_principal_id := '00000000-0000-0000-0000-000000000001';
    END IF;
    NEW.actor_identity := coalesce(NEW.actor_identity, '');
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER principals_event_actor BEFORE INSERT ON ci_events
    FOR EACH ROW EXECUTE FUNCTION principals_default_event_actor();

-- +goose StatementBegin
CREATE FUNCTION principals_default_approval_actor() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.actor_principal_id IS NULL AND NEW.person LIKE 'user:%' THEN
        SELECT principal_id INTO NEW.actor_principal_id FROM principals
         WHERE principal_id::text = substr(NEW.person, 6);
    END IF;
    IF NEW.actor_principal_id IS NULL AND NEW.person LIKE '%/%' THEN
        SELECT principal_id INTO NEW.actor_principal_id FROM principal_identities
         WHERE identity_type = 'vcs' AND identity_value = lower(NEW.person);
    END IF;
    IF NEW.actor_principal_id IS NULL THEN
        NEW.actor_principal_id := '00000000-0000-0000-0000-000000000001';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER principals_approval_actor BEFORE INSERT ON ci_approvals
    FOR EACH ROW EXECUTE FUNCTION principals_default_approval_actor();

-- An audit record of a job token gets the actor of the job.
-- +goose StatementBegin
CREATE FUNCTION principals_default_audit_actor() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.actor_principal_id IS NULL AND NEW.actor_user_id IS NOT NULL THEN
        SELECT principal_id INTO NEW.actor_principal_id FROM principals WHERE principal_id = NEW.actor_user_id;
    END IF;
    IF NEW.actor_principal_id IS NULL AND NEW.actor_credential_id IS NOT NULL THEN
        SELECT coalesce(t.principal_id, j.actor_principal_id) INTO NEW.actor_principal_id
          FROM api_tokens t LEFT JOIN jobs j ON j.job_id = t.bound_job_id
         WHERE t.token_id = NEW.actor_credential_id;
    END IF;
    IF NEW.actor_principal_id IS NULL THEN
        NEW.actor_principal_id := '00000000-0000-0000-0000-000000000001';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER principals_audit_actor BEFORE INSERT ON audit_events
    FOR EACH ROW EXECUTE FUNCTION principals_default_audit_actor();

-- +goose Down
DROP TRIGGER IF EXISTS principals_audit_actor ON audit_events;
DROP TRIGGER IF EXISTS principals_approval_actor ON ci_approvals;
DROP TRIGGER IF EXISTS principals_event_actor ON ci_events;
DROP TRIGGER IF EXISTS principals_workflow_actor ON workflow_instances;
DROP TRIGGER IF EXISTS principals_job_actor ON jobs;
DROP TRIGGER IF EXISTS principals_group_member ON group_members;
DROP TRIGGER IF EXISTS principals_api_token_insert ON api_tokens;
DROP TRIGGER IF EXISTS principals_vcs_identity_link ON vcs_identity_links;
DROP TRIGGER IF EXISTS principals_auth_identity ON auth_identities;
DROP TRIGGER IF EXISTS principals_user_status ON users;
DROP TRIGGER IF EXISTS principals_user_insert ON users;
DROP FUNCTION IF EXISTS principals_default_audit_actor();
DROP FUNCTION IF EXISTS principals_default_approval_actor();
DROP FUNCTION IF EXISTS principals_default_event_actor();
DROP FUNCTION IF EXISTS principals_default_workflow_actor();
DROP FUNCTION IF EXISTS principals_default_job_actor();
DROP FUNCTION IF EXISTS principals_on_group_member();
DROP FUNCTION IF EXISTS principals_on_api_token_insert();
DROP FUNCTION IF EXISTS principals_on_vcs_identity_link();
DROP FUNCTION IF EXISTS principals_webhook_only(uuid);
DROP FUNCTION IF EXISTS principals_merge(uuid, uuid);
DROP FUNCTION IF EXISTS principals_on_auth_identity();
DROP FUNCTION IF EXISTS principals_on_user_status();
DROP FUNCTION IF EXISTS principals_on_user_insert();

DELETE FROM role_assignments WHERE principal_type = 'principal';
ALTER TABLE role_assignments DROP CONSTRAINT role_assignments_principal_type_check;
ALTER TABLE role_assignments ADD CONSTRAINT role_assignments_principal_type_check
    CHECK (principal_type IN ('user', 'group'));

-- Members that are not users cannot exist in the old table.
DELETE FROM group_members WHERE user_id IS NULL;
DROP INDEX IF EXISTS group_members_principal_idx;
DROP INDEX IF EXISTS group_members_group_user_idx;
ALTER TABLE group_members DROP CONSTRAINT group_members_pkey;
ALTER TABLE group_members ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE group_members ADD CONSTRAINT group_members_pkey PRIMARY KEY (group_id, user_id);
ALTER TABLE group_members DROP COLUMN principal_id;

DROP INDEX IF EXISTS audit_events_actor_principal_idx;
DROP INDEX IF EXISTS ci_approvals_actor_principal_idx;
DROP INDEX IF EXISTS ci_events_actor_principal_idx;
DROP INDEX IF EXISTS workflow_instances_actor_principal_idx;
DROP INDEX IF EXISTS jobs_actor_principal_idx;
ALTER TABLE audit_events DROP COLUMN actor_principal_id;
ALTER TABLE ci_approvals DROP COLUMN actor_principal_id;
ALTER TABLE jobs DROP COLUMN actor_identity;
ALTER TABLE jobs DROP COLUMN actor_principal_id;
ALTER TABLE workflow_instances DROP COLUMN actor_identity;
ALTER TABLE workflow_instances DROP COLUMN actor_principal_id;
ALTER TABLE ci_events DROP COLUMN actor_identity;
ALTER TABLE ci_events DROP COLUMN actor_principal_id;

DROP INDEX IF EXISTS api_tokens_principal_idx;
-- A token issued for a principal has intersect_roles = true: its authority
-- is its capabilities intersected with the roles of the principal. Without
-- the column it would get its full capabilities, so revoke it.
UPDATE api_tokens SET is_active = false, revoked_at = timezone('utc', now())
 WHERE intersect_roles AND is_active;
ALTER TABLE api_tokens DROP COLUMN intersect_roles;
ALTER TABLE api_tokens DROP COLUMN principal_id;

DROP TABLE principal_identities;
DROP TABLE principals;
DROP FUNCTION IF EXISTS principal_slug(text);
