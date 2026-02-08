package postgres_store

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
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
