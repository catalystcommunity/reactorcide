// Package reporting holds the report modes, the agreement report, the code
// owners import, and owner acceptance of path dependencies (plan items A3,
// A2, A7; owner decisions D8, D15; docs/reporting-and-ownership.md).
//
//   - A report mode tells which VCS writes a project gets: full (commit
//     statuses and the pull request comment, the default), statuses_only (no
//     pull request comment), or silent (no VCS write; the results are only in
//     the Reactorcide UI and API).
//   - The agreement report compares the Reactorcide result of each commit
//     with the result of another CI system on the same commit. It is the
//     evidence for a cut-over.
//   - The code owners import (off by default) reads the code owners file from
//     the trusted base SHA and makes derived required reviews.
//   - Path acceptance (off by default) drops (enforce) or flags (warn) a path
//     match of a workflow in an area of another owner that has no acceptance.
package reporting

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/codeowners"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/subjects"
	"github.com/lib/pq"
)

// Limits of the settings.
const (
	MaxAgreementContexts   = 50
	MaxAgreementContextLen = 200
	MaxPathOwnerRules      = 500
	MaxPathOwnersPerRule   = 20
	MaxPatternLength       = 500
)

// Effective returns the settings that apply to a project. A store without
// reporting settings gives the built-in defaults (the behavior from before
// the feature).
func Effective(ctx context.Context, st any, orgID, projectID string) (models.EffectiveReportingSettings, error) {
	reports, ok := st.(store.ReportingStore)
	if !ok || st == nil {
		return models.ResolveReportingSettings(nil, nil), nil
	}
	var org *models.OrgReportingSettings
	var err error
	if orgID != "" {
		if org, err = reports.GetOrgReportingSettings(ctx, orgID); err != nil {
			return models.EffectiveReportingSettings{}, err
		}
	}
	var project *models.ProjectReportingSettings
	if projectID != "" {
		if project, err = reports.GetProjectReportingSettings(ctx, projectID); err != nil {
			return models.EffectiveReportingSettings{}, err
		}
	}
	return models.ResolveReportingSettings(org, project), nil
}

// Mode returns the report mode of a project. When the settings cannot be
// read, the mode is silent: a project that chose silent must not get a
// status or a comment because of a database error. A store without the
// settings, and an empty project, give full (the behavior from before the
// feature).
func Mode(ctx context.Context, st any, orgID, projectID string) string {
	if _, ok := st.(store.ReportingStore); !ok || (orgID == "" && projectID == "") {
		return models.ReportModeFull
	}
	settings, err := Effective(ctx, st, orgID, projectID)
	if err != nil {
		return models.ReportModeSilent
	}
	return settings.ReportMode
}

// AllowsStatuses reports whether the mode posts commit statuses.
func AllowsStatuses(mode string) bool { return mode != models.ReportModeSilent }

// AllowsComments reports whether the mode writes the pull request comment.
func AllowsComments(mode string) bool { return mode == "" || mode == models.ReportModeFull }

// ValidReportMode reports whether a report mode is valid.
func ValidReportMode(mode string) bool {
	switch mode {
	case models.ReportModeFull, models.ReportModeStatusesOnly, models.ReportModeSilent:
		return true
	}
	return false
}

// ValidCodeownersImport reports whether a code owners import value is valid.
func ValidCodeownersImport(value string) bool {
	return value == models.CodeownersImportOff || value == models.CodeownersImportOn
}

// ValidPathAcceptance reports whether a path acceptance value is valid.
func ValidPathAcceptance(value string) bool {
	switch value {
	case models.PathAcceptanceOff, models.PathAcceptanceWarn, models.PathAcceptanceEnforce:
		return true
	}
	return false
}

