package models

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lib/pq"
)

var approvalSecurityID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Approval kinds. Each is one action of one person on one pull request head.
const (
	// CIApprovalKindRun satisfies the run gate: the head may run on trusted
	// base CI (/reactorcide approve-ci).
	CIApprovalKindRun = "run"
	// CIApprovalKindHeadCI lets the head's own CI run (/reactorcide override-ci).
	CIApprovalKindHeadCI = "head_ci"
	// CIApprovalKindAccept counts toward required reviews (/reactorcide accept,
	// an approved pull request review). It never grants execution.
	CIApprovalKindAccept = "accept"
)

// Approval actions. The rows are append-only; for each role the last action
// wins.
const (
	CIApprovalActionApproved = "approved"
	CIApprovalActionRevoked  = "revoked"
)

// Approval channels.
const (
	CIApprovalChannelComment = "comment"
	CIApprovalChannelReview  = "review"
	CIApprovalChannelAPI     = "api"
	CIApprovalChannelUI      = "ui"
)

// SubjectNonAuthor matches any person who is not an author of the change. It
// is valid only in a required review.
const SubjectNonAuthor = "non_author"

// CIApproval is one approval action. An approval binds to the head SHA only.
type CIApproval struct {
	ApprovalID     string `gorm:"column:approval_id;primaryKey;type:uuid;default:generate_ulid()" json:"approval_id"`
	Kind           string `gorm:"type:text;not null" json:"kind"`
	Action         string `gorm:"type:text;not null" json:"action"`
	Label          string `gorm:"type:text;not null;default:''" json:"label,omitempty"`
	OrgID          string `gorm:"type:uuid;not null" json:"org_id"`
	ProjectID      string `gorm:"type:uuid;not null" json:"project_id"`
	PRNumber       int    `gorm:"not null" json:"pr_number"`
	HeadRepository string `gorm:"type:text;not null" json:"head_repository"`
	HeadSHA        string `gorm:"type:text;not null" json:"head_sha"`
	// Person is "PROVIDER/LOGIN" for a VCS account or "user:USER_ID" for a
	// Reactorcide user with no VCS account.
	Person string `gorm:"type:text;not null" json:"person"`
	// Subjects are every approver subject the person had when they acted.
	Subjects         pq.StringArray `gorm:"type:text[];not null" json:"subjects"`
	Channel          string         `gorm:"type:text;not null" json:"channel"`
	SourceObject     *string        `gorm:"type:text" json:"source_object,omitempty"`
	ApproverUserID   *string        `gorm:"type:uuid" json:"approver_user_id,omitempty"`
	ApproverProvider string         `gorm:"type:text" json:"approver_provider,omitempty"`
	CreatedAt        time.Time      `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	ExpiresAt        *time.Time     `json:"expires_at,omitempty"`
	InvalidatedAt    *time.Time     `json:"invalidated_at,omitempty"`
}

func (CIApproval) TableName() string { return "ci_approvals" }

// IsActive reports whether the action is neither invalidated nor expired.
func (a CIApproval) IsActive(now time.Time) bool {
	return a.InvalidatedAt == nil && (a.ExpiresAt == nil || a.ExpiresAt.After(now))
}

// HasSubject reports whether the person had the subject when they acted.
func (a CIApproval) HasSubject(subject string) bool {
	for _, value := range a.Subjects {
		if value == subject {
			return true
		}
	}
	return false
}

// ValidApproverSubject reports whether subject is a subject an approval can
// record. It is the same subject set that coordinator CI policy accepts.
func ValidApproverSubject(subject string) bool {
	if subject == "project_owner" || subject == "repository_write" {
		return true
	}
	for _, prefix := range []string{"vcs_user:", "vcs_team:", "reactorcide_group:"} {
		if strings.HasPrefix(subject, prefix) && len(strings.TrimPrefix(subject, prefix)) > 0 {
			return true
		}
	}
	return false
}

// ValidApprovalLabel reports whether label is a valid accept label.
func ValidApprovalLabel(label string) bool { return approvalSecurityID.MatchString(label) }

func (a CIApproval) Validate() error {
	if a.OrgID == "" || a.ProjectID == "" || a.PRNumber <= 0 || strings.TrimSpace(a.HeadRepository) == "" ||
		strings.TrimSpace(a.HeadSHA) == "" {
		return fmt.Errorf("organization, project, pull request, repository, and head SHA are required")
	}
	switch a.Kind {
	case CIApprovalKindRun, CIApprovalKindHeadCI, CIApprovalKindAccept:
	default:
		return fmt.Errorf("approval kind %q is unknown", a.Kind)
	}
	if a.Action != CIApprovalActionApproved && a.Action != CIApprovalActionRevoked {
		return fmt.Errorf("approval action %q is unknown", a.Action)
	}
	if a.Label != "" && (a.Kind != CIApprovalKindAccept || !ValidApprovalLabel(a.Label)) {
		return fmt.Errorf("a label is valid only on an accept, and it must be a security ID")
	}
	if strings.TrimSpace(a.Person) == "" {
		return fmt.Errorf("the person is required")
	}
	switch a.Channel {
	case CIApprovalChannelComment, CIApprovalChannelReview, CIApprovalChannelAPI, CIApprovalChannelUI:
	default:
		return fmt.Errorf("approval channel %q is unknown", a.Channel)
	}
	for _, subject := range a.Subjects {
		if !ValidApproverSubject(subject) {
			return fmt.Errorf("approver subject %q is invalid", subject)
		}
	}
	return nil
}

// VCSPerson returns the person key of a VCS account.
func VCSPerson(provider, login string) string {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(provider)) + "/" + login
}

// UserPerson returns the person key of a Reactorcide user with no VCS account.
func UserPerson(userID string) string { return "user:" + userID }
