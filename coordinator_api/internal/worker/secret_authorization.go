package worker

import (
	"context"
	"fmt"
	pathmatch "path"
	"regexp"
	"strings"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// SecretGrantStore is the narrow store capability secret-grant
// authorization needs. Exported (alongside AuthorizeSecretAccess) so
// internal/workerapi's coordinator-mediated RequestJob can run the grant-
// authorization decision without reimplementing it -- WORKERS_PLAN.md
// requires secret resolution + grant authorization to happen coordinator-
// side at lease hand-off using this same code path.
type SecretGrantStore interface {
	ListSecretGrantsForJob(ctx context.Context, userID string, projectID *string, jobName string) ([]models.SecretGrant, error)
}

// AuthorizeSecretAccess is the free-function core of secret-grant
// authorization: a job-scoped secret path (isJobScopedSecret) is always
// allowed; anything else requires a matching models.SecretGrant row (looked
// up via grantStore, or denied outright if grantStore is nil -- e.g. a
// store that doesn't implement SecretGrantStore). internal/workerapi's
// RequestJob calls this directly so a coordinator-mediated worker's job is
// authorized identically to (the now-removed) local worker's decision.
func AuthorizeSecretAccess(ctx context.Context, grantStore SecretGrantStore, job *models.Job, path, key string) error {
	if isJobScopedSecret(job, path) {
		logging.Log.WithFields(map[string]interface{}{
			"job_id": job.JobID,
			"path":   path,
			"key":    key,
			"scope":  "job",
		}).Info("Secret access allowed")
		return nil
	}

	if grantStore == nil {
		return fmt.Errorf("secret access denied for %s:%s: secret grants are not available", path, key)
	}
	grants, err := grantStore.ListSecretGrantsForJob(ctx, job.UserID, job.ProjectID, job.Name)
	if err != nil {
		return err
	}
	for _, grant := range grants {
		if grantMatchesSecret(grant, job, path) {
			logging.Log.WithFields(map[string]interface{}{
				"job_id":    job.JobID,
				"project":   derefSecretProjectID(job.ProjectID),
				"job_name":  job.Name,
				"job_file":  job.JobFile,
				"path":      path,
				"key":       key,
				"grant_id":  grant.GrantID,
				"grant":     grant.Name,
				"grant_for": grant.SecretPathPattern,
			}).Info("Secret access allowed")
			return nil
		}
	}

	logging.Log.WithFields(map[string]interface{}{
		"job_id":   job.JobID,
		"project":  derefSecretProjectID(job.ProjectID),
		"job_name": job.Name,
		"job_file": job.JobFile,
		"path":     path,
		"key":      key,
	}).Warn("Secret access denied")
	return fmt.Errorf("secret access denied for %s:%s", path, key)
}

func isJobScopedSecret(job *models.Job, path string) bool {
	if job.JobID != "" && path == "jobs/"+job.JobID {
		return true
	}
	if job.ProjectID != nil && *job.ProjectID != "" && job.JobFile != "" {
		return path == "projects/"+*job.ProjectID+"/jobs/"+job.JobFile
	}
	return false
}

func grantMatchesSecret(grant models.SecretGrant, job *models.Job, path string) bool {
	if !matchGrantPattern(grant.JobNameMatch, grant.JobNamePattern, job.Name, true) {
		return false
	}
	return matchGrantPattern(grant.SecretPathMatch, grant.SecretPathPattern, path, false)
}

func matchGrantPattern(matchType, pattern, value string, allowAny bool) bool {
	if matchType == "" {
		if allowAny && pattern == "" {
			matchType = models.SecretGrantMatchAny
		} else {
			matchType = models.SecretGrantMatchPrefix
		}
	}
	switch matchType {
	case models.SecretGrantMatchAny:
		return allowAny
	case models.SecretGrantMatchExact:
		return value == pattern
	case models.SecretGrantMatchPrefix:
		prefix := strings.TrimSuffix(pattern, "/")
		if prefix == "" {
			return false
		}
		if allowAny {
			return strings.HasPrefix(value, prefix)
		}
		return value == prefix || strings.HasPrefix(value, prefix+"/")
	case models.SecretGrantMatchGlob:
		ok, err := pathmatch.Match(pattern, value)
		return err == nil && ok
	case models.SecretGrantMatchRegex:
		ok, err := regexp.MatchString(pattern, value)
		return err == nil && ok
	default:
		return false
	}
}

func derefSecretProjectID(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
