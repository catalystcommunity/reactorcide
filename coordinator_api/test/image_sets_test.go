package test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagealias"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imageset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

// Migration 000069 and the image set store queries (docs/image-aliases.md).
func TestImageSetStoreQueries(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		org := newPrincipalOrg(t, ctx, uniqueName("imgset-org"))
		otherOrg := newPrincipalOrg(t, ctx, uniqueName("imgset-other"))
		project := &models.Project{Name: uniqueName("imgset-app"), RepoURL: "github.com/acme/" + uniqueName("imgset"), OrgID: org.OrgID}
		require.NoError(t, ps.CreateProject(ctx, project))

		// Pool class grants: an organization pool with the default class.
		pool := &models.WorkerPool{Name: uniqueName("imgset-pool"), OrgID: &org.OrgID}
		require.NoError(t, ps.CreateWorkerPool(ctx, pool))
		class, err := ps.GetWorkerClass(ctx, org.OrgID, "default")
		require.NoError(t, err)
		require.NoError(t, ps.GrantWorkerClassPool(ctx, class.ClassID, pool.PoolID))
		grants, err := ps.ListPoolClassGrants(ctx, []string{pool.PoolID})
		require.NoError(t, err)
		require.Len(t, grants, 1)
		assert.Equal(t, models.PoolClassGrant{PoolID: pool.PoolID, OrgID: org.OrgID, ClassName: "default"}, grants[0])
		all, err := ps.ListPoolClassGrants(ctx, nil)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(all), 1)
		none, err := ps.ListPoolClassGrants(ctx, []string{"not-a-uuid"})
		require.NoError(t, err)
		assert.Empty(t, none)

		// Aliases of a set of organizations, and the pending aliases.
		orgAlias := vmAlias(org.OrgID, nil, "win", aliasDigestA)
		orgAlias.Characteristics = characteristics.Characteristics{}
		require.NoError(t, ps.CreateImageAlias(ctx, orgAlias))
		projAlias := vmAlias(org.OrgID, &project.ProjectID, "win", aliasDigestB)
		projAlias.Characteristics = characteristics.Characteristics{}
		require.NoError(t, ps.CreateImageAlias(ctx, projAlias))
		require.NoError(t, ps.CreateImageAlias(ctx, vmAlias(otherOrg.OrgID, nil, "win", aliasDigestC)))
		forOrg, err := ps.ListImageAliasesForOrgs(ctx, []string{org.OrgID})
		require.NoError(t, err)
		require.Len(t, forOrg, 2)
		both, err := ps.ListImageAliasesForOrgs(ctx, []string{org.OrgID, otherOrg.OrgID})
		require.NoError(t, err)
		assert.Len(t, both, 3)

		lockedAlias, err := ps.LockImageAlias(ctx, orgAlias.AliasID)
		require.NoError(t, err)
		assert.Equal(t, "win", lockedAlias.Name)
		_, err = ps.LockImageAlias(ctx, "01000000-0000-0000-0000-00000000dead")
		assert.ErrorIs(t, err, store.ErrNotFound)

		_, err = ps.SetImageAliasPending(ctx, orgAlias.AliasID, "oci://registry.example.com/vm/win:2@"+aliasDigestC, "test")
		require.NoError(t, err)
		pending, err := ps.ListPendingImageAliases(ctx)
		require.NoError(t, err)
		found := false
		for _, a := range pending {
			found = found || a.AliasID == orgAlias.AliasID
			assert.NotNil(t, a.PendingDigest)
		}
		assert.True(t, found, "the pending alias is listed")

		// Draining images: jobs that named an alias and are not terminal.
		job := func(status string, projectID *string, alias, ref, digest string) {
			chars := characteristics.Characteristics{"os": characteristics.StringValue("windows"),
				imagealias.DigestCharacteristic: characteristics.StringValue(digest)}
			name := alias
			j := &models.Job{OrgID: org.OrgID, ProjectID: projectID, Name: "build", JobCommand: "true", Status: status,
				RunnerImage: ref, ImageAlias: &name, Characteristics: chars}
			require.NoError(t, ps.CreateJob(ctx, j))
		}
		oldRef := "registry.example.com/vm/win:0@" + aliasDigestC
		job("running", nil, "win", oldRef, aliasDigestC)
		job("submitted", nil, "win", oldRef, aliasDigestC) // the same image: one row
		job("completed", nil, "win", "registry.example.com/vm/win:9@"+aliasDigestB, aliasDigestB)
		job("queued", &project.ProjectID, "win", "registry.example.com/vm/win:1@"+aliasDigestB, aliasDigestB)
		drains, err := ps.ListDrainingImages(ctx, []string{org.OrgID})
		require.NoError(t, err)
		require.Len(t, drains, 2, "terminal jobs and duplicates are not listed: %+v", drains)
		byProject := map[bool]models.DrainingImage{}
		for _, d := range drains {
			byProject[d.ProjectID != nil] = d
		}
		assert.Equal(t, aliasDigestC, byProject[false].Digest)
		assert.Equal(t, oldRef, byProject[false].Ref)
		assert.Equal(t, "win", byProject[false].AliasName)
		require.NotNil(t, byProject[true].ProjectID)
		assert.Equal(t, project.ProjectID, *byProject[true].ProjectID)
		otherDrains, err := ps.ListDrainingImages(ctx, []string{otherOrg.OrgID})
		require.NoError(t, err)
		assert.Empty(t, otherDrains)

		// Direct pinned VM references (no alias): listed with the worker
		// class and the characteristics, so that the workers that could
		// run the job keep the digest (migration 000070).
		directDigest := "sha256:" + strings.Repeat("d", 64)
		directRef := "registry.example.com/vm/direct:1@" + directDigest
		directJob := func(status string) {
			chars := characteristics.Characteristics{"os": characteristics.StringValue("windows"),
				imagealias.DigestCharacteristic: characteristics.StringValue(directDigest)}
			j := &models.Job{OrgID: org.OrgID, Name: "direct", JobCommand: "true", Status: status,
				RunnerImage: directRef, Characteristics: chars}
			require.NoError(t, ps.CreateJob(ctx, j))
		}
		directJob("queued")
		directJob("running") // the same image: one row
		directJob("failed")  // terminal: not listed
		// A container job with no alias and no digest is not listed.
		require.NoError(t, ps.CreateJob(ctx, &models.Job{OrgID: org.OrgID, Name: "plain", JobCommand: "true", Status: "queued",
			RunnerImage: "alpine:3", Characteristics: characteristics.Characteristics{"os": characteristics.StringValue("linux")}}))
		drains, err = ps.ListDrainingImages(ctx, []string{org.OrgID})
		require.NoError(t, err)
		require.Len(t, drains, 3, "%+v", drains)
		var directRow *models.DrainingImage
		for i := range drains {
			if drains[i].AliasName == "" {
				directRow = &drains[i]
			}
		}
		require.NotNil(t, directRow, "the direct pinned job is listed")
		assert.Equal(t, directDigest, directRow.Digest)
		assert.Equal(t, directRef, directRow.Ref)
		assert.Equal(t, "default", directRow.WorkerClass)
		assert.Equal(t, characteristics.StringValue("windows"), directRow.Characteristics["os"])

		// Jobs that use an alias block its delete (delete-image-alias).
		// orgAlias "win" (active A, pending C): the running and the
		// submitted org jobs with alias win (the project job resolved the
		// project alias). The direct job uses digest D: no alias pins it.
		inUse, err := ps.CountActiveImageAliasJobs(ctx, orgAlias)
		require.NoError(t, err)
		assert.Equal(t, int64(2), inUse)
		projInUse, err := ps.CountActiveImageAliasJobs(ctx, projAlias)
		require.NoError(t, err)
		assert.Equal(t, int64(1), projInUse, "the queued project job")
		directAlias := vmAlias(org.OrgID, nil, "direct", directDigest)
		directAlias.Characteristics = characteristics.Characteristics{}
		require.NoError(t, ps.CreateImageAlias(ctx, directAlias))
		directInUse, err := ps.CountActiveImageAliasJobs(ctx, directAlias)
		require.NoError(t, err)
		assert.Equal(t, int64(2), directInUse, "the queued and the running direct pinned jobs (the failed job does not count)")
		require.NoError(t, ps.DeleteImageAlias(ctx, org.OrgID, nil, "direct"))
		otherInUse, err := ps.CountActiveImageAliasJobs(ctx, &models.ImageAlias{OrgID: otherOrg.OrgID, Name: "win", ActiveDigest: aliasDigestC})
		require.NoError(t, err)
		assert.Zero(t, otherInUse, "jobs of another organization do not count")

		// The worker runtime is stored at Register and cleared when an
		// older worker registers.
		chars, err := characteristics.ParseWorkerCharacteristics("windows", "amd64", nil)
		require.NoError(t, err)
		runtime := models.WorkerRuntimeVM
		w, err := ps.UpsertWorkerByKey(ctx, &models.Worker{PoolID: pool.PoolID, WorkerKey: uniqueName("imgset"), OS: "windows", Arch: "amd64",
			Characteristics: chars, ImageRuntime: &runtime})
		require.NoError(t, err)
		locked, err := ps.LockWorkerImageSet(ctx, w.WorkerID)
		require.NoError(t, err)
		require.NotNil(t, locked.ImageRuntime)
		assert.Equal(t, "vm", *locked.ImageRuntime)
		_, err = ps.LockWorkerImageSet(ctx, "01000000-0000-0000-0000-00000000dead")
		assert.ErrorIs(t, err, store.ErrNotFound)

		// Apply with the real store: the set has the active, the pending,
		// and the draining digests, and the version changes only on change.
		first, err := imageset.Apply(ctx, ps, w.WorkerID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), first.Version)
		assert.ElementsMatch(t, []string{aliasDigestA, aliasDigestB, aliasDigestC, directDigest}, first.Set.Digests())
		for _, entry := range first.Set.Entries {
			// aliasDigestC is the pending pin of orgAlias, but a running
			// job uses it (drain), so it is not pending.
			assert.False(t, entry.Pending, "pending marker of %s", entry.Digest)
		}
		again, err := imageset.Apply(ctx, ps, w.WorkerID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), again.Version)
		assert.False(t, again.Changed)

		// Rollout: a ready online worker makes the pending pin promotable.
		// Online needs an active session.
		require.NoError(t, ps.CreateWorkerSession(ctx, &models.WorkerSession{WorkerID: w.WorkerID,
			TokenHash: []byte(uniqueName("imgset-session")), ExpiresAt: time.Now().UTC().Add(time.Hour)}))
		require.NoError(t, ps.UpdateWorkerImageState(ctx, w.WorkerID, models.WorkerImageState{
			State: models.WorkerImageStateUpdated, Version: 1, ReadyDigests: []string{aliasDigestA, aliasDigestB, aliasDigestC}}))
		promoter := &imageset.Promoter{Store: ps, Now: func() time.Time { return time.Now().UTC() }}
		promoted, err := promoter.Tick(ctx)
		require.NoError(t, err)
		promotedHere := false
		for _, a := range promoted {
			promotedHere = promotedHere || a.AliasID == orgAlias.AliasID
		}
		assert.True(t, promotedHere, "the pending pin is promoted: %+v", promoted)
		current, err := ps.GetImageAliasByID(ctx, orgAlias.AliasID)
		require.NoError(t, err)
		assert.Equal(t, aliasDigestC, current.ActiveDigest)
		assert.Nil(t, current.PendingDigest)

		older, err := ps.UpsertWorkerByKey(ctx, &models.Worker{PoolID: pool.PoolID, WorkerKey: w.WorkerKey, OS: "windows", Arch: "amd64", Characteristics: chars})
		require.NoError(t, err)
		assert.Nil(t, older.ImageRuntime)

		require.NoError(t, tx.SavePoint("runtime_check").Error)
		assert.Error(t, tx.Exec(`UPDATE workers SET image_runtime = 'firecracker' WHERE worker_id = ?`, w.WorkerID).Error, "the image_runtime check")
		require.NoError(t, tx.RollbackTo("runtime_check").Error)
	})
}

