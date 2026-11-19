package test

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coredb"
)

// TestJobFailureClassMigrationBackfill runs migration 000047 over rows that
// exist before it and checks the conservative backfill, the new settings
// defaults, the CHECK constraints, and the Down migration.
func TestJobFailureClassMigrationBackfill(t *testing.T) {
	db := newScratchDBAt(t, "failure_class_migrate", 46)

	var orgID string
	require.NoError(t, db.QueryRow(`INSERT INTO organizations (name, display_name) VALUES ('fc-org', 'FC') RETURNING org_id`).Scan(&orgID))
	var projectID string
	require.NoError(t, db.QueryRow(`INSERT INTO projects (name, repo_url, org_id) VALUES ('fc', 'github.com/example/fc', $1) RETURNING project_id`, orgID).Scan(&projectID))

	insert := func(name, status string, exitCode *int, lastError string) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO jobs (org_id, name, job_command, status, exit_code, last_error) VALUES ($1, $2, 'true', $3, $4, $5)`,
			orgID, name, status, exitCode, lastError)
		require.NoError(t, err)
	}
	one, two := 1, 2
	insert("completed", "completed", nil, "")
	insert("running", "running", nil, "")
	insert("cancelled", "cancelled", nil, "cancelled")
	insert("timeout", "timeout", nil, "")
	insert("lost", "failed", nil, "lost: no lease after coordinator error; retries exhausted")
	insert("pull", "failed", nil, "pod failed to start: ErrImagePull - manifest unknown")
	insert("sched", "failed", nil, "pod failed to start: ResourceRequestTooLarge - too big")
	insert("exit", "failed", &two, "job exited with code 2")
	insert("spawn", "failed", &one, "failed to spawn job: boom")
	insert("unknown", "failed", nil, "something else")

	require.NoError(t, goose.UpTo(db, "migrations", 47))

	want := map[string]sql.NullString{
		"completed": {},
		"running":   {},
		"cancelled": {String: "cancelled", Valid: true},
		"timeout":   {String: "timeout", Valid: true},
		"lost":      {String: "infra.worker_lost", Valid: true},
		"pull":      {String: "infra.image_pull", Valid: true},
		"sched":     {String: "infra.scheduling", Valid: true},
		"exit":      {String: "job", Valid: true},
		"spawn":     {}, // exit 1 with a spawn error is not proven to be the job
		"unknown":   {},
	}
	rows, err := db.Query(`SELECT name, failure_class FROM jobs`)
	require.NoError(t, err)
	got := map[string]sql.NullString{}
	for rows.Next() {
		var name string
		var class sql.NullString
		require.NoError(t, rows.Scan(&name, &class))
		got[name] = class
	}
	require.NoError(t, rows.Close())
	require.Equal(t, want, got)

	var orgRetries int
	require.NoError(t, db.QueryRow(`SELECT infra_retries FROM organizations WHERE org_id = $1`, orgID).Scan(&orgRetries))
	require.Equal(t, 1, orgRetries)
	var projectRetries sql.NullInt64
	require.NoError(t, db.QueryRow(`SELECT infra_retries FROM projects WHERE project_id = $1`, projectID).Scan(&projectRetries))
	require.False(t, projectRetries.Valid, "a project inherits by default")

	_, err = db.Exec(`UPDATE jobs SET failure_class = 'bogus' WHERE name = 'exit'`)
	require.Error(t, err, "the CHECK constraint must refuse an unknown class")
	_, err = db.Exec(`UPDATE organizations SET infra_retries = 9 WHERE org_id = $1`, orgID)
	require.Error(t, err, "the CHECK constraint must refuse infra_retries above 5")

	require.NoError(t, goose.DownTo(db, "migrations", 46))
	var columns int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE (table_name = 'jobs' AND column_name IN ('failure_class', 'failure_detail'))
		   OR (table_name IN ('organizations', 'projects') AND column_name = 'infra_retries')`).Scan(&columns))
	require.Zero(t, columns)
}

// newScratchDBAt makes an empty database and migrates it to version.
func newScratchDBAt(t *testing.T, prefix string, version int64) *sql.DB {
	t.Helper()
	baseURI := os.Getenv("TEST_DB_URI")
	if baseURI == "" {
		t.Skip("TEST_DB_URI is not set")
	}
	parsedURI, err := url.Parse(baseURI)
	require.NoError(t, err)
	databaseName := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())

	adminDB, err := sql.Open("postgres", baseURI)
	require.NoError(t, err)
	_, err = adminDB.Exec(fmt.Sprintf(`CREATE DATABASE %q`, databaseName))
	require.NoError(t, err)

	scratchURI := *parsedURI
	scratchURI.Path = "/" + databaseName
	scratchDB, err := sql.Open("postgres", scratchURI.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = scratchDB.Close()
		_, _ = adminDB.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, databaseName))
		_ = adminDB.Close()
	})

	goose.SetBaseFS(coredb.Migrations)
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.UpTo(scratchDB, "migrations", version))
	return scratchDB
}
