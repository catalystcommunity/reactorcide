package test

import (
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
)

// Migration 000074 changes the runner image column defaults to the
// runnerbase image; Down restores the old defaults and Up applies again.
func TestRunnerImageDefaultMigration(t *testing.T) {
	const oldImage = "quay.io/catalystcommunity/reactorcide_runner"
	db := newScratchDBAt(t, "runner_image_default", 73)

	columnDefault := func(t *testing.T, db *sql.DB, table, column string) string {
		t.Helper()
		var def sql.NullString
		require.NoError(t, db.QueryRow(`SELECT column_default FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, table, column).Scan(&def))
		return def.String
	}
	check := func(t *testing.T, want string) {
		t.Helper()
		assert.Contains(t, columnDefault(t, db, "jobs", "runner_image"), want)
		assert.Contains(t, columnDefault(t, db, "projects", "default_runner_image"), want)
	}

	check(t, oldImage)
	require.NoError(t, goose.UpByOne(db, "migrations"))
	check(t, worker.DefaultRunnerImage)
	require.NoError(t, goose.Down(db, "migrations"))
	check(t, oldImage)
	require.NoError(t, goose.UpByOne(db, "migrations"))
	check(t, worker.DefaultRunnerImage)
}
