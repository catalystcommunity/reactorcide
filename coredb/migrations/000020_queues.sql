-- +goose Up
-- Characteristic-routed queues (WORKERS_PLAN.md "Queues").
--
-- A queue's identity in Corndogs is exactly its queue_uuid string -- nothing
-- else. This table is the source of truth for what characteristics that
-- UUID represents; Corndogs itself needs no pre-registration (queues are
-- implicit on first submit). characteristics_hash is a canonicalized,
-- type-sensitive SHA-256 of the characteristics jsonb (see
-- internal/characteristics.Hash) and is what the submit path's
-- find-or-create lookup keys on -- deliberately NOT computed in SQL, so the
-- one place that decides "these two characteristic sets are the same queue"
-- is the Go hash function, not a second implementation here that could
-- drift from it. For the same reason this migration does NOT seed the
-- default queue row: internal/store/postgres_store.EnsureDefaultQueue does
-- that at startup via the same ParseJobCharacteristics/Hash path every
-- other submit uses, so the seeded hash can never disagree with a
-- freshly-computed one.
CREATE TABLE queues (
    queue_id uuid DEFAULT generate_ulid() PRIMARY KEY,
    queue_uuid uuid NOT NULL DEFAULT gen_random_uuid(),
    characteristics jsonb NOT NULL,
    characteristics_hash text NOT NULL,
    display_name text,
    is_default boolean NOT NULL DEFAULT false,
    org_id uuid REFERENCES users(user_id),
    created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
    updated_at timestamp DEFAULT timezone('utc', now()) NOT NULL
);

CREATE UNIQUE INDEX queues_queue_uuid_idx ON queues(queue_uuid);
CREATE UNIQUE INDEX queues_characteristics_hash_idx ON queues(characteristics_hash);
CREATE INDEX queues_org_id_idx ON queues(org_id);

-- Per-job characteristics, matched against queues.characteristics at submit
-- time to resolve (find-or-create) the queue a job's Corndogs task is
-- routed to. Defaulted to '{}' rather than a populated {"os":"linux"} at
-- the DB layer for the same reason the default queue isn't seeded here --
-- internal/characteristics.ParseJobCharacteristics is the single place that
-- decides what an empty/omitted characteristics block resolves to, and the
-- submit path always writes an explicit value before a job row is created.
ALTER TABLE jobs ADD COLUMN characteristics jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE jobs DROP COLUMN IF EXISTS characteristics;
DROP TABLE IF EXISTS queues;
