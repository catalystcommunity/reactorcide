-- +goose Up
-- UI auth, RBAC, and visibility plumbing.
--
-- Existing users act as orgs until first-class org membership exists
-- (user_id IS the org id everywhere in this schema).

ALTER TABLE projects ADD COLUMN is_private boolean NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN is_private boolean NOT NULL DEFAULT false;

-- Graceful cancellation adds a transitional 'cancelling' job status.
ALTER TABLE jobs DROP CONSTRAINT jobs_status_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_status_check CHECK (status IN (
    'submitted', 'queued', 'running', 'cancelling', 'completed', 'failed', 'cancelled', 'timeout'
));

-- Simple global key/value settings store (jsonb values).
CREATE TABLE global_settings (
  key text PRIMARY KEY,
  value jsonb NOT NULL,
  updated_at timestamp DEFAULT timezone('utc', now()) NOT NULL
);

-- Groups of users, scoped to an org (org_id == users.user_id today).
CREATE TABLE groups (
  group_id uuid DEFAULT generate_ulid() PRIMARY KEY,
  org_id uuid NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  name text NOT NULL,
  description text,
  created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  updated_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  UNIQUE (org_id, name)
);

CREATE INDEX groups_org_id_idx ON groups(org_id);

CREATE TABLE group_members (
  group_id uuid NOT NULL REFERENCES groups(group_id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  added_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  PRIMARY KEY (group_id, user_id)
);

CREATE INDEX group_members_user_id_idx ON group_members(user_id);

-- Role assignments: who (user or group) holds what role at what scope.
CREATE TABLE role_assignments (
  assignment_id uuid DEFAULT generate_ulid() PRIMARY KEY,
  principal_type text NOT NULL,
  principal_id uuid NOT NULL,
  scope_type text NOT NULL,
  scope_id uuid,
  role text NOT NULL,
  created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  created_by uuid,
  CONSTRAINT role_assignments_principal_type_check CHECK (principal_type IN ('user', 'group')),
  CONSTRAINT role_assignments_scope_type_check CHECK (scope_type IN ('global', 'org', 'project')),
  CONSTRAINT role_assignments_role_check CHECK (role IN ('admin', 'owner', 'member'))
);

CREATE UNIQUE INDEX role_assignments_unique_idx ON role_assignments (
  principal_type,
  principal_id,
  scope_type,
  coalesce(scope_id, '00000000-0000-0000-0000-000000000000'::uuid),
  role
);

CREATE INDEX role_assignments_principal_idx ON role_assignments(principal_type, principal_id);
CREATE INDEX role_assignments_scope_idx ON role_assignments(scope_type, scope_id);

-- Opaque UI session tokens; only the SHA-256 hash is ever stored.
CREATE TABLE ui_sessions (
  session_id uuid DEFAULT generate_ulid() PRIMARY KEY,
  token_hash bytea NOT NULL,
  user_id uuid NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  expires_at timestamp NOT NULL,
  last_seen_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  revoked_at timestamp
);

CREATE UNIQUE INDEX ui_sessions_token_hash_idx ON ui_sessions(token_hash);
CREATE INDEX ui_sessions_user_id_idx ON ui_sessions(user_id);
CREATE INDEX ui_sessions_expires_at_idx ON ui_sessions(expires_at);

-- LinkKeys identity linked to a local users row (created on first login).
CREATE TABLE auth_identities (
  identity_id uuid DEFAULT generate_ulid() PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  subject text NOT NULL,
  handle text,
  domain text NOT NULL,
  display_name text,
  created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  last_login_at timestamp
);

CREATE UNIQUE INDEX auth_identities_user_id_idx ON auth_identities(user_id);
CREATE UNIQUE INDEX auth_identities_subject_idx ON auth_identities(subject);

-- Encrypted-at-rest auth credentials (local-RP identity bundle, RP API key),
-- Fernet-encrypted under a master key like org encryption keys.
CREATE TABLE auth_credentials (
  name text PRIMARY KEY,
  master_key_id uuid NOT NULL REFERENCES master_keys(key_id),
  encrypted_value bytea NOT NULL,
  created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  updated_at timestamp DEFAULT timezone('utc', now()) NOT NULL
);

-- Admission list of exact identity selectors. handle = '' is a bare-domain
-- wildcard row that matches any handle at that domain.
CREATE TABLE auth_trusted_identities (
  domain text NOT NULL,
  handle text NOT NULL DEFAULT '',
  source text NOT NULL DEFAULT 'admin',
  created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  PRIMARY KEY (domain, handle)
);

-- Admission list of domain regexes (RE2 via Go regexp, validated on write).
CREATE TABLE auth_trusted_domain_patterns (
  pattern_id uuid DEFAULT generate_ulid() PRIMARY KEY,
  pattern text NOT NULL,
  description text,
  created_by uuid,
  created_at timestamp DEFAULT timezone('utc', now()) NOT NULL
);

CREATE UNIQUE INDEX auth_trusted_domain_patterns_pattern_idx ON auth_trusted_domain_patterns(pattern);

-- Single-use, sha256-keyed pending login attempts (LinkKeys login flow state).
CREATE TABLE auth_login_attempts (
  attempt_hash bytea PRIMARY KEY,
  pending_login bytea NOT NULL,
  created_at timestamp DEFAULT timezone('utc', now()) NOT NULL,
  expires_at timestamp NOT NULL
);

CREATE INDEX auth_login_attempts_expires_at_idx ON auth_login_attempts(expires_at);

-- +goose Down
DROP INDEX IF EXISTS auth_login_attempts_expires_at_idx;
DROP TABLE IF EXISTS auth_login_attempts;

DROP INDEX IF EXISTS auth_trusted_domain_patterns_pattern_idx;
DROP TABLE IF EXISTS auth_trusted_domain_patterns;

DROP TABLE IF EXISTS auth_trusted_identities;

DROP TABLE IF EXISTS auth_credentials;

DROP INDEX IF EXISTS auth_identities_subject_idx;
DROP INDEX IF EXISTS auth_identities_user_id_idx;
DROP TABLE IF EXISTS auth_identities;

DROP INDEX IF EXISTS ui_sessions_expires_at_idx;
DROP INDEX IF EXISTS ui_sessions_user_id_idx;
DROP INDEX IF EXISTS ui_sessions_token_hash_idx;
DROP TABLE IF EXISTS ui_sessions;

DROP INDEX IF EXISTS role_assignments_scope_idx;
DROP INDEX IF EXISTS role_assignments_principal_idx;
DROP INDEX IF EXISTS role_assignments_unique_idx;
DROP TABLE IF EXISTS role_assignments;

DROP INDEX IF EXISTS group_members_user_id_idx;
DROP TABLE IF EXISTS group_members;

DROP INDEX IF EXISTS groups_org_id_idx;
DROP TABLE IF EXISTS groups;

DROP TABLE IF EXISTS global_settings;

ALTER TABLE jobs DROP CONSTRAINT jobs_status_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_status_check CHECK (status IN (
    'submitted', 'queued', 'running', 'completed', 'failed', 'cancelled', 'timeout'
));

ALTER TABLE users DROP COLUMN IF EXISTS is_private;
ALTER TABLE projects DROP COLUMN IF EXISTS is_private;
