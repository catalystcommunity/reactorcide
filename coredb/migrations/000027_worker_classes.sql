-- +goose Up
CREATE TABLE worker_classes (
    class_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    name text NOT NULL,
    protected boolean NOT NULL DEFAULT false,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    UNIQUE (org_id, name),
    CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$')
);

INSERT INTO worker_classes (org_id, name)
SELECT org_id, 'default' FROM organizations;

CREATE TABLE worker_class_pools (
    class_id uuid NOT NULL REFERENCES worker_classes(class_id) ON DELETE CASCADE,
    pool_id uuid NOT NULL REFERENCES worker_pools(pool_id) ON DELETE CASCADE,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    PRIMARY KEY (class_id, pool_id)
);
CREATE INDEX worker_class_pools_pool_id_idx ON worker_class_pools(pool_id);

INSERT INTO worker_class_pools (class_id, pool_id)
SELECT wc.class_id, wp.pool_id
  FROM worker_classes wc JOIN worker_pools wp ON wp.org_id = wc.org_id
ON CONFLICT DO NOTHING;

-- Preserve a small-installation global pool by granting the oldest one to
-- the default organization only.
INSERT INTO worker_class_pools (class_id, pool_id)
SELECT wc.class_id, wp.pool_id
  FROM worker_classes wc
  JOIN global_settings gs ON gs.key = 'default_org_id' AND trim(both '"' from gs.value::text) = wc.org_id::text
  CROSS JOIN LATERAL (
      SELECT pool_id FROM worker_pools WHERE org_id IS NULL ORDER BY created_at, pool_id LIMIT 1
  ) wp
ON CONFLICT DO NOTHING;

UPDATE queues SET org_id = trim(both '"' from (SELECT value::text FROM global_settings WHERE key = 'default_org_id'))::uuid
 WHERE org_id IS NULL;
ALTER TABLE queues ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE queues ADD COLUMN worker_class text NOT NULL DEFAULT 'default';
DROP INDEX queues_characteristics_hash_idx;
CREATE UNIQUE INDEX queues_org_class_characteristics_idx
    ON queues(org_id, worker_class, characteristics_hash);

ALTER TABLE jobs ADD COLUMN worker_class text NOT NULL DEFAULT 'default';
ALTER TABLE jobs ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE workflow_instances ALTER COLUMN org_id SET NOT NULL;

-- +goose Down
ALTER TABLE workflow_instances ALTER COLUMN org_id DROP NOT NULL;
ALTER TABLE jobs ALTER COLUMN org_id DROP NOT NULL;
ALTER TABLE jobs DROP COLUMN worker_class;
DROP INDEX queues_org_class_characteristics_idx;
CREATE UNIQUE INDEX queues_characteristics_hash_idx ON queues(characteristics_hash);
ALTER TABLE queues DROP COLUMN worker_class;
ALTER TABLE queues ALTER COLUMN org_id DROP NOT NULL;
DROP TABLE worker_class_pools;
DROP TABLE worker_classes;

