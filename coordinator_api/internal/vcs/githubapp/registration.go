package githubapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// ProjectRegistry finds, creates, enables, and disables the projects of
// installation repositories (self-service registration).
type ProjectRegistry interface {
	// FindProject returns the project of a canonical repository URL, or nil.
	FindProject(ctx context.Context, repoURL string) (*models.Project, error)
	// CreateProject creates an enabled project with the defaults.
	CreateProject(ctx context.Context, project *models.Project) error
	// SetProjectEnabled changes only the enabled flag.
	SetProjectEnabled(ctx context.Context, project *models.Project, enabled bool) error
	// SetProjectRepositoryID records the VCS repository ID of a project
	// that has none.
	SetProjectRepositoryID(ctx context.Context, project *models.Project, repositoryID int64) error
	// OrganizationFor returns the organization of a new project of the
	// account and where it came from: the account mapping, else the App
	// default, else the default organization.
	OrganizationFor(ctx context.Context, app *models.GitHubApp, accountLogin string) (orgID string, source OrgSource, err error)
}

// OrgSource says which rule chose the organization of a new project.
type OrgSource string

const (
	// OrgSourceAccountMapping: an administrator mapped the account to the
	// organization (github-apps map-account).
	OrgSourceAccountMapping OrgSource = "account_mapping"
	// OrgSourceAppDefault: the default organization of the App.
	OrgSourceAppDefault OrgSource = "app_default"
	// OrgSourceDeploymentDefault: the default organization of the
	// deployment.
	OrgSourceDeploymentDefault OrgSource = "deployment_default"
)

// RegistrationApproved reports whether a new project is created enabled.
// Anybody can install a public App on an account, so a new project is
// enabled only when an administrator chose it: an account mapping for the
// account, or open registration on the App with an explicit default
// organization. Every other new project is created disabled and waits for
// an administrator ("pending approval").
func RegistrationApproved(app *models.GitHubApp, source OrgSource) bool {
	switch source {
	case OrgSourceAccountMapping:
		return true
	case OrgSourceAppDefault:
		return app != nil && app.OpenRegistration && app.DefaultOrgID != nil && *app.DefaultOrgID != ""
	}
	return false
}

// StoreRegistry is the ProjectRegistry of the coordinator store.
type StoreRegistry struct {
	Store store.Store
	Apps  store.GitHubAppStore
}

var _ AccountEntitlements = StoreRegistry{}

// ProjectOrgID implements AccountEntitlements.
func (r StoreRegistry) ProjectOrgID(ctx context.Context, projectID string) (string, error) {
	project, err := r.Store.GetProjectByID(ctx, projectID)
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil || project == nil {
		return "", err
	}
	return project.OwnershipOrgID(), nil
}

type vcsOwnerAllowlistStore interface {
	GetOrganizationVCSOwnerAllowlist(ctx context.Context, orgID string) ([]string, error)
}

// VCSOwnerAllowlist implements AccountEntitlements. A store without the
// allowlist has an empty allowlist.
func (r StoreRegistry) VCSOwnerAllowlist(ctx context.Context, orgID string) ([]string, error) {
	lists, ok := r.Store.(vcsOwnerAllowlistStore)
	if !ok {
		return nil, nil
	}
	return lists.GetOrganizationVCSOwnerAllowlist(ctx, orgID)
}

type defaultOrganizationStore interface {
	GetDefaultOrganization(ctx context.Context) (*models.Organization, error)
}

func (r StoreRegistry) FindProject(ctx context.Context, repoURL string) (*models.Project, error) {
	project, err := r.Store.GetProjectByRepoURL(ctx, repoURL)
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return project, err
}

func (r StoreRegistry) CreateProject(ctx context.Context, project *models.Project) error {
	enabled := project.Enabled
	if err := r.Store.CreateProject(ctx, project); err != nil {
		return err
	}
	if enabled {
		return nil
	}
	// The enabled column has the default true, and an insert leaves out a
	// false field with a default. Write the disabled state explicitly.
	current, err := r.Store.GetProjectByID(ctx, project.ProjectID)
	if err == nil && !current.Enabled {
		return nil
	}
	if err == nil {
		current.Enabled = false
		err = r.Store.UpdateProject(ctx, current)
	}
	if err != nil {
		// Never leave an enabled project that must wait for approval.
		if derr := r.Store.DeleteProject(ctx, project.ProjectID); derr != nil {
			return fmt.Errorf("could not disable the new project (%v), and could not delete it: %w", err, derr)
		}
		return fmt.Errorf("could not disable the new project; it was deleted: %w", err)
	}
	project.Enabled = false
	return nil
}

