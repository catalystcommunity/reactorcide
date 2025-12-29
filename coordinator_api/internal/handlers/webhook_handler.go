package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/sirupsen/logrus"
)

// WebhookHandler handles VCS webhook events
type WebhookHandler struct {
	store         store.Store
	vcsClients    map[vcs.Provider]vcs.Client
	webhookSecret string
	logger        *logrus.Logger
}

// NewWebhookHandler creates a new webhook handler
func NewWebhookHandler(store store.Store) *WebhookHandler {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	return &WebhookHandler{
		store:      store,
		vcsClients: make(map[vcs.Provider]vcs.Client),
		logger:     logger,
	}
}

// AddVCSClient adds a VCS client for a specific provider
func (h *WebhookHandler) AddVCSClient(provider vcs.Provider, client vcs.Client) {
	h.vcsClients[provider] = client
	h.logger.WithField("provider", provider).Info("Added VCS client")
}

// SetWebhookSecret sets the webhook secret for validation
func (h *WebhookHandler) SetWebhookSecret(secret string) {
	h.webhookSecret = secret
}

// HandleGitHubWebhook handles GitHub webhook events
func (h *WebhookHandler) HandleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	h.handleWebhook(w, r, vcs.GitHub)
}

// HandleGitLabWebhook handles GitLab webhook events
func (h *WebhookHandler) HandleGitLabWebhook(w http.ResponseWriter, r *http.Request) {
	h.handleWebhook(w, r, vcs.GitLab)
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

	// Validate webhook signature if secret is configured
	if h.webhookSecret != "" {
		// Create a new request with the body for validation
		validateReq, _ := http.NewRequest(r.Method, r.URL.String(), bytes.NewReader(body))
		validateReq.Header = r.Header

		if err := client.ValidateWebhook(validateReq, h.webhookSecret); err != nil {
			h.logger.WithError(err).Warn("Invalid webhook signature")
			http.Error(w, "Invalid webhook signature", http.StatusUnauthorized)
			return
		}
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

	// Process the event based on type
	switch {
	case event.PullRequest != nil:
		if err := h.processPullRequestEvent(event, client); err != nil {
			h.logger.WithError(err).Error("Failed to process pull request event")
			http.Error(w, "Failed to process event", http.StatusInternalServerError)
			return
		}
	case event.Push != nil:
		if err := h.processPushEvent(event, client); err != nil {
			h.logger.WithError(err).Error("Failed to process push event")
			http.Error(w, "Failed to process event", http.StatusInternalServerError)
			return
		}
	default:
		h.logger.WithField("event_type", event.EventType).Debug("Ignoring unsupported event type")
	}

	// Send success response
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// processPullRequestEvent processes a pull request event
func (h *WebhookHandler) processPullRequestEvent(event *vcs.WebhookEvent, client vcs.Client) error {
	pr := event.PullRequest

	// Only process PR opened, reopened, or synchronize events
	if pr.Action != "opened" && pr.Action != "reopened" && pr.Action != "synchronize" {
		h.logger.WithField("action", pr.Action).Debug("Ignoring PR action")
		return nil
	}

	// Normalize repository URL and look up project
	normalizedRepoURL := vcs.NormalizeRepoURL(event.Repository.CloneURL)
	project, err := h.store.GetProjectByRepoURL(context.Background(), normalizedRepoURL)
	if err != nil {
		h.logger.WithFields(logrus.Fields{
			"repo_url":    event.Repository.CloneURL,
			"normalized":  normalizedRepoURL,
			"error":       err.Error(),
		}).Debug("No project found for repository - skipping event")
		return nil // Not an error - just no project configured
	}

	// Apply event filtering
	if !project.ShouldProcessEvent("pull_request", pr.BaseRef) {
		h.logger.WithFields(logrus.Fields{
			"project":     project.Name,
			"event_type":  "pull_request",
			"base_branch": pr.BaseRef,
		}).Debug("Event filtered out by project configuration")
		return nil
	}

	// Prepare source fields (untrusted code being tested)
	gitURL := event.Repository.CloneURL
	gitRef := pr.HeadSHA
	sourceType := models.SourceTypeGit

	// Prepare CI source fields (trusted CI pipeline code)
	var ciSourceType *models.SourceType
	var ciSourceURL *string
	var ciSourceRef *string
	if project.DefaultCISourceType != "" && project.DefaultCISourceType != models.SourceTypeNone {
		ciSourceType = &project.DefaultCISourceType
		if project.DefaultCISourceURL != "" {
			ciSourceURL = &project.DefaultCISourceURL
		}
		if project.DefaultCISourceRef != "" {
			ciSourceRef = &project.DefaultCISourceRef
		}
	}

	// Create a job for the PR
	job := &models.Job{
		ProjectID:   &project.ProjectID,
		Name:        fmt.Sprintf("PR #%d: %s", pr.Number, pr.Title),
		Description: fmt.Sprintf("CI build for PR #%d", pr.Number),
		// Source fields (untrusted)
		SourceURL:  &gitURL,
		SourceRef:  &gitRef,
		SourceType: &sourceType,
		// CI Source fields (trusted)
		CISourceType: ciSourceType,
		CISourceURL:  ciSourceURL,
		CISourceRef:  ciSourceRef,
		// Job configuration
		JobCommand:  h.getJobCommand(project),
		RunnerImage: project.DefaultRunnerImage,
		JobEnvVars: models.JSONB{
			"CI":             "true",
			"CI_PROVIDER":    string(event.Provider),
			"CI_PR_NUMBER":   fmt.Sprintf("%d", pr.Number),
			"CI_PR_SHA":      pr.HeadSHA,
			"CI_PR_REF":      pr.HeadRef,
			"CI_PR_BASE_REF": pr.BaseRef,
			"CI_REPO":        event.Repository.FullName,
		},
		Priority:  10, // Higher priority for PRs
		QueueName: project.DefaultQueueName,
	}

	// Store VCS metadata for status updates
	job.Notes = fmt.Sprintf(`{"vcs_provider":"%s","repo":"%s","pr_number":%d,"commit_sha":"%s"}`,
		event.Provider, event.Repository.FullName, pr.Number, pr.HeadSHA)

	// Create the job in the database
	err = h.store.CreateJob(context.Background(), job)
	if err != nil {
		return fmt.Errorf("creating job: %w", err)
	}

	// Update commit status to pending
	statusUpdate := vcs.StatusUpdate{
		SHA:         pr.HeadSHA,
		State:       vcs.StatusPending,
		TargetURL:   h.getJobURL(job.JobID),
		Description: "CI build queued",
		Context:     "continuous-integration/reactorcide",
	}

	if err := client.UpdateCommitStatus(context.Background(), event.Repository.FullName, statusUpdate); err != nil {
		h.logger.WithError(err).Warn("Failed to update commit status")
		// Don't fail the whole operation if status update fails
	}

	h.logger.WithFields(logrus.Fields{
		"job_id":    job.JobID,
		"project":   project.Name,
		"pr_number": pr.Number,
		"sha":       pr.HeadSHA,
	}).Info("Created job for pull request")

	return nil
}

// processPushEvent processes a push event
func (h *WebhookHandler) processPushEvent(event *vcs.WebhookEvent, client vcs.Client) error {
	push := event.Push

	// Skip deleted branches
	if push.Deleted {
		h.logger.WithField("ref", push.Ref).Debug("Ignoring branch deletion")
		return nil
	}

	// Extract branch name from ref
	branch := strings.TrimPrefix(push.Ref, "refs/heads/")

	// Normalize repository URL and look up project
	normalizedRepoURL := vcs.NormalizeRepoURL(event.Repository.CloneURL)
	project, err := h.store.GetProjectByRepoURL(context.Background(), normalizedRepoURL)
	if err != nil {
		h.logger.WithFields(logrus.Fields{
			"repo_url":    event.Repository.CloneURL,
			"normalized":  normalizedRepoURL,
			"error":       err.Error(),
		}).Debug("No project found for repository - skipping event")
		return nil // Not an error - just no project configured
	}

	// Apply event filtering
	if !project.ShouldProcessEvent("push", branch) {
		h.logger.WithFields(logrus.Fields{
			"project":    project.Name,
			"event_type": "push",
			"branch":     branch,
		}).Debug("Event filtered out by project configuration")
		return nil
	}

	// Prepare source fields (untrusted code being tested)
	gitURL := event.Repository.CloneURL
	gitRef := push.After
	sourceType := models.SourceTypeGit

	// Prepare CI source fields (trusted CI pipeline code)
	var ciSourceType *models.SourceType
	var ciSourceURL *string
	var ciSourceRef *string
	if project.DefaultCISourceType != "" && project.DefaultCISourceType != models.SourceTypeNone {
		ciSourceType = &project.DefaultCISourceType
		if project.DefaultCISourceURL != "" {
			ciSourceURL = &project.DefaultCISourceURL
		}
		if project.DefaultCISourceRef != "" {
			ciSourceRef = &project.DefaultCISourceRef
		}
	}

	// Create a job for the push
	job := &models.Job{
		ProjectID:   &project.ProjectID,
		Name:        fmt.Sprintf("Push to %s: %.7s", branch, push.After),
		Description: fmt.Sprintf("CI build for push to %s", branch),
		// Source fields (untrusted)
		SourceURL:  &gitURL,
		SourceRef:  &gitRef,
		SourceType: &sourceType,
		// CI Source fields (trusted)
		CISourceType: ciSourceType,
		CISourceURL:  ciSourceURL,
		CISourceRef:  ciSourceRef,
		// Job configuration
		JobCommand:  h.getJobCommand(project),
		RunnerImage: project.DefaultRunnerImage,
		JobEnvVars: models.JSONB{
			"CI":                "true",
			"CI_PROVIDER":       string(event.Provider),
			"CI_BRANCH":         branch,
			"CI_SHA":            push.After,
			"CI_REPO":           event.Repository.FullName,
			"CI_COMMIT_MESSAGE": h.getFirstCommitMessage(push),
		},
		Priority:  5, // Lower priority than PRs
		QueueName: project.DefaultQueueName,
	}

	// Store VCS metadata for status updates
	job.Notes = fmt.Sprintf(`{"vcs_provider":"%s","repo":"%s","branch":"%s","commit_sha":"%s"}`,
		event.Provider, event.Repository.FullName, branch, push.After)

	// Create the job in the database
	err = h.store.CreateJob(context.Background(), job)
	if err != nil {
		return fmt.Errorf("creating job: %w", err)
	}

	// Update commit status to pending
	statusUpdate := vcs.StatusUpdate{
		SHA:         push.After,
		State:       vcs.StatusPending,
		TargetURL:   h.getJobURL(job.JobID),
		Description: "CI build queued",
		Context:     "continuous-integration/reactorcide",
	}

	if err := client.UpdateCommitStatus(context.Background(), event.Repository.FullName, statusUpdate); err != nil {
		h.logger.WithError(err).Warn("Failed to update commit status")
		// Don't fail the whole operation if status update fails
	}

	h.logger.WithFields(logrus.Fields{
		"job_id":  job.JobID,
		"project": project.Name,
		"branch":  branch,
		"sha":     push.After,
	}).Info("Created job for push")

	return nil
}

// getJobCommand returns the job command from project config or default
func (h *WebhookHandler) getJobCommand(project *models.Project) string {
	if project.DefaultJobCommand != "" {
		return project.DefaultJobCommand
	}

	// Default CI script if no project command is configured
	return `#!/bin/bash
set -e

# Default CI script
echo "Running CI for repository: $CI_REPO"

# Try common CI patterns
if [ -f "Makefile" ]; then
    make test
elif [ -f "package.json" ]; then
    npm install && npm test
elif [ -f "go.mod" ]; then
    go test ./...
elif [ -f "requirements.txt" ]; then
    pip install -r requirements.txt && python -m pytest
else
    echo "No recognized test framework found"
    exit 0
fi
`
}

// getFirstCommitMessage gets the first commit message from a push
func (h *WebhookHandler) getFirstCommitMessage(push *vcs.PushInfo) string {
	if len(push.Commits) > 0 {
		return push.Commits[0].Message
	}
	return ""
}

// getJobURL returns the URL for a job
func (h *WebhookHandler) getJobURL(jobID string) string {
	// TODO: Make this configurable
	baseURL := "https://reactorcide.example.com"
	return fmt.Sprintf("%s/jobs/%s", baseURL, jobID)
}