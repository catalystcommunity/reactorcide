package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/sirupsen/logrus"
)

// WebhookHandler handles VCS webhook events
type WebhookHandler struct {
	store          store.Store
	corndogsClient corndogs.ClientInterface
	vcsClients     map[vcs.Provider]vcs.Client
	tokenResolver  vcs.TokenResolverFunc // optional: per-project secret resolution
	clientFactory  vcs.ClientFactoryFunc // optional: create client with per-project token
	logger         *logrus.Logger
}

// NewWebhookHandler creates a new webhook handler
func NewWebhookHandler(store store.Store, corndogsClient corndogs.ClientInterface) *WebhookHandler {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	return &WebhookHandler{
		store:          store,
		corndogsClient: corndogsClient,
		vcsClients:     make(map[vcs.Provider]vcs.Client),
		logger:         logger,
	}
}

// AddVCSClient adds a VCS client for a specific provider
func (h *WebhookHandler) AddVCSClient(provider vcs.Provider, client vcs.Client) {
	h.vcsClients[provider] = client
	h.logger.WithField("provider", provider).Info("Added VCS client")
}

// SetTokenResolver sets the function used to resolve per-project VCS token secrets.
func (h *WebhookHandler) SetTokenResolver(fn vcs.TokenResolverFunc) {
	h.tokenResolver = fn
}

// SetClientFactory sets the function used to create per-project VCS clients.
func (h *WebhookHandler) SetClientFactory(fn vcs.ClientFactoryFunc) {
	h.clientFactory = fn
}

// HandleGitHubWebhook handles GitHub webhook events
func (h *WebhookHandler) HandleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	h.handleWebhook(w, r, vcs.GitHub)
}

// HandleGitLabWebhook handles GitLab webhook events
func (h *WebhookHandler) HandleGitLabWebhook(w http.ResponseWriter, r *http.Request) {
	h.handleWebhook(w, r, vcs.GitLab)
}

// extractRepoCloneURL extracts the repository clone URL from a raw webhook
// payload without full parsing. This is used to look up the project before
// signature validation, enabling per-project webhook secrets.
func extractRepoCloneURL(body []byte, contentType string) (string, error) {
	jsonBody := body

	// Handle form-encoded payloads (payload=<url-encoded-json>)
	if strings.Contains(contentType, "application/x-www-form-urlencoded") {
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return "", fmt.Errorf("parse form: %w", err)
		}
		p := values.Get("payload")
		if p == "" {
			return "", fmt.Errorf("form-encoded body has no 'payload' field")
		}
		jsonBody = []byte(p)
	}

	// GitHub and GitLab both include repository info at the top level.
	// GitHub: {"repository": {"clone_url": "...", "full_name": "..."}}
	// GitLab: {"project": {"git_http_url": "...", "path_with_namespace": "..."}}
	var payload struct {
		Repository struct {
			CloneURL string `json:"clone_url"`
		} `json:"repository"`
		Project struct {
			GitHTTPURL string `json:"git_http_url"`
		} `json:"project"`
	}
	if err := json.Unmarshal(jsonBody, &payload); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}
	if payload.Repository.CloneURL != "" {
		return payload.Repository.CloneURL, nil
	}
	if payload.Project.GitHTTPURL != "" {
		return payload.Project.GitHTTPURL, nil
	}
	return "", fmt.Errorf("no clone URL found in payload")
}

// resolveWebhookSecret returns the per-project webhook secret for validating
// the incoming request. Each project must have a WebhookSecret field set
// (a "path:key" reference resolved from the secrets store). Returns empty
// string if no secret could be resolved.
func (h *WebhookHandler) resolveWebhookSecret(ctx context.Context, project *models.Project) string {
	if project == nil || project.WebhookSecret == "" {
		return ""
	}
	if h.tokenResolver == nil {
		h.logger.WithField("project", project.Name).Warn("Project has webhook_secret configured but no token resolver is available")
		return ""
	}
	secret, err := h.tokenResolver(ctx, project.WebhookSecret)
	if err != nil {
		h.logger.WithError(err).WithField("project", project.Name).Error("Failed to resolve per-project webhook secret")
		return ""
	}
	return secret
}

