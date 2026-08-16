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

func TestOrganizationMigrationAssignsLegacyOwnerlessProjects(t *testing.T) {
	scratchDB := newOrganizationMigrationDB(t)

	var userID string
	require.NoError(t, scratchDB.QueryRow(
		`INSERT INTO users (username, email, password, salt)
		 VALUES ('legacy-owner', 'legacy-owner@example.com', '\x00', '\x00')
		 RETURNING user_id`,
	).Scan(&userID))
	insertOwnerlessProjects(t, scratchDB)

	require.NoError(t, goose.Up(scratchDB, "migrations"))

	var projectCount int
	var assignedCount int
	require.NoError(t, scratchDB.QueryRow(
		`SELECT count(*), count(*) FILTER (WHERE org_id = $1) FROM projects`,
		userID,
	).Scan(&projectCount, &assignedCount))
	require.Equal(t, 2, projectCount)
	require.Equal(t, projectCount, assignedCount)

	var organizationCount int
	require.NoError(t, scratchDB.QueryRow(
		`SELECT count(*) FROM organizations WHERE org_id = $1`,
		userID,
	).Scan(&organizationCount))
	require.Equal(t, 1, organizationCount)
}

func TestOrganizationMigrationCreatesOwnerForTokenOnlyProjects(t *testing.T) {
	scratchDB := newOrganizationMigrationDB(t)
	insertOwnerlessProjects(t, scratchDB)

	require.NoError(t, goose.Up(scratchDB, "migrations"))

	var userCount int
	require.NoError(t, scratchDB.QueryRow(`SELECT count(*) FROM users`).Scan(&userCount))
	require.Zero(t, userCount)

	var organizationID string
	var organizationName string
	require.NoError(t, scratchDB.QueryRow(
		`SELECT org_id, name FROM organizations`,
	).Scan(&organizationID, &organizationName))
	require.Equal(t, "default", organizationName)

	var projectCount int
	var assignedCount int
	var anonymousCount int
	require.NoError(t, scratchDB.QueryRow(
		`SELECT count(*),
		        count(*) FILTER (WHERE org_id = $1),
		        count(*) FILTER (WHERE user_id IS NULL)
		   FROM projects`,
		organizationID,
	).Scan(&projectCount, &assignedCount, &anonymousCount))
	require.Equal(t, 2, projectCount)
	require.Equal(t, projectCount, assignedCount)
	require.Equal(t, projectCount, anonymousCount)

	var defaultOrganizationID string
	require.NoError(t, scratchDB.QueryRow(
		`SELECT trim(both '"' from value::text)
		   FROM global_settings
		  WHERE key = 'default_org_id'`,
	).Scan(&defaultOrganizationID))
	require.Equal(t, organizationID, defaultOrganizationID)
}

func newOrganizationMigrationDB(t *testing.T) *sql.DB {
	t.Helper()

	baseURI := os.Getenv("TEST_DB_URI")
	require.NotEmpty(t, baseURI)

	parsedURI, err := url.Parse(baseURI)
	require.NoError(t, err)
	databaseName := fmt.Sprintf("organization_migrate_%d", time.Now().UnixNano())

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
	require.NoError(t, goose.UpTo(scratchDB, "migrations", 24))
	return scratchDB
}

func insertOwnerlessProjects(t *testing.T, database *sql.DB) {
	t.Helper()
	for index := 1; index <= 2; index++ {
		_, err := database.Exec(
			`INSERT INTO projects (name, repo_url) VALUES ($1, $2)`,
			fmt.Sprintf("legacy-project-%d", index),
			fmt.Sprintf("github.com/example/legacy-project-%d", index),
		)
		require.NoError(t, err)
	}
}
