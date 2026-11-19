-- +goose Up
-- WP4: inbound federation for service principals, and Reactorcide as an
-- OIDC issuer for jobs (docs/federation.md, docs/oidc.md).
--
-- All tables are new. No existing row changes, so every existing org and
-- project keeps its current behavior:
--   - An org with no federation issuer accepts no federated token.
--   - The OIDC issuer is off for the whole deployment until the operator sets
--     REACTORCIDE_OIDC_ISSUER_URL. With no oidc_settings row, an org or a
--     project uses the default (enabled when the issuer is configured).

-- A trusted external OIDC issuer of one organization. An org admin registers
-- it (owner decision D24).
CREATE TABLE federation_issuers (
    issuer_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    -- A short name in the organization: lowercase letters, digits, hyphens.
    name text NOT NULL,
    -- The exact "iss" value of the tokens (no trailing-slash normalization).
    issuer_url text NOT NULL,
    -- The JWKS URL. Empty: read jwks_uri from
    -- ISSUER/.well-known/openid-configuration.
    jwks_url text NOT NULL DEFAULT '',
    -- A token must name one of these audiences in "aud".
    audiences text[] NOT NULL,
    -- Accepted signature algorithms. "none" and HMAC are never accepted.
    algorithms text[] NOT NULL DEFAULT ARRAY['RS256', 'ES256']::text[],
    clock_skew_seconds integer NOT NULL DEFAULT 60
        CHECK (clock_skew_seconds >= 0 AND clock_skew_seconds <= 600),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    CONSTRAINT federation_issuers_audiences_check CHECK (cardinality(audiences) > 0),
    CONSTRAINT federation_issuers_algorithms_check
        CHECK (cardinality(algorithms) > 0 AND algorithms <@ ARRAY['RS256', 'ES256', 'EdDSA']::text[])
);
CREATE UNIQUE INDEX federation_issuers_org_name_idx ON federation_issuers (org_id, name);
CREATE UNIQUE INDEX federation_issuers_org_url_idx ON federation_issuers (org_id, issuer_url);

-- A claim mapping: when every condition matches the claims of a verified
-- token of the issuer, the token exchange issues a short-lived token for the
-- service principal. Exactly one active mapping must match.
CREATE TABLE federation_mappings (
    mapping_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    issuer_id uuid NOT NULL REFERENCES federation_issuers(issuer_id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    name text NOT NULL,
    -- A service principal of the same organization.
    principal_id uuid NOT NULL REFERENCES principals(principal_id) ON DELETE CASCADE,
    -- [{"claim": "sub", "op": "equals" | "glob", "value": "..."}], at least
    -- one condition. The handler validates the shape.
    conditions jsonb NOT NULL,
    -- The capabilities of the issued token. They are always intersected with
    -- the roles of the principal. Never empty: a mapping never gives every
    -- capability.
    capabilities text[] NOT NULL,
    token_ttl_seconds integer NOT NULL DEFAULT 900
        CHECK (token_ttl_seconds >= 60 AND token_ttl_seconds <= 3600),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    CONSTRAINT federation_mappings_capabilities_check CHECK (cardinality(capabilities) > 0),
    CONSTRAINT federation_mappings_conditions_check
        CHECK (jsonb_typeof(conditions) = 'array' AND jsonb_array_length(conditions) > 0)
);
CREATE UNIQUE INDEX federation_mappings_issuer_name_idx ON federation_mappings (issuer_id, name);
CREATE INDEX federation_mappings_principal_idx ON federation_mappings (principal_id);

-- A token issued by a token exchange. The token itself is a normal
-- api_tokens row (service_token, intersect_roles); this row records where it
-- came from, so the expired rows can be removed.
CREATE TABLE federation_issued_tokens (
    token_id uuid PRIMARY KEY REFERENCES api_tokens(token_id) ON DELETE CASCADE,
    mapping_id uuid REFERENCES federation_mappings(mapping_id) ON DELETE SET NULL,
    issuer_id uuid REFERENCES federation_issuers(issuer_id) ON DELETE SET NULL,
    subject text NOT NULL,
    expires_at timestamp NOT NULL,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now())
);
CREATE INDEX federation_issued_tokens_expires_idx ON federation_issued_tokens (expires_at);

-- The signing keys of the Reactorcide OIDC issuer. The private key is a
-- PKCS#8 DER key, Fernet-encrypted under a master key (the same mechanism as
-- auth_credentials). The JWKS publishes every key from created_at until
-- retires_at. The issuer signs with the newest key whose activates_at has
-- passed. A rotation adds a key that activates later, so that consumers can
-- cache it before it is used.
CREATE TABLE oidc_signing_keys (
    kid text PRIMARY KEY,
    algorithm text NOT NULL CHECK (algorithm IN ('ES256', 'RS256')),
    public_jwk jsonb NOT NULL,
    master_key_id uuid NOT NULL REFERENCES master_keys(key_id),
    encrypted_private_key bytea NOT NULL,
    created_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    activates_at timestamp NOT NULL,
    retires_at timestamp
);
CREATE INDEX oidc_signing_keys_activates_idx ON oidc_signing_keys (activates_at);

-- The OIDC issuer settings of an organization (project_id NULL) or of one
-- project. With no row, the issuer is enabled (when it is configured) and no
-- extra claims apply.
CREATE TABLE oidc_settings (
    setting_id uuid PRIMARY KEY DEFAULT generate_ulid(),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    project_id uuid REFERENCES projects(project_id) ON DELETE CASCADE,
    -- inherit (project only), allow, or deny.
    issuer text NOT NULL DEFAULT 'inherit' CHECK (issuer IN ('inherit', 'allow', 'deny')),
    -- {"AUDIENCE": {"CLAIM": "VALUE", ...}, "*": {...}}. Operator-defined
    -- extra claims per audience. "*" applies to every audience. A standard
    -- claim name is refused by the handler.
    audience_claims jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    CONSTRAINT oidc_settings_org_issuer_check CHECK (project_id IS NOT NULL OR issuer <> 'inherit'),
    CONSTRAINT oidc_settings_claims_check CHECK (jsonb_typeof(audience_claims) = 'object')
);
CREATE UNIQUE INDEX oidc_settings_scope_idx
    ON oidc_settings (org_id, coalesce(project_id, '00000000-0000-0000-0000-000000000000'::uuid));

-- +goose Down
DROP TABLE IF EXISTS oidc_settings;
DROP TABLE IF EXISTS oidc_signing_keys;
DROP TABLE IF EXISTS federation_issued_tokens;
DROP TABLE IF EXISTS federation_mappings;
DROP TABLE IF EXISTS federation_issuers;
