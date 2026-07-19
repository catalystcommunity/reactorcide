package worker

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

const vcsAuthContainerDir = "/job/.reactorcide/vcs-auth"

func (jp *JobProcessor) prepareVCSCheckoutAuth(ctx context.Context, job *models.Job, env map[string]string, workspaceDir string) (*VCSAuthConfig, error) {
	urlsByProvider := checkoutURLsByProvider(env)
	if len(urlsByProvider) == 0 {
		return nil, nil
	}

	type providerToken struct {
		provider vcs.Provider
		token    string
		urls     []string
	}
	var tokens []providerToken
	for provider, urls := range urlsByProvider {
		token, ok, err := jp.resolveVCSCheckoutToken(ctx, job, provider)
		if err != nil {
			return nil, err
		}
		if !ok || token == "" {
			logging.Log.WithFields(map[string]interface{}{
				"job_id":   job.JobID,
				"provider": provider,
			}).Debug("No VCS checkout credential configured for provider")
			continue
		}
		tokens = append(tokens, providerToken{provider: provider, token: token, urls: urls})
	}
	if len(tokens) == 0 {
		return nil, nil
	}

	var credentials strings.Builder
	var secretValues []string
	for _, entry := range tokens {
		username := gitCredentialUsername(entry.provider)
		for _, rawURL := range entry.urls {
			lines := credentialLines(rawURL, username, entry.token)
			for _, line := range lines {
				credentials.WriteString(line)
				credentials.WriteByte('\n')
			}
		}
		secretValues = append(secretValues, entry.token)
	}

	auth := &VCSAuthConfig{
		ContainerDir: vcsAuthContainerDir,
		GitConfig: fmt.Sprintf(`[credential]
	helper = store --file %s/credentials
	useHttpPath = true
[safe]
	directory = *
`, vcsAuthContainerDir),
		Credentials:  credentials.String(),
		SecretValues: uniqueStrings(secretValues),
	}

	hostDir := filepath.Join(workspaceDir, ".reactorcide", "vcs-auth")
	if err := os.MkdirAll(hostDir, 0700); err != nil {
		return nil, fmt.Errorf("creating VCS auth dir: %w", err)
	}
	uid, gid := authFileOwner(job.RunAsUser)
	if err := os.Chown(hostDir, uid, gid); err != nil {
		logging.Log.WithError(err).WithField("path", hostDir).Warn("Failed to chown VCS auth dir")
		if chmodErr := os.Chmod(hostDir, 0755); chmodErr != nil {
			logging.Log.WithError(chmodErr).WithField("path", hostDir).Warn("Failed to relax VCS auth dir permissions after chown failure")
		}
	}
	if err := writePrivateFile(filepath.Join(hostDir, "gitconfig"), auth.GitConfig, uid, gid); err != nil {
		return nil, err
	}
	if err := writePrivateFile(filepath.Join(hostDir, "credentials"), auth.Credentials, uid, gid); err != nil {
		return nil, err
	}

	logging.Log.WithFields(map[string]interface{}{
		"job_id":    job.JobID,
		"auth_dir":  vcsAuthContainerDir,
		"providers": providerNames(urlsByProvider),
	}).Info("Prepared VCS checkout auth")
	return auth, nil
}

func cleanupVCSCheckoutAuth(workspaceDir string) {
	if workspaceDir == "" {
		return
	}
	_ = os.RemoveAll(filepath.Join(workspaceDir, ".reactorcide", "vcs-auth"))
}

// vcsCredentialRotationStore is the narrow store interface needed to list
// active rotatable VCS credentials and stamp last-used timestamps. Defined
// on the consumer side per the repo's narrow-interface + type-assertion
// pattern (see internal/handlers/project_handler.go and
// internal/worker/secret_authorization.go for precedent).
type vcsCredentialRotationStore interface {
	ListActiveProjectVCSCredentials(ctx context.Context, projectID, provider string) ([]models.ProjectVCSCredential, error)
	TouchProjectVCSCredentialLastUsed(ctx context.Context, id string) error
}

