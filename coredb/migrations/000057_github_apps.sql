-- +goose Up
-- GitHub Apps with a pool of Apps (plan item B6, decision D2,
-- docs/github-app.md).
--
-- A deployment can hold many GitHub Apps at the same time. Each installation
-- belongs to one App. Each repository is bound to one installation. A project
-- whose repository has a binding uses installation tokens of that
-- installation for VCS calls and checkout, and posts "Reactorcide Checks" and
-- "Reactorcide Reviews" as check runs. Every other project keeps its tokens
-- and the Commit Status API. Nothing is backfilled: no project is bound until
-- an operator registers an App and the App is installed.
--
-- Storage:
--   github_apps                       the App credentials (encrypted).
--   github_app_installations          one row for each installation.
--   github_app_installation_repositories  the repository bindings.
--   github_account_orgs               the account -> organization mapping of
--                                     self-service project registration.

CREATE TABLE github_apps (
    -- The numeric GitHub App ID.
    app_id bigint PRIMARY KEY CHECK (app_id > 0),
    slug text NOT NULL DEFAULT '',
    name text NOT NULL DEFAULT '',
    client_id text NOT NULL DEFAULT '',
    -- The PEM private key and the webhook secret, encrypted with the master
    -- key named in the *_key_name column (internal/secrets).
    private_key_key_name text NOT NULL,
    private_key_ciphertext bytea NOT NULL,
    webhook_secret_key_name text NOT NULL,
    webhook_secret_ciphertext bytea NOT NULL,
    -- The organization of new projects of this App when no account mapping
    -- applies. NULL: the default organization.
    default_org_id uuid REFERENCES organizations(org_id) ON DELETE SET NULL,
    -- Open registration: a new project of an account with no account
    -- mapping goes into default_org_id ENABLED. False (the default): such a
    -- project is created DISABLED and waits for an administrator to enable
    -- it ("pending approval"). An account mapping always enables.
    open_registration boolean NOT NULL DEFAULT false,
    enabled boolean NOT NULL DEFAULT true,
    -- The last rate-limit snapshot of App (JWT) calls.
    ratelimit_limit integer,
    ratelimit_remaining integer,
    ratelimit_used integer,
    ratelimit_reset_at timestamp with time zone,
    ratelimit_observed_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now())
);

CREATE TABLE github_app_installations (
    -- The numeric GitHub installation ID.
    installation_id bigint PRIMARY KEY CHECK (installation_id > 0),
    app_id bigint NOT NULL REFERENCES github_apps(app_id) ON DELETE CASCADE,
    account_login text NOT NULL DEFAULT '',
    -- "Organization" or "User".
    account_type text NOT NULL DEFAULT '',
    -- "all" or "selected".
    repository_selection text NOT NULL DEFAULT '',
    -- The permissions that the account granted, for example
    -- {"checks": "write"}. Token requests ask only for granted permissions.
    permissions jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(permissions) = 'object'),
    suspended boolean NOT NULL DEFAULT false,
    -- Set when GitHub sent installation "deleted". The row stays for audit.
    deleted_at timestamp with time zone,
    -- The last rate-limit snapshot of installation-token calls.
    ratelimit_limit integer,
    ratelimit_remaining integer,
    ratelimit_used integer,
    ratelimit_reset_at timestamp with time zone,
    ratelimit_observed_at timestamp with time zone,
    last_seen_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    created_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now())
);
CREATE INDEX github_app_installations_app_idx ON github_app_installations(app_id);
CREATE INDEX github_app_installations_account_idx ON github_app_installations(lower(account_login));

-- One binding for each repository. A repository in two installations (two
-- Apps on one account) keeps its first active binding, so check runs stay
-- with one App.
CREATE TABLE github_app_installation_repositories (
    -- owner/name, lowercase.
    repo_full_name text PRIMARY KEY CHECK (repo_full_name <> '' AND repo_full_name = lower(repo_full_name)),
    installation_id bigint NOT NULL REFERENCES github_app_installations(installation_id) ON DELETE CASCADE,
    repository_id bigint,
    private boolean NOT NULL DEFAULT false,
    -- The project of the repository, when one exists.
    project_id uuid REFERENCES projects(project_id) ON DELETE SET NULL,
    -- True when the App registration made the project. Only such a project
    -- is disabled when the repository leaves the installation.
    auto_registered boolean NOT NULL DEFAULT false,
    -- Set when the repository left the installation. A removed binding gives
    -- no token.
    removed_at timestamp with time zone,
    -- True when the removal disabled the project. A new add enables it again.
    disabled_by_removal boolean NOT NULL DEFAULT false,
    -- True when the App registration created the project DISABLED because
    -- no account mapping and no open registration applied. The project
    -- waits until an administrator enables it.
    pending_approval boolean NOT NULL DEFAULT false,
    added_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now()),
    updated_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now())
);
CREATE INDEX github_app_installation_repositories_installation_idx ON github_app_installation_repositories(installation_id);
CREATE INDEX github_app_installation_repositories_project_idx ON github_app_installation_repositories(project_id);

-- The numeric GitHub repository ID of the repository of a project. GitHub
-- can give a name to a new repository after the old repository with that
-- name is deleted or renamed. An App delivery or a binding with another ID
-- is for another repository, not for this project. NULL: not known yet (a
-- project from before the App); the first binding records it.
ALTER TABLE projects ADD COLUMN vcs_repository_id bigint;

CREATE TABLE github_account_orgs (
    -- The GitHub account login, lowercase.
    account_login text PRIMARY KEY CHECK (account_login <> '' AND account_login = lower(account_login)),
    org_id uuid NOT NULL REFERENCES organizations(org_id) ON DELETE CASCADE,
    created_at timestamp with time zone NOT NULL DEFAULT timezone('utc', now())
);

-- +goose Down
DROP TABLE IF EXISTS github_account_orgs;
ALTER TABLE projects DROP COLUMN IF EXISTS vcs_repository_id;
DROP TABLE IF EXISTS github_app_installation_repositories;
DROP TABLE IF EXISTS github_app_installations;
DROP TABLE IF EXISTS github_apps;
