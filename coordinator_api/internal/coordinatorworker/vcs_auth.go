package coordinatorworker

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// vcsAuthContainerDir is the in-container path a leased job's git checkout
// credentials are exposed at, matching the deleted
// internal/worker/vcs_checkout_auth.go's own constant and
// docs/vcs-credentials-and-secret-grants.md's documented
// "/job/.reactorcide/vcs-auth" layout.
const vcsAuthContainerDir = "/job/.reactorcide/vcs-auth"

// prepareVCSAuth turns a lease's coordinator-resolved VCSAuth (WORKERS_PLAN.md
// Wave 3 P3c) into per-lease git checkout credential material and registers
// the token with masker so it is scrubbed from every log line pumped to
// AppendLogs from this point on -- BEFORE the job container is ever spawned,
// exactly like lease.Secrets values are registered in runLease.
//
// The credential files are written under workspaceDir/.reactorcide/vcs-auth
// (host path). That placement does double duty:
//   - Docker/containerd runners bind-mount workspaceDir at /job, so the
//     files simply appear at vcsAuthContainerDir inside the container with
//     no runner-specific code (mirrors the deleted code's Docker/containerd
//     behavior, which never referenced JobConfig.VCSAuth at all).
//   - The returned *worker.VCSAuthConfig is also assigned to
//     JobConfig.VCSAuth so the Kubernetes runner (which does not bind-mount
//     a host workspace) can materialize the same content as a short-lived
//     Secret copied into an emptyDir (see internal/worker/kubernetes_runner.go).
//   - workspaceDir is removed by runLease's own `defer os.RemoveAll` once
//     the lease finishes, so this credential material is discarded with the
//     rest of the ephemeral workspace -- no separate cleanup step is needed.
//
// Returns (nil, nil) when the lease carries no vcs_auth (the common case --
// most jobs need no checkout credential).
func prepareVCSAuth(vcsAuth *csilapi.VCSAuth, workspaceDir, runAsUser string, masker *secrets.Masker) (*worker.VCSAuthConfig, error) {
	if vcsAuth == nil || vcsAuth.Token == "" {
		return nil, nil
	}

	// Register BEFORE writing/spawning anything, so a log line emitted
	// during or after container startup (e.g. a runnerlib checkout message
	// that echoes the credential URL) is masked from the very first chunk
	// pumped to AppendLogs.
	masker.RegisterSecret(vcsAuth.Token)

	lines := credentialLines(vcsAuth.Url, vcsAuth.Username, vcsAuth.Token)
	if len(lines) == 0 {
		logging.Log.WithFields(map[string]interface{}{
			"provider": vcsAuth.Provider,
			"url":      vcsAuth.Url,
		}).Warn("VCS auth token resolved but checkout URL could not be parsed; skipping git credential setup")
		return nil, nil
	}

	var credentials strings.Builder
	for _, line := range lines {
		credentials.WriteString(line)
		credentials.WriteByte('\n')
	}

	auth := &worker.VCSAuthConfig{
		ContainerDir: vcsAuthContainerDir,
		GitConfig: fmt.Sprintf(`[credential]
	helper = store --file %s/credentials
	useHttpPath = true
[safe]
	directory = *
`, vcsAuthContainerDir),
		Credentials:  credentials.String(),
		SecretValues: []string{vcsAuth.Token},
	}

	hostDir := filepath.Join(workspaceDir, ".reactorcide", "vcs-auth")
	if err := os.MkdirAll(hostDir, 0700); err != nil {
		return nil, fmt.Errorf("creating VCS auth dir: %w", err)
	}
	uid, gid := authFileOwner(runAsUser)
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
		"auth_dir": vcsAuthContainerDir,
		"provider": vcsAuth.Provider,
	}).Info("Prepared VCS checkout auth")
	return auth, nil
}

// credentialLines renders a resolved (url, username, token) tuple into the
// git-credential-store line(s) covering both the exact checkout URL form and
// its .git-suffixed/unsuffixed counterpart, mirroring the deleted
// vcs_checkout_auth.go's credentialLines exactly so both `git clone
// https://host/org/repo` and `.../repo.git` match a stored credential.
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

// parseCheckoutHTTPURL accepts an https(s) URL, an scp-like "git@host:org/repo"
// form, or a bare "host/org/repo" form, and normalizes all of them to an
// https URL git's credential store can match against -- mirroring the
// deleted vcs_checkout_auth.go's parseCheckoutHTTPURL exactly.
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

// authFileOwner resolves runAsUser (a lease's run_as_user, "uid:gid" form)
// to the uid/gid that should own the credential files so a non-root
// container user can still read them through the bind-mounted workspace,
// mirroring the deleted vcs_checkout_auth.go's authFileOwner exactly.
func authFileOwner(runAsUser string) (int, int) {
	user, err := worker.DefaultRunAsUser(runAsUser)
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
