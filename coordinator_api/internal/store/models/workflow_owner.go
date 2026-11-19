package models

import (
	"github.com/lib/pq"
)

// Workflow owners (docs/workflow-design.md "Owners and contact", plan B2).
//
// The owner snapshot of a workflow instance and a job is set once, at
// insert, from the TRUSTED CI definition. A database trigger copies it from
// the parent workflow to a child workflow, and from the workflow to its
// jobs, and keeps it unchanged on every update (migration 000053).

// Owner snapshot sources (workflow_instances.owner_source).
const (
	// OwnerSourceCI: read from the trusted CI definition at owner_ci_sha.
	OwnerSourceCI = "ci"
	// OwnerSourcePrior: the CI of the run was the event's own content (a
	// pull request head with no stored CI policy), so the coordinator copied
	// the latest earlier trusted snapshot of the same workflow.
	OwnerSourcePrior = "prior"
	// OwnerSourceParent: copied from the parent workflow.
	OwnerSourceParent = "parent"
	// OwnerSourceRetry: copied from the retried or re-run instance.
	OwnerSourceRetry = "retry"
)

// WorkflowContact is the contact block of a workflow definition. Every field
// is optional.
type WorkflowContact struct {
	Channel string `json:"channel,omitempty"`
	HelpURL string `json:"help_url,omitempty"`
	Email   string `json:"email,omitempty"`
}

// IsZero reports whether the contact has no field.
func (c *WorkflowContact) IsZero() bool {
	return c == nil || (c.Channel == "" && c.HelpURL == "" && c.Email == "")
}

// WorkflowOwnership is the owner snapshot of one workflow run.
//
// Group nil means unknown (no trusted definition was read). Group "" means
// the trusted definition declares no owner.
type WorkflowOwnership struct {
	Group    *string
	Subjects []string
	Contact  *WorkflowContact
	Source   string
	CISHA    string
	// Trusted: the snapshot came from a trusted ref (see
	// WorkflowInstance.OwnerTrusted). An untrusted snapshot gives no
	// authority.
	Trusted bool
	// RefClasses are the ref classes of the run that wrote the snapshot.
	RefClasses []string
}

// TrustedOwner returns o when it is trusted, else an unknown snapshot. Use
// it before a snapshot gives authority.
func (o WorkflowOwnership) TrustedOwner() WorkflowOwnership {
	if !o.Trusted {
		return WorkflowOwnership{}
	}
	return o
}

// Known reports whether the snapshot came from a trusted definition.
func (o WorkflowOwnership) Known() bool { return o.Group != nil }

// HasOwner reports whether the trusted definition names an owner.
func (o WorkflowOwnership) HasOwner() bool {
	return o.Group != nil && (*o.Group != "" || len(o.Subjects) > 0)
}

// GroupName returns the owner group, or "" when unknown or none.
func (o WorkflowOwnership) GroupName() string {
	if o.Group == nil {
		return ""
	}
	return *o.Group
}

// Ownership returns the owner snapshot of the workflow instance.
func (w *WorkflowInstance) Ownership() WorkflowOwnership {
	if w == nil {
		return WorkflowOwnership{}
	}
	return WorkflowOwnership{Group: w.OwnerGroup, Subjects: append([]string(nil), w.OwnerSubjects...),
		Contact: w.OwnerContact, Source: w.OwnerSource, CISHA: w.OwnerCISHA,
		Trusted: w.OwnerTrusted && w.OwnerGroup != nil, RefClasses: append([]string(nil), w.OwnerRefClasses...)}
}

// SetOwnership writes an owner snapshot onto a new workflow instance. An
// unknown snapshot clears every owner field.
func (w *WorkflowInstance) SetOwnership(o WorkflowOwnership, source string) {
	if o.Group == nil {
		w.OwnerGroup, w.OwnerSubjects, w.OwnerContact, w.OwnerSource, w.OwnerCISHA = nil, nil, nil, "", ""
		w.OwnerTrusted, w.OwnerRefClasses = false, nil
		return
	}
	group := *o.Group
	w.OwnerGroup = &group
	w.OwnerSubjects = nil
	if len(o.Subjects) > 0 {
		w.OwnerSubjects = pq.StringArray(append([]string(nil), o.Subjects...))
	}
	w.OwnerContact = nil
	if !o.Contact.IsZero() {
		contact := *o.Contact
		w.OwnerContact = &contact
	}
	w.OwnerSource = source
	w.OwnerCISHA = o.CISHA
	w.OwnerTrusted = o.Trusted
	w.OwnerRefClasses = nil
	if len(o.RefClasses) > 0 {
		w.OwnerRefClasses = pq.StringArray(append([]string(nil), o.RefClasses...))
	}
}

// Ownership returns the owner snapshot of the job.
func (j *Job) Ownership() WorkflowOwnership {
	if j == nil {
		return WorkflowOwnership{}
	}
	return WorkflowOwnership{Group: j.OwnerGroup, Subjects: append([]string(nil), j.OwnerSubjects...), Contact: j.OwnerContact}
}

// CopyOwnershipFrom sets the owner snapshot of a new job from its workflow.
func (j *Job) CopyOwnershipFrom(w *WorkflowInstance) {
	if w == nil || w.OwnerGroup == nil {
		return
	}
	group := *w.OwnerGroup
	j.OwnerGroup = &group
	j.OwnerSubjects = nil
	if len(w.OwnerSubjects) > 0 {
		j.OwnerSubjects = pq.StringArray(append([]string(nil), w.OwnerSubjects...))
	}
	j.OwnerContact = nil
	if !w.OwnerContact.IsZero() {
		contact := *w.OwnerContact
		j.OwnerContact = &contact
	}
}
