// Package owners reads, validates, and resolves workflow owners and contacts
// (docs/workflow-design.md "Owners and contact"; plan items B2, A6, B5).
//
// A workflow definition can declare:
//
//	owners:
//	  group: payments-ci            # a Reactorcide group name, or vcs_team:ORG/SLUG
//	  subjects: [vcs_user:github/alice]   # optional extra owner subjects
//	contact:
//	  channel: "#payments-ci"
//	  help_url: https://wiki.example/payments-ci
//	  email: payments-ci@example.com
//
// The coordinator reads these keys from the TRUSTED CI definition only (see
// worker.TriggerProcessor), and stores a snapshot on each workflow instance
// and job (models.WorkflowOwnership). Other packages use this package to
// read the trusted owner of a workflow (ForWorkflow) and to turn an owner
// into approver subjects (Subjects).
package owners

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/subjects"
)

// Limits. runnerlib (src/eval.py) uses the same values.
const (
	MaxGroupLength   = 200
	MaxSubjects      = 20
	MaxSubjectLength = 200
	MaxChannelLength = 100
	MaxHelpURLLength = 500
	MaxEmailLength   = 254
)

var groupNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Spec is the owners block of a workflow definition, as the eval reports it.
type Spec struct {
	Group    string   `json:"group,omitempty"`
	Subjects []string `json:"subjects,omitempty"`
}

// ContactSpec is the contact block of a workflow definition.
type ContactSpec struct {
	Channel string `json:"channel,omitempty"`
	HelpURL string `json:"help_url,omitempty"`
	Email   string `json:"email,omitempty"`
}

// NormalizeGroup validates an owner group and returns its stored form: a
// plain Reactorcide group name, or a vcs_team:ORG/SLUG subject.
// "reactorcide_group:NAME" is accepted and stored as NAME.
func NormalizeGroup(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > MaxGroupLength {
		return "", fmt.Errorf("owners.group is longer than %d characters", MaxGroupLength)
	}
	if strings.HasPrefix(value, subjects.PrefixGroup) {
		value = strings.TrimPrefix(value, subjects.PrefixGroup)
	}
	if strings.HasPrefix(value, subjects.PrefixVCSTeam) {
		team := strings.TrimPrefix(value, subjects.PrefixVCSTeam)
		org, slug, ok := strings.Cut(team, "/")
		if !ok || !validTeamPart(org) || !validTeamPart(slug) {
			return "", fmt.Errorf("owners.group %q must be vcs_team:ORG/SLUG", value)
		}
		return subjects.PrefixVCSTeam + strings.ToLower(org) + "/" + strings.ToLower(slug), nil
	}
	if !groupNamePattern.MatchString(value) {
		return "", fmt.Errorf("owners.group %q must be a Reactorcide group name (lowercase letters, digits, hyphens) or vcs_team:ORG/SLUG", value)
	}
	return value, nil
}

func validTeamPart(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for _, c := range value {
		if !(unicode.IsLetter(c) || unicode.IsDigit(c) || c == '-' || c == '_' || c == '.') || c > unicode.MaxASCII {
			return false
		}
	}
	return true
}

// ValidOwnerSubject reports whether an extra owner subject is allowed. Only a
// subject that names people is allowed: not "*", and not a computed role
// (repository_write, project_owner, workflow_owner).
func ValidOwnerSubject(value string) bool {
	if value == "" || len(value) > MaxSubjectLength || !subjects.Valid(value) {
		return false
	}
	switch value {
	case subjects.Anyone, subjects.RepositoryWrite, subjects.ProjectOwner, subjects.WorkflowOwner:
		return false
	}
	return !strings.HasPrefix(value, subjects.PrefixPrincipalKind)
}

func cleanText(field, value string, limit int) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return "", fmt.Errorf("contact.%s is longer than %d characters", field, limit)
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return "", fmt.Errorf("contact.%s has a control character", field)
		}
	}
	return value, nil
}

// NormalizeContact validates a contact block.
func NormalizeContact(spec ContactSpec) (*models.WorkflowContact, error) {
	channel, err := cleanText("channel", spec.Channel, MaxChannelLength)
	if err != nil {
		return nil, err
	}
	helpURL, err := cleanText("help_url", spec.HelpURL, MaxHelpURLLength)
	if err != nil {
		return nil, err
	}
	if helpURL != "" {
		parsed, parseErr := url.Parse(helpURL)
		if parseErr != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
			return nil, fmt.Errorf("contact.help_url must be an http or https URL")
		}
	}
	email, err := cleanText("email", spec.Email, MaxEmailLength)
	if err != nil {
		return nil, err
	}
	if email != "" {
		address, parseErr := mail.ParseAddress(email)
		if parseErr != nil || address.Address != email || address.Name != "" {
			return nil, fmt.Errorf("contact.email must be one plain email address")
		}
	}
	contact := &models.WorkflowContact{Channel: channel, HelpURL: helpURL, Email: email}
	if contact.IsZero() {
		return nil, nil
	}
	return contact, nil
}

