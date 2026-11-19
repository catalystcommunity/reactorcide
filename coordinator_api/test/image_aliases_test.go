package test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagealias"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

var (
	aliasDigestA = "sha256:" + strings.Repeat("a", 64)
	aliasDigestB = "sha256:" + strings.Repeat("b", 64)
	aliasDigestC = "sha256:" + strings.Repeat("c", 64)
)

func vmAlias(orgID string, projectID *string, name, digest string) *models.ImageAlias {
	return &models.ImageAlias{
		OrgID: orgID, ProjectID: projectID, Name: name, Kind: models.ImageAliasKindVM, OS: "windows", Arch: "amd64",
		Characteristics: characteristics.Characteristics{"worker_class": characteristics.StringValue("win-vs")},
		ActiveRef:       "oci://registry.example.com/vm/" + name + ":2026.11@" + digest, ActiveDigest: digest,
		UpdatedBy: "test",
	}
}

// Migration 000068: image alias store methods.
func TestImageAliasStore(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		org := newPrincipalOrg(t, ctx, uniqueName("alias-org"))
		project := &models.Project{Name: uniqueName("alias-app"), RepoURL: "github.com/acme/" + uniqueName("alias"), OrgID: org.OrgID}
		require.NoError(t, ps.CreateProject(ctx, project))
		other := &models.Project{Name: uniqueName("alias-other"), RepoURL: "github.com/acme/" + uniqueName("alias"), OrgID: org.OrgID}
		require.NoError(t, ps.CreateProject(ctx, other))

		orgAlias := vmAlias(org.OrgID, nil, "windows-vs2022", aliasDigestA)
		require.NoError(t, ps.CreateImageAlias(ctx, orgAlias))
		require.NotEmpty(t, orgAlias.AliasID)
		projAlias := vmAlias(org.OrgID, &project.ProjectID, "windows-vs2022", aliasDigestB)
		require.NoError(t, ps.CreateImageAlias(ctx, projAlias))

		// A statement that fails aborts the test transaction. Run each
		// expected database failure in a savepoint.
		inSavepoint := func(fn func() error) error {
			require.NoError(t, tx.SavePoint("expected_failure").Error)
			err := fn()
			require.NoError(t, tx.RollbackTo("expected_failure").Error)
			return err
		}

		// One name per scope.
		err := inSavepoint(func() error { return ps.CreateImageAlias(ctx, vmAlias(org.OrgID, nil, "windows-vs2022", aliasDigestC)) })
		assert.ErrorIs(t, err, store.ErrAlreadyExists)
		// An empty project ID is the organization scope.
		empty := ""
		err = inSavepoint(func() error {
			return ps.CreateImageAlias(ctx, vmAlias(org.OrgID, &empty, "windows-vs2022", aliasDigestC))
		})
		assert.ErrorIs(t, err, store.ErrAlreadyExists)
		// Validation.
		bad := vmAlias(org.OrgID, nil, "Bad/Name", aliasDigestC)
		assert.ErrorIs(t, ps.CreateImageAlias(ctx, bad), store.ErrInvalidInput)
		mismatch := vmAlias(org.OrgID, nil, "mismatch", aliasDigestC)
		mismatch.ActiveDigest = aliasDigestA
		assert.ErrorIs(t, ps.CreateImageAlias(ctx, mismatch), store.ErrInvalidInput)
		unpinned := vmAlias(org.OrgID, nil, "unpinned", aliasDigestC)
		unpinned.ActiveRef = "oci://registry.example.com/vm/x:1"
		assert.ErrorIs(t, ps.CreateImageAlias(ctx, unpinned), store.ErrInvalidInput)

		// Get by exact scope and by ID; characteristics round trip with types.
		got, err := ps.GetImageAlias(ctx, org.OrgID, nil, "windows-vs2022")
		require.NoError(t, err)
		assert.Equal(t, orgAlias.AliasID, got.AliasID)
		assert.Nil(t, got.ProjectID)
		assert.Equal(t, characteristics.StringValue("win-vs"), got.Characteristics["worker_class"])
		got, err = ps.GetImageAlias(ctx, org.OrgID, &project.ProjectID, "windows-vs2022")
		require.NoError(t, err)
		assert.Equal(t, projAlias.AliasID, got.AliasID)
		_, err = ps.GetImageAlias(ctx, org.OrgID, &other.ProjectID, "windows-vs2022")
		assert.ErrorIs(t, err, store.ErrNotFound)
		byID, err := ps.GetImageAliasByID(ctx, projAlias.AliasID)
		require.NoError(t, err)
		assert.Equal(t, "windows-vs2022", byID.Name)

		// Resolution: the project alias, then the organization alias.
		resolved, err := ps.ResolveImageAlias(ctx, org.OrgID, &project.ProjectID, "windows-vs2022")
		require.NoError(t, err)
		assert.Equal(t, aliasDigestB, resolved.ActiveDigest)
		resolved, err = ps.ResolveImageAlias(ctx, org.OrgID, &other.ProjectID, "windows-vs2022")
		require.NoError(t, err)
		assert.Equal(t, aliasDigestA, resolved.ActiveDigest)
		resolved, err = ps.ResolveImageAlias(ctx, org.OrgID, nil, "windows-vs2022")
		require.NoError(t, err)
		assert.Equal(t, aliasDigestA, resolved.ActiveDigest)
		_, err = ps.ResolveImageAlias(ctx, org.OrgID, nil, "missing")
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = ps.ResolveImageAlias(ctx, org.OrgID, nil, "ghcr.io/acme/x:1")
		assert.ErrorIs(t, err, store.ErrNotFound)

		// Lists.
		list, err := ps.ListImageAliases(ctx, org.OrgID, nil)
		require.NoError(t, err)
		require.Len(t, list, 1)
		list, err = ps.ListImageAliases(ctx, org.OrgID, &project.ProjectID)
		require.NoError(t, err)
		require.Len(t, list, 1)
		all, err := ps.ListOrgImageAliases(ctx, org.OrgID)
		require.NoError(t, err)
		require.Len(t, all, 2)
		assert.Nil(t, all[0].ProjectID, "organization aliases first")

		// Pending pin, digest lookup, and promotion.
		pendingRef := "oci://registry.example.com/vm/windows-vs2022:2026.12@" + aliasDigestC
		pending, err := ps.SetImageAliasPending(ctx, orgAlias.AliasID, pendingRef, "admin")
		require.NoError(t, err)
		require.NotNil(t, pending.PendingDigest)
		assert.Equal(t, aliasDigestC, *pending.PendingDigest)
		assert.NotNil(t, pending.PendingSince)
		assert.Equal(t, aliasDigestA, pending.ActiveDigest, "the active pin stays until promotion")
		_, err = ps.SetImageAliasPending(ctx, orgAlias.AliasID, "oci://registry.example.com/vm/x:1", "admin")
		assert.ErrorIs(t, err, store.ErrInvalidInput)

		win := models.ImagePlatform{Kind: models.ImageAliasKindVM, OS: "windows", Arch: "amd64"}
		withDigest, err := ps.ListImageAliasesWithDigest(ctx, org.OrgID, &other.ProjectID, aliasDigestC, win)
		require.NoError(t, err)
		require.Len(t, withDigest, 1, "a pending digest is in scope")
		withDigest, err = ps.ListImageAliasesWithDigest(ctx, org.OrgID, &other.ProjectID, aliasDigestB, win)
		require.NoError(t, err)
		assert.Empty(t, withDigest, "another project's pin is not in scope")
		withDigest, err = ps.ListImageAliasesWithDigest(ctx, org.OrgID, &project.ProjectID, aliasDigestB, win)
		require.NoError(t, err)
		assert.Len(t, withDigest, 1)
		// Kind and platform filter: another kind, os, or arch does not
		// count; an empty arch matches each arch.
		for _, other := range []models.ImagePlatform{
			{Kind: models.ImageAliasKindContainer, OS: "windows", Arch: "amd64"},
			{Kind: models.ImageAliasKindVM, OS: "macos", Arch: "amd64"},
			{Kind: models.ImageAliasKindVM, OS: "windows", Arch: "arm64"},
		} {
			withDigest, err = ps.ListImageAliasesWithDigest(ctx, org.OrgID, &project.ProjectID, aliasDigestB, other)
			require.NoError(t, err)
			assert.Empty(t, withDigest, "platform %+v", other)
		}
		withDigest, err = ps.ListImageAliasesWithDigest(ctx, org.OrgID, &project.ProjectID, aliasDigestB, models.ImagePlatform{Kind: models.ImageAliasKindVM, OS: "windows"})
		require.NoError(t, err)
		assert.Len(t, withDigest, 1, "an empty arch matches each arch")

		_, promoted, err := ps.PromoteImageAliasPending(ctx, orgAlias.AliasID, aliasDigestB)
		require.NoError(t, err)
		assert.False(t, promoted, "another pending digest is not promoted")
		after, promoted, err := ps.PromoteImageAliasPending(ctx, orgAlias.AliasID, aliasDigestC)
		require.NoError(t, err)
		require.True(t, promoted)
		assert.Equal(t, aliasDigestC, after.ActiveDigest)
		assert.Equal(t, pendingRef, after.ActiveRef)
		assert.Nil(t, after.PendingDigest)
		assert.Nil(t, after.PendingRef)
		assert.Nil(t, after.PendingSince)

		_, err = ps.SetImageAliasPending(ctx, orgAlias.AliasID, "oci://registry.example.com/vm/w:3@"+aliasDigestA, "admin")
		require.NoError(t, err)
		cleared, err := ps.ClearImageAliasPending(ctx, orgAlias.AliasID, "admin")
		require.NoError(t, err)
		assert.Nil(t, cleared.PendingDigest)

		// Update writes the mutable values.
		after.OS, after.Arch = "windows", "arm64"
		after.Characteristics = characteristics.Characteristics{"gpu": characteristics.BoolValue(true)}
		after.UpdatedBy = "other-admin"
		require.NoError(t, ps.UpdateImageAlias(ctx, after))
		reread, err := ps.GetImageAliasByID(ctx, orgAlias.AliasID)
		require.NoError(t, err)
		assert.Equal(t, "arm64", reread.Arch)
		assert.Equal(t, characteristics.BoolValue(true), reread.Characteristics["gpu"])
		assert.Equal(t, "other-admin", reread.UpdatedBy)

		// Delete by exact scope.
		require.NoError(t, ps.DeleteImageAlias(ctx, org.OrgID, &project.ProjectID, "windows-vs2022"))
		assert.ErrorIs(t, ps.DeleteImageAlias(ctx, org.OrgID, &project.ProjectID, "windows-vs2022"), store.ErrNotFound)
		resolved, err = ps.ResolveImageAlias(ctx, org.OrgID, &project.ProjectID, "windows-vs2022")
		require.NoError(t, err)
		assert.Equal(t, orgAlias.AliasID, resolved.AliasID, "the organization alias applies again")

		// Deleting the project deletes its aliases (foreign key cascade).
		require.NoError(t, ps.CreateImageAlias(ctx, vmAlias(org.OrgID, &other.ProjectID, "proj-only", aliasDigestA)))
		require.NoError(t, ps.DeleteProject(ctx, other.ProjectID))
		var n int64
		require.NoError(t, tx.Model(&models.ImageAlias{}).Where("project_id = ?", other.ProjectID).Count(&n).Error)
		assert.Zero(t, n)

		// The database refuses values that the store would refuse.
		for _, insert := range []string{
			`INSERT INTO image_aliases (org_id, name, kind, os, arch, active_ref, active_digest) VALUES (?, 'x', 'vm', 'windows', 'amd64', 'r', 'sha256:ABC')`,
			`INSERT INTO image_aliases (org_id, name, kind, os, arch, active_ref, active_digest) VALUES (?, 'X/y', 'vm', 'windows', 'amd64', 'r', '` + aliasDigestA + `')`,
			`INSERT INTO image_aliases (org_id, name, kind, os, arch, active_ref, active_digest) VALUES (?, 'x', 'lxc', 'windows', 'amd64', 'r', '` + aliasDigestA + `')`,
			`INSERT INTO image_aliases (org_id, name, kind, os, arch, active_ref, active_digest, pending_ref) VALUES (?, 'x', 'vm', 'windows', 'amd64', 'r', '` + aliasDigestA + `', 'p')`,
		} {
			err := inSavepoint(func() error { return tx.Exec(insert, org.OrgID).Error })
			assert.Error(t, err, insert)
		}

	})
}

