package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Webhook headers of App deliveries. GitHub sets the target type
// "integration" and the target ID (the App ID) on every App delivery.
const (
	HeaderHookTargetType = "X-GitHub-Hook-Installation-Target-Type"
	HeaderHookTargetID   = "X-GitHub-Hook-Installation-Target-ID"
)

// AppIDFromHeaders returns the App ID of an App delivery. ok is false for a
// repository or organization webhook.
func AppIDFromHeaders(targetType, targetID string) (int64, bool) {
	if !strings.EqualFold(strings.TrimSpace(targetType), "integration") {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimSpace(targetID), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// IsInstallationEvent reports whether the App handles the event type itself
// (not the CI event path).
func IsInstallationEvent(eventType string) bool {
	switch eventType {
	case "installation", "installation_repositories", "installation_target", "github_app_authorization":
		return true
	}
	return false
}

type installationPayload struct {
	Action              string           `json:"action"`
	Installation        InstallationInfo `json:"installation"`
	Repositories        []Repository     `json:"repositories"`
	RepositoriesAdded   []Repository     `json:"repositories_added"`
	RepositoriesRemoved []Repository     `json:"repositories_removed"`
	RepositorySelection string           `json:"repository_selection"`
	Sender              struct {
		Login string `json:"login"`
	} `json:"sender"`
}

// EventResult says what an installation event did.
type EventResult struct {
	InstallationID int64                `json:"installation_id"`
	Action         string               `json:"action"`
	Repositories   []RegistrationResult `json:"repositories,omitempty"`
}

func installationRow(appID int64, info InstallationInfo) *models.GitHubAppInstallation {
	row := &models.GitHubAppInstallation{InstallationID: info.ID, AppID: appID, AccountLogin: info.Account.Login,
		AccountType: info.Account.Type, RepositorySelection: info.RepositorySelection, Permissions: info.Permissions,
		Suspended: info.SuspendedAt != nil}
	if row.Permissions == nil {
		row.Permissions = map[string]string{}
	}
	return row
}

// HandleInstallationEvent applies a verified installation or
// installation_repositories delivery of the App. A new installation belongs
// to the App that received it.
func (s *Service) HandleInstallationEvent(ctx context.Context, appID int64, eventType string, body []byte) (EventResult, error) {
	var payload installationPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return EventResult{}, fmt.Errorf("parsing %s event: %w", eventType, err)
	}
	result := EventResult{InstallationID: payload.Installation.ID, Action: payload.Action}
	if eventType != "installation" && eventType != "installation_repositories" {
		return result, nil
	}
	if payload.Installation.ID <= 0 {
		return result, fmt.Errorf("the %s event has no installation", eventType)
	}
	app, err := s.store.GetGitHubApp(ctx, appID)
	if err != nil {
		return result, err
	}
	if payload.Installation.AppID != 0 && payload.Installation.AppID != appID {
		return result, fmt.Errorf("the installation belongs to App %d, not to App %d", payload.Installation.AppID, appID)
	}
	installationID := payload.Installation.ID
	details := models.JSONB{"app_id": appID, "installation_id": installationID, "account": payload.Installation.Account.Login,
		"sender": payload.Sender.Login, "action": payload.Action}

	if eventType == "installation" {
		switch payload.Action {
		case "deleted":
			removed, err := s.store.MarkGitHubAppInstallationDeleted(ctx, installationID, s.now())
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return result, err
			}
			s.tokens.forgetInstallation(installationID)
			for _, row := range removed {
				result.Repositories = append(result.Repositories, s.disableRemoved(ctx, app, installationID, row))
			}
			audit.Record(ctx, s.store, "", "github_app.installation_deleted", "github_app_installation", strconv.FormatInt(installationID, 10), details)
			return result, nil
		case "suspend", "unsuspend":
			row := installationRow(appID, payload.Installation)
			row.Suspended = payload.Action == "suspend"
			if err := s.store.UpsertGitHubAppInstallation(ctx, row); err != nil {
				return result, err
			}
			s.tokens.forgetInstallation(installationID)
			audit.Record(ctx, s.store, "", "github_app.installation_"+payload.Action+"ed", "github_app_installation", strconv.FormatInt(installationID, 10), details)
			return result, nil
		}
		// created, new_permissions_accepted, and others: save the row.
		row := installationRow(appID, payload.Installation)
		if err := s.store.UpsertGitHubAppInstallation(ctx, row); err != nil {
			return result, err
		}
		s.tokens.forgetInstallation(installationID)
		if payload.Action == "created" {
			audit.Record(ctx, s.store, "", "github_app.installation_created", "github_app_installation", strconv.FormatInt(installationID, 10), details)
			for _, repo := range payload.Repositories {
				result.Repositories = append(result.Repositories, s.addRepository(ctx, app, row, repo))
			}
		}
		return result, nil
	}

	// installation_repositories: added or removed.
	row := installationRow(appID, payload.Installation)
	if payload.RepositorySelection != "" {
		row.RepositorySelection = payload.RepositorySelection
	}
	if err := s.store.UpsertGitHubAppInstallation(ctx, row); err != nil {
		return result, err
	}
	for _, repo := range payload.RepositoriesAdded {
		result.Repositories = append(result.Repositories, s.addRepository(ctx, app, row, repo))
	}
	for _, repo := range payload.RepositoriesRemoved {
		result.Repositories = append(result.Repositories, s.removeRepository(ctx, app, installationID, repo.FullName))
	}
	return result, nil
}

