package models

import (
	"testing"
)

func TestProject_ShouldProcessEvent(t *testing.T) {
	tests := []struct {
		name          string
		project       *Project
		eventType     string
		targetBranch  string
		shouldProcess bool
	}{
		{
			name: "Enabled project with matching event and branch",
			project: &Project{
				Enabled:           true,
				TargetBranches:    []string{"main", "develop"},
				AllowedEventTypes: []string{"push", "pull_request"},
			},
			eventType:     "push",
			targetBranch:  "main",
			shouldProcess: true,
		},
		{
			name: "Disabled project",
			project: &Project{
				Enabled:           false,
				TargetBranches:    []string{"main"},
				AllowedEventTypes: []string{"push"},
			},
			eventType:     "push",
			targetBranch:  "main",
			shouldProcess: false,
		},
		{
			name: "Event type not allowed",
			project: &Project{
				Enabled:           true,
				TargetBranches:    []string{"main"},
				AllowedEventTypes: []string{"push"},
			},
			eventType:     "pull_request",
			targetBranch:  "main",
			shouldProcess: false,
		},
		{
			name: "Branch not in target branches",
			project: &Project{
				Enabled:           true,
				TargetBranches:    []string{"main", "develop"},
				AllowedEventTypes: []string{"push"},
			},
			eventType:     "push",
			targetBranch:  "feature-branch",
			shouldProcess: false,
		},
		{
			name: "Empty target branches allows all branches",
			project: &Project{
				Enabled:           true,
				TargetBranches:    []string{},
				AllowedEventTypes: []string{"push"},
			},
			eventType:     "push",
			targetBranch:  "any-branch",
			shouldProcess: true,
		},
		{
			name: "Multiple event types",
			project: &Project{
				Enabled:           true,
				TargetBranches:    []string{"main"},
				AllowedEventTypes: []string{"push", "pull_request", "tag"},
			},
			eventType:     "tag",
			targetBranch:  "main",
			shouldProcess: true,
		},
		{
			name: "Case sensitive branch matching",
			project: &Project{
				Enabled:           true,
				TargetBranches:    []string{"main"},
				AllowedEventTypes: []string{"push"},
			},
			eventType:     "push",
			targetBranch:  "Main",
			shouldProcess: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.project.ShouldProcessEvent(tt.eventType, tt.targetBranch)
			if result != tt.shouldProcess {
				t.Errorf("ShouldProcessEvent(%q, %q) = %v, want %v",
					tt.eventType, tt.targetBranch, result, tt.shouldProcess)
			}
		})
	}
}

func TestProject_IsEffectivelyPrivate(t *testing.T) {
	tests := []struct {
		name         string
		project      *Project
		orgIsPrivate bool
		want         bool
	}{
		{
			name:         "public project, public org",
			project:      &Project{IsPrivate: false},
			orgIsPrivate: false,
			want:         false,
		},
		{
			name:         "private project, public org",
			project:      &Project{IsPrivate: true},
			orgIsPrivate: false,
			want:         true,
		},
		{
			name:         "public project, private org",
			project:      &Project{IsPrivate: false},
			orgIsPrivate: true,
			want:         true,
		},
		{
			name:         "private project, private org",
			project:      &Project{IsPrivate: true},
			orgIsPrivate: true,
			want:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.project.IsEffectivelyPrivate(tt.orgIsPrivate)
			if got != tt.want {
				t.Errorf("IsEffectivelyPrivate(%v) = %v, want %v", tt.orgIsPrivate, got, tt.want)
			}
		})
	}
}

func TestSourceType_Constants(t *testing.T) {
	// Test that the constants are properly defined
	if SourceTypeGit != "git" {
		t.Errorf("SourceTypeGit = %q, want %q", SourceTypeGit, "git")
	}
	if SourceTypeCopy != "copy" {
		t.Errorf("SourceTypeCopy = %q, want %q", SourceTypeCopy, "copy")
	}
	if SourceTypeNone != "none" {
		t.Errorf("SourceTypeNone = %q, want %q", SourceTypeNone, "none")
	}
}