func (r StoreRegistry) SetProjectEnabled(ctx context.Context, project *models.Project, enabled bool) error {
	current, err := r.Store.GetProjectByID(ctx, project.ProjectID)
	if err != nil {
		return err
	}
	if current.Enabled == enabled {
		return nil
	}
	current.Enabled = enabled
	if err := r.Store.UpdateProject(ctx, current); err != nil {
		return err
	}
	project.Enabled = enabled
	return nil
}

func (r StoreRegistry) SetProjectRepositoryID(ctx context.Context, project *models.Project, repositoryID int64) error {
	current, err := r.Store.GetProjectByID(ctx, project.ProjectID)
	if err != nil {
		return err
	}
	if current.VCSRepositoryID != nil {
		return nil
	}
	id := repositoryID
	current.VCSRepositoryID = &id
	if err := r.Store.UpdateProject(ctx, current); err != nil {
		return err
	}
	project.VCSRepositoryID = &id
	return nil
}

func (r StoreRegistry) OrganizationFor(ctx context.Context, app *models.GitHubApp, accountLogin string) (string, OrgSource, error) {
	if r.Apps != nil && accountLogin != "" {
		mapping, err := r.Apps.GetGitHubAccountOrg(ctx, accountLogin)
		if err == nil && mapping != nil {
			return mapping.OrgID, OrgSourceAccountMapping, nil
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", "", err
		}
	}
	if app != nil && app.DefaultOrgID != nil && *app.DefaultOrgID != "" {
		return *app.DefaultOrgID, OrgSourceAppDefault, nil
	}
	orgs, ok := r.Store.(defaultOrganizationStore)
	if !ok {
		return "", "", fmt.Errorf("the store has no default organization")
	}
	org, err := orgs.GetDefaultOrganization(ctx)
	if err != nil {
		return "", "", fmt.Errorf("no default organization: %w", err)
	}
	return org.OrgID, OrgSourceDeploymentDefault, nil
}

// RegistrationResult says what happened to one repository.
type RegistrationResult struct {
	Repository string `json:"repository"`
	ProjectID  string `json:"project_id,omitempty"`
	// Action is "created", "pending_approval" (created disabled), "bound",
	// "enabled", "kept_other_installation", "removed", "disabled", or
	// "skipped".
	Action string `json:"action"`
	Detail string `json:"detail,omitempty"`
}

// addRepository binds a repository to the installation and registers its
// project.
func (s *Service) addRepository(ctx context.Context, app *models.GitHubApp, installation *models.GitHubAppInstallation, repo Repository) RegistrationResult {
	fullName := strings.TrimSpace(repo.FullName)
	result := RegistrationResult{Repository: fullName}
	if fullName == "" || !strings.Contains(fullName, "/") {
		result.Action, result.Detail = "skipped", "no full name"
		return result
	}
	binding := &models.GitHubAppRepository{RepoFullName: fullName, InstallationID: installation.InstallationID, Private: repo.Private}
	if repo.ID > 0 {
		id := repo.ID
		binding.RepositoryID = &id
	}
	row, bound, err := s.store.BindGitHubAppRepository(ctx, binding)
	if err != nil {
		result.Action, result.Detail = "skipped", err.Error()
		return result
	}
	if !bound {
		result.Action = "kept_other_installation"
		result.Detail = fmt.Sprintf("installation %d already holds the repository", row.InstallationID)
		return result
	}
	if s.projects == nil {
		result.Action = "bound"
		return result
	}
	repoURL := "github.com/" + fullName
	project, err := s.projects.FindProject(ctx, repoURL)
	if err != nil {
		result.Action, result.Detail = "bound", "project lookup failed: "+err.Error()
		return result
	}
	if project != nil && repo.ID > 0 && project.VCSRepositoryID != nil && *project.VCSRepositoryID != repo.ID {
		// GitHub gave the name to another repository (the old one was
		// deleted or renamed). The binding stays, but it does not get the
		// project: the project keeps its token path, and App deliveries
		// for the new repository do not reach it (docs/github-app.md).
		result.Action = "bound"
		result.Detail = fmt.Sprintf("project %s belongs to repository ID %d, not to %d; not linked", project.Name, *project.VCSRepositoryID, repo.ID)
		audit.Record(ctx, s.store, project.OwnershipOrgID(), "github_app.repository_id_mismatch", "project", project.ProjectID,
			models.JSONB{"app_id": app.AppID, "installation_id": installation.InstallationID, "repository": fullName,
				"project_repository_id": *project.VCSRepositoryID, "repository_id": repo.ID})
		return result
	}
	if project != nil {
		result.ProjectID = project.ProjectID
		result.Action = "bound"
		if repo.ID > 0 && project.VCSRepositoryID == nil {
			if err := s.projects.SetProjectRepositoryID(ctx, project, repo.ID); err != nil {
				result.Detail = "could not record the repository ID: " + err.Error()
			}
		}
		autoRegistered := row.AutoRegistered && row.ProjectID != nil && *row.ProjectID == project.ProjectID
		if row.DisabledByRemoval && !project.Enabled {
			if err := s.projects.SetProjectEnabled(ctx, project, true); err != nil {
				result.Detail = "could not enable the project: " + err.Error()
			} else {
				result.Action = "enabled"
				audit.Record(ctx, s.store, project.OwnershipOrgID(), "github_app.project_enabled", "project", project.ProjectID,
					models.JSONB{"app_id": app.AppID, "installation_id": installation.InstallationID, "repository": fullName})
			}
		}
		if err := s.store.SetGitHubAppRepositoryProject(ctx, fullName, project.ProjectID, autoRegistered); err != nil {
			result.Detail = err.Error()
		}
		return result
	}
	orgID, source, err := s.projects.OrganizationFor(ctx, app, installation.AccountLogin)
	if err != nil {
		result.Action, result.Detail = "bound", "no organization for a new project: "+err.Error()
		return result
	}
	approved := RegistrationApproved(app, source)
	name := fullName
	if _, repoName, ok := strings.Cut(fullName, "/"); ok {
		name = repoName
	}
	project = &models.Project{Name: name, RepoURL: repoURL, OrgID: orgID, Enabled: approved, IsPrivate: repo.Private,
		Description: fmt.Sprintf("Registered by GitHub App %s", firstNonEmpty(app.Slug, fmt.Sprint(app.AppID)))}
	if repo.ID > 0 {
		id := repo.ID
		project.VCSRepositoryID = &id
	}
	if err := s.projects.CreateProject(ctx, project); err != nil {
		result.Action, result.Detail = "bound", "could not create the project: "+err.Error()
		return result
	}
	result.ProjectID, result.Action = project.ProjectID, "created"
	if err := s.store.SetGitHubAppRepositoryProject(ctx, fullName, project.ProjectID, true); err != nil {
		result.Detail = err.Error()
	}
	details := models.JSONB{"app_id": app.AppID, "installation_id": installation.InstallationID, "repository": fullName,
		"account": installation.AccountLogin, "is_private": repo.Private, "org_source": string(source), "enabled": approved}
	audit.Record(ctx, s.store, orgID, "github_app.project_registered", "project", project.ProjectID, details)
	if !approved {
		result.Action = "pending_approval"
		result.Detail = "the project is disabled until an administrator enables it (no account mapping and no open registration)"
		if err := s.store.SetGitHubAppRepositoryPending(ctx, fullName, true); err != nil {
			result.Detail += "; " + err.Error()
		}
		audit.Record(ctx, s.store, orgID, "github_app.project_pending_approval", "project", project.ProjectID, details)
	}
	return result
}

