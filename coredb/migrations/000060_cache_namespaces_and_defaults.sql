-- +goose Up
-- WP14: cache and artifact segmentation, job links, environment defaults,
-- and the organization image policy (docs/builds-and-caches.md).
--
-- Execution profiles: the cache namespace of a job is the profile
-- cache_namespace (or the profile name). cache_namespace_classes splits the
-- namespace by ref class: the first listed class that the job has is added
-- as a suffix. cache_write_classes, when not empty, permits cache writes
-- only for a job with one of the listed classes (and trusted_cache_writes).
-- Both are empty for existing rows, so current jobs keep one namespace per
-- profile and the same write mode.
ALTER TABLE execution_profiles ADD COLUMN cache_namespace_classes text[] NOT NULL DEFAULT '{}';
ALTER TABLE execution_profiles ADD COLUMN cache_write_classes text[] NOT NULL DEFAULT '{}';

-- Named links that a job publishes in its workflow output ("links" key).
CREATE TABLE job_links (
    job_id uuid NOT NULL REFERENCES jobs(job_id) ON DELETE CASCADE,
    position integer NOT NULL,
    name text NOT NULL,
    url text NOT NULL,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    PRIMARY KEY (job_id, name),
    CHECK (char_length(name) BETWEEN 1 AND 100),
    CHECK (char_length(url) <= 2048 AND url ~* '^https?://')
);

-- Non-secret environment defaults for jobs, per organization, worker class,
-- or project. Precedence: job > project > worker class > organization. A
-- REACTORCIDE_* or RC_WF* key is reserved and refused.
CREATE TABLE environment_defaults (
    default_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    scope text NOT NULL CHECK (scope IN ('org', 'worker_class', 'project')),
    worker_class_id uuid REFERENCES worker_classes(class_id) ON DELETE CASCADE,
    project_id uuid REFERENCES projects(project_id) ON DELETE CASCADE,
    key text NOT NULL,
    value text NOT NULL,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    CHECK ((scope = 'org' AND worker_class_id IS NULL AND project_id IS NULL)
        OR (scope = 'worker_class' AND worker_class_id IS NOT NULL AND project_id IS NULL)
        OR (scope = 'project' AND project_id IS NOT NULL AND worker_class_id IS NULL)),
    CHECK (key ~ '^[A-Za-z_][A-Za-z0-9_]{0,127}$'),
    CHECK (upper(key) NOT LIKE 'REACTORCIDE\_%' AND upper(key) NOT LIKE 'RC\_WF%'),
    CHECK (char_length(value) <= 8192)
);
CREATE UNIQUE INDEX environment_defaults_scope_key ON environment_defaults
    (org_id, scope, coalesce(worker_class_id::text, ''), coalesce(project_id::text, ''), key);

-- The image policy of an organization. Off when there is no row or when
-- enabled is false, so existing organizations do not change.
CREATE TABLE organization_image_policies (
    org_id uuid PRIMARY KEY REFERENCES organizations(org_id) ON DELETE CASCADE,
    enabled boolean NOT NULL DEFAULT false,
    allowed_prefixes text[] NOT NULL DEFAULT '{}',
    require_digest boolean NOT NULL DEFAULT false,
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now())
);

-- +goose Down
DROP TABLE organization_image_policies;
DROP TABLE environment_defaults;
DROP TABLE job_links;
ALTER TABLE execution_profiles DROP COLUMN cache_write_classes;
ALTER TABLE execution_profiles DROP COLUMN cache_namespace_classes;
