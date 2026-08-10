package vcs

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcsreport"
)

type reportClientResolver struct{ updater *JobStatusUpdater }

func NewReportClientResolver(updater *JobStatusUpdater) vcsreport.ClientResolver {
	return &reportClientResolver{updater: updater}
}

func (r *reportClientResolver) ResolveReportClient(ctx context.Context, target *models.VCSReportTarget) (vcsreport.CommentClient, error) {
	if target.TargetType != "pull_request" {
		return nil, fmt.Errorf("report target %q has no comment surface", target.TargetType)
	}
	wf := &models.WorkflowInstance{ProjectID: target.ProjectID}
	client := r.updater.getClientForWorkflow(ctx, wf, Provider(target.Provider))
	if client == nil {
		return nil, fmt.Errorf("no VCS client for provider %q", target.Provider)
	}
	return &reportCommentClient{client: client}, nil
}

type reportCommentClient struct{ client Client }

type sharedReportClient interface {
	ReadPRReportComment(context.Context, string, int, string, string) (string, string, error)
	WritePRReportComment(context.Context, string, int, string, string) (string, error)
}

func (c *reportCommentClient) ReadComment(ctx context.Context, target *models.VCSReportTarget) (string, string, error) {
	var prNumber int
	if _, err := fmt.Sscanf(target.ExternalTargetID, "%d", &prNumber); err != nil {
		return "", "", err
	}
	if client, ok := c.client.(sharedReportClient); ok {
		return client.ReadPRReportComment(ctx, target.Repository, prNumber, valueOrEmpty(target.ProviderCommentID), target.RootMarker)
	}
	return valueOrEmpty(target.ProviderCommentID), "", nil
}

func (c *reportCommentClient) WriteComment(ctx context.Context, target *models.VCSReportTarget, commentID string, body string) (string, error) {
	var prNumber int
	if _, err := fmt.Sscanf(target.ExternalTargetID, "%d", &prNumber); err != nil {
		return "", err
	}
	if client, ok := c.client.(sharedReportClient); ok {
		return client.WritePRReportComment(ctx, target.Repository, prNumber, commentID, body)
	}
	if err := c.client.UpsertPRCommentByMarker(ctx, target.Repository, prNumber, target.RootMarker, body); err != nil {
		return "", err
	}
	// The existing provider interface does not expose the numeric comment ID.
	// The root marker remains a stable provider lookup identifier.
	return target.RootMarker, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
