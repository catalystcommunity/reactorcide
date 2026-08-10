-- +goose Up
CREATE TABLE organizations (
    org_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    name text NOT NULL UNIQUE,
    display_name text NOT NULL DEFAULT '',
    is_private boolean NOT NULL DEFAULT false,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'disabled')),
    secrets_initialized_at timestamp,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    CONSTRAINT organizations_name_check CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$' AND name = lower(name))
);

-- A user state is necessary for delegated-token invalidation. Existing users
-- remain active.
ALTER TABLE users ADD COLUMN status text NOT NULL DEFAULT 'active'
    CHECK (status IN ('active', 'suspended', 'disabled'));

-- Insert one same-UUID organization for each user that owns an existing
-- resource. Include loose jobs and workflows because they can have no project.
-- +goose StatementBegin
DO $$
DECLARE
    bad_user uuid;
BEGIN
    WITH roots AS (
        SELECT user_id FROM projects WHERE user_id IS NOT NULL
        UNION SELECT org_id FROM groups WHERE org_id IS NOT NULL
        UNION SELECT user_id FROM secrets WHERE user_id IS NOT NULL
        UNION SELECT user_id FROM org_encryption_keys WHERE user_id IS NOT NULL
        UNION SELECT user_id FROM secret_grants WHERE user_id IS NOT NULL
        UNION SELECT org_id FROM worker_pools WHERE org_id IS NOT NULL
        UNION SELECT org_id FROM queues WHERE org_id IS NOT NULL
        UNION SELECT user_id FROM jobs WHERE user_id IS NOT NULL
        UNION SELECT user_id FROM workflow_instances WHERE user_id IS NOT NULL
    )
    SELECT roots.user_id INTO bad_user
      FROM roots JOIN users ON users.user_id = roots.user_id
     WHERE users.username IS NULL
        OR trim(users.username) = ''
        OR trim(both '-' FROM lower(regexp_replace(users.username, '[^a-zA-Z0-9-]+', '-', 'g'))) !~ '^[a-z0-9][a-z0-9-]{0,62}$'
     LIMIT 1;
    IF bad_user IS NOT NULL THEN
        RAISE EXCEPTION 'cannot create organization for user %: set a username that matches [a-z0-9][a-z0-9-]{0,62}', bad_user;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE duplicate_name text;
BEGIN
    WITH roots AS (
        SELECT user_id FROM projects WHERE user_id IS NOT NULL
        UNION SELECT org_id FROM groups WHERE org_id IS NOT NULL
        UNION SELECT user_id FROM secrets WHERE user_id IS NOT NULL
        UNION SELECT user_id FROM org_encryption_keys WHERE user_id IS NOT NULL
        UNION SELECT user_id FROM secret_grants WHERE user_id IS NOT NULL
        UNION SELECT org_id FROM worker_pools WHERE org_id IS NOT NULL
        UNION SELECT org_id FROM queues WHERE org_id IS NOT NULL
        UNION SELECT user_id FROM jobs WHERE user_id IS NOT NULL
        UNION SELECT user_id FROM workflow_instances WHERE user_id IS NOT NULL
    ), candidates AS (
        SELECT trim(both '-' FROM lower(regexp_replace(users.username, '[^a-zA-Z0-9-]+', '-', 'g'))) AS name
        FROM roots JOIN users ON users.user_id = roots.user_id
    )
    SELECT name INTO duplicate_name FROM candidates GROUP BY name HAVING count(*) > 1 LIMIT 1;
    IF duplicate_name IS NOT NULL THEN
        RAISE EXCEPTION 'organization name collision after normalization: %; rename one source username', duplicate_name;
    END IF;
END $$;
-- +goose StatementEnd

