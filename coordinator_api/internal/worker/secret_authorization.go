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

type secretGrantStore interface {
	ListSecretGrantsForJob(ctx context.Context, userID string, projectID *string, jobName string) ([]models.SecretGrant, error)
}

func (jp *JobProcessor) authorizeSecretAccess(ctx context.Context, job *models.Job, path, key string) error {
	if isJobScopedSecret(job, path) {
		logging.Log.WithFields(map[string]interface{}{
			"job_id": job.JobID,
			"path":   path,
			"key":    key,
			"scope":  "job",
		}).Info("Secret access allowed")
		return nil
	}

	grantStore, ok := jp.store.(secretGrantStore)
	if !ok {
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
