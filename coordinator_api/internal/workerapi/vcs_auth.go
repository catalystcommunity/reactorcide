package workerapi

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// resolveVCSAuth resolves a coordinator-side git checkout credential for a
// job's source repo. It reuses internal/vcs's rotation-aware resolution
// helpers (HighestPrecedenceActiveVCSCredential,
// ProjectVCSCredentialSecretRef, UserVCSCredentialSecretRef) and mirrors the
// precedence order the deleted internal/worker/vcs_checkout_auth.go's
// (*JobProcessor).resolveVCSCheckoutToken used: highest-precedence active
// project_vcs_credentials rotation row -> project's static ref -> owning
// user/org's ref -> global deployment config. The old JobProcessor glue was
// deleted with the direct Corndogs worker. The resolution logic
// itself lives on unchanged in internal/vcs and is reused here verbatim, just
// moved coordinator-side and narrowed to the job's single primary checkout
// URL (job.SourceURL, falling back to the denormalized job.VCSRepo) instead
// of enumerating every REACTORCIDE_*_URL env var, because a Lease carries at
// most one vcs_auth entry.
//
// Returns (nil, nil) when the job has no recognizable git-hosting checkout
// URL, or one exists but no credential is configured for it (a public repo
// -- not an error). Returns a non-nil error only for an actual resolution
// failure (malformed secret ref, secrets not configured, a store error) --
// RequestJob fails the claim on that path exactly like a denied job secret
// (see finalizeSecretDenial in service.go), mirroring
// prepareVCSCheckoutAuth's old error contract of failing the job outright.
//
// SECURITY: the returned token is a secret. It must only ever be placed in
// Lease.vcs_auth (never env, never the corndogs TaskPayload, never a log
// line) -- see service.go's RequestJob for how the caller folds the
// returned token into the leaseSecretCache masking backstop.
func (d *Deps) resolveVCSAuth(ctx context.Context, job *models.Job) (*csilapi.VCSAuth, error) {
	rawURL := checkoutURLForJob(job)
	if rawURL == "" {
		return nil, nil
	}
	provider, ok := providerForCheckoutURL(rawURL)
	if !ok {
		return nil, nil
	}

	project, ownerID := d.checkoutProjectOwner(ctx, job)

	token, scope, err := d.resolveVCSCheckoutToken(ctx, job, project, ownerID, provider)
	if err != nil {
		return nil, err
	}
	if token == "" {
		logging.Log.WithFields(map[string]interface{}{
			"job_id":   job.JobID,
			"provider": provider,
		}).Debug("No VCS checkout credential configured for provider")
		return nil, nil
	}

	logging.Log.WithFields(map[string]interface{}{
		"job_id":   job.JobID,
		"provider": provider,
		"scope":    scope,
	}).Info("Using VCS checkout credential")

	return &csilapi.VCSAuth{
		Provider: string(provider),
		Url:      rawURL,
		Username: gitCredentialUsername(provider),
		Token:    token,
	}, nil
}

// checkoutProjectOwner resolves the job's project and the organization that
// owns its secrets. UserID is creator attribution and must not select a
// secrets namespace.
func (d *Deps) checkoutProjectOwner(ctx context.Context, job *models.Job) (*models.Project, string) {
	if job.ProjectID == nil || *job.ProjectID == "" {
		return nil, job.OrgID
	}
	project, err := d.Store.GetProjectByID(ctx, *job.ProjectID)
	if err != nil || project == nil {
		if err != nil {
			logging.Log.WithError(err).WithField("project_id", *job.ProjectID).Debug("Failed to load project for VCS checkout credential lookup")
		}
		return nil, job.OrgID
	}
	if orgID := project.OwnershipOrgID(); orgID != "" {
		return project, orgID
	}
	return project, job.OrgID
}

