package uiapi

import (
	"context"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func trustKey(orgID string, projectID *string) string {
	if projectID == nil {
		return orgID
	}
	return orgID + "/" + *projectID
}

func (f *fakeStore) GetCITrustSettings(_ context.Context, orgID string, projectID *string) (*models.CITrustSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.trustSettings[trustKey(orgID, projectID)]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &row, nil
}

func (f *fakeStore) PutCITrustSettings(_ context.Context, settings *models.CITrustSettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.trustSettings == nil {
		f.trustSettings = map[string]models.CITrustSettings{}
	}
	f.trustSettings[trustKey(settings.OrgID, settings.ProjectID)] = *settings
	return nil
}

func (f *fakeStore) EffectiveCITrust(ctx context.Context, orgID, projectID string) (models.EffectiveTrustSettings, error) {
	org, err := f.GetCITrustSettings(ctx, orgID, nil)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return models.EffectiveTrustSettings{}, err
	}
	project, err := f.GetCITrustSettings(ctx, orgID, &projectID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return models.EffectiveTrustSettings{}, err
	}
	return models.EffectiveTrust(org, project), nil
}

func (f *fakeStore) CreateCIApproval(_ context.Context, approval *models.CIApproval) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approvals = append(f.approvals, *approval)
	return nil
}

func (f *fakeStore) ListCIApprovalsForHead(_ context.Context, projectID string, prNumber int, headSHA string) ([]models.CIApproval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := []models.CIApproval{}
	for _, approval := range f.approvals {
		if approval.ProjectID == projectID && approval.PRNumber == prNumber && approval.HeadSHA == headSHA {
			result = append(result, approval)
		}
	}
	return result, nil
}

func (f *fakeStore) InvalidateCIApprovalsForNewHead(context.Context, string, int, string) (int64, error) {
	return 0, nil
}

// GetExecutionProfile knows the two built-in profiles of every organization.
func (f *fakeStore) GetExecutionProfile(_ context.Context, orgID, name string) (*models.ExecutionProfile, error) {
	switch name {
	case "standard":
		return &models.ExecutionProfile{OrgID: orgID, Name: name, MayRunAsRoot: true, TrustedCacheWrites: true}, nil
	case "pr-untrusted":
		return &models.ExecutionProfile{OrgID: orgID, Name: name, DenySecrets: true, RuntimeCapabilities: []string{}}, nil
	}
	return nil, store.ErrNotFound
}

// fakeExecutor records the requests the UI sends to the approval path.
type fakeExecutor struct {
	requests []approvals.Request
	err      error
	reviews  *approvals.HeadReviews
}

func (e *fakeExecutor) Execute(_ context.Context, request approvals.Request) (approvals.Result, error) {
	e.requests = append(e.requests, request)
	if e.err != nil {
		return approvals.Result{}, e.err
	}
	return approvals.Result{Effect: "reviews", Message: "accept recorded"}, nil
}

func (e *fakeExecutor) HeadReviews(context.Context, *models.Project, int) (*approvals.HeadReviews, error) {
	if e.reviews != nil {
		return e.reviews, nil
	}
	return &approvals.HeadReviews{HeadSHA: "head"}, nil
}
