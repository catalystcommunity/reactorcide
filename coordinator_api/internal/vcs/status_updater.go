package vcs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/sirupsen/logrus"
)

// JobStatusUpdaterInterface allows the worker to call status updates with a mock in tests.
type JobStatusUpdaterInterface interface {
	UpdateJobStatus(ctx context.Context, job *models.Job) error
}

// TokenResolverFunc resolves a "path:key" secret reference to a plaintext token.
type TokenResolverFunc func(ctx context.Context, secretRef string) (string, error)

// ClientFactoryFunc creates a VCS client for the given provider using a specific token.
type ClientFactoryFunc func(provider Provider, token string) (Client, error)

// ProjectLookupFunc retrieves a project by ID.
type ProjectLookupFunc func(ctx context.Context, projectID string) (*models.Project, error)

// JobStatusUpdater handles updating VCS commit statuses based on job status changes
type JobStatusUpdater struct {
	vcsClients    map[Provider]Client
	projectLookup ProjectLookupFunc // optional: per-project token resolution
	tokenResolver TokenResolverFunc // optional: per-project secret resolution
	clientFactory ClientFactoryFunc // optional: create client with per-project token
	baseURL       string            // base URL for job links in commit statuses
	logger        *logrus.Logger
}

// NewJobStatusUpdater creates a new job status updater
func NewJobStatusUpdater() *JobStatusUpdater {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	return &JobStatusUpdater{
		vcsClients: make(map[Provider]Client),
		logger:     logger,
	}
}

// AddVCSClient adds a VCS client for a specific provider
func (u *JobStatusUpdater) AddVCSClient(provider Provider, client Client) {
	u.vcsClients[provider] = client
	u.logger.WithField("provider", provider).Info("Added VCS client to status updater")
}

// SetProjectLookup sets the function used to look up projects by ID.
func (u *JobStatusUpdater) SetProjectLookup(fn ProjectLookupFunc) {
	u.projectLookup = fn
}

// SetTokenResolver sets the function used to resolve secret references.
func (u *JobStatusUpdater) SetTokenResolver(fn TokenResolverFunc) {
	u.tokenResolver = fn
}

// SetClientFactory sets the function used to create per-project VCS clients.
func (u *JobStatusUpdater) SetClientFactory(fn ClientFactoryFunc) {
	u.clientFactory = fn
}

// DefaultStatusContext is used when no custom context is configured.
const DefaultStatusContext = "continuous-integration/reactorcide"

// JobMetadata represents VCS metadata stored in job notes
type JobMetadata struct {
	VCSProvider   string `json:"vcs_provider"`
	Repo          string `json:"repo"`
	PRNumber      int    `json:"pr_number,omitempty"`
	Branch        string `json:"branch,omitempty"`
	CommitSHA     string `json:"commit_sha"`
	StatusContext string `json:"status_context,omitempty"`
	IsEval        bool   `json:"is_eval,omitempty"`
}

// GetStatusContext returns the status context, falling back to the default.
func (m *JobMetadata) GetStatusContext() string {
	if m.StatusContext != "" {
		return m.StatusContext
	}
	return DefaultStatusContext
}

// UpdateJobStatus updates the VCS commit status based on job status
func (u *JobStatusUpdater) UpdateJobStatus(ctx context.Context, job *models.Job) error {
	// Parse VCS metadata from job notes
	if job.Notes == "" {
		// No VCS metadata, skip update
		return nil
	}

	var metadata JobMetadata
	if err := json.Unmarshal([]byte(job.Notes), &metadata); err != nil {
		u.logger.WithError(err).Debug("Job has no VCS metadata, skipping status update")
		return nil
	}

	// Eval jobs should not update commit status — only their child jobs should.
	if metadata.IsEval {
		u.logger.WithField("job_id", job.JobID).Debug("Skipping VCS status update for eval job")
		return nil
	}

	// Get the appropriate VCS client (per-project token takes priority)
	provider := Provider(metadata.VCSProvider)
	client := u.getClientForJob(ctx, job, provider)
	if client == nil {
		u.logger.WithField("provider", provider).Debug("No VCS client available for provider")
		return nil
	}

	// Map job status to VCS status
	vcsStatus := u.mapJobStatusToVCSStatus(job.Status)

	// Create status update
	update := StatusUpdate{
		SHA:         metadata.CommitSHA,
		State:       vcsStatus,
		TargetURL:   u.getJobURL(job.JobID),
		Description: u.getStatusDescription(job),
		Context:     metadata.GetStatusContext(),
	}

	// Update commit status
	if err := client.UpdateCommitStatus(ctx, metadata.Repo, update); err != nil {
		u.logger.WithError(err).WithFields(logrus.Fields{
			"job_id":   job.JobID,
			"repo":     metadata.Repo,
			"sha":      metadata.CommitSHA,
			"provider": provider,
		}).Error("Failed to update commit status")
		return fmt.Errorf("updating commit status: %w", err)
	}

	u.logger.WithFields(logrus.Fields{
		"job_id":   job.JobID,
		"job_status": job.Status,
		"vcs_status": vcsStatus,
		"repo":     metadata.Repo,
		"sha":      metadata.CommitSHA,
	}).Info("Updated VCS commit status")

	// If this is a PR and the job completed, add a comment
	if metadata.PRNumber > 0 && u.isJobComplete(job.Status) {
		comment := u.generatePRComment(job)
		if err := client.UpdatePRComment(ctx, metadata.Repo, metadata.PRNumber, comment); err != nil {
			u.logger.WithError(err).Warn("Failed to add PR comment")
			// Don't fail the whole operation if comment fails
		}
	}

	return nil
}

