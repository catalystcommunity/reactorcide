package store

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// EnvDefaultsStore reads and writes environment defaults (WP14,
// docs/builds-and-caches.md). scopeRef is "" for the organization scope,
// the worker class name for the worker class scope, and the project ID for
// the project scope.
type EnvDefaultsStore interface {
	ListEnvDefaults(ctx context.Context, orgID, scope, scopeRef string) ([]models.EnvDefault, error)
	SetEnvDefault(ctx context.Context, orgID, scope, scopeRef, key, value string) (*models.EnvDefault, error)
	DeleteEnvDefault(ctx context.Context, orgID, scope, scopeRef, key string) error
	// EnvDefaultLayers returns the defaults of the organization, the worker
	// class (by name, "" for none), and the project (nil for none) as maps.
	EnvDefaultLayers(ctx context.Context, orgID, workerClass string, projectID *string) (org, workerClassDefaults, project map[string]string, err error)
}

// ImagePolicyStore reads and writes the organization image policy (WP14,
// docs/builds-and-caches.md). GetImagePolicy returns a disabled policy when the
// organization has none.
type ImagePolicyStore interface {
	GetImagePolicy(ctx context.Context, orgID string) (*models.ImagePolicy, error)
	SetImagePolicy(ctx context.Context, policy *models.ImagePolicy) error
}

// JobLinkStore stores the named links of jobs (WP14, docs/builds-and-caches.md).
type JobLinkStore interface {
	// ReplaceJobLinks replaces all links of a job.
	ReplaceJobLinks(ctx context.Context, jobID string, links []models.JobLink) error
	ListJobLinks(ctx context.Context, jobID string) ([]models.JobLink, error)
	// ListJobLinksForJobs returns the links of each job ID that has links.
	ListJobLinksForJobs(ctx context.Context, jobIDs []string) (map[string][]models.JobLink, error)
}
