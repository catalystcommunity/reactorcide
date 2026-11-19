package test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// TestJobRunAsAdministrator checks that the REST create path stores
// run_as.administrator in jobs.run_as_administrator, returns it, keeps NULL
// when it is not set, and refuses a non-boolean value.
func TestJobRunAsAdministrator(t *testing.T) {
	post := func(t *testing.T, ctx context.Context, tx *gorm.DB, body map[string]interface{}) *httptest.ResponseRecorder {
		t.Helper()
		user, err := (&DataUtils{db: tx}).CreateUser(DataSetup{"Username": "runas-admin", "Email": "runas-admin@example.com"})
		require.NoError(t, err)
		authHeader, err := createAuthTokenHeader(ctx, tx, user.UserID)
		require.NoError(t, err)
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
	base := func() map[string]interface{} {
		return map[string]interface{}{
			"name": "run-as-admin", "source_type": "git", "source_url": "https://github.com/test/repo.git",
			"source_ref": "main", "job_command": "echo ok",
		}
	}

	t.Run("false is stored and returned", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			body := base()
			body["run_as_administrator"] = false
			rr := post(t, ctx, tx, body)
			require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
			var resp map[string]interface{}
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
			assert.Equal(t, false, resp["run_as_administrator"])

			var job models.Job
			require.NoError(t, tx.First(&job, "job_id = ?", resp["job_id"]).Error)
			require.NotNil(t, job.RunAsAdministrator)
			assert.False(t, *job.RunAsAdministrator)
		})
	})

	t.Run("unset stays NULL and is omitted", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			rr := post(t, ctx, tx, base())
			require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
			var resp map[string]interface{}
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
			_, present := resp["run_as_administrator"]
			assert.False(t, present)

			var isNull bool
			require.NoError(t, tx.Raw(`SELECT run_as_administrator IS NULL FROM jobs WHERE job_id = ?`, resp["job_id"]).Scan(&isNull).Error)
			assert.True(t, isNull)
		})
	})

	t.Run("a non-boolean value is refused", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			body := base()
			body["run_as_administrator"] = "false"
			rr := post(t, ctx, tx, body)
			assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
		})
	})
}

// TestMigration067RunAsAdministrator applies 000067 on a database at 000066,
// checks that it adds a nullable boolean column, and checks that Down
// removes the column.
func TestMigration067RunAsAdministrator(t *testing.T) {
	db := newScratchDBAt(t, "run_as_admin", 66)
	require.NoError(t, goose.UpByOne(db, "migrations"))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.Equal(t, int64(67), version)

	columnExists := func() bool {
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'jobs' AND column_name = 'run_as_administrator' AND data_type = 'boolean' AND is_nullable = 'YES'`).Scan(&n))
		return n == 1
	}
	require.True(t, columnExists(), "000067 must add a nullable boolean column")

	require.NoError(t, goose.Down(db, "migrations"))
	require.False(t, columnExists(), "Down must drop the column")
	require.NoError(t, goose.UpByOne(db, "migrations"))
	require.True(t, columnExists())
}