func TestMigration069WorkerImageRuntime(t *testing.T) {
	db := newScratchDBAt(t, "worker_image_runtime", 68)
	require.NoError(t, goose.UpByOne(db, "migrations"))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.Equal(t, int64(69), version)
	count := func(query string) int {
		var n int
		require.NoError(t, db.QueryRow(query).Scan(&n))
		return n
	}
	column := `SELECT count(*) FROM information_schema.columns WHERE table_name = 'workers' AND column_name = 'image_runtime' AND is_nullable = 'YES'`
	index := `SELECT count(*) FROM pg_indexes WHERE indexname = 'jobs_image_alias_active_idx'`
	require.Equal(t, 1, count(column))
	require.Equal(t, 1, count(index))
	// An old binary inserts a worker without the new column.
	_, err = db.Exec(`INSERT INTO worker_pools (name) VALUES ('p')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO workers (pool_id, worker_key, os, arch, characteristics) SELECT pool_id, 'k', 'linux', 'amd64', '{}' FROM worker_pools WHERE name = 'p'`)
	require.NoError(t, err)
	require.NoError(t, goose.Down(db, "migrations"))
	require.Equal(t, 0, count(column))
	require.Equal(t, 0, count(index))
	require.NoError(t, goose.UpByOne(db, "migrations"))
	require.Equal(t, 1, count(column))
}

