package config

import (
	"github.com/catalystcommunity/app-utils-go/env"
)

// GitHub Apps (docs/github-app.md, plan item B6).
var (
	// GitHubAppAPIURL is the GitHub REST API of App calls. Set it for GitHub
	// Enterprise Server (for example https://github.example.com/api/v3).
	GitHubAppAPIURL = env.GetEnvOrDefault("REACTORCIDE_GITHUB_APP_API_URL", "https://api.github.com")
)
