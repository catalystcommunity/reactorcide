package oidcissuer

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// JobFacts are the facts of a running job that become token claims. The
// coordinator reads them from its own records; the job cannot set them.
type JobFacts struct {
	Org                string
	OrgID              string
	Project            string
	ProjectID          string
	Repository         string
	WorkflowSecurityID string
	WorkflowName       string
	WorkflowID         string
	Node               string
	JobID              string
	Ref                string
	HeadRef            string
	BaseRef            string
	SubmittedRef       string
	SHA                string
	EventType          string
	EventID            string
	PRNumber           int
	RefClasses         []string
	ActorPrincipalID   string
	ActorKind          string
	CIOrigin           string
	ExecutionProfile   string
	PolicyRevision     string
	OwnerGroup         string
}

// Claim names. These names are standard: operator-defined claims cannot use
// them.
var standardClaims = []string{
	"iss", "sub", "aud", "exp", "iat", "nbf", "jti",
	"org", "org_id", "project", "project_id", "repository", "workflow_security_id", "workflow_name",
	"workflow_id", "node", "job_id", "ref", "head_ref", "base_ref", "submitted_ref", "sha", "event_type", "event_id",
	"pr_number", "ref_classes", "actor_principal_id", "actor_kind", "ci_origin", "execution_profile",
	"policy_revision", "owner_group",
}

// ClaimNames returns the standard claim names.
func ClaimNames() []string { return append([]string(nil), standardClaims...) }

// IsStandardClaim reports whether name is a standard claim name.
func IsStandardClaim(name string) bool {
	for _, standard := range standardClaims {
		if standard == name {
			return true
		}
	}
	return false
}

var extraClaimName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ValidateAudienceClaims checks operator-defined claims: {AUDIENCE: {CLAIM:
// VALUE}}. "*" is every audience.
func ValidateAudienceClaims(claims map[string]map[string]string) error {
	if len(claims) > 64 {
		return fmt.Errorf("at most 64 audiences can have extra claims")
	}
	for audience, values := range claims {
		if strings.TrimSpace(audience) == "" || len(audience) > 512 {
			return fmt.Errorf("an audience must have 1 to 512 characters")
		}
		if len(values) > 32 {
			return fmt.Errorf("audience %q: at most 32 extra claims", audience)
		}
		for name, value := range values {
			if !extraClaimName.MatchString(name) {
				return fmt.Errorf("audience %q: claim name %q must be lowercase letters, digits, and underscores", audience, name)
			}
			if IsStandardClaim(name) {
				return fmt.Errorf("audience %q: claim %q is a standard claim", audience, name)
			}
			if len(value) > 1024 {
				return fmt.Errorf("audience %q: claim %q is too long", audience, name)
			}
		}
	}
	return nil
}

// subjectPart escapes one part of the subject. ":" separates the parts, so a
// ":" in a value becomes "%3A", and "%" becomes "%25".
func subjectPart(value string) string {
	value = strings.ReplaceAll(value, "%", "%25")
	return strings.ReplaceAll(value, ":", "%3A")
}

// Subject returns the "sub" claim:
//
//	org:ORG:project:PROJECT:workflow:SECURITY_ID:node:NODE:ref:REF
//
// An unknown part is empty. The format never changes its part order, so a
// consumer can match it with a glob.
func Subject(facts JobFacts) string {
	return strings.Join([]string{
		"org", subjectPart(facts.Org),
		"project", subjectPart(facts.Project),
		"workflow", subjectPart(facts.WorkflowSecurityID),
		"node", subjectPart(facts.Node),
		"ref", subjectPart(facts.Ref),
	}, ":")
}

// Claims returns the claims of a job token, without iss, aud, iat, nbf, exp,
// and jti (Sign sets them). extra are operator-defined claims; a standard
// claim name in extra is ignored.
func Claims(facts JobFacts, extra map[string]string) map[string]any {
	classes := append([]string{}, facts.RefClasses...)
	sort.Strings(classes)
	claims := map[string]any{
		"sub":                  Subject(facts),
		"org":                  facts.Org,
		"org_id":               facts.OrgID,
		"project":              facts.Project,
		"project_id":           facts.ProjectID,
		"repository":           facts.Repository,
		"workflow_security_id": facts.WorkflowSecurityID,
		"workflow_name":        facts.WorkflowName,
		"workflow_id":          facts.WorkflowID,
		"node":                 facts.Node,
		"job_id":               facts.JobID,
		"ref":                  facts.Ref,
		"head_ref":             facts.HeadRef,
		"base_ref":             facts.BaseRef,
		"submitted_ref":        facts.SubmittedRef,
		"sha":                  facts.SHA,
		"event_type":           facts.EventType,
		"event_id":             facts.EventID,
		"ref_classes":          classes,
		"actor_principal_id":   facts.ActorPrincipalID,
		"actor_kind":           facts.ActorKind,
		"ci_origin":            facts.CIOrigin,
		"execution_profile":    facts.ExecutionProfile,
		"policy_revision":      facts.PolicyRevision,
		"owner_group":          facts.OwnerGroup,
	}
	if facts.PRNumber > 0 {
		claims["pr_number"] = facts.PRNumber
	}
	for name, value := range extra {
		if IsStandardClaim(name) {
			continue
		}
		claims[name] = value
	}
	return claims
}

// ExtraClaimsFor returns the operator-defined claims for audience: the "*"
// claims, then the claims of the exact audience. The project settings apply
// after the organization settings, so a project value replaces an
// organization value.
func ExtraClaimsFor(audience string, layers ...map[string]map[string]string) map[string]string {
	out := map[string]string{}
	for _, layer := range layers {
		for _, key := range []string{"*", audience} {
			for name, value := range layer[key] {
				out[name] = value
			}
		}
	}
	return out
}

// ValidAudience reports whether audience is acceptable: 1 to 512 printable
// characters with no spaces.
func ValidAudience(audience string) bool {
	if audience == "" || len(audience) > 512 || audience == "*" {
		return false
	}
	for _, r := range audience {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}
