package workerapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
)

// defaultSecretsProviderForOrg builds a secrets.Provider scoped to orgID,
// mirroring internal/uiapi.Deps.defaultSecretsProviderForOrg and
// internal/worker's own (*JobProcessor).getSecretsProvider("database" case):
// resolve the org's encryption key under the configured master keys, then
// wrap the global DB handle in a DatabaseProvider. Returns an error if
// secrets aren't configured on this server (no master key manager) rather
// than a nil-pointer panic.
func (d *Deps) defaultSecretsProviderForOrg(ctx context.Context, orgID string) (secrets.Provider, error) {
	if d.KeyManager == nil {
		return nil, fmt.Errorf("secrets are not configured on this server")
	}
	db := store.GetDBFromContext(ctx)
	if db == nil {
		return nil, fmt.Errorf("database is not available")
	}
	orgKey, err := d.KeyManager.GetOrgEncryptionKey(db, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve org encryption key: %w", err)
	}
	provider, err := secrets.NewDatabaseProvider(db, orgID, orgKey)
	if err != nil {
		return nil, fmt.Errorf("failed to build secrets provider: %w", err)
	}
	return provider, nil
}

// resolvedLeaseSecrets is the coordinator-side outcome of resolving a job's
// ${secret:path:key} references at lease hand-off: Env holds every base env
// var that did NOT reference a secret (safe to put in the lease's `env`
// field), Secrets holds exactly the resolved values for var names that DID
// (destined for the lease's SEPARATE `secrets` field only), and SecretValues
// is the flat list of resolved values alone -- used solely to seed the
// coordinator's log-masking backstop cache (see leaseSecretCache in
// service.go), never returned to any caller.
type resolvedLeaseSecrets struct {
	Env          map[string]string
	Secrets      map[string]string
	SecretValues []string
}

// resolveJobSecrets resolves every ${secret:path:key} reference in env
// (built by worker.BuildJobEnv), authorizing each one via
// worker.AuthorizeSecretAccess against the SAME grant rules
// (*JobProcessor).authorizeSecretAccess enforces for a locally-run job --
// job-scoped secrets (jobs/{id} or projects/{p}/jobs/{file}) are always
// allowed; anything else needs a matching models.SecretGrant row. Returns an
// error (never a partially-resolved result) on the first denied or
// unresolvable reference, exactly like
// internal/worker.(*JobProcessor).resolveJobSecrets/ResolveSecretsInEnvFull --
// callers must fail the whole claim, not hand out a lease with some secrets
// missing.
func (d *Deps) resolveJobSecrets(ctx context.Context, job *models.Job, env map[string]string) (*resolvedLeaseSecrets, error) {
	hasSecrets := false
	for _, v := range env {
		if worker.HasSecretRefs(v) {
			hasSecrets = true
			break
		}
	}
	if !hasSecrets {
		return &resolvedLeaseSecrets{Env: env, Secrets: map[string]string{}}, nil
	}

	if d.SecretsProvider == nil {
		return nil, fmt.Errorf("job contains secret references but secrets are not configured")
	}
	provider, err := d.SecretsProvider(ctx, job.UserID)
	if err != nil {
		return nil, fmt.Errorf("failed to get secrets provider: %w", err)
	}
	if provider == nil {
		return nil, fmt.Errorf("job contains secret references but secrets are not configured")
	}

	getSecret := func(path, key string) (string, error) {
		if err := worker.AuthorizeSecretAccess(ctx, d.Store, job, path, key); err != nil {
			return "", err
		}
		return provider.Get(ctx, path, key)
	}

	result, err := worker.ResolveSecretsInEnvFull(env, getSecret)
	if err != nil {
		return nil, err
	}

	secretNames := make(map[string]bool, len(result.SecretEnvNames))
	for _, name := range result.SecretEnvNames {
		secretNames[name] = true
	}

	out := &resolvedLeaseSecrets{
		Env:          make(map[string]string, len(result.Resolved)),
		Secrets:      make(map[string]string, len(secretNames)),
		SecretValues: result.SecretValues,
	}
	for k, v := range result.Resolved {
		if secretNames[k] {
			out.Secrets[k] = v
		} else {
			out.Env[k] = v
		}
	}
	if len(secretNames) > 0 {
		names := make([]string, 0, len(secretNames))
		for name := range secretNames {
			names = append(names, name)
		}
		out.Env["REACTORCIDE_SECRET_ENV_NAMES"] = strings.Join(names, ",")
	}
	return out, nil
}
