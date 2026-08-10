package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerauth"
)

func poolToSummary(p *models.WorkerPool) csilapi.WorkerPoolSummary {
	out := csilapi.WorkerPoolSummary{
		PoolId:    p.PoolID,
		OrgId:     p.OrgID,
		Name:      p.Name,
		CreatedAt: formatTime(p.CreatedAt),
		UpdatedAt: formatTime(p.UpdatedAt),
	}
	if p.Description != "" {
		d := p.Description
		out.Description = &d
	}
	return out
}

// enrollmentTokenToSummary renders metadata only -- SECURITY: never the raw
// token or its hash. The model's TokenHash field is tagged json:"-" for any
// accidental direct serialization, but this DTO doesn't even read that
// field, so the hash cannot leak by construction here either.
func enrollmentTokenToSummary(t *models.PoolEnrollmentToken) csilapi.EnrollmentTokenSummary {
	out := csilapi.EnrollmentTokenSummary{
		TokenId:       t.TokenID,
		PoolId:        t.PoolID,
		IsActive:      t.IsActive,
		LastUsedAt:    formatTimePtr(t.LastUsedAt),
		CreatedAt:     formatTime(t.CreatedAt),
		DeactivatedAt: formatTimePtr(t.DeactivatedAt),
	}
	if t.Name != "" {
		n := t.Name
		out.Name = &n
	}
	return out
}

// ListPools requires org admin (of org_id) or global admin when org_id is
// given; global admin only when org_id is omitted (listing across every
// org, including global pools).
func (s *UiService) ListPools(ctx context.Context, req csilapi.ListPoolsRequest) (csilapi.ListPoolsResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.ListPoolsResponse{}, authErr
	}
	if err := s.requireManageWorkers(ctx, id, req.OrgId); err != nil {
		return csilapi.ListPoolsResponse{}, err
	}

	pools, err := s.deps.Store.ListWorkerPools(ctx, req.OrgId)
	if err != nil {
		return csilapi.ListPoolsResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.WorkerPoolSummary, len(pools))
	for i := range pools {
		out[i] = poolToSummary(&pools[i])
	}
	return csilapi.ListPoolsResponse{Pools: out}, nil
}

// CreatePool creates a worker pool, optionally scoped to org_id (a global
// pool when omitted). Requires org admin (of org_id) or global admin when
// org_id is given; global admin only when creating a global pool.
func (s *UiService) CreatePool(ctx context.Context, req csilapi.CreatePoolRequest) (csilapi.CreatePoolResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.CreatePoolResponse{}, authErr
	}
	if err := requireNonEmpty("name", req.Name, maxNameLength); err != nil {
		return csilapi.CreatePoolResponse{}, err
	}
	if err := s.requireManageWorkers(ctx, id, req.OrgId); err != nil {
		return csilapi.CreatePoolResponse{}, err
	}

	pool := &models.WorkerPool{OrgID: req.OrgId, Name: req.Name, Description: derefOr(req.Description, "")}
	if err := s.deps.Store.CreateWorkerPool(ctx, pool); err != nil {
		return csilapi.CreatePoolResponse{}, NewServiceError("internal", "failed to create worker pool")
	}
	s.recordAudit(ctx, derefOr(pool.OrgID, ""), "worker_pool.create", "worker_pool", pool.PoolID, models.JSONB{"name": pool.Name, "global": pool.OrgID == nil})
	return csilapi.CreatePoolResponse{Pool: poolToSummary(pool)}, nil
}

