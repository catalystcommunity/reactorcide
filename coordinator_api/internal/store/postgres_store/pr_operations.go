package postgres_store

import (
	"context"
	"errors"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/ctxkey"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// ListJobsForPRCommit returns every job with matching denormalized VCS
// metadata, newest-first for stable rendering.
func (ps PostgresDbStore) ListJobsForPRCommit(ctx context.Context, repo string, prNumber int, commitSHA string) ([]models.Job, error) {
	var jobs []models.Job
	err := ps.getDB(ctx).
		Where("vcs_repo = ? AND pr_number = ? AND commit_sha = ?", repo, prNumber, commitSHA).
		Order("created_at ASC").
		Find(&jobs).Error
	if err != nil {
		return nil, fmt.Errorf("listing jobs for PR commit: %w", err)
	}
	return jobs, nil
}

// ListJobsForPR returns every job matching (repo, prNumber) across all
// commits.
func (ps PostgresDbStore) ListJobsForPR(ctx context.Context, repo string, prNumber int) ([]models.Job, error) {
	var jobs []models.Job
	err := ps.getDB(ctx).
		Where("vcs_repo = ? AND pr_number = ?", repo, prNumber).
		Order("created_at ASC").
		Find(&jobs).Error
	if err != nil {
		return nil, fmt.Errorf("listing jobs for PR: %w", err)
	}
	return jobs, nil
}

// ForPRCommit runs fn inside a transaction that holds a Postgres advisory
// lock keyed on (repo, prNumber, commitSHA). The lock releases automatically
// at transaction end, so no explicit release is needed.
func (ps PostgresDbStore) ForPRCommit(ctx context.Context, repo string, prNumber int, commitSHA string, fn func(ctx context.Context) error) error {
	key := fmt.Sprintf("%s#%d@%s", repo, prNumber, commitSHA)
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?)::bigint)", key).Error; err != nil {
			return fmt.Errorf("acquiring advisory lock: %w", err)
		}
		txCtx := context.WithValue(ctx, ctxkey.TxKey(), tx)
		return fn(txCtx)
	})
}

// IsPRMerged returns true if a pr_merged row exists for (repo, prNumber).
func (ps PostgresDbStore) IsPRMerged(ctx context.Context, repo string, prNumber int) (bool, error) {
	var row models.PRMerged
	err := ps.getDB(ctx).Where("repo = ? AND pr_number = ?", repo, prNumber).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking pr_merged: %w", err)
	}
	return true, nil
}

// MarkPRMerged inserts a pr_merged row; silently idempotent via ON CONFLICT.
func (ps PostgresDbStore) MarkPRMerged(ctx context.Context, repo string, prNumber int) error {
	err := ps.getDB(ctx).Exec(
		"INSERT INTO pr_merged (repo, pr_number) VALUES (?, ?) ON CONFLICT (repo, pr_number) DO NOTHING",
		repo, prNumber,
	).Error
	if err != nil {
		return fmt.Errorf("marking PR merged: %w", err)
	}
	return nil
}
