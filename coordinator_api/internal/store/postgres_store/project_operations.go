package postgres_store

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// CreateProject creates a new project in the database
func (ps PostgresDbStore) CreateProject(ctx context.Context, project *models.Project) error {
	db := ps.getDB(ctx)
	result := db.Create(project)
	if result.Error != nil {
		return fmt.Errorf("failed to create project: %w", result.Error)
	}
	return nil
}

// GetProjectByID retrieves a project by its ID
func (ps PostgresDbStore) GetProjectByID(ctx context.Context, projectID string) (*models.Project, error) {
	if !isValidUUID(projectID) {
		return nil, store.ErrNotFound
	}

	db := ps.getDB(ctx)
	var project models.Project
	result := db.Where("project_id = ?", projectID).First(&project)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get project: %w", result.Error)
	}
	return &project, nil
}

func (ps PostgresDbStore) GetProjectByOrgAndName(ctx context.Context, orgID, name string) (*models.Project, error) {
	var project models.Project
	if err := ps.getDB(ctx).Where("org_id = ? AND name = ?", orgID, name).First(&project).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &project, nil
}

// GetProjectByRepoURL retrieves a project by its repository URL
// The repoURL should be in canonical form (e.g., github.com/org/repo)
func (ps PostgresDbStore) GetProjectByRepoURL(ctx context.Context, repoURL string) (*models.Project, error) {
	db := ps.getDB(ctx)
	var project models.Project
	result := db.Where("repo_url = ?", repoURL).First(&project)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get project by repo URL: %w", result.Error)
	}
	return &project, nil
}

// UpdateProject updates an existing project
func (ps PostgresDbStore) UpdateProject(ctx context.Context, project *models.Project) error {
	db := ps.getDB(ctx)
	result := db.Save(project)
	if result.Error != nil {
		return fmt.Errorf("failed to update project: %w", result.Error)
	}
	return nil
}

// DeleteProject deletes a project by its ID
func (ps PostgresDbStore) DeleteProject(ctx context.Context, projectID string) error {
	if !isValidUUID(projectID) {
		return store.ErrNotFound
	}

	db := ps.getDB(ctx)
	result := db.Where("project_id = ?", projectID).Delete(&models.Project{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete project: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ListProjects retrieves a list of projects with pagination
func (ps PostgresDbStore) ListProjects(ctx context.Context, limit, offset int) ([]models.Project, error) {
	db := ps.getDB(ctx)
	var projects []models.Project
	result := db.Limit(limit).Offset(offset).Order("created_at DESC").Find(&projects)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list projects: %w", result.Error)
	}
	return projects, nil
}

// ListProjectsByOrg retrieves a list of projects owned by a single org
// (org_id), with pagination. Added for Task G's list-projects CSIL op,
// whose request can filter to a single org_id.
func (ps PostgresDbStore) ListProjectsByOrg(ctx context.Context, orgID string, limit, offset int) ([]models.Project, error) {
	db := ps.getDB(ctx)
	var projects []models.Project
	result := db.Where("org_id = ?", orgID).Limit(limit).Offset(offset).Order("created_at DESC").Find(&projects)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list projects by org: %w", result.Error)
	}
	return projects, nil
}
