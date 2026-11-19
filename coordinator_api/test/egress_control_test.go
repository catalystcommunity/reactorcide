package test

// Postgres-backed coverage for migration 000063 (secret grant delivery and
// egress control) and postgres_store/egress_operations.go (WP18).

import (
	"context"
	"testing"

	"github.com/lib/pq"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestSecretProxyDeliveryMigration(t *testing.T) {
	db := newScratchDBAt(t, "egress_migrate", 62)

	var orgID string
	require.NoError(t, db.QueryRow(`INSERT INTO organizations (name, display_name) VALUES ('eg-org', 'EG') RETURNING org_id`).Scan(&orgID))
	var projectID string
	require.NoError(t, db.QueryRow(`INSERT INTO projects (name, repo_url, org_id) VALUES ('eg', 'github.com/example/eg', $1) RETURNING project_id`, orgID).Scan(&projectID))
	_, err := db.Exec(`INSERT INTO secret_grants (name, org_id, secret_path_pattern) VALUES ('old', $1, 'org/gh')`, orgID)
	require.NoError(t, err)

	require.NoError(t, goose.UpTo(db, "migrations", 63))

	var delivery, header, format string
	var hosts pq.StringArray
	require.NoError(t, db.QueryRow(`SELECT delivery, proxy_hosts, proxy_header, proxy_header_format FROM secret_grants WHERE name = 'old'`).
		Scan(&delivery, &hosts, &header, &format))
	require.Equal(t, "env", delivery, "an existing grant keeps env delivery")
	require.Empty(t, hosts)
	require.Empty(t, header)
	require.Empty(t, format)
	var orgOn, projectOn, profileOn bool
	require.NoError(t, db.QueryRow(`SELECT egress_control FROM organizations WHERE org_id = $1`, orgID).Scan(&orgOn))
	require.NoError(t, db.QueryRow(`SELECT egress_control FROM projects WHERE project_id = $1`, projectID).Scan(&projectOn))
	require.False(t, orgOn || projectOn, "egress control is off for existing rows")
	require.NoError(t, db.QueryRow(`SELECT coalesce(bool_or(egress_control), false) FROM execution_profiles`).Scan(&profileOn))
	require.False(t, profileOn)

	_, err = db.Exec(`UPDATE secret_grants SET delivery = 'file' WHERE name = 'old'`)
	require.Error(t, err, "an unknown delivery is refused")
	_, err = db.Exec(`UPDATE secret_grants SET delivery = 'proxy' WHERE name = 'old'`)
	require.Error(t, err, "proxy delivery needs proxy hosts")
	_, err = db.Exec(`UPDATE secret_grants SET delivery = 'proxy', proxy_hosts = '{api.github.com}', proxy_header_format = 'Bearer {}' WHERE name = 'old'`)
	require.NoError(t, err)

	require.NoError(t, goose.DownTo(db, "migrations", 62))
	var columns int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE (table_name = 'secret_grants' AND column_name IN ('delivery', 'proxy_hosts', 'proxy_header', 'proxy_header_format'))
		   OR (table_name IN ('organizations', 'projects', 'execution_profiles') AND column_name = 'egress_control')`).Scan(&columns))
	require.Zero(t, columns)
}

func TestEffectiveEgressControl(t *testing.T) {
	ctx := context.Background()
	settings, ok := store.AppStore.(store.EgressControlStore)
	require.True(t, ok, "store.AppStore must implement store.EgressControlStore")

	org := &models.Organization{Name: uniqueName("egorg"), DisplayName: "Org"}
	require.NoError(t, testDB.Create(org).Error)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	require.NoError(t, testDB.Model(&models.Project{}).Where("project_id = ?", project.ProjectID).Update("org_id", org.OrgID).Error)
	profile := &models.ExecutionProfile{OrgID: org.OrgID, Name: "egress-test", SecretPathAllowlist: pq.StringArray{}, RuntimeCapabilities: pq.StringArray{},
		AllowedWorkerClasses: pq.StringArray{}, ResourceCeilings: models.JSONB{}}
	require.NoError(t, testDB.Create(profile).Error)

	on, err := settings.EffectiveEgressControl(ctx, org.OrgID, &project.ProjectID, "egress-test")
	require.NoError(t, err)
	require.False(t, on, "off by default")

	require.NoError(t, testDB.Model(&models.ExecutionProfile{}).Where("profile_id = ?", profile.ProfileID).Update("egress_control", true).Error)
	on, _ = settings.EffectiveEgressControl(ctx, org.OrgID, &project.ProjectID, "egress-test")
	require.True(t, on, "the profile turns it on")
	on, _ = settings.EffectiveEgressControl(ctx, org.OrgID, &project.ProjectID, "standard")
	require.False(t, on, "another profile does not")

	loaded, err := store.AppStore.GetProjectByID(ctx, project.ProjectID)
	require.NoError(t, err)
	loaded.EgressControl = true
	require.NoError(t, store.AppStore.UpdateProject(ctx, loaded))
	on, _ = settings.EffectiveEgressControl(ctx, org.OrgID, &project.ProjectID, "")
	require.True(t, on, "the project turns it on (saved through the REST path)")
	on, _ = settings.EffectiveEgressControl(ctx, org.OrgID, nil, "")
	require.False(t, on, "a job with no project and no profile follows the organization")

	require.NoError(t, settings.SetOrganizationEgressControl(ctx, org.OrgID, true))
	on, _ = settings.EffectiveEgressControl(ctx, org.OrgID, nil, "")
	require.True(t, on, "the organization turns it on")
	require.ErrorIs(t, settings.SetOrganizationEgressControl(ctx, "not-a-uuid", true), store.ErrNotFound)
}

func TestSecretGrantDeliveryRoundTrip(t *testing.T) {
	ctx := context.Background()
	org := &models.Organization{Name: uniqueName("eggrant"), DisplayName: "Org"}
	require.NoError(t, testDB.Create(org).Error)
	grant := &models.SecretGrant{Name: "gh", UserID: org.OrgID, SecretPathMatch: models.SecretGrantMatchPrefix, SecretPathPattern: "org/gh",
		JobNameMatch: models.SecretGrantMatchAny, Delivery: models.SecretDeliveryProxy, ProxyHosts: pq.StringArray{"api.github.com"},
		ProxyHeader: "authorization", ProxyHeaderFormat: "Bearer {}"}
	require.NoError(t, testDB.Create(grant).Error)
	plain := &models.SecretGrant{Name: "plain", UserID: org.OrgID, SecretPathMatch: models.SecretGrantMatchPrefix, SecretPathPattern: "org/plain",
		JobNameMatch: models.SecretGrantMatchAny}
	require.NoError(t, testDB.Create(plain).Error)

	grantStore, ok := store.AppStore.(interface {
		ListSecretGrantsForJob(context.Context, string, *string, string) ([]models.SecretGrant, error)
	})
	require.True(t, ok)
	grants, err := grantStore.ListSecretGrantsForJob(ctx, org.OrgID, nil, "deploy")
	require.NoError(t, err)
	byName := map[string]models.SecretGrant{}
	for _, g := range grants {
		byName[g.Name] = g
	}
	require.Equal(t, models.SecretDeliveryProxy, byName["gh"].Delivery)
	require.Equal(t, []string{"api.github.com"}, []string(byName["gh"].ProxyHosts))
	require.Equal(t, "Bearer {}", byName["gh"].ProxyHeaderFormat)
	require.Equal(t, models.SecretDeliveryEnv, byName["plain"].EffectiveDelivery(), "the column default is env")
}
