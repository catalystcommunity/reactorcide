package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// Stubs of the organization settings, OIDC, alert rule, and diagnosis
// delete operations (ui_org_settings.go, ui_oidc.go, ui_alert_rules.go,
// ui_diagnosis.go have the real operations).

func (StubUi) SetShedActions(ctx context.Context, req csilapi.SetShedActionsRequest) (csilapi.SetShedLevelResponse, error) {
	return csilapi.SetShedLevelResponse{}, ErrUnimplemented("ReactorcideUi/set-shed-actions")
}

func (StubUi) GetOrgReportingSettings(ctx context.Context, req csilapi.GetOrgReportingSettingsRequest) (csilapi.GetOrgReportingSettingsResponse, error) {
	return csilapi.GetOrgReportingSettingsResponse{}, ErrUnimplemented("ReactorcideUi/get-org-reporting-settings")
}

func (StubUi) PutOrgReportingSettings(ctx context.Context, req csilapi.PutOrgReportingSettingsRequest) (csilapi.GetOrgReportingSettingsResponse, error) {
	return csilapi.GetOrgReportingSettingsResponse{}, ErrUnimplemented("ReactorcideUi/put-org-reporting-settings")
}

func (StubUi) GetRequiredSetOrgSettings(ctx context.Context, req csilapi.GetRequiredSetOrgSettingsRequest) (csilapi.GetRequiredSetOrgSettingsResponse, error) {
	return csilapi.GetRequiredSetOrgSettingsResponse{}, ErrUnimplemented("ReactorcideUi/get-required-set-org-settings")
}

func (StubUi) PutRequiredSetOrgSettings(ctx context.Context, req csilapi.PutRequiredSetOrgSettingsRequest) (csilapi.GetRequiredSetOrgSettingsResponse, error) {
	return csilapi.GetRequiredSetOrgSettingsResponse{}, ErrUnimplemented("ReactorcideUi/put-required-set-org-settings")
}

func (StubUi) GetVcsOwnerAllowlist(ctx context.Context, req csilapi.GetVcsOwnerAllowlistRequest) (csilapi.VcsOwnerAllowlistResponse, error) {
	return csilapi.VcsOwnerAllowlistResponse{}, ErrUnimplemented("ReactorcideUi/get-vcs-owner-allowlist")
}

func (StubUi) PutVcsOwnerAllowlist(ctx context.Context, req csilapi.PutVcsOwnerAllowlistRequest) (csilapi.VcsOwnerAllowlistResponse, error) {
	return csilapi.VcsOwnerAllowlistResponse{}, ErrUnimplemented("ReactorcideUi/put-vcs-owner-allowlist")
}

func (StubUi) GetOidcSettings(ctx context.Context, req csilapi.GetOidcSettingsRequest) (csilapi.OidcSettingsResponse, error) {
	return csilapi.OidcSettingsResponse{}, ErrUnimplemented("ReactorcideUi/get-oidc-settings")
}

func (StubUi) PutOidcSettings(ctx context.Context, req csilapi.PutOidcSettingsRequest) (csilapi.OidcSettingsResponse, error) {
	return csilapi.OidcSettingsResponse{}, ErrUnimplemented("ReactorcideUi/put-oidc-settings")
}

func (StubUi) ListOidcKeys(ctx context.Context, req csilapi.ListOidcKeysRequest) (csilapi.ListOidcKeysResponse, error) {
	return csilapi.ListOidcKeysResponse{}, ErrUnimplemented("ReactorcideUi/list-oidc-keys")
}

func (StubUi) RotateOidcKey(ctx context.Context, req csilapi.RotateOidcKeyRequest) (csilapi.ListOidcKeysResponse, error) {
	return csilapi.ListOidcKeysResponse{}, ErrUnimplemented("ReactorcideUi/rotate-oidc-key")
}

func (StubUi) RetireOidcKey(ctx context.Context, req csilapi.RetireOidcKeyRequest) (csilapi.ListOidcKeysResponse, error) {
	return csilapi.ListOidcKeysResponse{}, ErrUnimplemented("ReactorcideUi/retire-oidc-key")
}

func (StubUi) ListAlertRules(ctx context.Context, req csilapi.ListAlertRulesRequest) (csilapi.ListAlertRulesResponse, error) {
	return csilapi.ListAlertRulesResponse{}, ErrUnimplemented("ReactorcideUi/list-alert-rules")
}

func (StubUi) CreateAlertRule(ctx context.Context, req csilapi.CreateAlertRuleRequest) (csilapi.AlertRuleResponse, error) {
	return csilapi.AlertRuleResponse{}, ErrUnimplemented("ReactorcideUi/create-alert-rule")
}

func (StubUi) UpdateAlertRule(ctx context.Context, req csilapi.UpdateAlertRuleRequest) (csilapi.AlertRuleResponse, error) {
	return csilapi.AlertRuleResponse{}, ErrUnimplemented("ReactorcideUi/update-alert-rule")
}

func (StubUi) DeleteAlertRule(ctx context.Context, req csilapi.DeleteAlertRuleRequest) (csilapi.DeleteAlertRuleResponse, error) {
	return csilapi.DeleteAlertRuleResponse{}, ErrUnimplemented("ReactorcideUi/delete-alert-rule")
}

func (StubUi) DeleteDiagnosis(ctx context.Context, req csilapi.DeleteDiagnosisRequest) (csilapi.DeleteDiagnosisResponse, error) {
	return csilapi.DeleteDiagnosisResponse{}, ErrUnimplemented("ReactorcideUi/delete-diagnosis")
}