// UpdatePool updates a pool's mutable name/description. Requires org admin
// (of the pool's org) or global admin.
func (s *UiService) UpdatePool(ctx context.Context, req csilapi.UpdatePoolRequest) (csilapi.UpdatePoolResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.UpdatePoolResponse{}, authErr
	}
	if err := requireNonEmpty("pool_id", req.PoolId, 64); err != nil {
		return csilapi.UpdatePoolResponse{}, err
	}

	pool, err := s.deps.Store.GetWorkerPoolByID(ctx, req.PoolId)
	if err != nil {
		return csilapi.UpdatePoolResponse{}, mapStoreErr(err, "pool not found")
	}
	if err := s.requireManageWorkers(ctx, id, pool.OrgID); err != nil {
		return csilapi.UpdatePoolResponse{}, err
	}

	if req.Name != nil {
		if err := requireNonEmpty("name", *req.Name, maxNameLength); err != nil {
			return csilapi.UpdatePoolResponse{}, err
		}
		pool.Name = *req.Name
	}
	if req.Description != nil {
		pool.Description = *req.Description
	}
	if err := s.deps.Store.UpdateWorkerPool(ctx, pool.PoolID, pool.Name, pool.Description); err != nil {
		return csilapi.UpdatePoolResponse{}, mapStoreErr(err, "pool not found")
	}
	s.recordAudit(ctx, derefOr(pool.OrgID, ""), "worker_pool.update", "worker_pool", pool.PoolID, models.JSONB{"name": pool.Name})
	return csilapi.UpdatePoolResponse{Pool: poolToSummary(pool)}, nil
}

// DeletePool deletes a worker pool. Requires org admin (of the pool's org)
// or global admin. A pool with existing workers fails at the store layer
// (foreign key violation, DeleteWorkerPool's documented contract) -- this op
// does not orchestrate deleting/reassigning those workers first.
func (s *UiService) DeletePool(ctx context.Context, req csilapi.DeletePoolRequest) (csilapi.DeletePoolResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.DeletePoolResponse{}, authErr
	}
	if err := requireNonEmpty("pool_id", req.PoolId, 64); err != nil {
		return csilapi.DeletePoolResponse{}, err
	}

	pool, err := s.deps.Store.GetWorkerPoolByID(ctx, req.PoolId)
	if err != nil {
		return csilapi.DeletePoolResponse{}, mapStoreErr(err, "pool not found")
	}
	if err := s.requireManageWorkers(ctx, id, pool.OrgID); err != nil {
		return csilapi.DeletePoolResponse{}, err
	}

	if err := s.deps.Store.DeleteWorkerPool(ctx, req.PoolId); err != nil {
		return csilapi.DeletePoolResponse{}, mapStoreErr(err, "pool not found or still has workers")
	}
	s.recordAudit(ctx, derefOr(pool.OrgID, ""), "worker_pool.delete", "worker_pool", pool.PoolID, models.JSONB{"name": pool.Name})
	return csilapi.DeletePoolResponse{Deleted: true}, nil
}

// CreateEnrollmentToken mints a new enrollment token for a pool. SECURITY:
// the raw token is returned here exactly once, in this response's `token`
// field -- it is stored hash-only (internal/workerauth.GenerateEnrollmentToken
// / postgres_store.CreatePoolEnrollmentToken) and NEVER returned by any other
// op, including list-enrollment-tokens, and never logged. Requires org admin
// (of the pool's org) or global admin.
func (s *UiService) CreateEnrollmentToken(ctx context.Context, req csilapi.CreateEnrollmentTokenRequest) (csilapi.CreateEnrollmentTokenResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.CreateEnrollmentTokenResponse{}, authErr
	}
	if err := requireNonEmpty("pool_id", req.PoolId, 64); err != nil {
		return csilapi.CreateEnrollmentTokenResponse{}, err
	}

	pool, err := s.deps.Store.GetWorkerPoolByID(ctx, req.PoolId)
	if err != nil {
		return csilapi.CreateEnrollmentTokenResponse{}, mapStoreErr(err, "pool not found")
	}
	if err := s.requireManageWorkers(ctx, id, pool.OrgID); err != nil {
		return csilapi.CreateEnrollmentTokenResponse{}, err
	}

	raw, hash, err := workerauth.GenerateEnrollmentToken()
	if err != nil {
		return csilapi.CreateEnrollmentTokenResponse{}, NewServiceError("internal", "failed to generate enrollment token")
	}
	token, err := s.deps.Store.CreatePoolEnrollmentToken(ctx, req.PoolId, derefOr(req.Name, ""), hash)
	if err != nil {
		return csilapi.CreateEnrollmentTokenResponse{}, NewServiceError("internal", "failed to create enrollment token")
	}
	s.recordAudit(ctx, derefOr(pool.OrgID, ""), "worker_enrollment_token.create", "pool_enrollment_token", token.TokenID, models.JSONB{"pool_id": pool.PoolID, "name": token.Name})
	return csilapi.CreateEnrollmentTokenResponse{
		Token:   raw,
		Summary: enrollmentTokenToSummary(token),
	}, nil
}

