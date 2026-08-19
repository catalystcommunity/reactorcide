package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// DefaultCancelGrace is the fallback grace period used when a caller (e.g.
// internal/workerapi's lease construction) doesn't have an explicit
// operator-configured cancel grace. Mirrors
// REACTORCIDE_CANCEL_GRACE_SECONDS' own default in internal/config.
const DefaultCancelGrace = 60 * time.Second

// BuildJobEnv builds a job's base environment map (system REACTORCIDE_*/
// RC_WF_* vars, source/CI-source config, API credentials, and the job's own
// JobEnvVars). Exported so internal/workerapi's coordinator-mediated
// RequestJob can build an identical base env for a lease without duplicating
// this logic...} refs out of this map (see AuthorizeSecretAccess and
// ResolveSecretsInEnvFull).
func BuildJobEnv(job *models.Job) map[string]string {
	env := make(map[string]string)

	// Add system environment variables
	env["REACTORCIDE_JOB_ID"] = job.JobID
	env["REACTORCIDE_QUEUE"] = job.QueueName

	// Signal to runnerlib that it's running inside a container
	// This makes runnerlib use /job directly instead of creating ./job
	env["REACTORCIDE_IN_CONTAINER"] = "true"

	// Set the code and job directories for runnerlib
	// These absolute paths ensure runnerlib uses the correct paths in container mode
	env["REACTORCIDE_CODE_DIR"] = defaultJobCodeDir(job.CodeDir)
	env["REACTORCIDE_JOB_DIR"] = defaultJobDir(job.CodeDir, job.JobDir)

	if job.WorkflowID != nil && *job.WorkflowID != "" {
		env["RC_WF_ID"] = *job.WorkflowID
		env["RC_WF_VARS_FILE"] = "/job/workflow-vars.json"
		env["RC_WF_OUTPUT_FILE"] = "/job/workflow-output.json"
	}
	if job.WorkflowNodeID != nil && *job.WorkflowNodeID != "" {
		env["RC_WF_NODE_ID"] = *job.WorkflowNodeID
	}
	if job.WorkflowRunID != nil && *job.WorkflowRunID != "" {
		env["RC_WF_RUN_ID"] = *job.WorkflowRunID
	}
	if job.WorkflowNodeName != "" {
		env["RC_WF_NODE_NAME"] = job.WorkflowNodeName
	}

	// Pass API credentials so job containers can submit triggers via API
	jobAPIURL := os.Getenv("REACTORCIDE_JOB_API_URL")
	apiToken := os.Getenv("REACTORCIDE_API_TOKEN")
	if jobAPIURL != "" {
		env["REACTORCIDE_COORDINATOR_URL"] = jobAPIURL
	}
	if apiToken != "" {
		env["REACTORCIDE_API_TOKEN"] = apiToken
	}
	if jobAPIURL == "" || apiToken == "" {
		logging.Log.WithFields(map[string]interface{}{
			"has_api_url":   jobAPIURL != "",
			"has_api_token": apiToken != "",
		}).Warn("Job API trigger submission not fully configured — job containers will use file-based triggers only")
	}

	// Add job-specific environment variables
	if job.JobEnvVars != nil && len(job.JobEnvVars) > 0 {
		for key, value := range job.JobEnvVars {
			// Convert value to string
			var valueStr string
			switch v := value.(type) {
			case string:
				valueStr = v
			case int, int64, float64, bool:
				valueStr = fmt.Sprintf("%v", v)
			default:
				// For complex types, try JSON marshaling
				if jsonBytes, err := json.Marshal(v); err == nil {
					valueStr = string(jsonBytes)
				} else {
					valueStr = fmt.Sprintf("%v", v)
				}
			}
			env[key] = valueStr
		}
	}

	// Add authoritative source configuration after job-specific variables.
	// An eval job can carry an older CI source in its inherited environment.
	// The selected workflow source on the job model must take precedence.
	if job.SourceType != nil {
		env["REACTORCIDE_SOURCE_TYPE"] = string(*job.SourceType)
		if job.SourceURL != nil {
			env["REACTORCIDE_SOURCE_URL"] = *job.SourceURL
		}
		if job.SourceRef != nil {
			env["REACTORCIDE_SOURCE_REF"] = *job.SourceRef
		}
		if job.SourcePath != nil {
			env["REACTORCIDE_SOURCE_PATH"] = *job.SourcePath
		}
	}

	if job.CISourceType != nil {
		env["REACTORCIDE_CI_SOURCE_TYPE"] = string(*job.CISourceType)
		env["REACTORCIDE_CI_SOURCE_DIR"] = "/job/ci"
		if job.CISourceURL != nil {
			env["REACTORCIDE_CI_SOURCE_URL"] = *job.CISourceURL
		}
		if job.CISourceRef != nil {
			env["REACTORCIDE_CI_SOURCE_REF"] = *job.CISourceRef
		}
	}

	return env
}
