package uiapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// principalStore is the optional store surface of the principal operations.
// A store without it (some test fakes) answers "unavailable".
type principalStore interface {
	principals.Store
	ListPrincipals(ctx context.Context, orgID, kind string) ([]models.Principal, error)
	GetPrincipalsByIDs(ctx context.Context, principalIDs []string) (map[string]models.Principal, error)
	CreateServicePrincipal(ctx context.Context, principal *models.Principal) error
	SetPrincipalStatus(ctx context.Context, principalID, status string) error
	LinkPrincipalIdentity(ctx context.Context, identity *models.PrincipalIdentity) error
	UnlinkPrincipalIdentity(ctx context.Context, identityType, value string) error
	GetPrincipalIdentity(ctx context.Context, identityType, value string) (*models.PrincipalIdentity, error)
	ListGroupsForPrincipal(ctx context.Context, principalID string) ([]models.Group, error)
	AddGroupMemberPrincipal(ctx context.Context, groupID, principalID string) error
	RemoveGroupMemberPrincipal(ctx context.Context, groupID, principalID string) error
	ListAPITokensForPrincipal(ctx context.Context, principalID string) ([]models.APIToken, error)
	CreateAPIToken(ctx context.Context, token *models.APIToken) error
	GetAPITokenByID(ctx context.Context, tokenID string) (*models.APIToken, error)
	RevokeAPIToken(ctx context.Context, tokenID string) error
}

// principalAdmin authorizes an org admin (session, user token, service
// principal token, or the admin token) and returns the principal store. A
// token needs organizations:manage for the org, as in the REST routes, and
// each of extra (for example tokens:manage to issue or revoke a token).
func (s *UiService) principalAdmin(ctx context.Context, orgID string, extra ...string) (context.Context, principalStore, *models.Organization, error) {
	return s.principalAdminFor(ctx, orgID, []string{tokencaps.OrganizationsManage}, extra...)
}

// principalAdminRead is principalAdmin for a read: organizations:read or
// organizations:manage.
func (s *UiService) principalAdminRead(ctx context.Context, orgID string) (context.Context, principalStore, *models.Organization, error) {
	return s.principalAdminFor(ctx, orgID, orgReadCapabilities)
}

func (s *UiService) principalAdminFor(ctx context.Context, orgID string, base []string, extra ...string) (context.Context, principalStore, *models.Organization, error) {
	ctx, id, _, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return ctx, nil, nil, authErr
	}
	if err := requireNonEmpty("org_id", orgID, 64); err != nil {
		return ctx, nil, nil, err
	}
	if err := s.requireOrgAdminGrant(ctx, id, orgID, base...); err != nil {
		return ctx, nil, nil, err
	}
	for _, capability := range extra {
		if err := requireTokenGrant(id, &orgID, capability); err != nil {
			return ctx, nil, nil, err
		}
	}
	ps, ok := s.deps.Store.(principalStore)
	if !ok {
		return ctx, nil, nil, NewServiceError("unavailable", "principals are not available")
	}
	org := &models.Organization{OrgID: orgID}
	if orgs, ok := s.deps.Store.(interface {
		GetOrganizationByID(ctx context.Context, orgID string) (*models.Organization, error)
	}); ok {
		if found, err := orgs.GetOrganizationByID(ctx, orgID); err == nil && found != nil {
			org = found
		}
	}
	return principals.WithActor(ctx, principals.ActorOf(ctx)), ps, org, nil
}

// orgPrincipal loads a principal that belongs to the org.
func orgPrincipal(ctx context.Context, ps principalStore, orgID, principalID string) (*models.Principal, error) {
	if err := requireNonEmpty("principal_id", principalID, 64); err != nil {
		return nil, err
	}
	principal, err := ps.GetPrincipal(ctx, principalID)
	if err != nil || !principals.InOrg(ctx, ps, orgID, principal) {
		return nil, NewServiceError("not_found", "principal not found")
	}
	return principal, nil
}