// resolveVCSCheckoutToken mirrors the deleted
// (*JobProcessor).resolveVCSCheckoutToken resolution order exactly,
// coordinator-side.
func (d *Deps) resolveVCSCheckoutToken(ctx context.Context, job *models.Job, project *models.Project, ownerID string, provider vcs.Provider) (token, scope string, err error) {
	// Highest-precedence active project_vcs_credentials rotation row wins
	// over the legacy project ref. Deactivated rows are never returned by
	// ListActiveProjectVCSCredentials, so they are never used here.
	if project != nil {
		rows, listErr := d.Store.ListActiveProjectVCSCredentials(ctx, project.ProjectID, string(provider))
		if listErr != nil {
			logging.Log.WithError(listErr).WithFields(map[string]interface{}{
				"job_id":   job.JobID,
				"provider": provider,
			}).Warn("Failed to list active rotatable VCS credentials")
		}
		if row, ok := vcs.HighestPrecedenceActiveVCSCredential(rows); ok {
			t, rerr := d.resolveVCSSecretRef(ctx, ownerID, row.SecretRef)
			if rerr != nil {
				return "", "", fmt.Errorf("resolving project VCS checkout credential (rotation): %w", rerr)
			}
			if t != "" {
				d.touchVCSCredentialLastUsed(ctx, row.ID)
				return t, "project-rotation", nil
			}
		}
	}

	if ref := vcs.ProjectVCSCredentialSecretRef(project, provider); ref != "" {
		t, rerr := d.resolveVCSSecretRef(ctx, ownerID, ref)
		if rerr != nil {
			return "", "", fmt.Errorf("resolving project VCS checkout credential: %w", rerr)
		}
		if t != "" {
			return t, "project", nil
		}
	}

	orgID := ownerID
	if orgID == "" {
		orgID = job.OrgID
	}
	if orgID != "" {
		if user, uerr := d.Store.GetUserByID(ctx, orgID); uerr == nil && user != nil {
			if ref := vcs.UserVCSCredentialSecretRef(user, provider); ref != "" {
				t, rerr := d.resolveVCSSecretRef(ctx, orgID, ref)
				if rerr != nil {
					return "", "", fmt.Errorf("resolving org VCS checkout credential: %w", rerr)
				}
				if t != "" {
					return t, "org", nil
				}
			}
		}
	}

	switch provider {
	case vcs.GitHub:
		if config.VCSGitHubToken != "" {
			return config.VCSGitHubToken, "global", nil
		}
	case vcs.GitLab:
		if config.VCSGitLabToken != "" {
			return config.VCSGitLabToken, "global", nil
		}
	}
	return "", "", nil
}

// touchVCSCredentialLastUsed stamps last_used_at for the rotation row that
// was successfully resolved into a checkout token. Best-effort: a stamp
// failure must never fail the job's claim.
func (d *Deps) touchVCSCredentialLastUsed(ctx context.Context, rotationID string) {
	if err := d.Store.TouchProjectVCSCredentialLastUsed(ctx, rotationID); err != nil {
		logging.Log.WithError(err).WithField("rotation_id", rotationID).Warn("Failed to stamp VCS credential last_used_at")
	}
}

// resolveVCSSecretRef resolves a "path:key" secret reference under ownerID's
// secrets scope, using the same d.SecretsProvider a job's own
// ${secret:path:key} references are resolved through (see secrets.go) --
// but WITHOUT worker.AuthorizeSecretAccess's grant check, because VCS
// credentials are system credentials (docs/vcs-credentials-and-secret-
// grants.md: "Reactorcide treats VCS credentials as system credentials, not
// job credentials"), not subject to a job's secret-grant scoping.
func (d *Deps) resolveVCSSecretRef(ctx context.Context, ownerID, secretRef string) (string, error) {
	parts := strings.SplitN(secretRef, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("invalid secret reference: expected path:key")
	}
	if d.SecretsProvider == nil {
		return "", fmt.Errorf("secrets are not configured")
	}
	provider, err := d.SecretsProvider(ctx, ownerID)
	if err != nil {
		return "", err
	}
	if provider == nil {
		return "", fmt.Errorf("secrets are not configured")
	}
	return provider.Get(ctx, parts[0], parts[1])
}

// --- checkout URL / provider detection --------------------------------

// checkoutURLForJob picks the job's primary checkout URL: SourceURL (the
// untrusted repo being built/tested) when set, else the denormalized
// VCSRepo field (e.g. "github.com/org/repo") coerced to an https URL.
func checkoutURLForJob(job *models.Job) string {
	if job.SourceURL != nil {
		if raw := strings.TrimSpace(*job.SourceURL); raw != "" {
			return raw
		}
	}
	if job.VCSRepo != nil {
		if repo := strings.TrimSpace(*job.VCSRepo); repo != "" {
			if !strings.Contains(repo, "://") {
				return "https://" + repo
			}
			return repo
		}
	}
	return ""
}

func providerForCheckoutURL(raw string) (vcs.Provider, bool) {
	host := checkoutURLHost(raw)
	switch {
	case host == "github.com":
		return vcs.GitHub, true
	case host == "gitlab.com":
		return vcs.GitLab, true
	default:
		return "", false
	}
}

func checkoutURLHost(raw string) string {
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil {
			return strings.ToLower(u.Hostname())
		}
	}
	if strings.Contains(raw, "@") && strings.Contains(raw, ":") {
		afterAt := raw[strings.LastIndex(raw, "@")+1:]
		return strings.ToLower(strings.SplitN(afterAt, ":", 2)[0])
	}
	parts := strings.Split(strings.TrimPrefix(raw, "https://"), "/")
	if len(parts) > 0 {
		return strings.ToLower(parts[0])
	}
	return ""
}

// gitCredentialUsername returns the provider-appropriate git credential
// username for a token-based HTTPS credential -- GitHub's app/PAT
// convention ("x-access-token") vs GitLab's OAuth2-style convention
// ("oauth2"), mirroring the deleted vcs_checkout_auth.go exactly.
func gitCredentialUsername(provider vcs.Provider) string {
	switch provider {
	case vcs.GitHub:
		return "x-access-token"
	case vcs.GitLab:
		return "oauth2"
	default:
		return "oauth2"
	}
}