// NormalizeAgreementContexts trims, deduplicates, and checks the list of the
// status contexts and check run names of the other CI system. A name can be
// a glob ("ci/*").
func NormalizeAgreementContexts(values []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if len(value) > MaxAgreementContextLen {
			return nil, fmt.Errorf("agreement context %q is longer than %d characters", value, MaxAgreementContextLen)
		}
		if IsOwnContext(value) {
			return nil, fmt.Errorf("agreement context %q is a Reactorcide context", value)
		}
		if !validGlob(value) {
			return nil, fmt.Errorf("agreement context %q is not a valid pattern", value)
		}
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	if len(out) > MaxAgreementContexts {
		return nil, fmt.Errorf("more than %d agreement contexts", MaxAgreementContexts)
	}
	return out, nil
}

// ValidPathOwnerSubject reports whether a subject can own a path area: a
// subject that names people. Not "*", and not a computed role.
func ValidPathOwnerSubject(value string) bool {
	if value == "" || len(value) > 200 || !subjects.Valid(value) {
		return false
	}
	switch value {
	case subjects.Anyone, subjects.RepositoryWrite, subjects.ProjectOwner, subjects.WorkflowOwner:
		return false
	}
	return !strings.HasPrefix(value, subjects.PrefixPrincipalKind)
}

// NormalizePathOwners checks the rules of the path ownership registry. The
// patterns use the code owners syntax.
func NormalizePathOwners(rules []models.PathOwnerRule) ([]models.PathOwnerRule, error) {
	if len(rules) > MaxPathOwnerRules {
		return nil, fmt.Errorf("more than %d path owner rules", MaxPathOwnerRules)
	}
	out := make([]models.PathOwnerRule, 0, len(rules))
	for i, rule := range rules {
		pattern := strings.TrimSpace(rule.Pattern)
		if pattern == "" || len(pattern) > MaxPatternLength || strings.ContainsAny(pattern, " \t\n") {
			return nil, fmt.Errorf("path owner rule %d has an invalid pattern", i+1)
		}
		if err := ValidatePattern(pattern); err != nil {
			return nil, fmt.Errorf("path owner rule %d: %w", i+1, err)
		}
		if len(rule.Owners) > MaxPathOwnersPerRule {
			return nil, fmt.Errorf("path owner rule %d has more than %d owners", i+1, MaxPathOwnersPerRule)
		}
		ownerList := []string{}
		seen := map[string]bool{}
		for _, owner := range rule.Owners {
			owner = strings.TrimSpace(owner)
			if !ValidPathOwnerSubject(owner) {
				return nil, fmt.Errorf("path owner rule %d owner %q is not a valid owner subject", i+1, owner)
			}
			if !seen[owner] {
				seen[owner] = true
				ownerList = append(ownerList, owner)
			}
		}
		out = append(out, models.PathOwnerRule{Pattern: pattern, Owners: ownerList})
	}
	return out, nil
}

// ValidatePattern checks one pattern of the code owners syntax.
func ValidatePattern(pattern string) error {
	file := codeowners.Parse([]byte(escapeLeadingHash(pattern) + "\n"))
	if len(file.Rules) != 1 {
		if len(file.Warnings) > 0 {
			return fmt.Errorf("pattern %q: %s", pattern, file.Warnings[0].Message)
		}
		return fmt.Errorf("pattern %q is invalid", pattern)
	}
	return nil
}

// CompilePattern returns a matcher for one pattern of the code owners syntax.
func CompilePattern(pattern string) (func(path string) bool, error) {
	file := codeowners.Parse([]byte(escapeLeadingHash(pattern) + "\n"))
	if len(file.Rules) != 1 {
		return nil, fmt.Errorf("pattern %q is invalid", pattern)
	}
	rule := file.Rules[0]
	return rule.Matches, nil
}

func escapeLeadingHash(pattern string) string {
	if strings.HasPrefix(pattern, "#") {
		return `\` + pattern
	}
	return pattern
}

func pqStrings(values []string) pq.StringArray { return pq.StringArray(append([]string{}, values...)) }

func sortStrings(values []string) { sort.Strings(values) }