func principalToSummary(p *models.Principal) csilapi.PrincipalSummary {
	return csilapi.PrincipalSummary{PrincipalId: p.PrincipalID, Kind: p.Kind, Name: p.Name, DisplayName: p.Label(),
		Status: p.Status, OrgId: p.OrgID, CreatedAt: formatTime(p.CreatedAt)}
}

func principalTokenEntry(token *models.APIToken) csilapi.PrincipalTokenEntry {
	capabilities := []string{tokencaps.All}
	if !token.AllCapabilities {
		capabilities = append([]string{}, token.Capabilities...)
	}
	return csilapi.PrincipalTokenEntry{TokenId: token.TokenID, Name: token.Name, Capabilities: capabilities,
		CreatedAt: formatTime(token.CreatedAt), ExpiresAt: formatTimePtr(token.ExpiresAt),
		LastUsedAt: formatTimePtr(token.LastUsedAt), IsActive: token.IsValid()}
}

func principalDetail(ctx context.Context, ps principalStore, org *models.Organization, p *models.Principal) csilapi.PrincipalDetail {
	detail := csilapi.PrincipalDetail{Principal: principalToSummary(p), Identities: []csilapi.PrincipalIdentityEntry{},
		Groups: []string{}, Tokens: []csilapi.PrincipalTokenEntry{}, Subjects: []string{}}
	identities, _ := ps.ListPrincipalIdentities(ctx, p.PrincipalID)
	for _, identity := range identities {
		detail.Identities = append(detail.Identities, csilapi.PrincipalIdentityEntry{IdentityType: identity.IdentityType,
			IdentityValue: identity.IdentityValue, VerifiedBy: identity.VerifiedBy, CreatedAt: formatTime(identity.CreatedAt)})
	}
	if groups, err := ps.ListGroupsForPrincipal(ctx, p.PrincipalID); err == nil {
		for _, group := range groups {
			if group.OrgID == org.OrgID {
				detail.Groups = append(detail.Groups, group.Name)
			}
		}
	}
	if p.Kind == models.PrincipalKindService {
		if tokens, err := ps.ListAPITokensForPrincipal(ctx, p.PrincipalID); err == nil {
			for i := range tokens {
				detail.Tokens = append(detail.Tokens, principalTokenEntry(&tokens[i]))
			}
		}
	}
	detail.Subjects = append(detail.Subjects, principals.Subjects(&principals.Resolution{Principal: *p, Identities: identities},
		principals.SubjectContext{OrgID: org.OrgID, OrgName: org.Name})...)
	return detail
}

// ListPrincipals lists the principals of an org (org admin).
func (s *UiService) ListPrincipals(ctx context.Context, req csilapi.ListPrincipalsRequest) (csilapi.ListPrincipalsResponse, error) {
	ctx, ps, _, err := s.principalAdminRead(ctx, req.OrgId)
	if err != nil {
		return csilapi.ListPrincipalsResponse{}, err
	}
	rows, listErr := ps.ListPrincipals(ctx, req.OrgId, derefOr(req.Kind, ""))
	if listErr != nil {
		return csilapi.ListPrincipalsResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.PrincipalSummary, 0, len(rows))
	for i := range rows {
		out = append(out, principalToSummary(&rows[i]))
	}
	return csilapi.ListPrincipalsResponse{Principals: out}, nil
}

// GetPrincipal returns one principal of the org with its identities, groups,
// tokens, and subjects (org admin).
func (s *UiService) GetPrincipal(ctx context.Context, req csilapi.GetPrincipalRequest) (csilapi.GetPrincipalResponse, error) {
	ctx, ps, org, err := s.principalAdminRead(ctx, req.OrgId)
	if err != nil {
		return csilapi.GetPrincipalResponse{}, err
	}
	principal, err := orgPrincipal(ctx, ps, req.OrgId, req.PrincipalId)
	if err != nil {
		return csilapi.GetPrincipalResponse{}, err
	}
	return csilapi.GetPrincipalResponse{Principal: principalDetail(ctx, ps, org, principal)}, nil
}

