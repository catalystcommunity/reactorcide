// Package subjects is the one grammar of the subjects that approver lists,
// CI policy, required reviews, and actor facts use. It has no dependencies,
// so the models package and the policy package can both use it.
//
// A subject names who may do something. The kinds are:
//
//   - anyone (it must be written; an omitted list never means anyone)
//     principal:<uuid>          one principal
//     service:<org>/<name>      a service principal of an organization
//     service:<name>            a service principal of the organization of the project
//     principal_kind:<kind>     any principal of a kind (human, service, system)
//     identity:<type>:<value>   one identity (for example identity:linkkeys:alice@example.com)
//     vcs_user:<provider>/<login>
//     vcs_team:<owner>/<slug>
//     reactorcide_group:<name>
//     repository_write
//     project_owner
//     non_author                (required reviews only)
//
// A person has a list of subjects. The principal resolver adds the subjects
// of every identity linked to the principal of the person, so a subject
// vcs_user:github/alice matches a LinkKeys login of the same person, and
// identity:linkkeys:alice@example.com matches a comment by github/alice.
package subjects

import (
	"regexp"
	"strings"
)

// Anyone is the subject that matches every person.
const Anyone = "*"

// Prefixes of subjects.
const (
	PrefixPrincipal     = "principal:"
	PrefixService       = "service:"
	PrefixPrincipalKind = "principal_kind:"
	PrefixIdentity      = "identity:"
	PrefixVCSUser       = "vcs_user:"
	PrefixVCSTeam       = "vcs_team:"
	PrefixGroup         = "reactorcide_group:"
)

// Fixed subjects.
const (
	RepositoryWrite = "repository_write"
	ProjectOwner    = "project_owner"
	NonAuthor       = "non_author"
	// WorkflowOwner stands for the owners of the workflow that a decision
	// is about (docs/organizations-and-ci-policy.md). It is a placeholder:
	// the coordinator replaces it, per workflow, with the owner subjects of
	// the TRUSTED workflow definition (owners.Expand). It is valid only in
	// override_approvers and in head_ci approval lists (ValidWorkflowApprover),
	// never in a recorded approval or a person's subjects.
	WorkflowOwner = "workflow_owner"
)

var (
	uuidPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	namePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	identityTypes  = map[string]bool{"vcs": true, "vcsstar": true, "linkkeys": true, "federated": true}
	principalKinds = map[string]bool{"human": true, "service": true, "system": true}
)

// Principal returns the subject of one principal.
func Principal(principalID string) string { return PrefixPrincipal + strings.ToLower(principalID) }

// Service returns the qualified subject of a service principal.
func Service(orgName, name string) string { return PrefixService + orgName + "/" + name }

// LocalService returns the subject of a service principal within its own
// organization.
func LocalService(name string) string { return PrefixService + name }

// PrincipalKind returns the subject of a principal kind.
func PrincipalKind(kind string) string { return PrefixPrincipalKind + kind }

// Identity returns the subject of one identity. A vcs identity has the
// vcs_user form.
func Identity(identityType, value string) string {
	if identityType == "vcs" {
		return PrefixVCSUser + value
	}
	return PrefixIdentity + identityType + ":" + value
}

// Valid reports whether subject is a valid approver or policy subject.
// non_author is not valid here; ValidReview accepts it.
func Valid(subject string) bool {
	switch subject {
	case Anyone, RepositoryWrite, ProjectOwner:
		return true
	}
	switch {
	case strings.HasPrefix(subject, PrefixPrincipal):
		return uuidPattern.MatchString(strings.TrimPrefix(subject, PrefixPrincipal))
	case strings.HasPrefix(subject, PrefixService):
		rest := strings.TrimPrefix(subject, PrefixService)
		org, name, qualified := strings.Cut(rest, "/")
		if !qualified {
			return namePattern.MatchString(rest)
		}
		return namePattern.MatchString(org) && namePattern.MatchString(name)
	case strings.HasPrefix(subject, PrefixPrincipalKind):
		return principalKinds[strings.TrimPrefix(subject, PrefixPrincipalKind)]
	case strings.HasPrefix(subject, PrefixIdentity):
		identityType, value, ok := strings.Cut(strings.TrimPrefix(subject, PrefixIdentity), ":")
		return ok && identityTypes[identityType] && strings.TrimSpace(value) != ""
	}
	for _, prefix := range []string{PrefixVCSUser, PrefixVCSTeam, PrefixGroup} {
		if strings.HasPrefix(subject, prefix) && len(strings.TrimPrefix(subject, prefix)) > 0 {
			return true
		}
	}
	return false
}

// ValidWorkflowApprover reports whether subject is valid in an approver list
// that is decided per workflow: override_approvers and head_ci approval
// lists. It is Valid plus the workflow_owner placeholder.
func ValidWorkflowApprover(subject string) bool { return subject == WorkflowOwner || Valid(subject) }

// ValidReview reports whether subject is valid in a required review.
func ValidReview(subject string) bool { return subject == NonAuthor || Valid(subject) }

// IsRole reports whether subject is a role: a subject that some authority
// gave the person (repository write, a team, a group, project owner). The
// subjects of the person's own identities, the principal kind, and anyone
// are not roles. Any account can comment on a public repository, so a
// non-author review counts only a person with a role.
func IsRole(subject string) bool {
	switch {
	case subject == RepositoryWrite, subject == ProjectOwner:
		return true
	case strings.HasPrefix(subject, PrefixVCSTeam), strings.HasPrefix(subject, PrefixGroup):
		return true
	}
	return false
}

// Has reports whether a person with the subjects have matches wanted. The
// subject * matches every person who has at least one subject (a person
// that could be identified).
func Has(have []string, wanted string) bool {
	if wanted == Anyone {
		return len(have) > 0
	}
	for _, value := range have {
		if value == wanted {
			return true
		}
	}
	return false
}

// HasAny reports whether have matches at least one of wanted. An empty
// wanted list matches nobody.
func HasAny(have []string, wanted []string) bool {
	for _, subject := range wanted {
		if Has(have, subject) {
			return true
		}
	}
	return false
}

// HasAnyFact is HasAny for a fact set.
func HasAnyFact(facts map[string]bool, wanted []string) bool {
	for _, subject := range wanted {
		if subject == Anyone {
			for _, present := range facts {
				if present {
					return true
				}
			}
			continue
		}
		if facts[subject] {
			return true
		}
	}
	return false
}
