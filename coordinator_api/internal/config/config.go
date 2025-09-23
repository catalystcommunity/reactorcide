package config

import (
	"github.com/catalystcommunity/app-utils-go/env"
)

var (
	// DbUri is the database connection string
	DbUri string

	// Port is the HTTP server port
	Port int

	// CommitOnSuccess determines if transactions should be committed on successful responses (2xx status)
	// Default is true, but can be set to false for testing environments
	CommitOnSuccess = env.GetEnvAsBoolOrDefault("COMMIT_ON_SUCCESS", "true")

	// Corndogs integration
	CornDogsBaseURL = env.GetEnvOrDefault("CORNDOGS_BASE_URL", "http://corndogs:8080")
	CornDogsAPIKey  = env.GetEnvOrDefault("CORNDOGS_API_KEY", "")

	// Default queue settings
	DefaultQueueName = env.GetEnvOrDefault("DEFAULT_QUEUE_NAME", "reactorcide-jobs")
	DefaultTimeout   = env.GetEnvAsIntOrDefault("DEFAULT_TIMEOUT", "3600")

	// Default user for API token auth
	// NOTE: If DEFAULT_USER_ID is a valid UUID and doesn't exist in the DB,
	// we'll create a dummy user with an API token that can be retrieved from
	// the DB later. This is for convenience - proper user auth/management
	// will be implemented later.
	DefaultUserID = env.GetEnvOrDefault("DEFAULT_USER_ID", "") // UUID of default user

	// Object store configuration
	ObjectStoreType     = env.GetEnvOrDefault("OBJECT_STORE_TYPE", "filesystem") // s3, gcs, filesystem, memory
	ObjectStoreBucket   = env.GetEnvOrDefault("OBJECT_STORE_BUCKET", "reactorcide-objects")
	ObjectStoreBasePath = env.GetEnvOrDefault("OBJECT_STORE_BASE_PATH", "./objects") // for filesystem
	ObjectStorePrefix   = env.GetEnvOrDefault("OBJECT_STORE_PREFIX", "reactorcide/") // for s3/gcs

	// VCS Integration configuration
	VCSGitHubToken     = env.GetEnvOrDefault("VCS_GITHUB_TOKEN", "")
	VCSGitHubSecret    = env.GetEnvOrDefault("VCS_GITHUB_SECRET", "")
	VCSGitLabToken     = env.GetEnvOrDefault("VCS_GITLAB_TOKEN", "")
	VCSGitLabSecret    = env.GetEnvOrDefault("VCS_GITLAB_SECRET", "")
	VCSWebhookSecret   = env.GetEnvOrDefault("VCS_WEBHOOK_SECRET", "") // Shared secret for all providers
	VCSEnabled         = env.GetEnvAsBoolOrDefault("VCS_ENABLED", "false")
	VCSBaseURL         = env.GetEnvOrDefault("VCS_BASE_URL", "https://reactorcide.example.com") // Base URL for status links
)