// CreateServicePrincipal creates a service principal of the org (org admin).
func (s *UiService) CreateServicePrincipal(ctx context.Context, req csilapi.CreateServicePrincipalRequest) (csilapi.CreateServicePrincipalResponse, error) {
	ctx, ps, org, err := s.principalAdmin(ctx, req.OrgId)
	if err != nil {
		return csilapi.CreateServicePrincipalResponse{}, err
	}
	name := strings.TrimSpace(req.Name)
	if !models.ValidServicePrincipalName(name) {
		return csilapi.CreateServicePrincipalResponse{}, NewServiceError("invalid_argument", "name must match [a-z0-9][a-z0-9-]{0,62}")
	}
	orgID := req.OrgId
	principal := &models.Principal{OrgID: &orgID, Name: name, DisplayName: strings.TrimSpace(derefOr(req.DisplayName, ""))}
	if createErr := ps.CreateServicePrincipal(ctx, principal); createErr != nil {
		if errors.Is(createErr, store.ErrAlreadyExists) {
			return csilapi.CreateServicePrincipalResponse{}, NewServiceError("already_exists", "a service principal with this name exists")
		}
		return csilapi.CreateServicePrincipalResponse{}, NewServiceError("internal", "failed to create the service principal")
	}
	s.recordAudit(ctx, orgID, "principal.create", "principal", principal.PrincipalID, models.JSONB{"kind": principal.Kind, "name": name})
	return csilapi.CreateServicePrincipalResponse{Principal: principalDetail(ctx, ps, org, principal)}, nil
}

// SetPrincipalStatus enables or disables a service principal (org admin).
func (s *UiService) SetPrincipalStatus(ctx context.Context, req csilapi.SetPrincipalStatusRequest) (csilapi.SetPrincipalStatusResponse, error) {
	ctx, ps, org, err := s.principalAdmin(ctx, req.OrgId)
	if err != nil {
		return csilapi.SetPrincipalStatusResponse{}, err
	}
	principal, err := orgPrincipal(ctx, ps, req.OrgId, req.PrincipalId)
	if err != nil {
		return csilapi.SetPrincipalStatusResponse{}, err
	}
	if principal.Kind != models.PrincipalKindService {
		return csilapi.SetPrincipalStatusResponse{}, NewServiceError("invalid_argument", "only a service principal has a status here; a human follows its user")
	}
	if req.Status != models.PrincipalStatusActive && req.Status != models.PrincipalStatusDisabled {
		return csilapi.SetPrincipalStatusResponse{}, NewServiceError("invalid_argument", "status must be active or disabled")
	}
	if setErr := ps.SetPrincipalStatus(ctx, principal.PrincipalID, req.Status); setErr != nil {
		return csilapi.SetPrincipalStatusResponse{}, mapStoreErr(setErr, "principal not found")
	}
	principal.Status = req.Status
	s.recordAudit(ctx, req.OrgId, "principal.status", "principal", principal.PrincipalID, models.JSONB{"status": req.Status})
	return csilapi.SetPrincipalStatusResponse{Principal: principalDetail(ctx, ps, org, principal)}, nil
}