// disableRemoved disables the auto-registered project of a binding that an
// installation delete removed.
func (s *Service) disableRemoved(ctx context.Context, app *models.GitHubApp, installationID int64, row models.GitHubAppRepository) RegistrationResult {
	result := RegistrationResult{Repository: row.RepoFullName, Action: "removed"}
	if row.ProjectID != nil {
		result.ProjectID = *row.ProjectID
	}
	if !row.AutoRegistered || row.ProjectID == nil || s.projects == nil {
		return result
	}
	project, err := s.projects.FindProject(ctx, "github.com/"+row.RepoFullName)
	if err != nil || project == nil || project.ProjectID != *row.ProjectID || !project.Enabled {
		return result
	}
	if err := s.projects.SetProjectEnabled(ctx, project, false); err != nil {
		result.Detail = "could not disable the project: " + err.Error()
		return result
	}
	if _, _, err := s.store.RemoveGitHubAppRepository(ctx, installationID, row.RepoFullName, true, s.now()); err != nil {
		result.Detail = err.Error()
	}
	result.Action = "disabled"
	audit.Record(ctx, s.store, project.OwnershipOrgID(), "github_app.project_disabled", "project", project.ProjectID,
		models.JSONB{"app_id": app.AppID, "installation_id": installationID, "repository": row.RepoFullName})
	return result
}

// SyncResult is the result of a full sync of one App.
type SyncResult struct {
	AppID         int64                `json:"app_id"`
	Installations int                  `json:"installations"`
	Repositories  []RegistrationResult `json:"repositories"`
	Errors        []string             `json:"errors,omitempty"`
}

