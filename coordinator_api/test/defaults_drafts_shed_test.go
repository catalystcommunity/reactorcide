package test

// Postgres-backed coverage for WP9: migration 000055, secure defaults for
// new organizations, the legacy profile of existing organizations, draft_prs
// resolution, shed levels, and the rate_limited and skipped event statuses.

import (
	"context"
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cachens"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestDefaultsDraftsShedMigration(t *testing.T) {
	db := newScratchDBAt(t, "wp9_migrate", 51)

	var orgID string
	require.NoError(t, db.QueryRow(`INSERT INTO organizations (name, display_name, dynamic_jobs) VALUES ('wp9-org', 'WP9', 'allow') RETURNING org_id`).Scan(&orgID))
	var projectID string
	require.NoError(t, db.QueryRow(`INSERT INTO projects (name, repo_url, org_id) VALUES ('wp9', 'github.com/example/wp9', $1) RETURNING project_id`, orgID).Scan(&projectID))

	require.NoError(t, goose.UpTo(db, "migrations", 55))

	var profile, draftPRs, shedLevel string
	var actions sql.NullString
	require.NoError(t, db.QueryRow(`SELECT defaults_profile, draft_prs, shed_level, shed_actions FROM organizations WHERE org_id = $1`, orgID).
		Scan(&profile, &draftPRs, &shedLevel, &actions))
	require.Equal(t, models.DefaultsProfileLegacy, profile, "an existing organization keeps its behavior")
	require.Equal(t, models.DraftPRsRun, draftPRs)
	require.Equal(t, models.ShedLevelNormal, shedLevel)
	require.False(t, actions.Valid)
	var dynamicJobs string
	require.NoError(t, db.QueryRow(`SELECT dynamic_jobs FROM organizations WHERE org_id = $1`, orgID).Scan(&dynamicJobs))
	require.Equal(t, models.DynamicJobsAllow, dynamicJobs, "the migration does not change stored settings")
	var projectDrafts string
	require.NoError(t, db.QueryRow(`SELECT draft_prs FROM projects WHERE project_id = $1`, projectID).Scan(&projectDrafts))
	require.Equal(t, models.DraftPRsInherit, projectDrafts)

	_, err := db.Exec(`UPDATE organizations SET shed_level = 'panic' WHERE org_id = $1`, orgID)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE projects SET draft_prs = 'sometimes' WHERE project_id = $1`, projectID)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE organizations SET defaults_profile = 'strict' WHERE org_id = $1`, orgID)
	require.Error(t, err)

	for _, status := range []string{models.CIEventStatusRateLimited, models.CIEventStatusSkipped} {
		_, err = db.Exec(`INSERT INTO ci_events (org_id, project_id, source, event_type, status, summary)
			VALUES ($1, $2, 'webhook', 'pull_request_opened', $3, '{}')`, orgID, projectID, status)
		require.NoError(t, err, status)
	}

	require.NoError(t, goose.DownTo(db, "migrations", 51))
	var columns int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE (table_name = 'organizations' AND column_name IN ('defaults_profile', 'draft_prs', 'shed_level', 'shed_actions'))
		   OR (table_name = 'projects' AND column_name = 'draft_prs')`).Scan(&columns))
	require.Zero(t, columns)
	var failed int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM ci_events WHERE status = 'failed'`).Scan(&failed))
	require.Equal(t, 2, failed, "Down maps the new statuses to failed")
}