// removeRepository removes the binding. A project that the App registered is
// disabled, not deleted. Another project only goes back to its token path.
func (s *Service) removeRepository(ctx context.Context, app *models.GitHubApp, installationID int64, fullName string) RegistrationResult {
	result := RegistrationResult{Repository: fullName, Action: "removed"}
	current, err := s.findBinding(ctx, installationID, fullName)
	if err != nil {
		result.Action, result.Detail = "skipped", err.Error()
		return result
	}
	if current == nil {
		result.Action, result.Detail = "skipped", "the installation does not hold the repository"
		return result
	}
	disable := current.AutoRegistered && current.ProjectID != nil && s.projects != nil
	var project *models.Project
	if disable {
		project, err = s.projects.FindProject(ctx, "github.com/"+strings.TrimSpace(fullName))
		if err != nil || project == nil || project.ProjectID != *current.ProjectID || !project.Enabled {
			disable = false
		}
	}
	if disable {
		if err := s.projects.SetProjectEnabled(ctx, project, false); err != nil {
			result.Detail = "could not disable the project: " + err.Error()
			disable = false
		}
	}
	if _, _, err := s.store.RemoveGitHubAppRepository(ctx, installationID, fullName, disable, s.now()); err != nil {
		result.Action, result.Detail = "skipped", err.Error()
		return result
	}
	if current.ProjectID != nil {
		result.ProjectID = *current.ProjectID
	}
	s.tokens.forgetInstallation(installationID)
	if disable {
		result.Action = "disabled"
		audit.Record(ctx, s.store, project.OwnershipOrgID(), "github_app.project_disabled", "project", project.ProjectID,
			models.JSONB{"app_id": app.AppID, "installation_id": installationID, "repository": fullName})
	}
	return result
}

func (s *Service) findBinding(ctx context.Context, installationID int64, fullName string) (*models.GitHubAppRepository, error) {
	rows, err := s.store.ListGitHubAppRepositories(ctx, installationID)
	if err != nil {
		return nil, err
	}
	want := models.NormalizeGitHubRepoFullName(fullName)
	for i := range rows {
		if rows[i].RepoFullName == want {
			return &rows[i], nil
		}
	}
	return nil, nil
}
