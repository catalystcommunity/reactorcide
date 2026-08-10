package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

type uiOrganizationStore interface {
	CreateOrganization(context.Context, *models.Organization) error
	GetOrganizationByName(context.Context, string) (*models.Organization, error)
	UpdateOrganization(context.Context, *models.Organization) error
	SetDefaultOrganization(context.Context, string) error
	GetDefaultOrganization(context.Context) (*models.Organization, error)
}

func (s *UiService) organizationStore() (uiOrganizationStore, error) {
	value, ok := s.deps.Store.(uiOrganizationStore)
	if !ok {
		return nil, NewServiceError("internal", "organization management is not available")
	}
	return value, nil
}

func orgSummary(org *models.Organization, defaultOrg *models.Organization) csilapi.OrgSummary {
	return csilapi.OrgSummary{OrgId: org.OrgID, Name: org.Name, DisplayName: org.DisplayName, Status: org.Status,
		IsDefault: defaultOrg != nil && defaultOrg.OrgID == org.OrgID, IsPrivate: org.IsPrivate}
}

func (s *UiService) CreateOrg(ctx context.Context, req csilapi.CreateOrgRequest) (csilapi.CreateOrgResponse, error) {
	if _, err := s.requireGlobalAdmin(ctx); err != nil {
		return csilapi.CreateOrgResponse{}, err
	}
	value, err := s.organizationStore()
	if err != nil {
		return csilapi.CreateOrgResponse{}, err
	}
	org := &models.Organization{Name: req.Name, Status: models.OrganizationStatusActive}
	if req.DisplayName != nil {
		org.DisplayName = *req.DisplayName
	}
	if req.IsPrivate != nil {
		org.IsPrivate = *req.IsPrivate
	}
	if err := value.CreateOrganization(ctx, org); err != nil {
		return csilapi.CreateOrgResponse{}, mapStoreErr(err, "organization could not be created")
	}
	s.recordAudit(ctx, org.OrgID, "organization.create", "organization", org.Name, models.JSONB{"status": org.Status})
	defaultOrg, _ := value.GetDefaultOrganization(ctx)
	return csilapi.CreateOrgResponse{Org: orgSummary(org, defaultOrg)}, nil
}

func (s *UiService) UpdateOrg(ctx context.Context, req csilapi.UpdateOrgRequest) (csilapi.UpdateOrgResponse, error) {
	if _, err := s.requireGlobalAdmin(ctx); err != nil {
		return csilapi.UpdateOrgResponse{}, err
	}
	value, err := s.organizationStore()
	if err != nil {
		return csilapi.UpdateOrgResponse{}, err
	}
	org, err := value.GetOrganizationByName(ctx, req.Name)
	if err != nil {
		return csilapi.UpdateOrgResponse{}, mapStoreErr(err, "organization not found")
	}
	if req.DisplayName != nil {
		org.DisplayName = *req.DisplayName
	}
	if req.IsPrivate != nil {
		org.IsPrivate = *req.IsPrivate
	}
	if req.Status != nil {
		org.Status = *req.Status
	}
	if err := value.UpdateOrganization(ctx, org); err != nil {
		return csilapi.UpdateOrgResponse{}, mapStoreErr(err, "organization could not be updated")
	}
	s.recordAudit(ctx, org.OrgID, "organization.update", "organization", org.Name, models.JSONB{"status": org.Status})
	defaultOrg, _ := value.GetDefaultOrganization(ctx)
	return csilapi.UpdateOrgResponse{Org: orgSummary(org, defaultOrg)}, nil
}

func (s *UiService) SetDefaultOrg(ctx context.Context, req csilapi.SetDefaultOrgRequest) (csilapi.SetDefaultOrgResponse, error) {
	if _, err := s.requireGlobalAdmin(ctx); err != nil {
		return csilapi.SetDefaultOrgResponse{}, err
	}
	value, err := s.organizationStore()
	if err != nil {
		return csilapi.SetDefaultOrgResponse{}, err
	}
	org, err := value.GetOrganizationByName(ctx, req.Name)
	if err != nil {
		return csilapi.SetDefaultOrgResponse{}, mapStoreErr(err, "organization not found")
	}
	if err := value.SetDefaultOrganization(ctx, org.OrgID); err != nil {
		return csilapi.SetDefaultOrgResponse{}, mapStoreErr(err, "default organization could not be changed")
	}
	s.recordAudit(ctx, org.OrgID, "organization.set_default", "organization", org.Name, models.JSONB{})
	return csilapi.SetDefaultOrgResponse{Org: orgSummary(org, org)}, nil
}