func (jp *JobProcessor) resolveVCSCheckoutToken(ctx context.Context, job *models.Job, provider vcs.Provider) (string, bool, error) {
	project, ownerID := jp.checkoutProjectOwner(ctx, job)

	// Highest-precedence active project_vcs_credentials rotation row wins
	// over the legacy project ref. Deactivated rows are never returned by
	// ListActiveProjectVCSCredentials, so they are never used here.
	if project != nil {
		if rotationStore, ok := jp.store.(vcsCredentialRotationStore); ok {
			rows, err := rotationStore.ListActiveProjectVCSCredentials(ctx, project.ProjectID, string(provider))
			if err != nil {
				logging.Log.WithError(err).WithFields(map[string]interface{}{
					"job_id":   job.JobID,
					"provider": provider,
				}).Warn("Failed to list active rotatable VCS credentials")
			}
			if row, ok := vcs.HighestPrecedenceActiveVCSCredential(rows); ok {
				token, err := jp.resolveSecretRefForUser(ctx, ownerID, row.SecretRef)
				if err != nil {
					return "", false, fmt.Errorf("resolving project VCS checkout credential (rotation): %w", err)
				}
				if token != "" {
					logVCSCheckoutCredential(job.JobID, provider, "project-rotation")
					jp.touchVCSCredentialLastUsed(ctx, row.ID)
					return token, true, nil
				}
			}
		}
	}

	if ref := vcs.ProjectVCSCredentialSecretRef(project, provider); ref != "" {
		token, err := jp.resolveSecretRefForUser(ctx, ownerID, ref)
		if err != nil {
			return "", false, fmt.Errorf("resolving project VCS checkout credential: %w", err)
		}
		logVCSCheckoutCredential(job.JobID, provider, "project")
		return token, token != "", nil
	}
	if ownerID == "" {
		ownerID = job.UserID
	}
	if ownerID != "" {
		if user, err := jp.store.GetUserByID(ctx, ownerID); err == nil && user != nil {
			if ref := vcs.UserVCSCredentialSecretRef(user, provider); ref != "" {
				token, err := jp.resolveSecretRefForUser(ctx, ownerID, ref)
				if err != nil {
					return "", false, fmt.Errorf("resolving org VCS checkout credential: %w", err)
				}
				logVCSCheckoutCredential(job.JobID, provider, "org")
				return token, token != "", nil
			}
		}
	}
	switch provider {
	case vcs.GitHub:
		if config.VCSGitHubToken != "" {
			logVCSCheckoutCredential(job.JobID, provider, "global")
			return config.VCSGitHubToken, true, nil
		}
	case vcs.GitLab:
		if config.VCSGitLabToken != "" {
			logVCSCheckoutCredential(job.JobID, provider, "global")
			return config.VCSGitLabToken, true, nil
		}
	}
	return "", false, nil
}

// touchVCSCredentialLastUsed stamps last_used_at for the rotation row that
// was successfully resolved into a checkout token. Best-effort: a stamp
// failure must never fail the job's checkout.
func (jp *JobProcessor) touchVCSCredentialLastUsed(ctx context.Context, rotationID string) {
	rotationStore, ok := jp.store.(vcsCredentialRotationStore)
	if !ok {
		return
	}
	if err := rotationStore.TouchProjectVCSCredentialLastUsed(ctx, rotationID); err != nil {
		logging.Log.WithError(err).WithField("rotation_id", rotationID).Warn("Failed to stamp VCS credential last_used_at")
	}
}

func (jp *JobProcessor) checkoutProjectOwner(ctx context.Context, job *models.Job) (*models.Project, string) {
	if job.ProjectID == nil || *job.ProjectID == "" {
		return nil, job.UserID
	}
	project, err := jp.store.GetProjectByID(ctx, *job.ProjectID)
	if err != nil || project == nil {
		if err != nil {
			logging.Log.WithError(err).WithField("project_id", *job.ProjectID).Debug("Failed to load project for VCS checkout credential lookup")
		}
		return nil, job.UserID
	}
	if project.UserID != nil && *project.UserID != "" {
		return project, *project.UserID
	}
	return project, job.UserID
}

func (jp *JobProcessor) resolveSecretRefForUser(ctx context.Context, userID, secretRef string) (string, error) {
	parts := strings.SplitN(secretRef, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("invalid secret reference: expected path:key")
	}
	provider, err := jp.getSecretsProviderForUser(ctx, userID)
	if err != nil {
		return "", err
	}
	if provider == nil {
		return "", fmt.Errorf("secrets are not configured")
	}
	return provider.Get(ctx, parts[0], parts[1])
}

