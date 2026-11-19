package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

type uiOrganizationStore interface {
	CreateOrganization(context.Context, *models.Organization) error
	GetOrganizationByName(context.Context, string) (*models.Organization, error)
	UpdateOrganization(context.Context, *models.Organization) error
	SetDefaultOrganization(context.Context, string) error
	GetDefaultOrganization(context.Context) (*models.Organization, error)
}

type uiOrganizationDeleteStore interface {
	DeleteOrganization(context.Context, string, string) error
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
		IsDefault: defaultOrg != nil && defaultOrg.OrgID == org.OrgID, IsPrivate: org.IsPrivate,
		DuplicateEvents: optionalText(org.DuplicateEvents), DynamicJobs: optionalText(org.DynamicJobs),
		DraftPrs: optionalText(org.DraftPRs), DefaultsProfile: optionalText(org.DefaultsProfile), ShedLevel: optionalText(org.ShedLevel),
		InfraRetries: intPtr64(org.InfraRetries), EgressControl: optionalBool(org.EgressControl)}
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
	settingsChanged, err := applyOrgEventSettings(org, req.DuplicateEvents, req.DynamicJobs)
	if err != nil {
		return csilapi.UpdateOrgResponse{}, err
	}
	draftChanged, err := s.validateOrgDraftPRs(org, req.DraftPrs)
	if err != nil {
		return csilapi.UpdateOrgResponse{}, err
	}
	if req.InfraRetries != nil && !models.IsValidInfraRetries(int(*req.InfraRetries)) {
		return csilapi.UpdateOrgResponse{}, NewServiceError("invalid_argument", "infra_retries must be from 0 to 5")
	}
	infraRetries, _ := s.deps.Store.(store.InfraRetryStore)
	egress, _ := s.deps.Store.(store.EgressControlStore)
	if (req.InfraRetries != nil && infraRetries == nil) || (req.EgressControl != nil && egress == nil) {
		return csilapi.UpdateOrgResponse{}, NewServiceError("unavailable", "infra_retries and egress_control are not available")
	}
	if err := value.UpdateOrganization(ctx, org); err != nil {
		return csilapi.UpdateOrgResponse{}, mapStoreErr(err, "organization could not be updated")
	}
	if draftChanged != nil {
		if err := s.writeOrgDraftPRs(ctx, org, *req.DraftPrs); err != nil {
			return csilapi.UpdateOrgResponse{}, err
		}
		if settingsChanged == nil {
			settingsChanged = models.JSONB{}
		}
		settingsChanged["draft_prs"] = draftChanged
	}
	details := models.JSONB{"status": org.Status}
	if req.InfraRetries != nil {
		value := int(*req.InfraRetries)
		if err := infraRetries.SetOrganizationInfraRetries(ctx, org.OrgID, value); err != nil {
			return csilapi.UpdateOrgResponse{}, mapStoreErr(err, "organization not found")
		}
		org.InfraRetries = &value
		details["infra_retries"] = value
	}
	if req.EgressControl != nil {
		if err := egress.SetOrganizationEgressControl(ctx, org.OrgID, *req.EgressControl); err != nil {
			return csilapi.UpdateOrgResponse{}, mapStoreErr(err, "organization not found")
		}
		org.EgressControl = *req.EgressControl
		details["egress_control"] = *req.EgressControl
	}
	if settingsChanged != nil {
		s.recordAudit(ctx, org.OrgID, "organization.event_settings", "organization", org.Name, settingsChanged)
	}
	s.recordAudit(ctx, org.OrgID, "organization.update", "organization", org.Name, details)
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

func (s *UiService) DeleteOrg(ctx context.Context, req csilapi.DeleteOrgRequest) (csilapi.DeleteOrgResponse, error) {
	ctx, id, _, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return csilapi.DeleteOrgResponse{}, err
	}
	if !req.Confirm {
		return csilapi.DeleteOrgResponse{}, NewServiceError("invalid_argument", "confirm must be true")
	}
	value, err := s.organizationStore()
	if err != nil {
		return csilapi.DeleteOrgResponse{}, err
	}
	deleteStore, ok := s.deps.Store.(uiOrganizationDeleteStore)
	if !ok {
		return csilapi.DeleteOrgResponse{}, NewServiceError("internal", "organization deletion is not available")
	}
	organization, err := value.GetOrganizationByName(ctx, req.Name)
	if err != nil {
		return csilapi.DeleteOrgResponse{}, mapStoreErr(err, "organization not found")
	}
	replacement, err := value.GetOrganizationByName(ctx, req.Replacement)
	if err != nil {
		return csilapi.DeleteOrgResponse{}, mapStoreErr(err, "replacement organization not found")
	}
	if err := s.requireOrgAdminGrant(ctx, id, organization.OrgID, tokencaps.OrganizationsManage); err != nil {
		return csilapi.DeleteOrgResponse{}, mapPermissionErr(err)
	}
	if err := s.requireOrgAdminGrant(ctx, id, replacement.OrgID, tokencaps.OrganizationsManage); err != nil {
		return csilapi.DeleteOrgResponse{}, mapPermissionErr(err)
	}
	if err := deleteStore.DeleteOrganization(ctx, organization.OrgID, replacement.OrgID); err != nil {
		return csilapi.DeleteOrgResponse{}, mapStoreErr(err, "organization not found")
	}
	s.recordAudit(ctx, replacement.OrgID, "organization.delete", "organization", organization.Name,
		models.JSONB{"replacement": replacement.Name})
	return csilapi.DeleteOrgResponse{Replacement: orgSummary(replacement, replacement)}, nil
}