// mapJobStatusToVCSStatus maps job status to VCS commit status
func (u *JobStatusUpdater) mapJobStatusToVCSStatus(jobStatus string) StatusState {
	switch jobStatus {
	case "submitted", "queued":
		return StatusPending
	case "running":
		return StatusRunning
	case "completed":
		return StatusSuccess
	case "failed":
		return StatusFailure
	case "cancelled":
		return StatusCancelled
	case "timeout":
		return StatusError
	default:
		return StatusError
	}
}

// getStatusDescription generates a status description based on job state
func (u *JobStatusUpdater) getStatusDescription(job *models.Job) string {
	switch job.Status {
	case "submitted":
		return "CI build submitted"
	case "queued":
		return "CI build queued"
	case "running":
		return "CI build in progress"
	case "completed":
		if job.ExitCode != nil && *job.ExitCode == 0 {
			return "CI build passed"
		}
		return fmt.Sprintf("CI build completed with exit code %d", *job.ExitCode)
	case "failed":
		if job.LastError != "" {
			// Truncate error message if too long (accounting for "CI build failed: " prefix)
			// Target is ~65 chars total, so error message should be ~44 chars + "..."
			errMsg := job.LastError
			if len(errMsg) > 44 {
				errMsg = errMsg[:44] + "..."
			}
			return fmt.Sprintf("CI build failed: %s", errMsg)
		}
		return "CI build failed"
	case "cancelled":
		return "CI build cancelled"
	case "timeout":
		return "CI build timed out"
	default:
		return fmt.Sprintf("CI build %s", job.Status)
	}
}

// isJobComplete checks if a job is in a terminal state
func (u *JobStatusUpdater) isJobComplete(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "timeout"
}

// generatePRComment generates a comment for a PR based on job results
func (u *JobStatusUpdater) generatePRComment(job *models.Job) string {
	emoji := "❌"
	status := "Failed"

	if job.Status == "completed" && job.ExitCode != nil && *job.ExitCode == 0 {
		emoji = "✅"
		status = "Passed"
	} else if job.Status == "cancelled" {
		emoji = "⚠️"
		status = "Cancelled"
	} else if job.Status == "timeout" {
		emoji = "⏱️"
		status = "Timed Out"
	}

	comment := fmt.Sprintf(`## %s Reactorcide CI Build %s

**Job ID:** %s
**Status:** %s`,
		emoji, status, job.JobID, job.Status)

	if job.ExitCode != nil {
		comment += fmt.Sprintf("\n**Exit Code:** %d", *job.ExitCode)
	}

	if job.StartedAt != nil && job.CompletedAt != nil {
		duration := job.CompletedAt.Sub(*job.StartedAt)
		comment += fmt.Sprintf("\n**Duration:** %s", duration.Round(1).String())
	}

	if job.LastError != "" && job.Status == "failed" {
		comment += fmt.Sprintf("\n\n### Error Details\n```\n%s\n```", job.LastError)
	}

	comment += fmt.Sprintf("\n\n[View Full Logs](%s)", u.getJobURL(job.JobID))

	return comment
}

// getClientForJob returns the best VCS client for the job, trying per-project
// token first and falling back to the global client.
func (u *JobStatusUpdater) getClientForJob(ctx context.Context, job *models.Job, provider Provider) Client {
	// Try per-project token if all dependencies are wired
	if job.ProjectID != nil && u.projectLookup != nil && u.tokenResolver != nil && u.clientFactory != nil {
		project, err := u.projectLookup(ctx, *job.ProjectID)
		if err == nil && project != nil && project.VCSTokenSecret != "" {
			token, err := u.tokenResolver(ctx, project.VCSTokenSecret)
			if err != nil {
				u.logger.WithError(err).WithField("project_id", *job.ProjectID).Warn("Failed to resolve per-project VCS token, falling back to global")
			} else if token != "" {
				client, err := u.clientFactory(provider, token)
				if err != nil {
					u.logger.WithError(err).Warn("Failed to create per-project VCS client, falling back to global")
				} else {
					return client
				}
			}
		}
	}

	// Fall back to global client
	if client, ok := u.vcsClients[provider]; ok {
		return client
	}
	return nil
}

// SetBaseURL sets the base URL used for job links in commit statuses.
func (u *JobStatusUpdater) SetBaseURL(baseURL string) {
	u.baseURL = baseURL
}

// getJobURL returns the URL for a job
func (u *JobStatusUpdater) getJobURL(jobID string) string {
	if u.baseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/jobs/%s", u.baseURL, jobID)
}