// ListEnrollmentTokens lists every enrollment token (active and deactivated)
// for a pool -- metadata only, never a raw value or hash (see
// enrollmentTokenToSummary). Requires org admin (of the pool's org) or
// global admin.
func (s *UiService) ListEnrollmentTokens(ctx context.Context, req csilapi.ListEnrollmentTokensRequest) (csilapi.ListEnrollmentTokensResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.ListEnrollmentTokensResponse{}, authErr
	}
	if err := requireNonEmpty("pool_id", req.PoolId, 64); err != nil {
		return csilapi.ListEnrollmentTokensResponse{}, err
	}

	pool, err := s.deps.Store.GetWorkerPoolByID(ctx, req.PoolId)
	if err != nil {
		return csilapi.ListEnrollmentTokensResponse{}, mapStoreErr(err, "pool not found")
	}
	if err := s.requireManageWorkers(ctx, id, pool.OrgID); err != nil {
		return csilapi.ListEnrollmentTokensResponse{}, err
	}

	tokens, err := s.deps.Store.ListPoolEnrollmentTokens(ctx, req.PoolId)
	if err != nil {
		return csilapi.ListEnrollmentTokensResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.EnrollmentTokenSummary, len(tokens))
	for i := range tokens {
		out[i] = enrollmentTokenToSummary(&tokens[i])
	}
	return csilapi.ListEnrollmentTokensResponse{Tokens: out}, nil
}

// DeactivateEnrollmentToken deactivates an enrollment token by ID. Requires
// org admin (of the token's pool's org) or global admin.
func (s *UiService) DeactivateEnrollmentToken(ctx context.Context, req csilapi.DeactivateEnrollmentTokenRequest) (csilapi.DeactivateEnrollmentTokenResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.DeactivateEnrollmentTokenResponse{}, authErr
	}
	if err := requireNonEmpty("token_id", req.TokenId, 64); err != nil {
		return csilapi.DeactivateEnrollmentTokenResponse{}, err
	}

	token, err := s.deps.Store.GetPoolEnrollmentTokenByID(ctx, req.TokenId)
	if err != nil {
		return csilapi.DeactivateEnrollmentTokenResponse{}, mapStoreErr(err, "enrollment token not found")
	}
	pool, err := s.deps.Store.GetWorkerPoolByID(ctx, token.PoolID)
	if err != nil {
		return csilapi.DeactivateEnrollmentTokenResponse{}, mapStoreErr(err, "token's pool not found")
	}
	if err := s.requireManageWorkers(ctx, id, pool.OrgID); err != nil {
		return csilapi.DeactivateEnrollmentTokenResponse{}, err
	}

	if err := s.deps.Store.DeactivatePoolEnrollmentToken(ctx, req.TokenId); err != nil {
		return csilapi.DeactivateEnrollmentTokenResponse{}, mapStoreErr(err, "enrollment token not found")
	}
	s.recordAudit(ctx, derefOr(pool.OrgID, ""), "worker_enrollment_token.deactivate", "pool_enrollment_token", token.TokenID, models.JSONB{"pool_id": pool.PoolID})
	token.IsActive = false
	return csilapi.DeactivateEnrollmentTokenResponse{Summary: enrollmentTokenToSummary(token)}, nil
}
