package models

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var approvalSecurityID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type CIApproval struct {
	ApprovalID       string     `gorm:"column:approval_id;primaryKey;type:uuid;default:generate_ulid()" json:"approval_id"`
	OrgID            string     `gorm:"type:uuid;not null" json:"org_id"`
	ProjectID        string     `gorm:"type:uuid;not null" json:"project_id"`
	PRNumber         int        `gorm:"not null" json:"pr_number"`
	HeadRepository   string     `gorm:"type:text;not null" json:"head_repository"`
	HeadSHA          string     `gorm:"type:text;not null" json:"head_sha"`
	BaseSHA          string     `gorm:"type:text;not null" json:"base_sha"`
	PolicyRevision   string     `gorm:"type:text;not null" json:"policy_revision"`
	WorkflowScope    string     `gorm:"type:text;not null" json:"workflow_scope"`
	ExecutionProfile string     `gorm:"type:text;not null" json:"execution_profile"`
	ApproverUserID   *string    `gorm:"type:uuid" json:"approver_user_id,omitempty"`
	ApproverProvider string     `gorm:"type:text" json:"approver_provider,omitempty"`
	ApproverSubject  string     `gorm:"type:text;not null" json:"approver_subject"`
	CreatedAt        time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	InvalidatedAt    *time.Time `json:"invalidated_at,omitempty"`
}

func (CIApproval) TableName() string { return "ci_approvals" }

func (a CIApproval) IsValid(now time.Time, headSHA, policyRevision string) bool {
	return a.InvalidatedAt == nil && (a.ExpiresAt == nil || a.ExpiresAt.After(now)) &&
		a.HeadSHA == headSHA && a.PolicyRevision == policyRevision
}

func (a CIApproval) Validate() error {
	if a.OrgID == "" || a.ProjectID == "" || a.PRNumber <= 0 || strings.TrimSpace(a.HeadRepository) == "" ||
		strings.TrimSpace(a.HeadSHA) == "" || strings.TrimSpace(a.BaseSHA) == "" || strings.TrimSpace(a.PolicyRevision) == "" {
		return fmt.Errorf("organization, project, pull request, repository, SHAs, and policy revision are required")
	}
	if a.WorkflowScope != "*" && !approvalSecurityID.MatchString(a.WorkflowScope) {
		return fmt.Errorf("workflow scope has an invalid security ID")
	}
	if !approvalSecurityID.MatchString(a.ExecutionProfile) {
		return fmt.Errorf("execution profile has an invalid security ID")
	}
	validSubject := a.ApproverSubject == "project_owner" || a.ApproverSubject == "repository_write" ||
		(strings.HasPrefix(a.ApproverSubject, "vcs_team:") && len(strings.TrimPrefix(a.ApproverSubject, "vcs_team:")) > 0) ||
		(strings.HasPrefix(a.ApproverSubject, "reactorcide_group:") && len(strings.TrimPrefix(a.ApproverSubject, "reactorcide_group:")) > 0)
	if !validSubject {
		return fmt.Errorf("approver subject is invalid")
	}
	return nil
}
