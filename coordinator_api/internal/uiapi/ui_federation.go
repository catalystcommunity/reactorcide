package uiapi

import (
	"context"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/federation"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// federationStore is the optional store surface of the federation
// operations (WP4, docs/federation.md).
type federationStore interface {
	CreateFederationIssuer(ctx context.Context, issuer *models.FederationIssuer) error
	UpdateFederationIssuer(ctx context.Context, issuer *models.FederationIssuer) error
	ListFederationIssuers(ctx context.Context, orgID string) ([]models.FederationIssuer, error)
	GetFederationIssuer(ctx context.Context, orgID, nameOrID string) (*models.FederationIssuer, error)
	DeleteFederationIssuer(ctx context.Context, orgID, issuerID string) error
	CreateFederationMapping(ctx context.Context, mapping *models.FederationMapping) error
	UpdateFederationMapping(ctx context.Context, mapping *models.FederationMapping) error
	ListFederationMappings(ctx context.Context, orgID, issuerID string) ([]models.FederationMapping, error)
	GetFederationMapping(ctx context.Context, orgID, issuerID, nameOrID string) (*models.FederationMapping, error)
	DeleteFederationMapping(ctx context.Context, orgID, mappingID string) error
}

// federationAdmin authorizes an org admin (owner decision D24) and returns
// the stores.
func (s *UiService) federationAdmin(ctx context.Context, orgID string) (context.Context, principalStore, federationStore, error) {
	ctx, ps, _, err := s.principalAdmin(ctx, orgID)
	if err != nil {
		return ctx, nil, nil, err
	}
	fs, ok := s.deps.Store.(federationStore)
	if !ok {
		return ctx, nil, nil, NewServiceError("unavailable", "federation is not available")
	}
	return ctx, ps, fs, nil
}

func issuerEntry(issuer *models.FederationIssuer) csilapi.FederationIssuerEntry {
	return csilapi.FederationIssuerEntry{IssuerId: issuer.IssuerID, Name: issuer.Name, IssuerUrl: issuer.IssuerURL,
		JwksUrl: issuer.JWKSURL, Audiences: append([]string{}, issuer.Audiences...), Algorithms: append([]string{}, issuer.Algorithms...),
		ClockSkewSeconds: int64(issuer.ClockSkewSeconds), Status: issuer.Status, CreatedAt: formatTime(issuer.CreatedAt)}
}

func mappingEntry(ctx context.Context, ps principalStore, mapping *models.FederationMapping) csilapi.FederationMappingEntry {
	entry := csilapi.FederationMappingEntry{MappingId: mapping.MappingID, IssuerId: mapping.IssuerID, Name: mapping.Name,
		PrincipalId: mapping.PrincipalID, Conditions: []csilapi.FederationConditionEntry{},
		Capabilities: append([]string{}, mapping.Capabilities...), TokenTtlSeconds: int64(mapping.TokenTTLSeconds),
		Status: mapping.Status, CreatedAt: formatTime(mapping.CreatedAt)}
	for _, condition := range mapping.Conditions {
		entry.Conditions = append(entry.Conditions, csilapi.FederationConditionEntry{Claim: condition.Claim, Op: condition.Op, Value: condition.Value})
	}
	if principal, err := ps.GetPrincipal(ctx, mapping.PrincipalID); err == nil && principal != nil {
		entry.PrincipalName = principal.Name
	}
	return entry
}

func federationStoreError(err error) error {
	switch {
	case errors.Is(err, store.ErrAlreadyExists):
		return NewServiceError("already_exists", "the name or the issuer URL is already in use")
	case errors.Is(err, store.ErrNotFound):
		return NewServiceError("not_found", "not found")
	}
	return NewServiceError("internal", "an internal error occurred")
}

// ListFederationIssuers lists the issuers and mappings of an org.
func (s *UiService) ListFederationIssuers(ctx context.Context, req csilapi.ListFederationIssuersRequest) (csilapi.ListFederationIssuersResponse, error) {
	ctx, ps, fs, err := s.federationAdmin(ctx, req.OrgId)
	if err != nil {
		return csilapi.ListFederationIssuersResponse{}, err
	}
	issuers, err := fs.ListFederationIssuers(ctx, req.OrgId)
	if err != nil {
		return csilapi.ListFederationIssuersResponse{}, federationStoreError(err)
	}
	mappings, err := fs.ListFederationMappings(ctx, req.OrgId, "")
	if err != nil {
		return csilapi.ListFederationIssuersResponse{}, federationStoreError(err)
	}
	out := csilapi.ListFederationIssuersResponse{Issuers: []csilapi.FederationIssuerEntry{}, Mappings: []csilapi.FederationMappingEntry{}}
	for i := range issuers {
		out.Issuers = append(out.Issuers, issuerEntry(&issuers[i]))
	}
	for i := range mappings {
		out.Mappings = append(out.Mappings, mappingEntry(ctx, ps, &mappings[i]))
	}
	return out, nil
}

// SaveFederationIssuer creates or updates an issuer of an org.
func (s *UiService) SaveFederationIssuer(ctx context.Context, req csilapi.SaveFederationIssuerRequest) (csilapi.SaveFederationIssuerResponse, error) {
	ctx, _, fs, err := s.federationAdmin(ctx, req.OrgId)
	if err != nil {
		return csilapi.SaveFederationIssuerResponse{}, err
	}
	issuer := &models.FederationIssuer{OrgID: req.OrgId, ClockSkewSeconds: federation.DefaultClockSkew}
	creating := req.IssuerId == nil || *req.IssuerId == ""
	if !creating {
		found, getErr := fs.GetFederationIssuer(ctx, req.OrgId, *req.IssuerId)
		if getErr != nil {
			return csilapi.SaveFederationIssuerResponse{}, NewServiceError("not_found", "issuer not found")
		}
		issuer = found
	} else {
		issuer.Name = req.Name
	}
	issuer.IssuerURL = req.IssuerUrl
	if req.JwksUrl != nil {
		issuer.JWKSURL = *req.JwksUrl
	}
	issuer.Audiences = req.Audiences
	if req.Algorithms != nil {
		issuer.Algorithms = req.Algorithms
	}
	if req.ClockSkewSeconds != nil {
		issuer.ClockSkewSeconds = int(*req.ClockSkewSeconds)
	}
	if req.Status != nil && *req.Status != "" {
		issuer.Status = *req.Status
	}
	if normErr := federation.NormalizeIssuer(issuer, config.AllowInsecureTransport); normErr != nil {
		return csilapi.SaveFederationIssuerResponse{}, NewServiceError("invalid_argument", normErr.Error())
	}
	action := "federation.issuer.update"
	if creating {
		action = "federation.issuer.create"
		err = fs.CreateFederationIssuer(ctx, issuer)
	} else {
		err = fs.UpdateFederationIssuer(ctx, issuer)
	}
	if err != nil {
		return csilapi.SaveFederationIssuerResponse{}, federationStoreError(err)
	}
	s.recordAudit(ctx, req.OrgId, action, "federation_issuer", issuer.IssuerID, models.JSONB{"name": issuer.Name,
		"issuer_url": issuer.IssuerURL, "audiences": []string(issuer.Audiences), "algorithms": []string(issuer.Algorithms),
		"status": issuer.Status})
	return csilapi.SaveFederationIssuerResponse{Issuer: issuerEntry(issuer)}, nil
}

// DeleteFederationIssuer removes an issuer and its mappings.
func (s *UiService) DeleteFederationIssuer(ctx context.Context, req csilapi.DeleteFederationIssuerRequest) (csilapi.DeleteFederationIssuerResponse, error) {
	ctx, _, fs, err := s.federationAdmin(ctx, req.OrgId)
	if err != nil {
		return csilapi.DeleteFederationIssuerResponse{}, err
	}
	issuer, err := fs.GetFederationIssuer(ctx, req.OrgId, req.IssuerId)
	if err != nil {
		return csilapi.DeleteFederationIssuerResponse{}, NewServiceError("not_found", "issuer not found")
	}
	if err := fs.DeleteFederationIssuer(ctx, req.OrgId, issuer.IssuerID); err != nil {
		return csilapi.DeleteFederationIssuerResponse{}, federationStoreError(err)
	}
	s.recordAudit(ctx, req.OrgId, "federation.issuer.delete", "federation_issuer", issuer.IssuerID,
		models.JSONB{"name": issuer.Name, "issuer_url": issuer.IssuerURL})
	return csilapi.DeleteFederationIssuerResponse{Deleted: true}, nil
}

// mappingLimit is the authority of the caller for mapping capabilities. An
// API credential gives only what it has and needs tokens:manage; a UI
// session of an org admin is not limited by a token.
func mappingLimit(ctx context.Context) (federation.CapabilityLimit, error) {
	credential := checkauth.GetPrincipalFromContext(ctx)
	if credential == nil {
		return federation.CapabilityLimit{All: true}, nil
	}
	if !credential.HasCapability(tokencaps.TokensManage) {
		return federation.CapabilityLimit{}, NewServiceError("forbidden", "a mapping issues tokens: tokens:manage is necessary")
	}
	return federation.CapabilityLimit{All: credential.AllCapabilities, Capabilities: credential.Capabilities}, nil
}

// SaveFederationMapping creates or updates a mapping of an issuer.
func (s *UiService) SaveFederationMapping(ctx context.Context, req csilapi.SaveFederationMappingRequest) (csilapi.SaveFederationMappingResponse, error) {
	ctx, ps, fs, err := s.federationAdmin(ctx, req.OrgId)
	if err != nil {
		return csilapi.SaveFederationMappingResponse{}, err
	}
	limit, err := mappingLimit(ctx)
	if err != nil {
		return csilapi.SaveFederationMappingResponse{}, err
	}
	issuer, err := fs.GetFederationIssuer(ctx, req.OrgId, req.IssuerId)
	if err != nil {
		return csilapi.SaveFederationMappingResponse{}, NewServiceError("not_found", "issuer not found")
	}
	principal, err := orgPrincipal(ctx, ps, req.OrgId, req.PrincipalId)
	if err != nil || principal.Kind != models.PrincipalKindService || principal.OrgID == nil || *principal.OrgID != req.OrgId {
		return csilapi.SaveFederationMappingResponse{}, NewServiceError("invalid_argument", "the principal must be a service principal of the organization")
	}
	mapping := &models.FederationMapping{IssuerID: issuer.IssuerID, OrgID: req.OrgId}
	creating := req.MappingId == nil || *req.MappingId == ""
	if !creating {
		found, getErr := fs.GetFederationMapping(ctx, req.OrgId, issuer.IssuerID, *req.MappingId)
		if getErr != nil {
			return csilapi.SaveFederationMappingResponse{}, NewServiceError("not_found", "mapping not found")
		}
		mapping = found
	} else {
		mapping.Name = req.Name
	}
	mapping.PrincipalID = principal.PrincipalID
	mapping.Conditions = make([]models.FederationCondition, 0, len(req.Conditions))
	for _, condition := range req.Conditions {
		mapping.Conditions = append(mapping.Conditions, models.FederationCondition{Claim: condition.Claim, Op: condition.Op, Value: condition.Value})
	}
	mapping.Capabilities = req.Capabilities
	if req.TokenTtlSeconds != nil {
		mapping.TokenTTLSeconds = int(*req.TokenTtlSeconds)
	}
	if req.Status != nil && *req.Status != "" {
		mapping.Status = *req.Status
	}
	if normErr := federation.NormalizeMapping(mapping, limit); normErr != nil {
		return csilapi.SaveFederationMappingResponse{}, NewServiceError("invalid_argument", normErr.Error())
	}
	action := "federation.mapping.update"
	if creating {
		action = "federation.mapping.create"
		err = fs.CreateFederationMapping(ctx, mapping)
	} else {
		err = fs.UpdateFederationMapping(ctx, mapping)
	}
	if err != nil {
		return csilapi.SaveFederationMappingResponse{}, federationStoreError(err)
	}
	conditions := make([]any, 0, len(mapping.Conditions))
	for _, c := range mapping.Conditions {
		conditions = append(conditions, map[string]any{"claim": c.Claim, "op": c.Op, "value": c.Value})
	}
	s.recordAudit(ctx, req.OrgId, action, "federation_mapping", mapping.MappingID, models.JSONB{"issuer": issuer.Name,
		"name": mapping.Name, "principal_id": mapping.PrincipalID, "conditions": conditions,
		"capabilities": []string(mapping.Capabilities), "token_ttl_seconds": mapping.TokenTTLSeconds, "status": mapping.Status})
	return csilapi.SaveFederationMappingResponse{Mapping: mappingEntry(ctx, ps, mapping)}, nil
}

// DeleteFederationMapping removes a mapping.
func (s *UiService) DeleteFederationMapping(ctx context.Context, req csilapi.DeleteFederationMappingRequest) (csilapi.DeleteFederationMappingResponse, error) {
	ctx, _, fs, err := s.federationAdmin(ctx, req.OrgId)
	if err != nil {
		return csilapi.DeleteFederationMappingResponse{}, err
	}
	if _, err := mappingLimit(ctx); err != nil {
		return csilapi.DeleteFederationMappingResponse{}, err
	}
	if err := requireNonEmpty("mapping_id", req.MappingId, 64); err != nil {
		return csilapi.DeleteFederationMappingResponse{}, err
	}
	if err := fs.DeleteFederationMapping(ctx, req.OrgId, req.MappingId); err != nil {
		return csilapi.DeleteFederationMappingResponse{}, federationStoreError(err)
	}
	s.recordAudit(ctx, req.OrgId, "federation.mapping.delete", "federation_mapping", req.MappingId, nil)
	return csilapi.DeleteFederationMappingResponse{Deleted: true}, nil
}
