-- +goose Up
CREATE TABLE execution_profiles (
    profile_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    name text NOT NULL,
    deny_secrets boolean NOT NULL DEFAULT false,
    secret_path_allowlist text[] NOT NULL DEFAULT '{}',
    runtime_capabilities text[],
    may_run_as_root boolean NOT NULL DEFAULT true,
    allowed_worker_classes text[],
    timeout_ceiling_seconds integer,
    resource_ceilings jsonb NOT NULL DEFAULT '{}',
    cache_namespace text,
    artifact_namespace text,
    trusted_cache_writes boolean NOT NULL DEFAULT true,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    UNIQUE (org_id, name),
    CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$')
);

ALTER TABLE secret_grants
    ADD COLUMN execution_profiles text[],
    ADD COLUMN ci_origins text[];

INSERT INTO execution_profiles (org_id, name)
SELECT org_id, 'standard' FROM organizations;
INSERT INTO execution_profiles (
    org_id, name, deny_secrets, runtime_capabilities, may_run_as_root,
    allowed_worker_classes, trusted_cache_writes
)
SELECT org_id, 'pr-untrusted', true, ARRAY['gpu']::text[], false,
       ARRAY(SELECT name FROM worker_classes wc WHERE wc.org_id = organizations.org_id AND NOT wc.protected),
       false
  FROM organizations;

-- +goose Down
ALTER TABLE secret_grants DROP COLUMN ci_origins, DROP COLUMN execution_profiles;
DROP TABLE execution_profiles;
