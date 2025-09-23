package vcs

import (
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/sirupsen/logrus"
)

// Manager manages VCS clients and operations
type Manager struct {
	clients        map[Provider]Client
	statusUpdater  *JobStatusUpdater
	logger         *logrus.Logger
	baseURL        string
	webhookSecret  string
	enabled        bool
}

// NewManager creates a new VCS manager
func NewManager() *Manager {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	m := &Manager{
		clients:        make(map[Provider]Client),
		statusUpdater:  NewJobStatusUpdater(),
		logger:         logger,
		baseURL:        config.VCSBaseURL,
		webhookSecret:  config.VCSWebhookSecret,
		enabled:        config.VCSEnabled,
	}

	// Initialize VCS clients if enabled
	if m.enabled {
		m.initializeClients()
	}

	return m
}

// initializeClients initializes VCS clients based on configuration
func (m *Manager) initializeClients() {
	// Initialize GitHub client if configured
	if config.VCSGitHubToken != "" {
		githubConfig := Config{
			Provider:      GitHub,
			Token:         config.VCSGitHubToken,
			WebhookSecret: config.VCSGitHubSecret,
		}

		if githubConfig.WebhookSecret == "" {
			githubConfig.WebhookSecret = m.webhookSecret
		}

		client, err := NewGitHubClient(githubConfig)
		if err != nil {
			m.logger.WithError(err).Error("Failed to create GitHub client")
		} else {
			m.clients[GitHub] = client
			m.statusUpdater.AddVCSClient(GitHub, client)
			m.logger.Info("GitHub VCS client initialized")
		}
	}

	// Initialize GitLab client if configured
	if config.VCSGitLabToken != "" {
		gitlabConfig := Config{
			Provider:      GitLab,
			Token:         config.VCSGitLabToken,
			WebhookSecret: config.VCSGitLabSecret,
		}

		if gitlabConfig.WebhookSecret == "" {
			gitlabConfig.WebhookSecret = m.webhookSecret
		}

		client, err := NewGitLabClient(gitlabConfig)
		if err != nil {
			m.logger.WithError(err).Error("Failed to create GitLab client")
		} else {
			m.clients[GitLab] = client
			m.statusUpdater.AddVCSClient(GitLab, client)
			m.logger.Info("GitLab VCS client initialized")
		}
	}

	// Configure base URL for status updater
	if m.baseURL != "" {
		// Override the default URL in status updater
		// This would require modifying JobStatusUpdater to accept configurable base URL
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

// GetWebhookSecret returns the configured webhook secret
func (m *Manager) GetWebhookSecret() string {
	return m.webhookSecret
}

// GetClients returns all configured VCS clients
func (m *Manager) GetClients() map[Provider]Client {
	return m.clients
}