WITH roots AS (
    SELECT user_id FROM projects WHERE user_id IS NOT NULL
    UNION SELECT org_id FROM groups WHERE org_id IS NOT NULL
    UNION SELECT user_id FROM secrets WHERE user_id IS NOT NULL
    UNION SELECT user_id FROM org_encryption_keys WHERE user_id IS NOT NULL
    UNION SELECT user_id FROM secret_grants WHERE user_id IS NOT NULL
    UNION SELECT org_id FROM worker_pools WHERE org_id IS NOT NULL
    UNION SELECT org_id FROM queues WHERE org_id IS NOT NULL
    UNION SELECT user_id FROM jobs WHERE user_id IS NOT NULL
    UNION SELECT user_id FROM workflow_instances WHERE user_id IS NOT NULL
), candidates AS (
    SELECT roots.user_id AS org_id,
           trim(both '-' FROM lower(regexp_replace(users.username, '[^a-zA-Z0-9-]+', '-', 'g'))) AS name,
           coalesce(nullif(users.username, ''), 'Organization') AS display_name,
           users.is_private,
           users.created_at,
           users.updated_at
      FROM roots JOIN users ON users.user_id = roots.user_id
)
INSERT INTO organizations (org_id, name, display_name, is_private, created_at, updated_at)
SELECT org_id, name, display_name, is_private, created_at, updated_at FROM candidates;

-- +goose StatementBegin
DO $$
DECLARE duplicate_name text;
BEGIN
    SELECT name INTO duplicate_name FROM organizations GROUP BY name HAVING count(*) > 1 LIMIT 1;
    IF duplicate_name IS NOT NULL THEN
        RAISE EXCEPTION 'organization name collision after normalization: %', duplicate_name;
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE groups DROP CONSTRAINT groups_org_id_fkey;
ALTER TABLE groups ADD CONSTRAINT groups_org_id_fkey FOREIGN KEY (org_id) REFERENCES organizations(org_id) ON DELETE CASCADE;
ALTER TABLE queues DROP CONSTRAINT queues_org_id_fkey;
ALTER TABLE queues ADD CONSTRAINT queues_org_id_fkey FOREIGN KEY (org_id) REFERENCES organizations(org_id);
ALTER TABLE worker_pools DROP CONSTRAINT worker_pools_org_id_fkey;
ALTER TABLE worker_pools ADD CONSTRAINT worker_pools_org_id_fkey FOREIGN KEY (org_id) REFERENCES organizations(org_id);

ALTER TABLE secrets DROP CONSTRAINT secrets_user_id_fkey;
ALTER TABLE secrets RENAME COLUMN user_id TO org_id;
ALTER INDEX secrets_user_id_idx RENAME TO secrets_org_id_idx;
ALTER TABLE secrets RENAME CONSTRAINT secrets_unique_per_org TO secrets_unique_per_org_legacy;
ALTER TABLE secrets ADD CONSTRAINT secrets_org_id_fkey FOREIGN KEY (org_id) REFERENCES organizations(org_id) ON DELETE CASCADE;

ALTER TABLE org_encryption_keys DROP CONSTRAINT org_encryption_keys_user_id_fkey;
ALTER TABLE org_encryption_keys RENAME COLUMN user_id TO org_id;
ALTER INDEX org_encryption_keys_user_id_idx RENAME TO org_encryption_keys_org_id_idx;
ALTER TABLE org_encryption_keys RENAME CONSTRAINT org_keys_unique_per_master TO org_keys_unique_per_master_legacy;
ALTER TABLE org_encryption_keys ADD CONSTRAINT org_encryption_keys_org_id_fkey FOREIGN KEY (org_id) REFERENCES organizations(org_id) ON DELETE CASCADE;

ALTER TABLE secret_grants DROP CONSTRAINT secret_grants_user_id_fkey;
ALTER TABLE secret_grants RENAME COLUMN user_id TO org_id;
ALTER INDEX secret_grants_user_id_idx RENAME TO secret_grants_org_id_idx;
ALTER TABLE secret_grants ADD CONSTRAINT secret_grants_org_id_fkey FOREIGN KEY (org_id) REFERENCES organizations(org_id) ON DELETE CASCADE;

ALTER TABLE projects ADD COLUMN org_id uuid REFERENCES organizations(org_id);
UPDATE projects SET org_id = user_id;
ALTER TABLE projects ALTER COLUMN org_id SET NOT NULL;
CREATE INDEX projects_org_id_idx ON projects(org_id);

