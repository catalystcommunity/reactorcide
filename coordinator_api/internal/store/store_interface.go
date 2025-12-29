package store

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

var AppStore Store

// GetDB returns the database connection
func GetDB() *gorm.DB {
	// This is a convenience function to access the DB from other packages
	// It's used by the transaction middleware
	if store, ok := AppStore.(interface{ GetDB() *gorm.DB }); ok {
		return store.GetDB()
	}
	return nil
}

type Store interface {
	Initialize() (deferredFunc func(), err error)

	// Project operations
	CreateProject(ctx context.Context, project *models.Project) error
	GetProjectByID(ctx context.Context, projectID string) (*models.Project, error)
	GetProjectByRepoURL(ctx context.Context, repoURL string) (*models.Project, error)
	UpdateProject(ctx context.Context, project *models.Project) error
	DeleteProject(ctx context.Context, projectID string) error
	ListProjects(ctx context.Context, limit, offset int) ([]models.Project, error)

	// Job operations
	GetJobsByUser(ctx context.Context, userID string, limit, offset int) ([]models.Job, error)
	GetJobByID(ctx context.Context, jobID string) (*models.Job, error)
	CreateJob(ctx context.Context, job *models.Job) error
	UpdateJob(ctx context.Context, job *models.Job) error
	DeleteJob(ctx context.Context, jobID string) error
	ListJobs(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.Job, error)

	// API Token operations
	ValidateAPIToken(ctx context.Context, token string) (*models.APIToken, *models.User, error)
	CreateAPIToken(ctx context.Context, apiToken *models.APIToken) error
	UpdateTokenLastUsed(ctx context.Context, tokenID string, lastUsed time.Time) error
	GetAPITokensByUser(ctx context.Context, userID string) ([]models.APIToken, error)
	DeleteAPIToken(ctx context.Context, tokenID string) error

	// User operations
	GetUserByID(ctx context.Context, userID string) (*models.User, error)
	CreateUser(ctx context.Context, user *models.User) error
	EnsureDefaultUser() error
}
