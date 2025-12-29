package vcs

import (
	"strings"
)

// NormalizeRepoURL converts various repository URL formats to canonical form: github.com/org/repo
// Handles:
//   - https://github.com/org/repo
//   - http://github.com/org/repo
//   - git://github.com/org/repo.git
//   - git@github.com:org/repo.git
//   - ssh://git@github.com/org/repo.git
//   - https://raw.githubusercontent.com/org/repo
//
// Returns: github.com/org/repo
func NormalizeRepoURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	url := strings.TrimSpace(rawURL)

	// Remove common protocols
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	url = strings.TrimPrefix(url, "git://")
	url = strings.TrimPrefix(url, "ssh://")

	// Handle git@ format: git@github.com:org/repo.git -> github.com:org/repo.git
	if strings.HasPrefix(url, "git@") {
		url = strings.TrimPrefix(url, "git@")
		// Replace first colon with slash: github.com:org/repo -> github.com/org/repo
		url = strings.Replace(url, ":", "/", 1)
	}

	// Remove .git suffix
	url = strings.TrimSuffix(url, ".git")

	// Handle raw.githubusercontent.com -> github.com
	url = strings.Replace(url, "raw.githubusercontent.com", "github.com", 1)

	// Handle gitlab instances and other providers similarly
	// Note: This assumes the canonical form is always host/org/repo

	// Remove trailing slashes
	url = strings.TrimSuffix(url, "/")

	return url
}

// MatchRepoURL compares two repository URLs after normalizing both
func MatchRepoURL(url1, url2 string) bool {
	return NormalizeRepoURL(url1) == NormalizeRepoURL(url2)
}
