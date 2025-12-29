package vcs

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
)

// BranchProtectionRule represents a branch protection rule
type BranchProtectionRule struct {
	Pattern                  string   // Branch name pattern (e.g., "main", "release/*")
	RequireStatusChecks      bool
	RequiredStatusCheckNames []string // e.g., ["continuous-integration/reactorcide"]
	RequirePRReviews         bool
	RequiredReviewCount      int
	DismissStaleReviews      bool
	RequireUpToDate          bool // Require branch to be up to date before merging
	EnforceAdmins            bool
	AllowForcePush           bool
	AllowDeletions           bool
}

// BranchProtectionService handles branch protection operations
type BranchProtectionService struct {
	rules  map[string]*BranchProtectionRule // Map of repo -> rule
	logger *logrus.Logger
}

// NewBranchProtectionService creates a new branch protection service
func NewBranchProtectionService() *BranchProtectionService {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	return &BranchProtectionService{
		rules:  make(map[string]*BranchProtectionRule),
		logger: logger,
	}
}

// AddRule adds a branch protection rule for a repository
func (s *BranchProtectionService) AddRule(repo string, rule *BranchProtectionRule) {
	s.rules[repo] = rule
	s.logger.WithFields(logrus.Fields{
		"repo":    repo,
		"pattern": rule.Pattern,
	}).Info("Added branch protection rule")
}

// CheckMergeability checks if a PR can be merged based on branch protection rules
func (s *BranchProtectionService) CheckMergeability(ctx context.Context, client Client, repo string, pr *PullRequestInfo) (*MergeabilityResult, error) {
	result := &MergeabilityResult{
		CanMerge: true,
		Reasons:  []string{},
	}

	// Get the protection rule for this repo
	rule, exists := s.rules[repo]
	if !exists {
		// No protection rules, allow merge
		return result, nil
	}

	// Check if this branch matches the protection pattern
	if !s.matchesPattern(pr.BaseRef, rule.Pattern) {
		// Branch doesn't match pattern, no protection needed
		return result, nil
	}

	// Check required status checks
	if rule.RequireStatusChecks && len(rule.RequiredStatusCheckNames) > 0 {
		passed, missing := s.checkRequiredStatuses(ctx, client, repo, pr.HeadSHA, rule.RequiredStatusCheckNames)
		if !passed {
			result.CanMerge = false
			for _, check := range missing {
				result.Reasons = append(result.Reasons, fmt.Sprintf("Required status check '%s' is not passing", check))
			}
		}
	}

	// Check if branch is up to date
	if rule.RequireUpToDate {
		upToDate := s.checkIfUpToDate(ctx, client, repo, pr)
		if !upToDate {
			result.CanMerge = false
			result.Reasons = append(result.Reasons, "Pull request is not up to date with base branch")
		}
	}

	// Check PR reviews (simplified - would need more API calls for full implementation)
	if rule.RequirePRReviews && rule.RequiredReviewCount > 0 {
		// This would require additional API calls to check review status
		s.logger.Debug("PR review checks not fully implemented")
	}

	return result, nil
}

// MergeabilityResult represents the result of a mergeability check
type MergeabilityResult struct {
	CanMerge bool
	Reasons  []string // Reasons why merge is blocked
}

// matchesPattern checks if a branch name matches a pattern
func (s *BranchProtectionService) matchesPattern(branch, pattern string) bool {
	// Simple pattern matching - could be enhanced with glob patterns
	if pattern == branch {
		return true
	}

	// Handle wildcard patterns like "release/*"
	if strings.HasSuffix(pattern, "/*") {
		prefix := strings.TrimSuffix(pattern, "/*")
		return strings.HasPrefix(branch, prefix+"/")
	}

	return false
}

// checkRequiredStatuses checks if all required status checks are passing
func (s *BranchProtectionService) checkRequiredStatuses(ctx context.Context, client Client, repo, sha string, required []string) (bool, []string) {
	// This is a simplified implementation
	// In a real implementation, we would fetch the actual status checks from the VCS API

	// For GitHub, we would use: GET /repos/{owner}/{repo}/commits/{ref}/status
	// For GitLab, we would use: GET /projects/{id}/repository/commits/{sha}/statuses

	missing := []string{}
	allPassed := true

	// For now, we'll just log that we would check these
	for _, check := range required {
		s.logger.WithFields(logrus.Fields{
			"repo":   repo,
			"sha":    sha,
			"check":  check,
		}).Debug("Would check status")

		// In a real implementation, we would check if this status exists and is passing
		// For demonstration, we'll assume they're all passing
	}

	return allPassed, missing
}

// checkIfUpToDate checks if a PR is up to date with its base branch
func (s *BranchProtectionService) checkIfUpToDate(ctx context.Context, client Client, repo string, pr *PullRequestInfo) bool {
	// This would require comparing the PR's base SHA with the current HEAD of the base branch
	// For now, return true as a placeholder
	s.logger.WithFields(logrus.Fields{
		"repo":     repo,
		"pr":       pr.Number,
		"base_ref": pr.BaseRef,
	}).Debug("Would check if PR is up to date")

	return true
}

// ApplyProtection applies branch protection rules to a repository
func (s *BranchProtectionService) ApplyProtection(ctx context.Context, client Client, repo string, rule *BranchProtectionRule) error {
	switch client.GetProvider() {
	case GitHub:
		return s.applyGitHubProtection(ctx, client, repo, rule)
	case GitLab:
		return s.applyGitLabProtection(ctx, client, repo, rule)
	default:
		return fmt.Errorf("unsupported provider: %s", client.GetProvider())
	}
}

// applyGitHubProtection applies branch protection rules via GitHub API
func (s *BranchProtectionService) applyGitHubProtection(ctx context.Context, client Client, repo string, rule *BranchProtectionRule) error {
	// GitHub API: PUT /repos/{owner}/{repo}/branches/{branch}/protection

	// Build the protection payload
	payload := map[string]interface{}{
		"required_status_checks": map[string]interface{}{
			"strict":   rule.RequireUpToDate,
			"contexts": rule.RequiredStatusCheckNames,
		},
		"enforce_admins": rule.EnforceAdmins,
		"required_pull_request_reviews": map[string]interface{}{
			"required_approving_review_count": rule.RequiredReviewCount,
			"dismiss_stale_reviews":            rule.DismissStaleReviews,
		},
		"restrictions": nil, // No user/team restrictions for now
		"allow_force_pushes": rule.AllowForcePush,
		"allow_deletions":    rule.AllowDeletions,
	}

	// This would make the actual API call
	s.logger.WithFields(logrus.Fields{
		"repo":    repo,
		"branch":  rule.Pattern,
		"payload": payload,
	}).Info("Would apply GitHub branch protection")

	return nil
}

// applyGitLabProtection applies branch protection rules via GitLab API
func (s *BranchProtectionService) applyGitLabProtection(ctx context.Context, client Client, repo string, rule *BranchProtectionRule) error {
	// GitLab API: POST /projects/{id}/protected_branches

	// Build the protection payload
	payload := map[string]interface{}{
		"name":                rule.Pattern,
		"push_access_level":   40, // Maintainer
		"merge_access_level":  40, // Maintainer
		"allow_force_push":    rule.AllowForcePush,
	}

	// This would make the actual API call
	s.logger.WithFields(logrus.Fields{
		"repo":    repo,
		"branch":  rule.Pattern,
		"payload": payload,
	}).Info("Would apply GitLab branch protection")

	return nil
}