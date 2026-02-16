package vcs

import (
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/sirupsen/logrus"
)

// Manager manages VCS clients and operations
type Manager struct {
	clients       map[Provider]Client
	statusUpdater *JobStatusUpdater
	logger        *logrus.Logger
	baseURL       string
	enabled       bool
}

// NewManager creates a new VCS manager
func NewManager() *Manager {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	statusUpdater := NewJobStatusUpdater()
	statusUpdater.SetBaseURL(config.VCSBaseURL)

	m := &Manager{
		clients:       make(map[Provider]Client),
		statusUpdater: statusUpdater,
		logger:        logger,
		baseURL:       config.VCSBaseURL,
		enabled:       config.VCSEnabled,
	}

	// Initialize VCS clients if enabled
	if m.enabled {
		m.initializeClients()
	}

	return m
}

// initializeClients initializes VCS clients based on configuration.
// Clients are always created when VCS is enabled. Webhook secret validation
// is handled per-project by the webhook handler, not by the client.
func (m *Manager) initializeClients() {
	// Initialize GitHub client (token may be empty; status updates use per-project tokens)
	githubConfig := Config{
		Provider: GitHub,
		Token:    config.VCSGitHubToken,
	}

	client, err := NewGitHubClient(githubConfig)
	if err != nil {
		m.logger.WithError(err).Error("Failed to create GitHub client")
	} else {
		m.clients[GitHub] = client
		m.statusUpdater.AddVCSClient(GitHub, client)
		m.logger.Info("GitHub VCS client initialized")
	}

	// Initialize GitLab client (token may be empty; status updates use per-project tokens)
	gitlabConfig := Config{
		Provider: GitLab,
		Token:    config.VCSGitLabToken,
	}

	gitlabClient, err := NewGitLabClient(gitlabConfig)
	if err != nil {
		m.logger.WithError(err).Error("Failed to create GitLab client")
	} else {
		m.clients[GitLab] = gitlabClient
		m.statusUpdater.AddVCSClient(GitLab, gitlabClient)
		m.logger.Info("GitLab VCS client initialized")
	}

	// Configure base URL for status updater
	if m.baseURL != "" {
		m.logger.WithField("base_url", m.baseURL).Info("VCS base URL configured")
	}
}

// GetClient returns a VCS client for the specified provider
func (m *Manager) GetClient(provider Provider) (Client, error) {
	if !m.enabled {
		return nil, fmt.Errorf("VCS integration is disabled")
	}

	client, ok := m.clients[provider]
	if !ok {
		return nil, fmt.Errorf("VCS client not configured for provider: %s", provider)
	}

	return client, nil
}

// GetStatusUpdater returns the job status updater
func (m *Manager) GetStatusUpdater() *JobStatusUpdater {
	return m.statusUpdater
}

// IsEnabled returns whether VCS integration is enabled
func (m *Manager) IsEnabled() bool {
	return m.enabled
}

// CreateClientWithToken creates a new VCS client for the given provider
// using a per-project token instead of the global token.
// BaseURL is left empty so that each provider uses its default API endpoint
// (e.g., https://api.github.com for GitHub). For GitHub Enterprise or
// self-hosted GitLab, per-project API URL configuration would be needed.
func (m *Manager) CreateClientWithToken(provider Provider, token string) (Client, error) {
	switch provider {
	case GitHub:
		return NewGitHubClient(Config{
			Provider: GitHub,
			Token:    token,
		})
	case GitLab:
		return NewGitLabClient(Config{
			Provider: GitLab,
			Token:    token,
		})
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}

// GetClients returns all configured VCS clients
func (m *Manager) GetClients() map[Provider]Client {
	return m.clients
}