func TestNewOrganizationGetsSecureDefaults(t *testing.T) {
	ctx := context.Background()
	orgs, ok := store.AppStore.(interface {
		CreateOrganization(context.Context, *models.Organization) error
	})
	require.True(t, ok)
	trust, ok := store.AppStore.(store.CITrustSettingsStore)
	require.True(t, ok)
	events, ok := store.AppStore.(store.EventSettingsStore)
	require.True(t, ok)

	secure := &models.Organization{Name: uniqueName("secure"), DisplayName: "Secure"}
	require.NoError(t, orgs.CreateOrganization(ctx, secure))
	require.Equal(t, models.DefaultsProfileSecure, secure.DefaultsProfile)
	var stored models.Organization
	require.NoError(t, testDB.Where("org_id = ?", secure.OrgID).First(&stored).Error)
	require.Equal(t, models.DefaultsProfileSecure, stored.DefaultsProfile)
	require.Equal(t, models.DynamicJobsDeny, stored.DynamicJobs)

	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	require.NoError(t, testDB.Model(&models.Project{}).Where("project_id = ?", project.ProjectID).Update("org_id", secure.OrgID).Error)
	effective, err := trust.EffectiveCITrust(ctx, secure.OrgID, project.ProjectID)
	require.NoError(t, err)
	require.Equal(t, []string{models.RunGateOutsideContributors}, effective.RunGate)
	dynamic, err := events.EffectiveDynamicJobs(ctx, secure.OrgID, &project.ProjectID)
	require.NoError(t, err)
	require.Equal(t, models.DynamicJobsDeny, dynamic)

	// Secure: a pull request job does not write the cache of the standard
	// profile; a default-branch or tag job does.
	var standard models.ExecutionProfile
	require.NoError(t, testDB.Where("org_id = ? AND name = 'standard'", secure.OrgID).First(&standard).Error)
	require.Equal(t, []string{"default", "tag"}, []string(standard.CacheWriteClasses))
	require.False(t, cachens.Compute(secure.OrgID, "standard", &standard, []string{"pr"}, "standard").Writable)
	require.True(t, cachens.Compute(secure.OrgID, "standard", &standard, []string{"default"}, "standard").Writable)
	require.True(t, cachens.Compute(secure.OrgID, "standard", &standard, []string{"tag"}, "standard").Writable)

	// An operator can still create a legacy organization.
	legacy := &models.Organization{Name: uniqueName("legacy"), DisplayName: "Legacy", DefaultsProfile: models.DefaultsProfileLegacy}
	require.NoError(t, orgs.CreateOrganization(ctx, legacy))
	legacyTrust, err := trust.GetCITrustSettings(ctx, legacy.OrgID, nil)
	require.NoError(t, err)
	require.Empty(t, legacyTrust.RunGate)
	var legacyStandard models.ExecutionProfile
	require.NoError(t, testDB.Where("org_id = ? AND name = 'standard'", legacy.OrgID).First(&legacyStandard).Error)
	require.Empty(t, legacyStandard.CacheWriteClasses, "legacy keeps cache writes for every ref class")

	// A row that the coordinator did not create through CreateOrganization
	// (an existing row) is legacy.
	existing := &models.Organization{Name: uniqueName("existing"), DisplayName: "Existing"}
	require.NoError(t, testDB.Create(existing).Error)
	var existingStored models.Organization
	require.NoError(t, testDB.Where("org_id = ?", existing.OrgID).First(&existingStored).Error)
	require.Equal(t, models.DefaultsProfileLegacy, existingStored.DefaultsProfile)
}

func TestApplyDefaultsProfileKeepsOtherTrustSettings(t *testing.T) {
	ctx := context.Background()
	profiles, ok := store.AppStore.(store.DefaultsProfileStore)
	require.True(t, ok)
	trust := store.AppStore.(store.CITrustSettingsStore)

	org := &models.Organization{Name: uniqueName("upgrade"), DisplayName: "Upgrade"}
	require.NoError(t, testDB.Create(org).Error)
	profileName := "custom-untrusted"
	require.NoError(t, trust.PutCITrustSettings(ctx, &models.CITrustSettings{OrgID: org.OrgID, OverrideProfile: &profileName,
		RunApprovers: []string{"project_owner"}}))

	require.NoError(t, profiles.ApplyOrganizationDefaultsProfile(ctx, org.OrgID, models.DefaultsProfileSecure, "test"))
	row, err := trust.GetCITrustSettings(ctx, org.OrgID, nil)
	require.NoError(t, err)
	require.Equal(t, []string{models.RunGateOutsideContributors}, []string(row.RunGate))
	require.Equal(t, profileName, *row.OverrideProfile, "the profile writes only run_gate")
	require.Equal(t, []string{"project_owner"}, []string(row.RunApprovers))
	var stored models.Organization
	require.NoError(t, testDB.Where("org_id = ?", org.OrgID).First(&stored).Error)
	require.Equal(t, models.DefaultsProfileSecure, stored.DefaultsProfile)
	require.Equal(t, models.DynamicJobsDeny, stored.DynamicJobs)

	require.NoError(t, profiles.ApplyOrganizationDefaultsProfile(ctx, org.OrgID, models.DefaultsProfileLegacy, "test"))
	row, err = trust.GetCITrustSettings(ctx, org.OrgID, nil)
	require.NoError(t, err)
	require.Empty(t, row.RunGate)
	var reverted models.Organization
	require.NoError(t, testDB.Where("org_id = ?", org.OrgID).First(&reverted).Error)
	require.Equal(t, models.DynamicJobsAllow, reverted.DynamicJobs)
	require.Error(t, profiles.ApplyOrganizationDefaultsProfile(ctx, org.OrgID, "strict", "test"))
}

