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
	CommitOnSuccess = env.GetEnvAsBoolOrDefault("REACTORCIDE_COMMIT_ON_SUCCESS", "true")

	// Corndogs integration (gRPC address - no http:// prefix)
	CornDogsBaseURL = env.GetEnvOrDefault("REACTORCIDE_CORNDOGS_BASE_URL", "")
	CornDogsAPIKey  = env.GetEnvOrDefault("REACTORCIDE_CORNDOGS_API_KEY", "")

	// Default queue settings
	DefaultQueueName = env.GetEnvOrDefault("REACTORCIDE_DEFAULT_QUEUE_NAME", "reactorcide-jobs")
	DefaultTimeout   = env.GetEnvAsIntOrDefault("REACTORCIDE_DEFAULT_TIMEOUT", "3600")

	// Default runner image for jobs that don't specify one
	// Should be configured per deployment (e.g., "registry.example.com/reactorcide/runner:latest")
	DefaultRunnerImage = env.GetEnvOrDefault("REACTORCIDE_DEFAULT_RUNNER_IMAGE", "")

	// Default user for API token auth
	DefaultUserID = env.GetEnvOrDefault("REACTORCIDE_DEFAULT_USER_ID", "")

	// Object store configuration
	ObjectStoreType     = env.GetEnvOrDefault("REACTORCIDE_OBJECT_STORE_TYPE", "filesystem") // s3, gcs, filesystem, memory
	ObjectStoreBucket   = env.GetEnvOrDefault("REACTORCIDE_OBJECT_STORE_BUCKET", "reactorcide-objects")
	ObjectStoreBasePath = env.GetEnvOrDefault("REACTORCIDE_OBJECT_STORE_BASE_PATH", "./objects") // for filesystem
	ObjectStorePrefix   = env.GetEnvOrDefault("REACTORCIDE_OBJECT_STORE_PREFIX", "reactorcide/") // for s3/gcs

	// VCS Integration configuration
	VCSGitHubToken   = env.GetEnvOrDefault("REACTORCIDE_VCS_GITHUB_TOKEN", "")
	VCSGitHubSecret  = env.GetEnvOrDefault("REACTORCIDE_VCS_GITHUB_SECRET", "")
	VCSGitLabToken   = env.GetEnvOrDefault("REACTORCIDE_VCS_GITLAB_TOKEN", "")
	VCSGitLabSecret  = env.GetEnvOrDefault("REACTORCIDE_VCS_GITLAB_SECRET", "")
	VCSWebhookSecret = env.GetEnvOrDefault("REACTORCIDE_VCS_WEBHOOK_SECRET", "") // Shared secret for all providers
	VCSEnabled       = env.GetEnvAsBoolOrDefault("REACTORCIDE_VCS_ENABLED", "false")
	VCSBaseURL       = env.GetEnvOrDefault("REACTORCIDE_VCS_BASE_URL", "https://reactorcide.example.com") // Base URL for status links

	// CI Code Security configuration
	CiCodeAllowlist = env.GetEnvOrDefault("REACTORCIDE_CI_CODE_ALLOWLIST", "")

	// Default CI code repository for jobs that don't specify one
	DefaultCiSourceURL = env.GetEnvOrDefault("REACTORCIDE_DEFAULT_CI_SOURCE_URL", "")
	DefaultCiSourceRef = env.GetEnvOrDefault("REACTORCIDE_DEFAULT_CI_SOURCE_REF", "main")

	// Secrets configuration
	// SecretsStorageType determines where secrets are stored: "database" (default), "local", or external providers (future)
	SecretsStorageType = env.GetEnvOrDefault("REACTORCIDE_SECRETS_STORAGE_TYPE", "database")
	// SecretsLocalPath is the path for local secrets storage (only used when SecretsStorageType="local")
	SecretsLocalPath = env.GetEnvOrDefault("REACTORCIDE_SECRETS_LOCAL_PATH", "")
)
