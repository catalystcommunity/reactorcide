package test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/auth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestLoginHistoryGuard runs the startup guard of mode none against
// Postgres: a fresh instance in mode none works, an instance that ran rp then
// starts in mode none is refused, and the override clears the history.
func TestLoginHistoryGuard(t *testing.T) {
	origMode, origGuard := config.UIAuthMode, config.NoLoginGuardPassed()
	defer func() {
		config.UIAuthMode = origMode
		config.SetNoLoginGuardPassed(origGuard)
	}()

	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		s := postgres_store.PostgresStore
		_, err := s.GetAuthLoginHistory(ctx)
		require.ErrorIs(t, err, store.ErrNotFound, "the test database is a fresh instance")

		config.UIAuthMode = config.UIAuthModeNone
		require.NoError(t, auth.EnforceLoginHistory(ctx, s, false), "fresh instance in mode none")
		assert.True(t, config.NoLoginMode())

		config.UIAuthMode = config.UIAuthModeRP
		require.NoError(t, auth.EnforceLoginHistory(ctx, s, false), "start in mode rp")
		assert.False(t, config.NoLoginMode())
		history, err := s.GetAuthLoginHistory(ctx)
		require.NoError(t, err)
		assert.Equal(t, "rp", history.LoginMode)
		assert.Equal(t, models.AuthLoginHistorySourceStartup, history.Source)
		first := history.FirstMarkedAt

		config.UIAuthMode = config.UIAuthModeLocalRP
		require.NoError(t, auth.EnforceLoginHistory(ctx, s, false), "start in mode local-rp")
		history, err = s.GetAuthLoginHistory(ctx)
		require.NoError(t, err)
		assert.Equal(t, "local-rp", history.LoginMode)
		assert.True(t, history.FirstMarkedAt.Equal(first), "a new mark keeps first_marked_at")

		// The REACTORCIDE_UI_AUTH_MODE setting is lost (the default is none).
		config.UIAuthMode = config.UIAuthModeNone
		err = auth.EnforceLoginHistory(ctx, s, false)
		require.Error(t, err)
		assert.True(t, errors.Is(err, auth.ErrDowngradeToNone))
		assert.Contains(t, err.Error(), "REACTORCIDE_UI_AUTH_ALLOW_DOWNGRADE_TO_NONE")
		assert.False(t, config.NoLoginMode(), "a refused downgrade gives no no-login behaviour")
		_, err = s.GetAuthLoginHistory(ctx)
		require.NoError(t, err, "a refused downgrade keeps the history")

		require.NoError(t, auth.EnforceLoginHistory(ctx, s, true), "the override")
		assert.True(t, config.NoLoginMode())
		_, err = s.GetAuthLoginHistory(ctx)
		require.ErrorIs(t, err, store.ErrNotFound, "the override deletes the history")

		require.NoError(t, auth.EnforceLoginHistory(ctx, s, false), "the next start needs no override")
		assert.True(t, config.NoLoginMode())
	})
}

// TestLoginHistoryMigrationBackfill checks migration 000073: data that only
// a login mode makes marks the instance, so an existing production instance
// is covered on the first start of the new version. Data that mode none can
// make (the bootstrap-admin identity) does not.
func TestLoginHistoryMigrationBackfill(t *testing.T) {
	insertUser := func(t *testing.T, db *sql.DB, name string) string {
		t.Helper()
		var userID string
		require.NoError(t, db.QueryRow(`INSERT INTO users (username, email, password, salt)
			VALUES ($1, $1 || '@example.com', '\x00', '\x00') RETURNING user_id`, name).Scan(&userID))
		return userID
	}
	cases := []struct {
		name   string
		seed   func(t *testing.T, db *sql.DB)
		marked bool
	}{
		{"empty instance", func(*testing.T, *sql.DB) {}, false},
		{"bootstrap-admin identity only", func(t *testing.T, db *sql.DB) {
			userID := insertUser(t, db, "bootstrap-admin")
			_, err := db.Exec(`INSERT INTO auth_identities (user_id, subject, handle, domain)
				VALUES ($1, 'bootstrap-admin', 'bootstrap-admin', 'reactorcide.local-bootstrap')`, userID)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO principals (principal_id, kind, name, display_name)
				VALUES ($1, 'human', 'bootstrap-admin', 'Bootstrap Admin') ON CONFLICT DO NOTHING`, userID)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO principal_identities (identity_type, identity_value, principal_id, verified_by)
				VALUES ('linkkeys', 'bootstrap-admin@reactorcide.local-bootstrap', $1, 'linkkeys')
				ON CONFLICT DO NOTHING`, userID)
			require.NoError(t, err)
		}, false},
		{"a LinkKeys login identity", func(t *testing.T, db *sql.DB) {
			userID := insertUser(t, db, "alice")
			_, err := db.Exec(`INSERT INTO auth_identities (user_id, subject, handle, domain)
				VALUES ($1, '0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b', 'alice', 'example.com')`, userID)
			require.NoError(t, err)
		}, true},
		{"a trusted identity", func(t *testing.T, db *sql.DB) {
			_, err := db.Exec(`INSERT INTO auth_trusted_identities (domain, handle, source) VALUES ('example.com', '', 'config')`)
			require.NoError(t, err)
		}, true},
		{"a trusted domain pattern", func(t *testing.T, db *sql.DB) {
			_, err := db.Exec(`INSERT INTO auth_trusted_domain_patterns (pattern) VALUES ('.*\.example\.com')`)
			require.NoError(t, err)
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newScratchDBAt(t, "login_history", 72)
			tc.seed(t, db)
			require.NoError(t, goose.UpByOne(db, "migrations"))
			var count int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM auth_login_history`).Scan(&count))
			if !tc.marked {
				assert.Equal(t, 0, count)
				return
			}
			require.Equal(t, 1, count)
			var mode, source string
			require.NoError(t, db.QueryRow(`SELECT login_mode, source FROM auth_login_history`).Scan(&mode, &source))
			assert.Equal(t, "unknown", mode)
			assert.Equal(t, models.AuthLoginHistorySourceExistingData, source)

			// The Down removes the table; a second Up marks again.
			require.NoError(t, goose.Down(db, "migrations"))
			require.NoError(t, goose.UpByOne(db, "migrations"))
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM auth_login_history`).Scan(&count))
			assert.Equal(t, 1, count)
		})
	}
}