func TestDraftPRsAndShedStore(t *testing.T) {
	ctx := context.Background()
	shed, ok := store.AppStore.(store.DraftShedStore)
	require.True(t, ok)

	org := &models.Organization{Name: uniqueName("shed"), DisplayName: "Shed"}
	require.NoError(t, testDB.Create(org).Error)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	require.NoError(t, testDB.Model(&models.Project{}).Where("project_id = ?", project.ProjectID).Update("org_id", org.OrgID).Error)

	value, err := shed.EffectiveDraftPRs(ctx, project.ProjectID)
	require.NoError(t, err)
	require.Equal(t, models.DraftPRsRun, value, "the default keeps the previous behavior")
	require.NoError(t, shed.SetOrganizationDraftPRs(ctx, org.OrgID, models.DraftPRsSkipWhenShedding))
	value, err = shed.EffectiveDraftPRs(ctx, project.ProjectID)
	require.NoError(t, err)
	require.Equal(t, models.DraftPRsSkipWhenShedding, value)
	require.NoError(t, testDB.Model(&models.Project{}).Where("project_id = ?", project.ProjectID).Update("draft_prs", "skip").Error)
	value, err = shed.EffectiveDraftPRs(ctx, project.ProjectID)
	require.NoError(t, err)
	require.Equal(t, models.DraftPRsSkip, value, "the project value wins")
	require.Error(t, shed.SetOrganizationDraftPRs(ctx, org.OrgID, "inherit"))

	state, err := shed.GetShedState(ctx, org.OrgID)
	require.NoError(t, err)
	require.Equal(t, models.ShedLevelNormal, state.Level)

	require.NoError(t, shed.SetGlobalShedLevel(ctx, models.ShedLevelDegraded))
	t.Cleanup(func() { _ = shed.SetGlobalShedLevel(ctx, models.ShedLevelNormal) })
	state, err = shed.GetShedState(ctx, org.OrgID)
	require.NoError(t, err)
	require.Equal(t, models.ShedLevelDegraded, state.Level)
	require.Equal(t, "global", state.Source)
	require.True(t, state.Actions.PauseSchedules)

	require.NoError(t, shed.SetOrganizationShed(ctx, org.OrgID, models.ShedLevelCritical, nil, false))
	state, err = shed.GetShedState(ctx, org.OrgID)
	require.NoError(t, err)
	require.Equal(t, models.ShedLevelCritical, state.Level, "the level in force is the higher one")
	require.Equal(t, "org", state.Source)
	require.True(t, state.Actions.RefuseManualRuns)

	custom := models.ShedActionsConfig{Critical: models.ShedActions{SkipAdvisory: true}}
	require.NoError(t, shed.SetOrganizationShed(ctx, org.OrgID, "", &custom, false))
	state, err = shed.GetShedState(ctx, org.OrgID)
	require.NoError(t, err)
	require.Equal(t, models.ShedLevelCritical, state.OrgLevel, "an actions update keeps the level")
	require.Equal(t, models.ShedActions{SkipAdvisory: true}, state.Actions)
	require.NoError(t, shed.SetOrganizationShed(ctx, org.OrgID, "", nil, true))
	state, err = shed.GetShedState(ctx, org.OrgID)
	require.NoError(t, err)
	require.True(t, state.Actions.RefuseManualRuns, "reset returns to the built-in actions")

	require.Error(t, shed.SetGlobalShedLevel(ctx, "panic"))
	global, err := shed.GetShedState(ctx, "")
	require.NoError(t, err)
	require.Equal(t, models.ShedLevelDegraded, global.GlobalLevel)
}

// A rate-limited or skipped event never ran, so it is not the first event of
// its change: with duplicate_events skip, the next delivery runs.
func TestRecordCIEvent_RateLimitedIsNotTheFirstOfItsChange(t *testing.T) {
	ctx := context.Background()
	events := store.AppStore.(store.CIEventStore)
	org := &models.Organization{Name: uniqueName("dups"), DisplayName: "Dups", DuplicateEvents: models.DuplicateEventsSkip}
	require.NoError(t, testDB.Create(org).Error)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	require.NoError(t, testDB.Model(&models.Project{}).Where("project_id = ?", project.ProjectID).Update("org_id", org.OrgID).Error)

	semantic := "github:o/r:pull_request_updated:feature:abc:1:" + uniqueName("k")
	limitedKey := uniqueName("delivery")
	limited := &models.CIEvent{OrgID: org.OrgID, ProjectID: &project.ProjectID, Source: models.CIEventSourceWebhook, EventType: "pull_request_updated",
		Status: models.CIEventStatusRateLimited, SemanticKey: semantic, IdempotencyKey: &limitedKey}
	recorded, outcome, err := events.RecordCIEvent(ctx, limited)
	require.NoError(t, err)
	require.Equal(t, store.CIEventCreated, outcome)
	require.Equal(t, models.CIEventStatusRateLimited, recorded.Status)

	nextKey := uniqueName("delivery")
	next := &models.CIEvent{OrgID: org.OrgID, ProjectID: &project.ProjectID, Source: models.CIEventSourceWebhook, EventType: "pull_request_updated",
		SemanticKey: semantic, IdempotencyKey: &nextKey}
	_, outcome, err = events.RecordCIEvent(ctx, next)
	require.NoError(t, err)
	require.Equal(t, store.CIEventCreated, outcome, "the rate-limited event does not make the next delivery a duplicate")

	summary := models.CIEventSummary{SkipReasons: []models.DecisionReason{{Kind: models.SkipReasonDraft, Detail: "draft pull request"}}}
	require.NoError(t, events.SetCIEventSummary(ctx, recorded.EventID, summary))
	require.NoError(t, events.SetCIEventStatus(ctx, recorded.EventID, models.CIEventStatusSkipped))
	loaded, err := events.GetCIEvent(ctx, recorded.EventID)
	require.NoError(t, err)
	require.Equal(t, models.CIEventStatusSkipped, loaded.Status)
	require.Equal(t, "draft pull request", loaded.Summary.SkipReasons[0].Detail)
}
