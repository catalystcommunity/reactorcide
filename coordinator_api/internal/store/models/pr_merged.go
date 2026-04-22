package models

import "time"

// PRMerged marks that a given (repo, pr_number) has received a merge
// webhook. Presence of a row flips the PR-comment flow from the rolling
// per-commit comment to per-job result comments.
type PRMerged struct {
	Repo     string    `gorm:"type:text;primaryKey" json:"repo"`
	PRNumber int       `gorm:"primaryKey" json:"pr_number"`
	MergedAt time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"merged_at"`
}

// TableName specifies the table name for the model.
func (PRMerged) TableName() string {
	return "pr_merged"
}
