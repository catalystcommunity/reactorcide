package models

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Run gate conditions. A pull request that matches one condition does not run
// until a run approver comments /reactorcide approve-ci.
const (
	RunGateForks                 = "forks"
	RunGateFirstTimeContributors = "first_time_contributors"
	RunGateOutsideContributors   = "outside_contributors"
)

// Authority of an approved gated run.
const (
	// GatedRunNoSecrets runs trusted base CI with the pr-untrusted profile.
	GatedRunNoSecrets = "no_secrets"
	// GatedRunNormal runs trusted base CI with the normal profile.
	GatedRunNormal = "normal"
)

// GatedRunNoSecretsProfile is the profile of a no_secrets gated run.
const GatedRunNoSecretsProfile = "pr-untrusted"

// Defaults when no settings row sets a value.
var (
	DefaultRunApprovers      = []string{"project_owner", "repository_write"}
	DefaultOverrideApprovers = []string{"project_owner", "repository_write"}
)

const DefaultOverrideProfile = "pr-untrusted"

// RequiredReview is one review a pull request needs before it can merge. The
// ID is also the label of an accept that is only for this review.
type RequiredReview struct {
	ID string `json:"id" yaml:"id"`
	// Description is a display text for people.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// Paths limits the review to changes that touch one of these patterns.
	// Empty means every pull request.
	Paths []string `json:"paths,omitempty" yaml:"paths,omitempty"`
	// Accept lists the subjects that can satisfy the review, including
	// non_author.
	Accept []string `json:"accept" yaml:"accept"`
	// Count is the number of different people that must accept. Zero means 1.
	Count int `json:"count,omitempty" yaml:"count,omitempty"`
}

// RequiredCount returns the number of people the review needs.
func (r RequiredReview) RequiredCount() int {
	if r.Count < 1 {
		return 1
	}
	return r.Count
}

