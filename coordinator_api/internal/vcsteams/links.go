package vcsteams

import (
	"context"
	"errors"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Errors of CreateLink. Callers map them to 400 (and ErrAlreadyExists of the
// store to 409).
var (
	ErrUnsupportedProvider = errors.New("only the github provider can link teams")
	ErrInvalidTeam         = errors.New("team must be OWNER/SLUG")
	ErrNoOwnerProject      = errors.New("no project of the organization has a repository under the team owner")
)

// RepoOwner returns the owner of a canonical repository URL
// (host/owner/repo), lowercase.
func RepoOwner(repoURL string) string {
	parts := strings.Split(strings.Trim(strings.TrimSpace(repoURL), "/"), "/")
	if len(parts) < 3 {
		return ""
	}
	return strings.ToLower(parts[1])
}

// ProjectsForOwner returns the projects whose repository is under owner.
func ProjectsForOwner(projects []models.Project, owner string) []models.Project {
	owner = strings.ToLower(owner)
	out := []models.Project{}
	for _, project := range projects {
		if RepoOwner(project.RepoURL) == owner {
			out = append(out, project)
		}
	}
	return out
}

// CreateLink validates and stores a link of a group to a team. The
// organization must have a project with a repository under the team owner:
// the sync reads the team with the VCS credential of that project. This
// keeps an org admin from reading the members of the teams of an owner that
// the organization has no repository of.
func CreateLink(ctx context.Context, st store.GroupVCSStore, group *models.Group, provider, team string, createdBy string) (*models.GroupVCSLink, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "github"
	}
	if provider != "github" {
		return nil, ErrUnsupportedProvider
	}
	owner, slug, ok := NormalizeTeam(team)
	if !ok {
		return nil, ErrInvalidTeam
	}
	projects, err := st.ListOrgProjectsForTeamSync(ctx, group.OrgID)
	if err != nil {
		return nil, err
	}
	if len(ProjectsForOwner(projects, owner)) == 0 {
		return nil, ErrNoOwnerProject
	}
	link := &models.GroupVCSLink{GroupID: group.GroupID, Provider: provider, Owner: owner, TeamSlug: slug}
	if createdBy != "" {
		link.CreatedByPrincipalID = &createdBy
	}
	if err := st.CreateGroupVCSLink(ctx, link); err != nil {
		return nil, err
	}
	return link, nil
}

// IsValidationError reports whether err is a CreateLink input error.
func IsValidationError(err error) bool {
	return errors.Is(err, ErrUnsupportedProvider) || errors.Is(err, ErrInvalidTeam) || errors.Is(err, ErrNoOwnerProject)
}

// FindLink returns the link of a group by link ID or by team (OWNER/SLUG).
func FindLink(ctx context.Context, st store.GroupVCSStore, groupID, ref string) (*models.GroupVCSLink, error) {
	links, err := st.ListGroupVCSLinks(ctx, groupID)
	if err != nil {
		return nil, err
	}
	owner, slug, isTeam := NormalizeTeam(ref)
	for i := range links {
		if links[i].LinkID == ref || (isTeam && links[i].Owner == owner && links[i].TeamSlug == slug) {
			return &links[i], nil
		}
	}
	return nil, store.ErrNotFound
}