// Migration 000068: worker image state columns.
func TestWorkerImageStore(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		pool := &models.WorkerPool{Name: uniqueName("pool")}
		require.NoError(t, ps.CreateWorkerPool(ctx, pool))
		chars, err := characteristics.ParseWorkerCharacteristics("windows", "amd64", nil)
		require.NoError(t, err)
		w, err := ps.UpsertWorkerByKey(ctx, &models.Worker{PoolID: pool.PoolID, WorkerKey: uniqueName("img"), OS: "windows", Arch: "amd64", Characteristics: chars})
		require.NoError(t, err)

		state, err := ps.GetWorkerImageState(ctx, w.WorkerID)
		require.NoError(t, err)
		assert.Nil(t, state, "a worker without a report has no state")
		fresh, err := ps.GetWorkerByID(ctx, w.WorkerID)
		require.NoError(t, err)
		assert.Equal(t, int64(0), fresh.ImageSetVersion)
		assert.Nil(t, fresh.ImageReadyDigests)

		require.NoError(t, ps.SetWorkerImageSet(ctx, w.WorkerID, 4, "set-hash"))
		require.NoError(t, ps.UpdateWorkerImageState(ctx, w.WorkerID, models.WorkerImageState{
			State: models.WorkerImageStateFailed, Version: 4, Detail: "disk full", ReadyDigests: []string{aliasDigestA}}))
		state, err = ps.GetWorkerImageState(ctx, w.WorkerID)
		require.NoError(t, err)
		require.NotNil(t, state)
		assert.Equal(t, models.WorkerImageState{State: "failed", Version: 4, Detail: "disk full", ReadyDigests: []string{aliasDigestA}}, *state)

		require.NoError(t, ps.UpdateWorkerImageState(ctx, w.WorkerID, models.WorkerImageState{State: models.WorkerImageStateUpdated, Version: 4}))
		state, err = ps.GetWorkerImageState(ctx, w.WorkerID)
		require.NoError(t, err)
		assert.Equal(t, models.WorkerImageState{State: "updated", Version: 4, ReadyDigests: []string{}}, *state)

		row, err := ps.GetWorkerByID(ctx, w.WorkerID)
		require.NoError(t, err)
		assert.Equal(t, int64(4), row.ImageSetVersion)
		require.NotNil(t, row.ImageSetHash)
		assert.Equal(t, "set-hash", *row.ImageSetHash)

		// A new Register keeps the image columns.
		again, err := ps.UpsertWorkerByKey(ctx, &models.Worker{PoolID: pool.PoolID, WorkerKey: w.WorkerKey, OS: "windows", Arch: "amd64", Characteristics: chars})
		require.NoError(t, err)
		assert.Equal(t, int64(4), again.ImageSetVersion)

		assert.ErrorIs(t, ps.UpdateWorkerImageState(ctx, w.WorkerID, models.WorkerImageState{State: "ready"}), store.ErrInvalidInput)
		require.NoError(t, tx.SavePoint("image_state_check").Error)
		assert.Error(t, tx.Exec(`UPDATE workers SET image_state = 'ready' WHERE worker_id = ?`, w.WorkerID).Error, "the image_state check")
		require.NoError(t, tx.RollbackTo("image_state_check").Error)
		assert.ErrorIs(t, ps.UpdateWorkerImageState(ctx, "01000000-0000-0000-0000-00000000dead", models.WorkerImageState{State: "updated"}), store.ErrNotFound)
	})
}