// LinkPrincipalIdentity links an identity to a principal of the org. An org
// admin may link to a service principal of the org; a human principal needs
// a global admin. A login that only a webhook saw is merged into a human.
func (s *UiService) LinkPrincipalIdentity(ctx context.Context, req csilapi.PrincipalIdentityRequest) (csilapi.PrincipalIdentityResponse, error) {
	ctx, ps, org, err := s.principalAdmin(ctx, req.OrgId)
	if err != nil {
		return csilapi.PrincipalIdentityResponse{}, err
	}
	principal, err := orgPrincipal(ctx, ps, req.OrgId, req.PrincipalId)
	if err != nil {
		return csilapi.PrincipalIdentityResponse{}, err
	}
	if req.IdentityType == models.IdentityTypeFederated {
		return csilapi.PrincipalIdentityResponse{}, NewServiceError("invalid_argument", "a federated subject comes from a registered issuer")
	}
	// The subjects of a human principal apply in every organization, so an
	// org admin may link identities only to service principals of the org.
	// A human needs a global admin (or the admin token), or self-proof
	// through a LinkKeys login.
	if principal.Kind != models.PrincipalKindService {
		_, id, _, authErr := s.deps.requireManagementIdentity(ctx)
		if authErr != nil {
			return csilapi.PrincipalIdentityResponse{}, authErr
		}
		if adminErr := s.requireGlobalAdminGrant(ctx, id); adminErr != nil {
			return csilapi.PrincipalIdentityResponse{}, adminErr
		}
	}
	identity := &models.PrincipalIdentity{IdentityType: strings.TrimSpace(req.IdentityType), IdentityValue: req.IdentityValue,
		PrincipalID: principal.PrincipalID, VerifiedBy: models.IdentityVerifiedByAdmin}
	if linkErr := ps.LinkPrincipalIdentity(ctx, identity); linkErr != nil {
		switch {
		case errors.Is(linkErr, store.ErrConflict):
			return csilapi.PrincipalIdentityResponse{}, NewServiceError("conflict", "the identity belongs to another principal")
		case errors.Is(linkErr, store.ErrInvalidInput):
			return csilapi.PrincipalIdentityResponse{}, NewServiceError("invalid_argument", "the identity type or value is not valid here")
		}
		return csilapi.PrincipalIdentityResponse{}, mapStoreErr(linkErr, "principal not found")
	}
	s.recordAudit(ctx, req.OrgId, "principal.identity.link", "principal", principal.PrincipalID,
		models.JSONB{"identity_type": identity.IdentityType, "identity_value": identity.IdentityValue})
	return csilapi.PrincipalIdentityResponse{Principal: principalDetail(ctx, ps, org, principal)}, nil
}

// UnlinkPrincipalIdentity removes an identity of a principal of the org.
func (s *UiService) UnlinkPrincipalIdentity(ctx context.Context, req csilapi.PrincipalIdentityRequest) (csilapi.PrincipalIdentityResponse, error) {
	ctx, ps, org, err := s.principalAdmin(ctx, req.OrgId)
	if err != nil {
		return csilapi.PrincipalIdentityResponse{}, err
	}
	principal, err := orgPrincipal(ctx, ps, req.OrgId, req.PrincipalId)
	if err != nil {
		return csilapi.PrincipalIdentityResponse{}, err
	}
	existing, getErr := ps.GetPrincipalIdentity(ctx, req.IdentityType, req.IdentityValue)
	if getErr != nil || existing.PrincipalID != principal.PrincipalID {
		return csilapi.PrincipalIdentityResponse{}, NewServiceError("not_found", "identity not found")
	}
	if unlinkErr := ps.UnlinkPrincipalIdentity(ctx, existing.IdentityType, existing.IdentityValue); unlinkErr != nil {
		if errors.Is(unlinkErr, store.ErrInvalidInput) {
			return csilapi.PrincipalIdentityResponse{}, NewServiceError("invalid_argument", "user and token identities cannot be removed")
		}
		return csilapi.PrincipalIdentityResponse{}, mapStoreErr(unlinkErr, "identity not found")
	}
	s.recordAudit(ctx, req.OrgId, "principal.identity.unlink", "principal", principal.PrincipalID,
		models.JSONB{"identity_type": existing.IdentityType, "identity_value": existing.IdentityValue})
	return csilapi.PrincipalIdentityResponse{Principal: principalDetail(ctx, ps, org, principal)}, nil
}

