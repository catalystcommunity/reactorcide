-- +goose Up
ALTER TABLE api_tokens ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE api_tokens ADD COLUMN subject_type text NOT NULL DEFAULT 'user_token'
    CHECK (subject_type IN ('instance_token', 'service_token', 'user_token', 'job_token'));
ALTER TABLE api_tokens ADD COLUMN owner_org_id uuid REFERENCES organizations(org_id) ON DELETE CASCADE;
ALTER TABLE api_tokens ADD COLUMN all_organizations boolean NOT NULL DEFAULT false;
ALTER TABLE api_tokens ADD COLUMN all_capabilities boolean NOT NULL DEFAULT false;
ALTER TABLE api_tokens ADD COLUMN bound_job_id uuid REFERENCES jobs(job_id) ON DELETE CASCADE;
ALTER TABLE api_tokens ADD COLUMN revoked_at timestamp;

UPDATE api_tokens SET all_organizations = true, all_capabilities = true;
DROP INDEX api_tokens_token_hash_idx;
CREATE UNIQUE INDEX api_tokens_token_hash_idx ON api_tokens(token_hash);

CREATE TABLE api_token_organizations (
    token_id uuid NOT NULL REFERENCES api_tokens(token_id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    PRIMARY KEY (token_id, org_id)
);
CREATE INDEX api_token_organizations_org_id_idx ON api_token_organizations(org_id);

CREATE TABLE api_token_capabilities (
    token_id uuid NOT NULL REFERENCES api_tokens(token_id) ON DELETE CASCADE,
    capability text NOT NULL,
    PRIMARY KEY (token_id, capability)
);

-- +goose Down
DROP TABLE api_token_capabilities;
DROP TABLE api_token_organizations;
DROP INDEX api_tokens_token_hash_idx;
CREATE INDEX api_tokens_token_hash_idx ON api_tokens(token_hash);
ALTER TABLE api_tokens DROP COLUMN revoked_at;
ALTER TABLE api_tokens DROP COLUMN bound_job_id;
ALTER TABLE api_tokens DROP COLUMN all_capabilities;
ALTER TABLE api_tokens DROP COLUMN all_organizations;
ALTER TABLE api_tokens DROP COLUMN owner_org_id;
ALTER TABLE api_tokens DROP COLUMN subject_type;
DELETE FROM api_tokens WHERE user_id IS NULL;
ALTER TABLE api_tokens ALTER COLUMN user_id SET NOT NULL;

