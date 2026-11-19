package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs/githubapp"
	"github.com/sirupsen/logrus"
)

// GitHub App deliveries (WP11, docs/github-app.md). Every App delivery has
// the App ID in X-GitHub-Hook-Installation-Target-ID. The coordinator
// verifies the delivery with the webhook secret of that App, not with a
// project secret. Installation events register projects. check_run and
// check_suite re-run requests go through the one approval path. Push, pull
// request, comment, review, and team events then continue as for a
// repository webhook.

// githubAppWebhooks is the part of the App pool the webhook handler uses.
type githubAppWebhooks interface {
	WebhookSecret(ctx context.Context, appID int64) (secret string, ok bool, err error)
	HandleInstallationEvent(ctx context.Context, appID int64, eventType string, body []byte) (githubapp.EventResult, error)
	Store() store.GitHubAppStore
}

// SetGitHubApps wires the App pool. nil leaves App deliveries refused.
func (h *WebhookHandler) SetGitHubApps(apps githubAppWebhooks) {
	h.githubApps = apps
}

func githubAppDeliveryTarget(r *http.Request) (int64, bool) {
	return githubapp.AppIDFromHeaders(r.Header.Get(githubapp.HeaderHookTargetType), r.Header.Get(githubapp.HeaderHookTargetID))
}

// handleGitHubAppDelivery verifies an App delivery. It returns true when it
// wrote the response (an error, or an event that ends here).
func (h *WebhookHandler) handleGitHubAppDelivery(w http.ResponseWriter, r *http.Request, client vcs.Client, body []byte, appID int64) bool {
	fields := logrus.Fields{"app_id": appID, "event_type": r.Header.Get("X-GitHub-Event"), "delivery_id": r.Header.Get("X-GitHub-Delivery")}
	if h.githubApps == nil {
		h.logger.WithFields(fields).Warn("GitHub App delivery, but no GitHub App is configured")
		http.Error(w, "GitHub Apps are not configured", http.StatusUnauthorized)
		return true
	}
	ctx := r.Context()
	secret, ok, err := h.githubApps.WebhookSecret(ctx, appID)
	if err != nil {
		h.logger.WithError(err).WithFields(fields).Error("Could not read the GitHub App webhook secret")
		http.Error(w, "GitHub App webhook secret unavailable", http.StatusInternalServerError)
		return true
	}
	if !ok || secret == "" {
		h.logger.WithFields(fields).Warn("GitHub App delivery for an unknown or disabled App")
		http.Error(w, "Unknown GitHub App", http.StatusUnauthorized)
		return true
	}
	validateReq, _ := http.NewRequest(r.Method, r.URL.String(), bytes.NewReader(body))
	validateReq.Header = r.Header
	if err := client.ValidateWebhook(validateReq, secret); err != nil {
		h.logger.WithFields(fields).Warn("Invalid GitHub App webhook signature")
		http.Error(w, "Invalid webhook signature", http.StatusUnauthorized)
		return true
	}
	eventType := r.Header.Get("X-GitHub-Event")
	switch {
	case githubapp.IsInstallationEvent(eventType):
		result, err := h.githubApps.HandleInstallationEvent(ctx, appID, eventType, body)
		if err != nil {
			h.logger.WithError(err).WithFields(fields).Error("Could not apply a GitHub App installation event")
			http.Error(w, "Could not apply the installation event", http.StatusInternalServerError)
			return true
		}
		h.logger.WithFields(fields).WithField("installation_id", result.InstallationID).WithField("action", result.Action).
			WithField("repositories", len(result.Repositories)).Info("Applied GitHub App installation event")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "result": result})
		return true
	case eventType == "check_run" || eventType == "check_suite":
		request, rerun, err := githubapp.ParseRerunRequest(eventType, body)
		if err != nil {
			http.Error(w, "Failed to parse webhook", http.StatusBadRequest)
			return true
		}
		if rerun {
			if err := h.processCheckRerun(ctx, appID, request, r.Header.Get("X-GitHub-Delivery")); err != nil {
				h.logger.WithError(err).WithFields(fields).Warn("Could not process a check run re-run request")
				http.Error(w, "Could not process the re-run request", http.StatusInternalServerError)
				return true
			}
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return true
	}
	return false
}