// SetPrincipalGroup adds a principal of the org to a group of the org, or
// removes it.
func (s *UiService) SetPrincipalGroup(ctx context.Context, req csilapi.SetPrincipalGroupRequest) (csilapi.SetPrincipalGroupResponse, error) {
	ctx, ps, org, err := s.principalAdmin(ctx, req.OrgId)
	if err != nil {
		return csilapi.SetPrincipalGroupResponse{}, err
	}
	principal, err := orgPrincipal(ctx, ps, req.OrgId, req.PrincipalId)
	if err != nil {
		return csilapi.SetPrincipalGroupResponse{}, err
	}
	group, groupErr := s.deps.Store.GetGroupByID(ctx, req.GroupId)
	if groupErr != nil || group.OrgID != req.OrgId {
		return csilapi.SetPrincipalGroupResponse{}, NewServiceError("not_found", "group not found")
	}
	action := "group.member.add"
	if req.Member {
		err = ps.AddGroupMemberPrincipal(ctx, group.GroupID, principal.PrincipalID)
	} else {
		action = "group.member.remove"
		err = ps.RemoveGroupMemberPrincipal(ctx, group.GroupID, principal.PrincipalID)
		if errors.Is(err, store.ErrNotFound) {
			err = nil
		}
	}
	if err != nil {
		return csilapi.SetPrincipalGroupResponse{}, mapStoreErr(err, "group member not found")
	}
	s.recordAudit(ctx, req.OrgId, action, "group", group.GroupID, models.JSONB{"principal_id": principal.PrincipalID})
	return csilapi.SetPrincipalGroupResponse{Principal: principalDetail(ctx, ps, org, principal)}, nil
}

// IssuePrincipalToken issues a token for a service principal of the org. Its
// authority is its capabilities intersected with the roles and groups of the
// principal. A token caller cannot give capabilities it does not have.
func (s *UiService) IssuePrincipalToken(ctx context.Context, req csilapi.IssuePrincipalTokenRequest) (csilapi.IssuePrincipalTokenResponse, error) {
	ctx, ps, _, err := s.principalAdmin(ctx, req.OrgId, tokencaps.TokensManage)
	if err != nil {
		return csilapi.IssuePrincipalTokenResponse{}, err
	}
	principal, err := orgPrincipal(ctx, ps, req.OrgId, req.PrincipalId)
	if err != nil {
		return csilapi.IssuePrincipalTokenResponse{}, err
	}
	if principal.Kind != models.PrincipalKindService || !principal.IsActive() {
		return csilapi.IssuePrincipalTokenResponse{}, NewServiceError("invalid_argument", "tokens are issued only for an active service principal")
	}
	requested, capErr := tokencaps.New(req.Capabilities...)
	if capErr != nil {
		return csilapi.IssuePrincipalTokenResponse{}, NewServiceError("invalid_argument", capErr.Error())
	}
	allCapabilities := len(req.Capabilities) == 0
	if credential := checkauth.GetPrincipalFromContext(ctx); credential != nil {
		if !credential.HasCapability(tokencaps.TokensManage) || (allCapabilities && !credential.AllCapabilities) ||
			(!credential.AllCapabilities && !requested.IsSubsetOf(credential.Capabilities)) {
			return csilapi.IssuePrincipalTokenResponse{}, NewServiceError("forbidden", "the token cannot give these capabilities")
		}
	}
	// A no-login instance gives a token with no capability list all
	// capabilities and all organizations. Only a global admin can issue it.
	noLoginAdmin := allCapabilities && config.NoLoginMode()
	if noLoginAdmin {
		_, id, _, authErr := s.deps.requireManagementIdentity(ctx)
		if authErr != nil {
			return csilapi.IssuePrincipalTokenResponse{}, authErr
		}
		if grantErr := s.requireGlobalAdminGrant(ctx, id, tokencaps.OrganizationsManage); grantErr != nil {
			return csilapi.IssuePrincipalTokenResponse{}, grantErr
		}
	}
	var expiresAt *time.Time
	if req.ExpiresAt != nil && strings.TrimSpace(*req.ExpiresAt) != "" {
		parsed, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(*req.ExpiresAt))
		if parseErr != nil {
			return csilapi.IssuePrincipalTokenResponse{}, NewServiceError("invalid_argument", "expires_at must be RFC 3339")
		}
		expiresAt = &parsed
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = principal.Name
	}
	value, genErr := generateTokenValue()
	if genErr != nil {
		return csilapi.IssuePrincipalTokenResponse{}, NewServiceError("internal", "failed to generate a token")
	}
	orgID := req.OrgId
	principalID := principal.PrincipalID
	token := checkauth.NewPrincipalToken(value, name, expiresAt, orgID, principalID, requested, allCapabilities, noLoginAdmin)
	if createErr := ps.CreateAPIToken(ctx, token); createErr != nil {
		return csilapi.IssuePrincipalTokenResponse{}, NewServiceError("internal", "failed to create the token")
	}
	s.recordAudit(ctx, orgID, "token.create", "api_token", token.TokenID, models.JSONB{"subject_type": token.SubjectType,
		"principal_id": principalID, "all_capabilities": allCapabilities, "all_organizations": token.AllOrganizations,
		"capabilities": token.Capabilities, "intersect_roles": token.IntersectRoles})
	return csilapi.IssuePrincipalTokenResponse{Token: value, Info: principalTokenEntry(token)}, nil
}