ALTER TABLE jobs ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE workflow_instances ALTER COLUMN user_id DROP NOT NULL;

ALTER TABLE jobs ADD COLUMN org_id uuid REFERENCES organizations(org_id);
UPDATE jobs SET org_id = coalesce((SELECT p.org_id FROM projects p WHERE p.project_id = jobs.project_id), jobs.user_id);
CREATE INDEX jobs_org_id_idx ON jobs(org_id);

ALTER TABLE workflow_instances ADD COLUMN org_id uuid REFERENCES organizations(org_id);
UPDATE workflow_instances SET org_id = coalesce(
    (SELECT p.org_id FROM projects p WHERE p.project_id = workflow_instances.project_id),
    workflow_instances.user_id
);
CREATE INDEX workflow_instances_org_id_idx ON workflow_instances(org_id);

INSERT INTO global_settings (key, value)
SELECT 'default_org_id', to_jsonb(org_id::text)
  FROM organizations
 ORDER BY created_at, org_id
 LIMIT 1
ON CONFLICT (key) DO NOTHING;

-- +goose Down
DELETE FROM global_settings WHERE key = 'default_org_id';
DROP INDEX IF EXISTS workflow_instances_org_id_idx;
ALTER TABLE workflow_instances DROP COLUMN IF EXISTS org_id;
DROP INDEX IF EXISTS jobs_org_id_idx;
ALTER TABLE jobs DROP COLUMN IF EXISTS org_id;
DROP INDEX IF EXISTS projects_org_id_idx;
ALTER TABLE projects DROP COLUMN IF EXISTS org_id;
DELETE FROM jobs WHERE user_id IS NULL;
DELETE FROM workflow_instances WHERE user_id IS NULL;
ALTER TABLE jobs ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE workflow_instances ALTER COLUMN user_id SET NOT NULL;

ALTER TABLE secret_grants DROP CONSTRAINT secret_grants_org_id_fkey;
ALTER TABLE secret_grants RENAME COLUMN org_id TO user_id;
ALTER INDEX secret_grants_org_id_idx RENAME TO secret_grants_user_id_idx;
ALTER TABLE secret_grants ADD CONSTRAINT secret_grants_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE;
ALTER TABLE org_encryption_keys DROP CONSTRAINT org_encryption_keys_org_id_fkey;
ALTER TABLE org_encryption_keys RENAME COLUMN org_id TO user_id;
ALTER INDEX org_encryption_keys_org_id_idx RENAME TO org_encryption_keys_user_id_idx;
ALTER TABLE org_encryption_keys RENAME CONSTRAINT org_keys_unique_per_master_legacy TO org_keys_unique_per_master;
ALTER TABLE org_encryption_keys ADD CONSTRAINT org_encryption_keys_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE;
ALTER TABLE secrets DROP CONSTRAINT secrets_org_id_fkey;
ALTER TABLE secrets RENAME COLUMN org_id TO user_id;
ALTER INDEX secrets_org_id_idx RENAME TO secrets_user_id_idx;
ALTER TABLE secrets RENAME CONSTRAINT secrets_unique_per_org_legacy TO secrets_unique_per_org;
ALTER TABLE secrets ADD CONSTRAINT secrets_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE;

ALTER TABLE worker_pools DROP CONSTRAINT worker_pools_org_id_fkey;
ALTER TABLE worker_pools ADD CONSTRAINT worker_pools_org_id_fkey FOREIGN KEY (org_id) REFERENCES users(user_id);
ALTER TABLE queues DROP CONSTRAINT queues_org_id_fkey;
ALTER TABLE queues ADD CONSTRAINT queues_org_id_fkey FOREIGN KEY (org_id) REFERENCES users(user_id);
ALTER TABLE groups DROP CONSTRAINT groups_org_id_fkey;
ALTER TABLE groups ADD CONSTRAINT groups_org_id_fkey FOREIGN KEY (org_id) REFERENCES users(user_id) ON DELETE CASCADE;
ALTER TABLE users DROP COLUMN IF EXISTS status;
DROP TABLE organizations;