// appDeliveryRepository is the repository of an App delivery payload.
type appDeliveryRepository struct {
	Repository *struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// appEventsWithoutRepository are the App events that have no repository and
// continue (team sync of groups, group_vcs_handler.go).
var appEventsWithoutRepository = map[string]bool{"team": true, "membership": true, "organization": true}

// githubAppDeliveryProject returns the project of a verified App delivery.
// The App secret only proves that the delivery came from that App, so:
//   - the repository must be bound to an active installation of that App,
//   - the GitHub repository ID must be the ID of the binding,
//   - the project must be the project of the binding, and must have the
//     same repository ID (a new repository with the name of a deleted one
//     is another repository).
//
// reason is not empty when the delivery must be ignored.
func (h *WebhookHandler) githubAppDeliveryProject(ctx context.Context, appID int64, eventType string, body []byte, project *models.Project) (*models.Project, string, error) {
	var payload appDeliveryRepository
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, "the payload is not JSON", nil
	}
	if payload.Repository == nil || strings.TrimSpace(payload.Repository.FullName) == "" {
		if appEventsWithoutRepository[eventType] {
			return nil, "", nil
		}
		return nil, "the delivery has no repository", nil
	}
	if h.githubApps == nil {
		return nil, "GitHub Apps are not configured", nil
	}
	repo := payload.Repository
	binding, err := h.githubApps.Store().ResolveGitHubAppBinding(ctx, repo.FullName)
	if err != nil {
		return nil, "", err
	}
	if binding == nil || binding.App.AppID != appID {
		return nil, "the repository is not in an active installation of this App", nil
	}
	if repo.ID > 0 && binding.Repository.RepositoryID != nil && *binding.Repository.RepositoryID != repo.ID {
		return nil, "the repository ID is not the ID of the binding", nil
	}
	if project == nil {
		return nil, "the repository has no project", nil
	}
	if repo.ID > 0 && project.VCSRepositoryID != nil && *project.VCSRepositoryID != repo.ID {
		// The same name, another repository: it is a new, unbound repository.
		return nil, "the project belongs to another repository with the same name", nil
	}
	if binding.Repository.ProjectID == nil || *binding.Repository.ProjectID != project.ProjectID {
		return nil, "the project is not the project of the App binding", nil
	}
	return project, "", nil
}

// processCheckRerun turns a "Re-run" of a Reactorcide check run (or a
// re-run of its check suite) into the rerun command of the GitHub sender.
// The sender must be a run approver of the project, the same as for a
// "/reactorcide rerun" comment.
func (h *WebhookHandler) processCheckRerun(ctx context.Context, appID int64, request githubapp.CheckRunRequest, deliveryID string) error {
	if request.AppID != appID {
		// The run or suite of another App.
		return nil
	}
	if request.Kind == "check_run" && request.Name != models.CIEventChecksContext {
		return nil
	}
	if request.RepoFullName == "" || request.HeadSHA == "" {
		return nil
	}
	project, err := h.store.GetProjectByRepoURL(ctx, vcs.NormalizeRepoURL("github.com/"+request.RepoFullName))
	if err != nil || project == nil {
		h.logger.WithField("repository", request.RepoFullName).Info("Check run re-run for a repository with no project")
		return nil
	}
	if h.githubApps == nil {
		return nil
	}
	binding, err := h.githubApps.Store().ResolveGitHubAppBinding(ctx, request.RepoFullName)
	if err != nil {
		return err
	}
	if binding == nil || binding.App.AppID != appID || binding.Repository.ProjectID == nil || *binding.Repository.ProjectID != project.ProjectID {
		h.logger.WithFields(logrus.Fields{"repository": request.RepoFullName, "app_id": appID}).
			Warn("Check run re-run refused: the repository is not bound to this App for the project")
		return nil
	}
	prNumber := 0
	if len(request.PRNumbers) > 0 {
		prNumber = request.PRNumbers[0]
	} else if h.githubApps != nil {
		if number, ok, lookupErr := h.githubApps.Store().LatestPRNumberForCommit(ctx, project.ProjectID, request.HeadSHA); lookupErr != nil {
			return lookupErr
		} else if ok {
			prNumber = number
		}
	}
	if prNumber <= 0 {
		// A re-run of a push has no pull request head to run again.
		h.logger.WithFields(logrus.Fields{"repository": request.RepoFullName, "head_sha": request.HeadSHA}).
			Info("Check run re-run refused: the commit has no pull request")
		return nil
	}
	command := approvals.Request{Project: project, PRNumber: prNumber, HeadSHA: request.HeadSHA,
		Command: approvals.Command{Name: approvals.CommandRerun}, Channel: models.CIApprovalChannelCheckRun,
		Provider: string(vcs.GitHub), Login: request.SenderLogin}
	if deliveryID != "" {
		command.SourceObject = fmt.Sprintf("github:%s:%d:%s", request.Kind, request.ObjectID, deliveryID)
	}
	return h.executeProviderCommand(ctx, command)
}