// SyncApp reads every installation and repository of the App from GitHub
// and applies them as the webhooks do. Use it after a missed webhook or
// when an App is registered after it was installed.
func (s *Service) SyncApp(ctx context.Context, appID int64) (SyncResult, error) {
	result := SyncResult{AppID: appID, Repositories: []RegistrationResult{}}
	app, err := s.store.GetGitHubApp(ctx, appID)
	if err != nil {
		return result, err
	}
	if !app.Enabled {
		return result, fmt.Errorf("GitHub App %d is disabled", appID)
	}
	appJWT, err := s.appJWT(app)
	if err != nil {
		return result, err
	}
	installations, err := s.apiFor(rateLimitTarget{appID: appID}).listInstallations(ctx, appJWT)
	if err != nil {
		return result, fmt.Errorf("listing installations: %w", err)
	}
	seen := map[int64]bool{}
	for _, info := range installations {
		seen[info.ID] = true
		result.Installations++
		row := installationRow(appID, info)
		if err := s.store.UpsertGitHubAppInstallation(ctx, row); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("installation %d: %v", info.ID, err))
			continue
		}
		s.tokens.forgetInstallation(info.ID)
		if row.Suspended {
			continue
		}
		token, err := s.installationToken(ctx, app, row, "")
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("installation %d: %v", info.ID, err))
			continue
		}
		repos, err := s.apiFor(rateLimitTarget{installationID: info.ID}).listInstallationRepositories(ctx, token)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("installation %d: listing repositories: %v", info.ID, err))
			continue
		}
		present := map[string]bool{}
		for _, repo := range repos {
			present[models.NormalizeGitHubRepoFullName(repo.FullName)] = true
			result.Repositories = append(result.Repositories, s.addRepository(ctx, app, row, repo))
		}
		bound, err := s.store.ListGitHubAppRepositories(ctx, info.ID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("installation %d: %v", info.ID, err))
			continue
		}
		for _, binding := range bound {
			if !present[binding.RepoFullName] {
				result.Repositories = append(result.Repositories, s.removeRepository(ctx, app, info.ID, binding.RepoFullName))
			}
		}
	}
	// An installation that GitHub no longer lists was deleted.
	known, err := s.store.ListGitHubAppInstallations(ctx, appID)
	if err == nil {
		for _, installation := range known {
			if seen[installation.InstallationID] || installation.DeletedAt != nil {
				continue
			}
			removed, err := s.store.MarkGitHubAppInstallationDeleted(ctx, installation.InstallationID, s.now())
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("installation %d: %v", installation.InstallationID, err))
				continue
			}
			s.tokens.forgetInstallation(installation.InstallationID)
			for _, row := range removed {
				result.Repositories = append(result.Repositories, s.disableRemoved(ctx, app, installation.InstallationID, row))
			}
		}
	}
	audit.Record(ctx, s.store, "", "github_app.synced", "github_app", strconv.FormatInt(appID, 10),
		models.JSONB{"installations": result.Installations, "repositories": len(result.Repositories), "errors": len(result.Errors)})
	return result, nil
}

// CheckRunRequest is a check_run or check_suite delivery that asks for a
// re-run.
type CheckRunRequest struct {
	// Kind is "check_run" or "check_suite".
	Kind   string
	Action string
	// ObjectID is the check run or check suite ID.
	ObjectID int64
	Name     string
	HeadSHA  string
	// AppID is the App that owns the run or the suite.
	AppID        int64
	PRNumbers    []int
	RepoFullName string
	SenderLogin  string
	// Identifier is the requested_action identifier.
	Identifier string
}

type checkPayload struct {
	Action   string `json:"action"`
	CheckRun *struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		HeadSHA string `json:"head_sha"`
		App     struct {
			ID int64 `json:"id"`
		} `json:"app"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_run"`
	CheckSuite *struct {
		ID      int64  `json:"id"`
		HeadSHA string `json:"head_sha"`
		App     struct {
			ID int64 `json:"id"`
		} `json:"app"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_suite"`
	RequestedAction *struct {
		Identifier string `json:"identifier"`
	} `json:"requested_action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

// ParseRerunRequest reads a check_run or check_suite delivery. ok is false
// when the delivery does not ask for a re-run: an action other than
// rerequested or requested_action "rerun".
func ParseRerunRequest(eventType string, body []byte) (CheckRunRequest, bool, error) {
	if eventType != "check_run" && eventType != "check_suite" {
		return CheckRunRequest{}, false, nil
	}
	var payload checkPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return CheckRunRequest{}, false, fmt.Errorf("parsing %s event: %w", eventType, err)
	}
	request := CheckRunRequest{Kind: eventType, Action: payload.Action, RepoFullName: payload.Repository.FullName,
		SenderLogin: payload.Sender.Login}
	switch {
	case eventType == "check_run" && payload.CheckRun != nil:
		run := payload.CheckRun
		request.ObjectID, request.Name, request.HeadSHA, request.AppID = run.ID, run.Name, run.HeadSHA, run.App.ID
		for _, pr := range run.PullRequests {
			request.PRNumbers = append(request.PRNumbers, pr.Number)
		}
	case eventType == "check_suite" && payload.CheckSuite != nil:
		suite := payload.CheckSuite
		request.ObjectID, request.HeadSHA, request.AppID = suite.ID, suite.HeadSHA, suite.App.ID
		for _, pr := range suite.PullRequests {
			request.PRNumbers = append(request.PRNumbers, pr.Number)
		}
	default:
		return request, false, nil
	}
	if payload.RequestedAction != nil {
		request.Identifier = payload.RequestedAction.Identifier
	}
	switch payload.Action {
	case "rerequested":
		return request, true, nil
	case "requested_action":
		return request, request.Identifier == "rerun", nil
	}
	return request, false, nil
}
