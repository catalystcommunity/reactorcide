-- +goose Up
-- WP18: secret delivery through the egress proxy, and egress control.
--
-- A secret grant delivers its secrets as environment values ('env', the
-- behavior before this migration) or through the egress proxy ('proxy').
-- With 'proxy', the job gets a placeholder. The proxy puts the real value
-- in place of the placeholder, only in the named request header and only
-- for a host that matches proxy_hosts. proxy_header_format is an optional
-- template for the full header value, with "{}" for the secret (for
-- example "Bearer {}").
ALTER TABLE secret_grants ADD COLUMN delivery text NOT NULL DEFAULT 'env'
    CHECK (delivery IN ('env', 'proxy'));
ALTER TABLE secret_grants ADD COLUMN proxy_hosts text[] NOT NULL DEFAULT '{}';
ALTER TABLE secret_grants ADD COLUMN proxy_header text NOT NULL DEFAULT '';
ALTER TABLE secret_grants ADD COLUMN proxy_header_format text NOT NULL DEFAULT '';
ALTER TABLE secret_grants ADD CONSTRAINT secret_grants_proxy_hosts_required
    CHECK (delivery <> 'proxy' OR cardinality(proxy_hosts) > 0);

-- Egress control: when it is on for the organization, the project, or the
-- execution profile of a job, all egress of the job goes through the egress
-- proxy. Off for all existing rows, so current jobs do not change.
ALTER TABLE organizations ADD COLUMN egress_control boolean NOT NULL DEFAULT false;
ALTER TABLE projects ADD COLUMN egress_control boolean NOT NULL DEFAULT false;
ALTER TABLE execution_profiles ADD COLUMN egress_control boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE execution_profiles DROP COLUMN egress_control;
ALTER TABLE projects DROP COLUMN egress_control;
ALTER TABLE organizations DROP COLUMN egress_control;
ALTER TABLE secret_grants DROP CONSTRAINT secret_grants_proxy_hosts_required;
ALTER TABLE secret_grants DROP COLUMN proxy_header_format;
ALTER TABLE secret_grants DROP COLUMN proxy_header;
ALTER TABLE secret_grants DROP COLUMN proxy_hosts;
ALTER TABLE secret_grants DROP COLUMN delivery;