// FromSpec validates the owners and contact blocks of a trusted definition
// and returns the snapshot. A definition with no owners block gives a known
// snapshot with no owner (Group ""). An invalid block gives an error; the
// caller records no owner for it.
func FromSpec(owner *Spec, contact *ContactSpec, ciSHA string) (models.WorkflowOwnership, error) {
	group := ""
	result := models.WorkflowOwnership{Group: &group, CISHA: ciSHA}
	if owner != nil {
		normalized, err := NormalizeGroup(owner.Group)
		if err != nil {
			return result, err
		}
		if len(owner.Subjects) > MaxSubjects {
			return result, fmt.Errorf("owners.subjects has more than %d entries", MaxSubjects)
		}
		seen := map[string]bool{}
		extra := []string{}
		for _, subject := range owner.Subjects {
			subject = strings.TrimSpace(subject)
			if !ValidOwnerSubject(subject) {
				return result, fmt.Errorf("owners.subjects entry %q is not a valid owner subject", subject)
			}
			if !seen[subject] {
				seen[subject] = true
				extra = append(extra, subject)
			}
		}
		group = normalized
		result.Group = &group
		if len(extra) > 0 {
			result.Subjects = extra
		}
	}
	if contact != nil {
		normalized, err := NormalizeContact(*contact)
		if err != nil {
			return result, err
		}
		result.Contact = normalized
	}
	return result, nil
}

// Subjects returns the approver subjects that the owner of a workflow
// stands for: reactorcide_group:NAME (or the vcs_team subject) for the
// group, and every extra subject. An unknown or empty owner gives nil, so a
// workflow_owner approver list never matches a workflow with no owner.
func Subjects(o models.WorkflowOwnership) []string {
	if o.Group == nil {
		return nil
	}
	out := []string{}
	if group := *o.Group; group != "" {
		if strings.HasPrefix(group, subjects.PrefixVCSTeam) {
			out = append(out, group)
		} else {
			out = append(out, subjects.PrefixGroup+group)
		}
	}
	for _, subject := range o.Subjects {
		if ValidOwnerSubject(subject) {
			out = append(out, subject)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Store is the store surface that ForWorkflow reads.
type Store interface {
	LatestWorkflowOwnership(ctx context.Context, projectID, workflowSecurityID string) (*models.WorkflowOwnership, error)
}

// ForWorkflow returns the latest trusted owner snapshot of a workflow
// security ID in a project. A workflow that has no trusted snapshot gives an
// unknown snapshot (Known() false) and no error. Use it where no workflow
// instance is in view, for example quarantine and required-set proposals.
func ForWorkflow(ctx context.Context, st interface{}, projectID, workflowSecurityID string) (models.WorkflowOwnership, error) {
	reader, ok := st.(Store)
	if !ok || projectID == "" || workflowSecurityID == "" {
		return models.WorkflowOwnership{}, nil
	}
	snapshot, err := reader.LatestWorkflowOwnership(ctx, projectID, workflowSecurityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return models.WorkflowOwnership{}, nil
		}
		return models.WorkflowOwnership{}, err
	}
	if snapshot == nil {
		return models.WorkflowOwnership{}, nil
	}
	return *snapshot, nil
}

// ProjectStore is the store surface that ForProject reads.
type ProjectStore interface {
	ListLatestWorkflowOwnerships(ctx context.Context, projectID string) (map[string]models.WorkflowOwnership, error)
}

// ForProject returns the latest trusted owner snapshot of every workflow of
// a project, keyed by workflow security ID.
func ForProject(ctx context.Context, st interface{}, projectID string) (map[string]models.WorkflowOwnership, error) {
	reader, ok := st.(ProjectStore)
	if !ok || projectID == "" {
		return map[string]models.WorkflowOwnership{}, nil
	}
	return reader.ListLatestWorkflowOwnerships(ctx, projectID)
}

// Expand replaces the workflow_owner placeholder in a subject list with the
// owner subjects of one workflow. Other subjects stay. The result never
// contains workflow_owner, so a workflow with no owner matches nothing for
// that entry.
func Expand(list []string, ownerSubjects []string) []string {
	out := make([]string, 0, len(list)+len(ownerSubjects))
	seen := map[string]bool{}
	add := func(value string) {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	for _, subject := range list {
		if subject == subjects.WorkflowOwner {
			for _, owner := range ownerSubjects {
				add(owner)
			}
			continue
		}
		add(subject)
	}
	return out
}
