package handlers

import (
	"context"
	"net/http"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/sirupsen/logrus"
)

// dynamicJobsDeniedMessage names the setting so the job author knows what to
// change.
const dynamicJobsDeniedMessage = "Dynamic jobs are disabled for this project: the dynamic_jobs setting " +
	"(organization or project) is \"deny\". Declare the job in a workflow file instead, or ask an administrator to allow dynamic jobs."

// isEvalResultParent reports whether a trigger request comes from an
// event's eval job. The eval result declares the event's workflows; it is not
// a dynamic job. The marker is the coordinator-written job metadata, never
// the trigger_type the client sends, so a regular job cannot claim to be an
// eval to get past the setting.
func isEvalResultParent(job *models.Job) bool {
	metadata, err := vcs.MetadataFromJob(job)
	return err == nil && metadata != nil && metadata.IsEval
}

// refuseDynamicJobs writes a 403 and an audit record and returns true when
// parentJob may not create jobs at runtime. A store without the setting, or
// a failed read, allows: the pre-setting behavior. A read failure is logged.
func (h *JobHandler) refuseDynamicJobs(ctx context.Context, w http.ResponseWriter, parentJob *models.Job) bool {
	if isEvalResultParent(parentJob) {
		return false
	}
	settings, ok := h.store.(store.EventSettingsStore)
	if !ok {
		return false
	}
	effective, err := settings.EffectiveDynamicJobs(ctx, parentJob.OrgID, parentJob.ProjectID)
	if err != nil {
		logrus.WithError(err).WithField("job_id", parentJob.JobID).Warn("Could not read dynamic_jobs setting; allowing the trigger")
		return false
	}
	if effective != models.DynamicJobsDeny {
		return false
	}
	details := models.JSONB{"setting": "dynamic_jobs", "value": effective}
	if parentJob.ProjectID != nil {
		details["project_id"] = *parentJob.ProjectID
	}
	audit.Record(ctx, h.store, parentJob.OrgID, "trigger.dynamic_jobs_denied", "job", parentJob.JobID, details)
	h.respondWithJSON(w, http.StatusForbidden, ErrorResponse{Error: "dynamic_jobs_denied", Message: dynamicJobsDeniedMessage})
	return true
}