// TestJobImageAliasREST checks resolution on the REST create path: an
// organization alias gives the job its pinned reference, image_alias, and
// the image_digest characteristic, and a VM job with an unknown image is
// refused.
func TestJobImageAliasREST(t *testing.T) {
	post := func(t *testing.T, ctx context.Context, authHeader string, body map[string]interface{}) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(body)
		require.NoError(t, err)
		req, err := http.NewRequestWithContext(ctx, "POST", "/api/v1/jobs", bytes.NewBuffer(data))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader)
		rr := httptest.NewRecorder()
		GetTestMux().ServeHTTP(rr, req)
		return rr
	}
	base := func(image string, osName string) map[string]interface{} {
		return map[string]interface{}{
			"name": "alias-job", "source_type": "git", "source_url": "https://github.com/test/repo.git",
			"source_ref": "main", "job_command": "echo ok", "runner_image": image,
			"characteristics": map[string]interface{}{"os": osName},
		}
	}

	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		// The fixture makes the organization of the user the default
		// organization, which a REST submit without a name uses.
		user, err := (&DataUtils{db: tx}).CreateUser(DataSetup{"Username": uniqueName("alias-user"), "Email": uniqueName("alias") + "@example.com"})
		require.NoError(t, err)
		authHeader, err := createAuthTokenHeader(ctx, tx, user.UserID)
		require.NoError(t, err)
		org, err := ps.GetDefaultOrganization(ctx)
		require.NoError(t, err)
		name := uniqueName("win")
		// No worker_class: the jobs use the default worker class (an alias
		// for another class refuses them, see the imagealias tests).
		restAlias := vmAlias(org.OrgID, nil, name, aliasDigestA)
		restAlias.Characteristics = characteristics.Characteristics{}
		require.NoError(t, ps.CreateImageAlias(ctx, restAlias))

		rr := post(t, ctx, authHeader, base(name, "windows"))
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
		// The job stores the plain form of an oci:// reference.
		wantRef := "registry.example.com/vm/" + name + ":2026.11@" + aliasDigestA
		assert.Equal(t, wantRef, resp["runner_image"])
		assert.Equal(t, name, resp["image_alias"])
		assert.Equal(t, name+" ("+wantRef+")", resp["image_display"])

		var job models.Job
		require.NoError(t, tx.First(&job, "job_id = ?", resp["job_id"]).Error)
		require.NotNil(t, job.ImageAlias)
		assert.Equal(t, name, *job.ImageAlias)
		assert.Equal(t, characteristics.StringValue(aliasDigestA), job.Characteristics[imagealias.DigestCharacteristic])
		var queue models.Queue
		require.NoError(t, tx.First(&queue, "queue_uuid = ?", job.QueueName).Error)
		assert.Equal(t, characteristics.StringValue(aliasDigestA), queue.Characteristics[imagealias.DigestCharacteristic],
			"the digest is in the queue characteristics")

		// A pinned reference that the alias pins is accepted. The job stores
		// the reference of the alias (the location that the job named does
		// not choose the pull location), with no alias name.
		rr = post(t, ctx, authHeader, base("registry.example.com/vm/other:tag@"+aliasDigestA, "windows"))
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		direct := map[string]interface{}{}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &direct))
		assert.Equal(t, wantRef, direct["runner_image"])
		_, directAlias := direct["image_alias"]
		assert.False(t, directAlias)
		assert.Equal(t, wantRef, direct["image_display"])

		// Refusals.
		for _, image := range []string{"no-such-alias", "registry.example.com/vm/other:tag", "registry.example.com/vm/other:tag@" + aliasDigestB} {
			rr = post(t, ctx, authHeader, base(image, "windows"))
			assert.Equal(t, http.StatusBadRequest, rr.Code, image)
			assert.Contains(t, rr.Body.String(), "image_alias", image)
		}

		// A job characteristic that conflicts with an alias characteristic
		// is refused at creation. The message names the alias, the key, the
		// value of the job, the value of the alias, and the fix.
		vsName := uniqueName("win-vs")
		vsAlias := vmAlias(org.OrgID, nil, vsName, aliasDigestB)
		vsAlias.Characteristics = characteristics.Characteristics{"vs": characteristics.StringValue("2022")}
		require.NoError(t, ps.CreateImageAlias(ctx, vsAlias))
		conflict := base(vsName, "windows")
		conflict["characteristics"] = map[string]interface{}{"os": "windows", "vs": "2019"}
		rr = post(t, ctx, authHeader, conflict)
		require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
		refusal := map[string]interface{}{}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &refusal))
		assert.Equal(t, "image_alias", refusal["error"])
		for _, want := range []string{`image alias "` + vsName + `"`, "vs=2022", "vs=2019", "Set the job characteristic vs to 2022"} {
			assert.Contains(t, refusal["message"], want)
		}

		// A container job with a plain reference routes as before.
		rr = post(t, ctx, authHeader, base("alpine:3.20", "linux"))
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		resp = map[string]interface{}{}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
		assert.Equal(t, "alpine:3.20", resp["runner_image"])
		_, hasAlias := resp["image_alias"]
		assert.False(t, hasAlias)
	})
}

