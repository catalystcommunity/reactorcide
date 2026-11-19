package test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

// An update can write false to every boolean column (GORM Updates with a
// struct skips zero values).
func TestUpdateExecutionProfileWritesFalse(t *testing.T) {
	bools := map[string]func(*models.ExecutionProfile) *bool{
		"deny_secrets":         func(p *models.ExecutionProfile) *bool { return &p.DenySecrets },
		"may_run_as_root":      func(p *models.ExecutionProfile) *bool { return &p.MayRunAsRoot },
		"trusted_cache_writes": func(p *models.ExecutionProfile) *bool { return &p.TrustedCacheWrites },
		"egress_control":       func(p *models.ExecutionProfile) *bool { return &p.EgressControl },
	}
	for column, field := range bools {
		t.Run(column, func(t *testing.T) {
			RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
				ps := postgres_store.PostgresStore
				org := newPrincipalOrg(t, ctx, "profile-bools-"+strings.ReplaceAll(column, "_", "-"))
				profile := &models.ExecutionProfile{OrgID: org.OrgID, Name: "bools", DenySecrets: true, MayRunAsRoot: true,
					TrustedCacheWrites: true, EgressControl: true, SecretPathAllowlist: []string{"a/*"}, ResourceCeilings: models.JSONB{}}
				require.NoError(t, ps.CreateExecutionProfile(ctx, profile))

				stored, err := ps.GetExecutionProfile(ctx, org.OrgID, "bools")
				require.NoError(t, err)
				for name, get := range bools {
					require.True(t, *get(stored), "%s is true after create", name)
				}
				*field(stored) = false
				require.NoError(t, ps.UpdateExecutionProfile(ctx, stored))

				updated, err := ps.GetExecutionProfile(ctx, org.OrgID, "bools")
				require.NoError(t, err)
				for name, get := range bools {
					assert.Equal(t, name != column, *get(updated), name)
				}
				assert.Equal(t, []string{"a/*"}, []string(updated.SecretPathAllowlist), "other columns keep their values")

				// And back to true.
				*field(updated) = true
				require.NoError(t, ps.UpdateExecutionProfile(ctx, updated))
				again, err := ps.GetExecutionProfile(ctx, org.OrgID, "bools")
				require.NoError(t, err)
				assert.True(t, *field(again))
			})
		})
	}
}