// RevokePrincipalToken revokes a token of a service principal of the org.
func (s *UiService) RevokePrincipalToken(ctx context.Context, req csilapi.RevokePrincipalTokenRequest) (csilapi.RevokePrincipalTokenResponse, error) {
	ctx, ps, _, err := s.principalAdmin(ctx, req.OrgId, tokencaps.TokensManage)
	if err != nil {
		return csilapi.RevokePrincipalTokenResponse{}, err
	}
	principal, err := orgPrincipal(ctx, ps, req.OrgId, req.PrincipalId)
	if err != nil {
		return csilapi.RevokePrincipalTokenResponse{}, err
	}
	token, getErr := ps.GetAPITokenByID(ctx, req.TokenId)
	if getErr != nil || token.PrincipalID == nil || *token.PrincipalID != principal.PrincipalID {
		return csilapi.RevokePrincipalTokenResponse{}, NewServiceError("not_found", "token not found")
	}
	if revokeErr := ps.RevokeAPIToken(ctx, token.TokenID); revokeErr != nil {
		return csilapi.RevokePrincipalTokenResponse{}, mapStoreErr(revokeErr, "token not found")
	}
	s.recordAudit(ctx, req.OrgId, "token.revoke", "api_token", token.TokenID, models.JSONB{"principal_id": principal.PrincipalID})
	return csilapi.RevokePrincipalTokenResponse{Revoked: true}, nil
}

// ResolvePrincipal maps one identity to its principal and all linked
// identities. The principal must belong to the org (org admin).
func (s *UiService) ResolvePrincipal(ctx context.Context, req csilapi.ResolvePrincipalRequest) (csilapi.ResolvePrincipalResponse, error) {
	ctx, ps, org, err := s.principalAdminRead(ctx, req.OrgId)
	if err != nil {
		return csilapi.ResolvePrincipalResponse{}, err
	}
	resolver := principals.NewResolver(ps, 0)
	var resolution *principals.Resolution
	var resolveErr error
	if req.IdentityType == "principal" {
		resolution, resolveErr = resolver.ResolveID(ctx, strings.TrimSpace(req.IdentityValue))
	} else {
		resolution, resolveErr = resolver.Resolve(ctx, strings.TrimSpace(req.IdentityType), req.IdentityValue)
	}
	if resolveErr != nil || !principals.InOrg(ctx, ps, req.OrgId, &resolution.Principal) {
		return csilapi.ResolvePrincipalResponse{}, NewServiceError("not_found", "no principal of the org has this identity")
	}
	return csilapi.ResolvePrincipalResponse{Principal: principalDetail(ctx, ps, org, &resolution.Principal), Identity: resolution.Identity}, nil
}

// generateTokenValue returns a new random API token value (64 hex
// characters), the same form as the REST token routes.
func generateTokenValue() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
