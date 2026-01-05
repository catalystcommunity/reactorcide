-- +goose Up
-- Secrets management tables for multi-tenant encrypted secrets storage

-- Master keys registry
-- Allows multiple active master keys for rotation without downtime
CREATE TABLE master_keys (
    key_id uuid DEFAULT generate_ulid() PRIMARY KEY,
    created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
    name text NOT NULL,                -- Human-readable name (e.g., "mk-2026-01")
    is_active boolean DEFAULT true NOT NULL,  -- Can be used for decryption
    is_primary boolean DEFAULT false NOT NULL, -- Used for new encryptions
    description text,
    -- Optional: 32-byte key material for auto-generated keys
    -- NULL for env-var-provided keys (those keys live only in env)
    key_material bytea
);

-- Only one primary key at a time
CREATE UNIQUE INDEX master_keys_single_primary ON master_keys(is_primary) WHERE is_primary = true;
CREATE UNIQUE INDEX master_keys_name_unique ON master_keys(name);

-- Org encryption keys - encrypted once per active master key
CREATE TABLE org_encryption_keys (
    id uuid DEFAULT generate_ulid() PRIMARY KEY,
    created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,

    -- Currently references user_id; will change to org_id when orgs are added
    user_id uuid NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    master_key_id uuid NOT NULL REFERENCES master_keys(key_id) ON DELETE CASCADE,

    -- The org's encryption key, encrypted with the specified master key
    encrypted_key bytea NOT NULL,
    -- Salt used for this org (same across all master key encryptions)
    salt bytea NOT NULL,

    CONSTRAINT org_keys_unique_per_master UNIQUE (user_id, master_key_id)
);

CREATE INDEX org_encryption_keys_user_id_idx ON org_encryption_keys(user_id);

-- Track when org secrets were initialized
ALTER TABLE users ADD COLUMN secrets_initialized_at timestamp;

-- Individual secrets storage
CREATE TABLE secrets (
    secret_id uuid DEFAULT generate_ulid() PRIMARY KEY,
    created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
    updated_at timestamp DEFAULT timezone('utc', now()) NOT NULL,

    -- Ownership/namespacing
    -- Currently user_id acts as org; will add org_id FK when orgs are implemented
    user_id uuid NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    -- Namespace is the org identifier as string. Used for:
    -- 1. Indexing and fast lookups
    -- 2. Prefix in external providers (OpenBao path, separate vault, etc.)
    -- 3. Display in admin views (not typically shown to end users)
    -- Will become org_id string when orgs are added
    namespace text NOT NULL,

    -- Secret identification
    path text NOT NULL,  -- Logical path (e.g., "project/prod")
    key text NOT NULL,   -- Key within path (e.g., "api_key")

    -- Encrypted value (Fernet format, encrypted with org key)
    encrypted_value bytea NOT NULL,

    -- Ensure uniqueness per org
    CONSTRAINT secrets_unique_per_org UNIQUE (user_id, path, key)
);

-- Indexes for efficient queries
CREATE INDEX secrets_user_id_idx ON secrets(user_id);
CREATE INDEX secrets_namespace_idx ON secrets(namespace);
CREATE INDEX secrets_namespace_path_idx ON secrets(namespace, path);

-- +goose Down
DROP TABLE IF EXISTS secrets;
DROP TABLE IF EXISTS org_encryption_keys;
DROP TABLE IF EXISTS master_keys;
ALTER TABLE users DROP COLUMN IF EXISTS secrets_initialized_at;