func (jp *JobProcessor) getSecretsProviderForUser(ctx context.Context, userID string) (secrets.Provider, error) {
	storageType := jp.config.SecretsStorageType
	if storageType == "" {
		storageType = "database"
	}
	switch storageType {
	case "none", "disabled":
		return nil, nil
	case "local":
		if jp.config.SecretsLocalPassword == "" {
			return nil, fmt.Errorf("local secrets password not configured")
		}
		return secrets.NewLocalProvider(jp.config.SecretsLocalPath, jp.config.SecretsLocalPassword)
	case "database":
		if userID == "" {
			return nil, fmt.Errorf("user id is required for database secrets")
		}
		if jp.config.SecretsKeyManager == nil {
			return nil, fmt.Errorf("secrets key manager not configured")
		}
		db := store.GetDB()
		if db == nil {
			return nil, fmt.Errorf("database not available")
		}
		orgKey, err := jp.config.SecretsKeyManager.GetOrgEncryptionKey(db, userID)
		if err != nil {
			if err == secrets.ErrNotInitialized {
				return nil, nil
			}
			return nil, fmt.Errorf("failed to get org encryption key: %w", err)
		}
		return secrets.NewDatabaseProvider(db, userID, orgKey)
	default:
		return nil, fmt.Errorf("unknown secrets storage type: %s", storageType)
	}
}

func checkoutURLsByProvider(env map[string]string) map[vcs.Provider][]string {
	keys := []string{
		"REACTORCIDE_SOURCE_URL",
		"REACTORCIDE_CI_SOURCE_URL",
		"REACTORCIDE_HEAD_URL",
		"REACTORCIDE_BASE_URL",
	}
	result := map[vcs.Provider][]string{}
	seen := map[string]bool{}
	for _, key := range keys {
		raw := strings.TrimSpace(env[key])
		if raw == "" || seen[raw] {
			continue
		}
		provider, ok := providerForCheckoutURL(raw)
		if !ok {
			continue
		}
		result[provider] = append(result[provider], raw)
		seen[raw] = true
	}
	return result
}

func providerForCheckoutURL(raw string) (vcs.Provider, bool) {
	host := checkoutURLHost(raw)
	switch {
	case strings.Contains(host, "github.com"):
		return vcs.GitHub, true
	case strings.Contains(host, "gitlab.com"):
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

func credentialLines(raw, username, token string) []string {
	base, ok := credentialURL(raw, username, token)
	if !ok {
		return nil
	}
	lines := []string{base}
	if strings.HasSuffix(base, ".git") {
		lines = append(lines, strings.TrimSuffix(base, ".git"))
	} else {
		lines = append(lines, base+".git")
	}
	return lines
}

func credentialURL(raw, username, token string) (string, bool) {
	u, err := parseCheckoutHTTPURL(raw)
	if err != nil {
		return "", false
	}
	u.User = url.UserPassword(username, token)
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), true
}

func parseCheckoutHTTPURL(raw string) (*url.URL, error) {
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("unsupported checkout URL scheme")
		}
		u.Scheme = "https"
		return u, nil
	}
	if strings.Contains(raw, "@") && strings.Contains(raw, ":") {
		afterAt := raw[strings.LastIndex(raw, "@")+1:]
		parts := strings.SplitN(afterAt, ":", 2)
		return &url.URL{Scheme: "https", Host: parts[0], Path: "/" + parts[1]}, nil
	}
	return url.Parse("https://" + raw)
}

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

func writePrivateFile(path, contents string, uid, gid int) error {
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	if err := os.Chown(path, uid, gid); err != nil {
		logging.Log.WithError(err).WithField("path", path).Warn("Failed to chown VCS auth file")
		if chmodErr := os.Chmod(path, 0644); chmodErr != nil {
			logging.Log.WithError(chmodErr).WithField("path", path).Warn("Failed to relax VCS auth file permissions after chown failure")
		}
	}
	return nil
}

func authFileOwner(runAsUser string) (int, int) {
	user, err := DefaultRunAsUser(runAsUser)
	if err != nil {
		return 1001, 1001
	}
	parts := strings.SplitN(user, ":", 2)
	uid, err := strconv.Atoi(parts[0])
	if err != nil {
		uid = 1001
	}
	gid := uid
	if len(parts) == 2 {
		if parsed, err := strconv.Atoi(parts[1]); err == nil {
			gid = parsed
		}
	}
	return uid, gid
}

func logVCSCheckoutCredential(jobID string, provider vcs.Provider, scope string) {
	logging.Log.WithFields(map[string]interface{}{
		"job_id":   jobID,
		"provider": provider,
		"scope":    scope,
	}).Info("Using VCS checkout credential")
}

func providerNames(values map[vcs.Provider][]string) []string {
	names := make([]string, 0, len(values))
	for provider := range values {
		names = append(names, string(provider))
	}
	return names
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