func TestMigration070DirectImageDrainIndex(t *testing.T) {
	db := newScratchDBAt(t, "direct_image_drain", 69)
	require.NoError(t, goose.UpByOne(db, "migrations"))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.Equal(t, int64(70), version)
	count := func() int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM pg_indexes WHERE indexname = 'jobs_image_digest_direct_active_idx'`).Scan(&n))
		return n
	}
	require.Equal(t, 1, count())
	require.NoError(t, goose.Down(db, "migrations"))
	require.Equal(t, 0, count())
	require.NoError(t, goose.UpByOne(db, "migrations"))
	require.Equal(t, 1, count())
}

func TestMigration071WorkerImageFailures(t *testing.T) {
	db := newScratchDBAt(t, "worker_image_failures", 70)
	require.NoError(t, goose.UpByOne(db, "migrations"))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.Equal(t, int64(71), version)
	count := func() int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'workers' AND column_name = 'image_failed'`).Scan(&n))
		return n
	}
	require.Equal(t, 1, count())
	require.NoError(t, goose.Down(db, "migrations"))
	require.Equal(t, 0, count())
	require.NoError(t, goose.UpByOne(db, "migrations"))
	require.Equal(t, 1, count())
}

func TestMigration072JobImagePinAlias(t *testing.T) {
	db := newScratchDBAt(t, "job_image_pin_alias", 71)
	require.NoError(t, goose.UpByOne(db, "migrations"))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.Equal(t, int64(72), version)
	count := func() int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE
			(table_name = 'jobs' AND column_name IN ('image_pin_alias', 'image_retry_of')) OR
			(table_name = 'workflow_nodes' AND column_name = 'carried_image_pin_alias')`).Scan(&n))
		return n
	}
	require.Equal(t, 3, count())
	require.NoError(t, goose.Down(db, "migrations"))
	require.Equal(t, 0, count())
	require.NoError(t, goose.UpByOne(db, "migrations"))
	require.Equal(t, 3, count())
}
