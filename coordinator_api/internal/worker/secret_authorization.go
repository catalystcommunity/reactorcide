package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type secretGrantStore interface {
	ListSecretGrantsForJob(ctx context.Context, userID string, projectID *string, jobName, jobFile string) ([]models.SecretGrant, error)
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
	grants, err := grantStore.ListSecretGrantsForJob(ctx, job.UserID, job.ProjectID, job.Name, job.JobFile)
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
				"grant_for": grant.SecretPathPrefix,
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
	if grant.JobName != "" && grant.JobName != job.Name {
		return false
	}
	if grant.JobFile != "" && grant.JobFile != job.JobFile {
		return false
	}
	prefix := strings.TrimSuffix(grant.SecretPathPrefix, "/")
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func derefSecretProjectID(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