// CITrustSettings is the run gate, override, and required-review settings of
// an organization (ProjectID nil) or of a project. A nil field of a project
// row inherits the organization value.
type CITrustSettings struct {
	SettingsID        string            `gorm:"column:settings_id;primaryKey;type:uuid;default:generate_ulid()" json:"settings_id"`
	OrgID             string            `gorm:"type:uuid;not null" json:"org_id"`
	ProjectID         *string           `gorm:"type:uuid" json:"project_id,omitempty"`
	RunGate           pq.StringArray    `gorm:"type:text[]" json:"run_gate"`
	GatedRunAuthority *string           `gorm:"type:text" json:"gated_run_authority,omitempty"`
	RunApprovers      pq.StringArray    `gorm:"type:text[]" json:"run_approvers"`
	OverrideApprovers pq.StringArray    `gorm:"type:text[]" json:"override_approvers"`
	OverrideProfile   *string           `gorm:"type:text" json:"override_profile,omitempty"`
	RequiredReviews   *[]RequiredReview `gorm:"type:jsonb;serializer:json" json:"required_reviews,omitempty"`
	UpdatedAt         time.Time         `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
	UpdatedBy         string            `gorm:"type:text;not null;default:''" json:"updated_by"`
}

func (CITrustSettings) TableName() string { return "ci_trust_settings" }

// EffectiveRequiredReview is a required review with the scope it comes from.
type EffectiveRequiredReview struct {
	RequiredReview
	// Scope is "org" or "project".
	Scope string `json:"scope"`
}

// EffectiveTrustSettings is the settings in force for one project.
type EffectiveTrustSettings struct {
	RunGate           []string                  `json:"run_gate"`
	GatedRunAuthority string                    `json:"gated_run_authority"`
	RunApprovers      []string                  `json:"run_approvers"`
	OverrideApprovers []string                  `json:"override_approvers"`
	OverrideProfile   string                    `json:"override_profile"`
	RequiredReviews   []EffectiveRequiredReview `json:"required_reviews"`
}

// GatedRunProfile returns the profile of an approved gated run, or "" when the
// run keeps its normal profile.
func (e EffectiveTrustSettings) GatedRunProfile() string {
	if e.GatedRunAuthority == GatedRunNormal {
		return ""
	}
	return GatedRunNoSecretsProfile
}

// HasRunGate reports whether the gate holds pull requests that match condition.
func (e EffectiveTrustSettings) HasRunGate(condition string) bool {
	for _, value := range e.RunGate {
		if value == condition {
			return true
		}
	}
	return false
}

// EffectiveTrust applies a project row over an organization row. Either may
// be nil.
func EffectiveTrust(org, project *CITrustSettings) EffectiveTrustSettings {
	result := EffectiveTrustSettings{
		RunGate:           []string{},
		GatedRunAuthority: GatedRunNoSecrets,
		RunApprovers:      append([]string(nil), DefaultRunApprovers...),
		OverrideApprovers: append([]string(nil), DefaultOverrideApprovers...),
		OverrideProfile:   DefaultOverrideProfile,
		RequiredReviews:   []EffectiveRequiredReview{},
	}
	for _, row := range []*CITrustSettings{org, project} {
		if row == nil {
			continue
		}
		if row.RunGate != nil {
			result.RunGate = append([]string{}, row.RunGate...)
		}
		if row.GatedRunAuthority != nil {
			result.GatedRunAuthority = *row.GatedRunAuthority
		}
		if row.RunApprovers != nil {
			result.RunApprovers = append([]string{}, row.RunApprovers...)
		}
		if row.OverrideApprovers != nil {
			result.OverrideApprovers = append([]string{}, row.OverrideApprovers...)
		}
		if row.OverrideProfile != nil {
			result.OverrideProfile = *row.OverrideProfile
		}
	}
	// Required reviews add. A project cannot remove an organization review.
	for scope, row := range map[string]*CITrustSettings{"org": org, "project": project} {
		if row == nil || row.RequiredReviews == nil {
			continue
		}
		for _, review := range *row.RequiredReviews {
			result.RequiredReviews = append(result.RequiredReviews, EffectiveRequiredReview{RequiredReview: review, Scope: scope})
		}
	}
	sort.SliceStable(result.RequiredReviews, func(i, j int) bool {
		if result.RequiredReviews[i].Scope != result.RequiredReviews[j].Scope {
			return result.RequiredReviews[i].Scope == "org"
		}
		return result.RequiredReviews[i].ID < result.RequiredReviews[j].ID
	})
	return result
}

// Validate checks the values of a settings row.
func (s CITrustSettings) Validate() error {
	for _, condition := range s.RunGate {
		switch condition {
		case RunGateForks, RunGateFirstTimeContributors, RunGateOutsideContributors:
		default:
			return fmt.Errorf("run_gate condition %q is unknown", condition)
		}
	}
	if s.GatedRunAuthority != nil && *s.GatedRunAuthority != GatedRunNoSecrets && *s.GatedRunAuthority != GatedRunNormal {
		return fmt.Errorf("gated_run_authority must be no_secrets or normal")
	}
	for name, subjects := range map[string][]string{"run_approvers": s.RunApprovers, "override_approvers": s.OverrideApprovers} {
		for _, subject := range subjects {
			if !ValidApproverSubject(subject) {
				return fmt.Errorf("%s subject %q is invalid", name, subject)
			}
		}
	}
	if s.OverrideProfile != nil && !approvalSecurityID.MatchString(*s.OverrideProfile) {
		return fmt.Errorf("override_profile has an invalid name")
	}
	if s.RequiredReviews != nil {
		seen := map[string]bool{}
		for _, review := range *s.RequiredReviews {
			if !approvalSecurityID.MatchString(review.ID) {
				return fmt.Errorf("required review ID %q is invalid", review.ID)
			}
			if seen[review.ID] {
				return fmt.Errorf("required review ID %q is used more than one time", review.ID)
			}
			seen[review.ID] = true
			if len(review.Accept) == 0 {
				return fmt.Errorf("required review %q must name at least one subject that can accept", review.ID)
			}
			for _, subject := range review.Accept {
				if subject != SubjectNonAuthor && !ValidApproverSubject(subject) {
					return fmt.Errorf("required review %q subject %q is invalid", review.ID, subject)
				}
			}
			if review.Count < 0 || review.Count > 20 {
				return fmt.Errorf("required review %q count must be from 1 to 20", review.ID)
			}
			for _, pattern := range review.Paths {
				if strings.TrimSpace(pattern) == "" || strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "..") {
					return fmt.Errorf("required review %q path %q is invalid", review.ID, pattern)
				}
			}
		}
	}
	return nil
}

// LooserThan lists each way that next is less strict than current. A project
// owner can make a project more strict; only an organization administrator can
// make it less strict. profileWeakerOrEqual reports whether profile a has the
// same or less authority than profile b.
func LooserThan(next, current EffectiveTrustSettings, profileWeakerOrEqual func(a, b string) bool) []string {
	var reasons []string
	for _, condition := range current.RunGate {
		if !next.HasRunGate(condition) {
			reasons = append(reasons, fmt.Sprintf("removes run gate condition %s", condition))
		}
	}
	if current.GatedRunAuthority == GatedRunNoSecrets && next.GatedRunAuthority == GatedRunNormal {
		reasons = append(reasons, "gives gated runs their normal profile and secrets")
	}
	if added := addedSubjects(next.RunApprovers, current.RunApprovers); len(added) > 0 {
		reasons = append(reasons, "adds run approvers "+strings.Join(added, ", "))
	}
	if added := addedSubjects(next.OverrideApprovers, current.OverrideApprovers); len(added) > 0 {
		reasons = append(reasons, "adds override approvers "+strings.Join(added, ", "))
	}
	if next.OverrideProfile != current.OverrideProfile && (profileWeakerOrEqual == nil || !profileWeakerOrEqual(next.OverrideProfile, current.OverrideProfile)) {
		reasons = append(reasons, fmt.Sprintf("override profile %s has more authority than %s", next.OverrideProfile, current.OverrideProfile))
	}
	return reasons
}

func addedSubjects(next, current []string) []string {
	have := map[string]bool{}
	for _, subject := range current {
		have[subject] = true
	}
	var added []string
	for _, subject := range next {
		if !have[subject] {
			added = append(added, subject)
		}
	}
	return added
}