// TestMigration068ImageAliases applies 000068 on a database at 000067,
// checks the new table and columns, and checks that Down removes them.
func TestMigration068ImageAliases(t *testing.T) {
	db := newScratchDBAt(t, "image_aliases", 67)
	require.NoError(t, goose.UpByOne(db, "migrations"))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.Equal(t, int64(68), version)

	count := func(query string, args ...any) int {
		var n int
		require.NoError(t, db.QueryRow(query, args...).Scan(&n))
		return n
	}
	columns := func() int {
		return count(`SELECT count(*) FROM information_schema.columns WHERE
			(table_name = 'jobs' AND column_name = 'image_alias' AND is_nullable = 'YES') OR
			(table_name = 'workers' AND column_name IN ('image_set_hash', 'image_state', 'image_state_version', 'image_state_detail', 'image_ready_digests') AND is_nullable = 'YES') OR
			(table_name = 'workers' AND column_name = 'image_set_version' AND is_nullable = 'NO' AND column_default = '0') OR
			(table_name = 'workflow_nodes' AND column_name IN ('carried_image_ref', 'carried_image_alias', 'carried_image_digest') AND is_nullable = 'YES')`)
	}
	table := func() int {
		return count(`SELECT count(*) FROM information_schema.tables WHERE table_name = 'image_aliases'`)
	}
	require.Equal(t, 10, columns(), "000068 must add nullable or defaulted columns")
	require.Equal(t, 1, table())

	// An old binary inserts a worker and a job without the new columns.
	_, err = db.Exec(`INSERT INTO worker_pools (name) VALUES ('p')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO workers (pool_id, worker_key, os, arch, characteristics) SELECT pool_id, 'k', 'linux', 'amd64', '{}' FROM worker_pools WHERE name = 'p'`)
	require.NoError(t, err)

	require.NoError(t, goose.Down(db, "migrations"))
	require.Equal(t, 0, columns(), "Down must drop the columns")
	require.Equal(t, 0, table(), "Down must drop the table")
	require.NoError(t, goose.UpByOne(db, "migrations"))
	require.Equal(t, 10, columns())
}
