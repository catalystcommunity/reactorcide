package vcs

import (
	"testing"
)

func TestNormalizeRepoURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "HTTPS URL",
			input:    "https://github.com/org/repo",
			expected: "github.com/org/repo",
		},
		{
			name:     "HTTPS URL with .git suffix",
			input:    "https://github.com/org/repo.git",
			expected: "github.com/org/repo",
		},
		{
			name:     "HTTP URL",
			input:    "http://github.com/org/repo",
			expected: "github.com/org/repo",
		},
		{
			name:     "Git protocol",
			input:    "git://github.com/org/repo.git",
			expected: "github.com/org/repo",
		},
		{
			name:     "SSH URL with git@",
			input:    "git@github.com:org/repo.git",
			expected: "github.com/org/repo",
		},
		{
			name:     "SSH URL with ssh://",
			input:    "ssh://git@github.com/org/repo.git",
			expected: "github.com/org/repo",
		},
		{
			name:     "Raw GitHub URL",
			input:    "https://raw.githubusercontent.com/org/repo",
			expected: "github.com/org/repo",
		},
		{
			name:     "Already normalized",
			input:    "github.com/org/repo",
			expected: "github.com/org/repo",
		},
		{
			name:     "With trailing slash",
			input:    "https://github.com/org/repo/",
			expected: "github.com/org/repo",
		},
		{
			name:     "Empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "GitLab URL",
			input:    "https://gitlab.com/org/repo.git",
			expected: "gitlab.com/org/repo",
		},
		{
			name:     "Self-hosted GitLab",
			input:    "https://gitlab.example.com/org/repo.git",
			expected: "gitlab.example.com/org/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeRepoURL(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizeRepoURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMatchRepoURL(t *testing.T) {
	tests := []struct {
		name     string
		url1     string
		url2     string
		expected bool
	}{
		{
			name:     "Same URLs with different protocols",
			url1:     "https://github.com/org/repo",
			url2:     "git@github.com:org/repo.git",
			expected: true,
		},
		{
			name:     "Different repos",
			url1:     "https://github.com/org/repo1",
			url2:     "https://github.com/org/repo2",
			expected: false,
		},
		{
			name:     "Same URL different formats",
			url1:     "https://github.com/org/repo.git",
			url2:     "git://github.com/org/repo",
			expected: true,
		},
		{
			name:     "Identical URLs",
			url1:     "github.com/org/repo",
			url2:     "github.com/org/repo",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MatchRepoURL(tt.url1, tt.url2)
			if result != tt.expected {
				t.Errorf("MatchRepoURL(%q, %q) = %v, want %v", tt.url1, tt.url2, result, tt.expected)
			}
		})
	}
}
