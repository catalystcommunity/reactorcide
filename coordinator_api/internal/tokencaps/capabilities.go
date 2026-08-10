// Package tokencaps defines coordinator API token capabilities.
//
// These capabilities control coordinator operations. They are separate from
// the runtime capabilities on a job, such as docker, builder, and gpu.
package tokencaps

import (
	"fmt"
	"sort"
)

const All = "*"

const (
	OrganizationsRead   = "organizations:read"
	OrganizationsManage = "organizations:manage"
	ProjectsRead        = "projects:read"
	ProjectsCreate      = "projects:create"
	ProjectsManage      = "projects:manage"
	JobsSubmit          = "jobs:submit"
	JobsRead            = "jobs:read"
	JobsCancel          = "jobs:cancel"
	JobsRetry           = "jobs:retry"
	LogsRead            = "logs:read"
	WorkflowsRead       = "workflows:read"
	WorkflowsControl    = "workflows:control"
	SecretsManage       = "secrets:manage"
	WorkersManage       = "workers:manage"
	PoliciesManage      = "policies:manage"
	TokensManage        = "tokens:manage"
	AuditRead           = "audit:read"
)

var ordered = []string{
	OrganizationsRead, OrganizationsManage,
	ProjectsRead, ProjectsCreate, ProjectsManage,
	JobsSubmit, JobsRead, JobsCancel, JobsRetry,
	LogsRead, WorkflowsRead, WorkflowsControl,
	SecretsManage, WorkersManage, PoliciesManage, TokensManage, AuditRead,
}

var known = func() map[string]struct{} {
	m := make(map[string]struct{}, len(ordered))
	for _, capability := range ordered {
		m[capability] = struct{}{}
	}
	return m
}()

// Values returns all concrete capabilities in a stable order.
func Values() []string {
	return append([]string(nil), ordered...)
}

// Validate rejects an unknown capability. All is accepted as the display
// sentinel for an all-capabilities token.
func Validate(capability string) error {
	if capability == All {
		return nil
	}
	if _, ok := known[capability]; !ok {
		return fmt.Errorf("unknown token capability %q", capability)
	}
	return nil
}

// Set is a set of concrete token capability strings.
type Set map[string]struct{}

func New(values ...string) (Set, error) {
	result := make(Set, len(values))
	for _, value := range values {
		if value == All {
			return nil, fmt.Errorf("%q is a display sentinel, not a concrete capability", All)
		}
		if err := Validate(value); err != nil {
			return nil, err
		}
		result[value] = struct{}{}
	}
	return result, nil
}

func (s Set) Has(capability string) bool {
	_, ok := s[capability]
	return ok
}

func (s Set) Slice() []string {
	result := make([]string, 0, len(s))
	for capability := range s {
		result = append(result, capability)
	}
	sort.Strings(result)
	return result
}

func (s Set) IsSubsetOf(parent Set) bool {
	for capability := range s {
		if !parent.Has(capability) {
			return false
		}
	}
	return true
}

func Intersect(left, right Set) Set {
	if len(left) > len(right) {
		left, right = right, left
	}
	result := make(Set)
	for capability := range left {
		if right.Has(capability) {
			result[capability] = struct{}{}
		}
	}
	return result
}
