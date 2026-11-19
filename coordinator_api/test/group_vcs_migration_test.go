package test

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coredb"
)

const groupVCSMigrationVersion = 54

func newGroupVCSScratchDB(t *testing.T, prefix string, version int64) *sql.DB {
	t.Helper()
	baseURI := os.Getenv("TEST_DB_URI")
	require.NotEmpty(t, baseURI)
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

// 000054 keeps existing members manual, backfills history, and its Down
// removes only synced memberships.
func TestGroupVCSMigrationUpDown(t *testing.T) {
	db := newGroupVCSScratchDB(t, "group_vcs_migrate", groupVCSMigrationVersion-1)
	var userID, orgID, groupID string
	require.NoError(t, db.QueryRow(`INSERT INTO users (username, email, password, salt)
		VALUES ('alice', 'alice@example.com', '\x00', '\x00') RETURNING user_id`).Scan(&userID))
	require.NoError(t, db.QueryRow(`INSERT INTO organizations (name) VALUES ('acme') RETURNING org_id`).Scan(&orgID))
	require.NoError(t, db.QueryRow(`INSERT INTO groups (org_id, name) VALUES ($1, 'release') RETURNING group_id`, orgID).Scan(&groupID))
	_, err := db.Exec(`INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)`, groupID, userID)
	require.NoError(t, err)

	require.NoError(t, goose.UpTo(db, "migrations", groupVCSMigrationVersion))
	assert.Equal(t, "manual", scalar(t, db, `SELECT source FROM group_members WHERE group_id = $1`, groupID))
	assert.Equal(t, 1, count(t, db, `SELECT count(*) FROM group_membership_history WHERE group_id = $1 AND change = 'added' AND group_name = 'release'`, groupID))

	// A synced member, then a deleted group: history keeps the name.
	var linkID, webhookPrincipal string
	require.NoError(t, db.QueryRow(`INSERT INTO group_vcs_links (group_id, provider, owner, team_slug)
		VALUES ($1, 'github', 'acme', 'platform') RETURNING link_id`, groupID).Scan(&linkID))
	require.NoError(t, db.QueryRow(`INSERT INTO principals (kind, name, display_name) VALUES ('human', 'github/bob', 'bob')
		RETURNING principal_id`).Scan(&webhookPrincipal))
	_, err = db.Exec(`INSERT INTO principal_identities (identity_type, identity_value, principal_id, verified_by)
		VALUES ('vcs', 'github/bob', $1, 'webhook')`, webhookPrincipal)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO group_vcs_link_members (link_id, principal_id, vcs_login) VALUES ($1, $2, 'bob')`, linkID, webhookPrincipal)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO group_members (group_id, principal_id, source) VALUES ($1, $2, 'vcs_sync')`, groupID, webhookPrincipal)
	require.NoError(t, err)
	assert.Equal(t, "true", scalar(t, db, `SELECT principals_webhook_only($1)::text`, webhookPrincipal),
		"a synced membership does not stop a merge")

	require.NoError(t, goose.DownTo(db, "migrations", groupVCSMigrationVersion-1))
	assert.Equal(t, 1, count(t, db, `SELECT count(*) FROM group_members WHERE group_id = $1`, groupID), "the manual member stays")
	assert.Equal(t, 0, count(t, db, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'group_vcs_links'`))
	// The restored 000048 merge works.
	_, err = db.Exec(`SELECT principals_merge($1, $2)`, webhookPrincipal, userID)
	require.NoError(t, err)

	require.NoError(t, goose.UpTo(db, "migrations", groupVCSMigrationVersion))
	_, err = db.Exec(`DELETE FROM groups WHERE group_id = $1`, groupID)
	require.NoError(t, err)
	assert.Equal(t, "release", scalar(t, db, `SELECT group_name FROM group_membership_history
		WHERE group_id = $1 AND change = 'removed' ORDER BY history_id DESC LIMIT 1`, groupID))
}
