package vcsreport

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type Store interface {
	ListDirtyVCSReportTargets(context.Context, int) ([]models.VCSReportTarget, error)
	WithVCSReportTargetLock(context.Context, string, func(context.Context, *models.VCSReportTarget, []models.VCSReportEntry) error) error
	SetVCSReportRendered(context.Context, string, string, int64) error
	RecordVCSReportError(context.Context, string, error) error
}

type CommentClient interface {
	ReadComment(context.Context, *models.VCSReportTarget) (commentID string, body string, err error)
	WriteComment(context.Context, *models.VCSReportTarget, string, string) (commentID string, err error)
}

type ClientResolver interface {
	ResolveReportClient(context.Context, *models.VCSReportTarget) (CommentClient, error)
}

type Reconciler struct {
	Store   Store
	Clients ClientResolver

	mu       sync.Mutex
	failures map[string]retryState
	now      func() time.Time
}

type retryState struct {
	count int
	next  time.Time
}

const (
	baseRetryDelay = time.Second
	maxRetryDelay  = time.Minute
)

func (r *Reconciler) ReconcileOnce(ctx context.Context) error {
	targets, err := r.Store.ListDirtyVCSReportTargets(ctx, 50)
	if err != nil {
		return err
	}
	for i := range targets {
		target := targets[i]
		if !r.retryReady(target.ReportTargetID) {
			continue
		}
		audit.Record(ctx, r.Store, target.OrgID, "vcs_report.refresh_attempt", "vcs_report_target", target.ReportTargetID,
			models.JSONB{"desired_revision": target.DesiredRevision, "rendered_revision": target.RenderedRevision})
		var reconcileErr error
		var commentID string
		for attempt := 0; attempt < 2; attempt++ {
			commentID, reconcileErr = r.reconcileTarget(ctx, target.ReportTargetID)
			if reconcileErr != nil {
				break
			}
		}
		if reconcileErr != nil {
			_ = r.Store.RecordVCSReportError(ctx, target.ReportTargetID, reconcileErr)
			r.recordFailure(target.ReportTargetID)
			audit.Record(ctx, r.Store, target.OrgID, "vcs_report.refresh_failure", "vcs_report_target", target.ReportTargetID,
				models.JSONB{"failed": true})
		} else {
			r.clearFailure(target.ReportTargetID)
			audit.Record(ctx, r.Store, target.OrgID, "vcs_report.refresh_success", "vcs_report_target", target.ReportTargetID,
				models.JSONB{"desired_revision": target.DesiredRevision, "provider_comment_id": commentID})
		}
	}
	return nil
}

func (r *Reconciler) reconcileTarget(ctx context.Context, targetID string) (string, error) {
	var writtenCommentID string
	err := r.Store.WithVCSReportTargetLock(ctx, targetID, func(lockCtx context.Context, current *models.VCSReportTarget, rows []models.VCSReportEntry) error {
		if current.RenderedRevision >= current.DesiredRevision {
			writtenCommentID = value(current.ProviderCommentID)
			return r.Store.SetVCSReportRendered(lockCtx, current.ReportTargetID, writtenCommentID, current.DesiredRevision)
		}
		client, err := r.Clients.ResolveReportClient(lockCtx, current)
		if err != nil {
			return err
		}
		commentID, existing, err := client.ReadComment(lockCtx, current)
		if err != nil {
			return err
		}
		entries := make([]Entry, 0, len(rows))
		for _, row := range rows {
			title, _ := row.StructuredState["title"].(string)
			body, _ := row.StructuredState["body"].(string)
			entries = append(entries, Entry{Key: row.EntryKey, Title: title, Body: body, Status: row.Status, Generation: row.Generation})
		}
		body, _ := Merge(existing, entries, current.CurrentGeneration, current.GenerationComplete)
		commentID, err = client.WriteComment(lockCtx, current, commentID, body)
		if err != nil {
			return err
		}
		writtenCommentID = commentID
		return r.Store.SetVCSReportRendered(lockCtx, current.ReportTargetID, writtenCommentID, current.DesiredRevision)
	})
	return writtenCommentID, err
}

func (r *Reconciler) retryReady(targetID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, found := r.failures[targetID]
	return !found || !r.currentTime().Before(state.next)
}

func (r *Reconciler) recordFailure(targetID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failures == nil {
		r.failures = make(map[string]retryState)
	}
	state := r.failures[targetID]
	state.count++
	delay := baseRetryDelay << min(state.count-1, 6)
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	state.next = r.currentTime().Add(delay)
	r.failures[targetID] = state
}

func (r *Reconciler) clearFailure(targetID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.failures, targetID)
}

func (r *Reconciler) currentTime() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func (r *Reconciler) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.ReconcileOnce(ctx); err != nil {
			_ = fmt.Sprintf("report reconciliation: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
