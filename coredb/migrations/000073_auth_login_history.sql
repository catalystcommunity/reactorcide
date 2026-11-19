-- +goose Up
-- The login history of the instance (docs/ui-auth.md "Login history guard").
--
-- The default REACTORCIDE_UI_AUTH_MODE is none. In mode none the
-- coordinator enables no-login behaviours: wide service principal tokens,
-- anonymous cancel and retry, and pull request commands from the admin
-- token. An instance that used logins and then loses its auth mode setting
-- must not get these behaviours. The coordinator writes this row at each
-- start in mode local-rp or rp. At a start in mode none it refuses to start
-- while the row exists, unless REACTORCIDE_UI_AUTH_ALLOW_DOWNGRADE_TO_NONE
-- is true. That override deletes the row.
--
-- The table holds at most one row (singleton is always true).
CREATE TABLE auth_login_history (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    -- The last login mode that marked the row: local-rp, rp, or unknown
    -- (found in existing data, see below).
    login_mode text NOT NULL CHECK (login_mode IN ('local-rp', 'rp', 'unknown')),
    -- startup: a start in a login mode wrote the row. existing_data: this
    -- migration found data that only a login mode makes.
    source text NOT NULL CHECK (source IN ('startup', 'existing_data')),
    first_marked_at timestamp NOT NULL DEFAULT timezone('utc', now()),
    last_marked_at timestamp NOT NULL DEFAULT timezone('utc', now())
);

-- An instance that ran a login mode before this migration has data that
-- proves it. Mark it now, so the first start of this version in mode none
-- is refused. The evidence:
--   - a LinkKeys login identity (auth_identities). The synthetic
--     bootstrap-admin identity (domain reactorcide.local-bootstrap) is not
--     evidence: mode none can make it. Every real login session belongs to
--     a user with a login identity, so sessions need no separate check.
--   - a LinkKeys principal identity that a login verified (not the
--     bootstrap-admin identity).
--   - a stored login credential (the local-RP identity bundle or the RP
--     API key). Only a login mode stores them.
--   - a trusted identity or a trusted domain pattern (the admission list of
--     logins).
INSERT INTO auth_login_history (login_mode, source)
SELECT CASE
           WHEN EXISTS (SELECT 1 FROM auth_credentials WHERE name = 'rp_api_key') THEN 'rp'
           WHEN EXISTS (SELECT 1 FROM auth_credentials WHERE name = 'local_rp_identity') THEN 'local-rp'
           ELSE 'unknown'
       END,
       'existing_data'
 WHERE EXISTS (SELECT 1 FROM auth_identities WHERE domain <> 'reactorcide.local-bootstrap')
    OR EXISTS (SELECT 1 FROM principal_identities
                WHERE identity_type = 'linkkeys' AND verified_by = 'linkkeys'
                  AND identity_value <> 'bootstrap-admin'
                  AND identity_value NOT LIKE '%@reactorcide.local-bootstrap')
    OR EXISTS (SELECT 1 FROM auth_credentials)
    OR EXISTS (SELECT 1 FROM auth_trusted_identities)
    OR EXISTS (SELECT 1 FROM auth_trusted_domain_patterns);

-- +goose Down
DROP TABLE IF EXISTS auth_login_history;