// handleWebhook processes webhook events from a specific provider
func (h *WebhookHandler) handleWebhook(w http.ResponseWriter, r *http.Request, provider vcs.Provider) {
	// Get the VCS client for this provider
	client, ok := h.vcsClients[provider]
	if !ok {
		h.logger.WithField("provider", provider).Error("VCS client not configured")
		http.Error(w, "VCS provider not configured", http.StatusInternalServerError)
		return
	}

	// Read the request body first for validation
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.logger.WithError(err).Error("Failed to read webhook body")
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	// Replace the body for parsing
	r.Body = io.NopCloser(bytes.NewReader(body))

	// Extract the repo clone URL from the raw payload and look up the project.
	// This enables per-project webhook secrets: we identify which project the
	// webhook is for, resolve its secret, then validate the HMAC signature.
	var project *models.Project
	repoCloneURL, extractErr := extractRepoCloneURL(body, r.Header.Get("Content-Type"))
	if extractErr != nil {
		h.logger.WithError(extractErr).Warn("Could not extract repo clone URL from webhook payload")
	} else {
		normalizedURL := vcs.NormalizeRepoURL(repoCloneURL)
		if p, err := h.store.GetProjectByRepoURL(context.Background(), normalizedURL); err == nil {
			project = p
		} else {
			h.logger.WithError(err).WithField("normalized_url", normalizedURL).Warn("Failed to look up project by repo URL")
		}
	}

	// Resolve webhook secret from per-project configuration
	secret := h.resolveWebhookSecret(context.Background(), project)
	if secret == "" {
		h.logger.WithField("project_found", project != nil).Error("Webhook secret not configured — rejecting request")
		http.Error(w, "Webhook secret not configured", http.StatusInternalServerError)
		return
	}

	// Create a new request with the body for validation
	validateReq, _ := http.NewRequest(r.Method, r.URL.String(), bytes.NewReader(body))
	validateReq.Header = r.Header

	if err := client.ValidateWebhook(validateReq, secret); err != nil {
		h.logger.WithError(err).Warn("Invalid webhook signature")
		http.Error(w, "Invalid webhook signature", http.StatusUnauthorized)
		return
	}

	// Parse the webhook event
	event, err := client.ParseWebhook(r)
	if err != nil {
		h.logger.WithError(err).Error("Failed to parse webhook")
		http.Error(w, "Failed to parse webhook", http.StatusBadRequest)
		return
	}

	// Log the received event
	h.logger.WithFields(logrus.Fields{
		"provider":   provider,
		"event_type": event.EventType,
		"repository": event.Repository.FullName,
	}).Info("Received webhook event")

	// Skip events that don't map to a known generic event type
	if event.GenericEvent == vcs.EventUnknown {
		h.logger.WithFields(logrus.Fields{
			"event_type": event.EventType,
			"provider":   provider,
		}).Debug("Ignoring unsupported event type")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	// Process the event based on type, passing the already-fetched project
	// to avoid a duplicate database lookup.
	switch {
	case event.PullRequest != nil:
		if err := h.processPullRequestEvent(event, client, project); err != nil {
			h.logger.WithError(err).Error("Failed to process pull request event")
			http.Error(w, "Failed to process event", http.StatusInternalServerError)
			return
		}
	case event.Push != nil:
		if err := h.processPushEvent(event, client, project); err != nil {
			h.logger.WithError(err).Error("Failed to process push event")
			http.Error(w, "Failed to process event", http.StatusInternalServerError)
			return
		}
	default:
		h.logger.WithField("event_type", event.EventType).Debug("Ignoring event with no PR or push info")
	}

	// Send success response
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// processPullRequestEvent processes a pull request event.
// The project parameter may be non-nil if it was already looked up during
// webhook secret resolution. If nil, the project is fetched by repo URL.
func (h *WebhookHandler) processPullRequestEvent(event *vcs.WebhookEvent, client vcs.Client, project *models.Project) error {
	pr := event.PullRequest

	// Use the pre-fetched project or look it up now
	if project == nil {
		normalizedRepoURL := vcs.NormalizeRepoURL(event.Repository.CloneURL)
		var err error
		project, err = h.store.GetProjectByRepoURL(context.Background(), normalizedRepoURL)
		if err != nil {
			h.logger.WithFields(logrus.Fields{
				"repo_url":    event.Repository.CloneURL,
				"normalized":  normalizedRepoURL,
				"error":       err.Error(),
			}).Debug("No project found for repository - skipping event")
			return nil // Not an error - just no project configured
		}
	}

	// Apply event filtering using the generic event type
	if !project.ShouldProcessEvent(string(event.GenericEvent), pr.BaseRef) {
		h.logger.WithFields(logrus.Fields{
			"project":      project.Name,
			"generic_event": string(event.GenericEvent),
			"base_branch":  pr.BaseRef,
		}).Debug("Event filtered out by project configuration")
		return nil
	}

	// Build eval job using the shared builder
	job := BuildEvalJob(project, event)

	// Store VCS metadata for status updates
	statusContext := vcs.DefaultStatusContext
	metadata := vcs.JobMetadata{
		VCSProvider:   string(event.Provider),
		Repo:          event.Repository.FullName,
		PRNumber:      pr.Number,
		CommitSHA:     pr.HeadSHA,
		StatusContext: statusContext,
	}
	metadataJSON, _ := json.Marshal(metadata)
	job.Notes = string(metadataJSON)

	// Create the job in the database
	if err := h.store.CreateJob(context.Background(), job); err != nil {
		return fmt.Errorf("creating job: %w", err)
	}

	// Submit job to Corndogs task queue
	h.submitJobToCorndogs(job)

	// Update commit status to pending (use per-project client if available)
	statusClient := h.getStatusClient(context.Background(), project, event.Provider, client)
	statusUpdate := vcs.StatusUpdate{
		SHA:         pr.HeadSHA,
		State:       vcs.StatusPending,
		TargetURL:   h.getJobURL(job.JobID),
		Description: "CI build queued",
		Context:     statusContext,
	}

	if err := statusClient.UpdateCommitStatus(context.Background(), event.Repository.FullName, statusUpdate); err != nil {
		h.logger.WithError(err).Warn("Failed to update commit status")
		// Don't fail the whole operation if status update fails
	}

	h.logger.WithFields(logrus.Fields{
		"job_id":    job.JobID,
		"project":   project.Name,
		"pr_number": pr.Number,
		"sha":       pr.HeadSHA,
	}).Info("Created eval job for pull request")

	return nil
}

// processPushEvent processes a push event.
// The project parameter may be non-nil if it was already looked up during
// webhook secret resolution. If nil, the project is fetched by repo URL.
func (h *WebhookHandler) processPushEvent(event *vcs.WebhookEvent, client vcs.Client, project *models.Project) error {
	push := event.Push

	// Skip deleted branches
	if push.Deleted {
		h.logger.WithField("ref", push.Ref).Debug("Ignoring branch deletion")
		return nil
	}

	// Extract branch name from ref
	branch := strings.TrimPrefix(push.Ref, "refs/heads/")

	// Use the pre-fetched project or look it up now
	if project == nil {
		normalizedRepoURL := vcs.NormalizeRepoURL(event.Repository.CloneURL)
		var err error
		project, err = h.store.GetProjectByRepoURL(context.Background(), normalizedRepoURL)
		if err != nil {
			h.logger.WithFields(logrus.Fields{
				"repo_url":    event.Repository.CloneURL,
				"normalized":  normalizedRepoURL,
				"error":       err.Error(),
			}).Debug("No project found for repository - skipping event")
			return nil // Not an error - just no project configured
		}
	}

	// Apply event filtering using the generic event type
	if !project.ShouldProcessEvent(string(event.GenericEvent), branch) {
		h.logger.WithFields(logrus.Fields{
			"project":       project.Name,
			"generic_event": string(event.GenericEvent),
			"branch":        branch,
		}).Debug("Event filtered out by project configuration")
		return nil
	}

	// Build eval job using the shared builder
	job := BuildEvalJob(project, event)

	// Store VCS metadata for status updates
	statusContext := vcs.DefaultStatusContext
	metadata := vcs.JobMetadata{
		VCSProvider:   string(event.Provider),
		Repo:          event.Repository.FullName,
		Branch:        branch,
		CommitSHA:     push.After,
		StatusContext: statusContext,
	}
	metadataJSON, _ := json.Marshal(metadata)
	job.Notes = string(metadataJSON)

	// Create the job in the database
	if err := h.store.CreateJob(context.Background(), job); err != nil {
		return fmt.Errorf("creating job: %w", err)
	}

	// Submit job to Corndogs task queue
	h.submitJobToCorndogs(job)

	// Update commit status to pending (use per-project client if available)
	statusClient := h.getStatusClient(context.Background(), project, event.Provider, client)
	statusUpdate := vcs.StatusUpdate{
		SHA:         push.After,
		State:       vcs.StatusPending,
		TargetURL:   h.getJobURL(job.JobID),
		Description: "CI build queued",
		Context:     statusContext,
	}

	if err := statusClient.UpdateCommitStatus(context.Background(), event.Repository.FullName, statusUpdate); err != nil {
		h.logger.WithError(err).Warn("Failed to update commit status")
		// Don't fail the whole operation if status update fails
	}

	h.logger.WithFields(logrus.Fields{
		"job_id":  job.JobID,
		"project": project.Name,
		"branch":  branch,
		"sha":     push.After,
	}).Info("Created eval job for push")

	return nil
}

// getStatusClient returns a per-project VCS client if a project token is configured,
// otherwise falls back to the provided global client.
func (h *WebhookHandler) getStatusClient(ctx context.Context, project *models.Project, provider vcs.Provider, fallback vcs.Client) vcs.Client {
	if project.VCSTokenSecret != "" && h.tokenResolver != nil && h.clientFactory != nil {
		token, err := h.tokenResolver(ctx, project.VCSTokenSecret)
		if err != nil {
			h.logger.WithError(err).WithField("project", project.Name).Warn("Failed to resolve per-project VCS token, falling back to global")
			return fallback
		}
		if token != "" {
			client, err := h.clientFactory(provider, token)
			if err != nil {
				h.logger.WithError(err).WithField("project", project.Name).Warn("Failed to create per-project VCS client, falling back to global")
				return fallback
			}
			return client
		}
	}
	return fallback
}

// submitJobToCorndogs submits a job to the Corndogs task queue
func (h *WebhookHandler) submitJobToCorndogs(job *models.Job) {
	if h.corndogsClient == nil {
		return
	}

	// Dereference pointer fields for payload
	sourceTypeStr := ""
	if job.SourceType != nil {
		sourceTypeStr = string(*job.SourceType)
	}
	sourceURL := ""
	if job.SourceURL != nil {
		sourceURL = *job.SourceURL
	}
	sourceRef := ""
	if job.SourceRef != nil {
		sourceRef = *job.SourceRef
	}
	sourcePath := ""
	if job.SourcePath != nil {
		sourcePath = *job.SourcePath
	}

	taskPayload := &corndogs.TaskPayload{
		JobID:   job.JobID,
		JobType: "run",
		Config: map[string]interface{}{
			"image":       job.RunnerImage,
			"command":     job.JobCommand,
			"working_dir": job.JobDir,
			"timeout":     job.TimeoutSeconds,
			"code_dir":    job.CodeDir,
			"job_dir":     job.JobDir,
		},
		Source: map[string]interface{}{
			"type":        sourceTypeStr,
			"url":         sourceURL,
			"ref":         sourceRef,
			"source_path": sourcePath,
		},
		Metadata: map[string]interface{}{
			"submitted_at": job.CreatedAt,
			"name":         job.Name,
			"description":  job.Description,
		},
	}

	// Add environment variables if present
	if job.JobEnvVars != nil {
		taskPayload.Config["environment"] = job.JobEnvVars
	}
	if job.JobEnvFile != "" {
		taskPayload.Config["env_file"] = job.JobEnvFile
	}

	task, err := h.corndogsClient.SubmitTask(context.Background(), taskPayload, int64(job.Priority))
	if err != nil {
		h.logger.WithFields(logrus.Fields{
			"job_id":   job.JobID,
			"job_name": job.Name,
			"queue":    job.QueueName,
			"error":    err.Error(),
		}).Error("Failed to submit task to Corndogs")
		job.Status = "failed"
		metrics.RecordCornDogsTaskSubmission(job.QueueName, false)
	} else {
		metrics.RecordCornDogsTaskSubmission(job.QueueName, true)
		taskID := task.Uuid
		job.CorndogsTaskID = &taskID
		job.Status = task.CurrentState
	}

	// Update job with Corndogs task ID and status
	if err := h.store.UpdateJob(context.Background(), job); err != nil {
		h.logger.WithFields(logrus.Fields{
			"job_id": job.JobID,
			"error":  err.Error(),
		}).Error("Failed to update job after Corndogs submission")
	}
}

// getJobURL returns the URL for a job
func (h *WebhookHandler) getJobURL(jobID string) string {
	if config.VCSBaseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/jobs/%s", config.VCSBaseURL, jobID)
}