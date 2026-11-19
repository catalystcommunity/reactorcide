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

const principalsMigrationVersion = 48
const systemPrincipalID = "00000000-0000-0000-0000-000000000001"

// newPrincipalsMigrationDB returns a scratch database migrated to the
// version before the principals migration.
func newPrincipalsMigrationDB(t *testing.T) *sql.DB {
	t.Helper()
	baseURI := os.Getenv("TEST_DB_URI")
	require.NotEmpty(t, baseURI)
	parsedURI, err := url.Parse(baseURI)
	require.NoError(t, err)
	databaseName := fmt.Sprintf("principals_migrate_%d", time.Now().UnixNano())
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
	require.NoError(t, goose.UpTo(scratchDB, "migrations", principalsMigrationVersion-1))
	return scratchDB
}

func scalar(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var value sql.NullString
	require.NoError(t, db.QueryRow(query, args...).Scan(&value), query)
	return value.String
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var value int
	require.NoError(t, db.QueryRow(query, args...).Scan(&value), query)
	return value
}

type legacyRows struct {
	userID, orgID, projectID             string
	serviceTokenID, instanceTokenID      string
	userTokenID, jobTokenID              string
	webhookEventID, scheduleEventID      string
	webhookJobID, scheduleJobID, groupID string
	workflowID                           string
}

// insertLegacyRows writes the rows of an installation before principals.
func insertLegacyRows(t *testing.T, db *sql.DB) legacyRows {
	t.Helper()
	var rows legacyRows
	require.NoError(t, db.QueryRow(`INSERT INTO users (username, email, password, salt)
		VALUES ('alice', 'alice@example.com', '\x00', '\x00') RETURNING user_id`).Scan(&rows.userID))
	require.NoError(t, db.QueryRow(`INSERT INTO organizations (name) VALUES ('acme') RETURNING org_id`).Scan(&rows.orgID))
	require.NoError(t, db.QueryRow(`INSERT INTO projects (name, repo_url, org_id) VALUES ('app', 'github.com/acme/app', $1)
		RETURNING project_id`, rows.orgID).Scan(&rows.projectID))
	_, err := db.Exec(`INSERT INTO auth_identities (user_id, subject, handle, domain)
		VALUES ($1, '0f0e@Example.com', 'Alice', 'example.com')`, rows.userID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO vcs_identity_links (provider, external_subject, user_id, verified_by)
		VALUES ('github', 'Alice-GH', $1, 'admin')`, rows.userID)
	require.NoError(t, err)
	insertToken := func(name, subjectType string, owner, user, job any) string {
		var id string
		require.NoError(t, db.QueryRow(`INSERT INTO api_tokens (token_hash, name, is_active, subject_type, owner_org_id, user_id, bound_job_id)
			VALUES (gen_random_bytes(32), $1, true, $2, $3, $4, $5) RETURNING token_id`,
			name, subjectType, owner, user, job).Scan(&id))
		return id
	}
	rows.serviceTokenID = insertToken("CI Deployer", "service_token", rows.orgID, nil, nil)
	// A second token with the same name in the same org gets a suffix.
	insertToken("ci deployer", "service_token", rows.orgID, nil, nil)
	rows.instanceTokenID = insertToken("admin", "instance_token", nil, nil, nil)
	rows.userTokenID = insertToken("alice laptop", "user_token", nil, rows.userID, nil)

	require.NoError(t, db.QueryRow(`INSERT INTO ci_events (org_id, project_id, source, provider, event_type, actor)
		VALUES ($1, $2, 'webhook', 'github', 'push', 'Octo-Cat') RETURNING event_id`, rows.orgID, rows.projectID).Scan(&rows.webhookEventID))
	require.NoError(t, db.QueryRow(`INSERT INTO ci_events (org_id, project_id, source, event_type)
		VALUES ($1, $2, 'schedule', 'schedule') RETURNING event_id`, rows.orgID, rows.projectID).Scan(&rows.scheduleEventID))
	insertJob := func(eventID any) string {
		var id string
		require.NoError(t, db.QueryRow(`INSERT INTO jobs (org_id, project_id, name, job_command, event_id)
			VALUES ($1, $2, 'build', 'make', $3) RETURNING job_id`, rows.orgID, rows.projectID, eventID).Scan(&id))
		return id
	}
	rows.webhookJobID = insertJob(rows.webhookEventID)
	rows.scheduleJobID = insertJob(rows.scheduleEventID)
	insertJob(nil)
	rows.jobTokenID = insertToken("job:x", "job_token", rows.orgID, nil, rows.webhookJobID)
	require.NoError(t, db.QueryRow(`INSERT INTO workflow_instances (user_id, org_id, project_id, name, workflow_security_id, event_id)
		VALUES ($1, $2, $3, 'ci', 'ci', $4) RETURNING workflow_id`, rows.userID, rows.orgID, rows.projectID, rows.webhookEventID).Scan(&rows.workflowID))

	_, err = db.Exec(`INSERT INTO ci_approvals (org_id, project_id, pr_number, head_repository, head_sha, person, kind, channel)
		VALUES ($1, $2, 1, 'acme/app', 'abc', 'github/reviewer-1', 'accept', 'comment'),
		       ($1, $2, 1, 'acme/app', 'abc', 'user:' || $3, 'run', 'ui')`, rows.orgID, rows.projectID, rows.userID)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`INSERT INTO groups (org_id, name) VALUES ($1, 'release') RETURNING group_id`, rows.orgID).Scan(&rows.groupID))
	_, err = db.Exec(`INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)`, rows.groupID, rows.userID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO audit_events (action, subject_type, actor_user_id) VALUES ('x', 'y', $1)`, rows.userID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO audit_events (action, subject_type, actor_credential_id, actor_credential_type)
		VALUES ('x', 'y', $1, 'service_token')`, rows.serviceTokenID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO audit_events (action, subject_type) VALUES ('webhook', 'project')`)
	require.NoError(t, err)
	return rows
}

func TestPrincipalsMigrationBackfill(t *testing.T) {
	db := newPrincipalsMigrationDB(t)
	rows := insertLegacyRows(t, db)
	require.NoError(t, goose.UpTo(db, "migrations", principalsMigrationVersion))

	// The system principal exists and has no roles.
	require.Equal(t, "system", scalar(t, db, `SELECT kind FROM principals WHERE principal_id = $1`, systemPrincipalID))
	require.Zero(t, count(t, db, `SELECT count(*) FROM role_assignments WHERE principal_id = $1`, systemPrincipalID))

	// A human principal with the user UUID, and its identities.
	require.Equal(t, "human", scalar(t, db, `SELECT kind FROM principals WHERE principal_id = $1`, rows.userID))
	for _, identity := range [][2]string{{"user", rows.userID}, {"linkkeys", "0f0e@example.com"},
		{"linkkeys", "alice@example.com"}, {"vcs", "github/alice-gh"}} {
		require.Equal(t, rows.userID, scalar(t, db, `SELECT principal_id FROM principal_identities
			WHERE identity_type = $1 AND identity_value = $2`, identity[0], identity[1]), identity)
	}

	// VCS logins seen in events and approvals became human principals.
	octo := scalar(t, db, `SELECT principal_id FROM principal_identities WHERE identity_type = 'vcs' AND identity_value = 'github/octo-cat'`)
	require.NotEmpty(t, octo)
	require.Equal(t, "human", scalar(t, db, `SELECT kind FROM principals WHERE principal_id = $1`, octo))
	reviewer := scalar(t, db, `SELECT principal_id FROM principal_identities WHERE identity_type = 'vcs' AND identity_value = 'github/reviewer-1'`)
	require.NotEmpty(t, reviewer)

	// Service and instance tokens became service principals with the token
	// UUID. Old tokens keep their authority (intersect_roles false).
	require.Equal(t, "service", scalar(t, db, `SELECT kind FROM principals WHERE principal_id = $1`, rows.serviceTokenID))
	require.Equal(t, "ci-deployer", scalar(t, db, `SELECT name FROM principals WHERE principal_id = $1`, rows.serviceTokenID))
	require.Equal(t, rows.orgID, scalar(t, db, `SELECT org_id FROM principals WHERE principal_id = $1`, rows.serviceTokenID))
	require.Equal(t, 2, count(t, db, `SELECT count(*) FROM principals WHERE kind = 'service' AND org_id = $1 AND name LIKE 'ci-deployer%'`, rows.orgID))
	require.Equal(t, "", scalar(t, db, `SELECT org_id FROM principals WHERE principal_id = $1`, rows.instanceTokenID))
	require.Zero(t, count(t, db, `SELECT count(*) FROM api_tokens WHERE intersect_roles`))
	require.Equal(t, rows.serviceTokenID, scalar(t, db, `SELECT principal_id FROM api_tokens WHERE token_id = $1`, rows.serviceTokenID))
	require.Equal(t, rows.userID, scalar(t, db, `SELECT principal_id FROM api_tokens WHERE token_id = $1`, rows.userTokenID))
	require.Equal(t, "", scalar(t, db, `SELECT principal_id FROM api_tokens WHERE token_id = $1`, rows.jobTokenID))
	require.Equal(t, rows.serviceTokenID, scalar(t, db, `SELECT principal_id FROM principal_identities WHERE identity_type = 'token' AND identity_value = $1`, rows.serviceTokenID))

	// Actor backfill: the webhook event and its job and workflow have the
	// sender; everything else has the system principal.
	require.Equal(t, octo, scalar(t, db, `SELECT actor_principal_id FROM ci_events WHERE event_id = $1`, rows.webhookEventID))
	require.Equal(t, "vcs:github/octo-cat", scalar(t, db, `SELECT actor_identity FROM ci_events WHERE event_id = $1`, rows.webhookEventID))
	require.Equal(t, systemPrincipalID, scalar(t, db, `SELECT actor_principal_id FROM ci_events WHERE event_id = $1`, rows.scheduleEventID))
	require.Equal(t, octo, scalar(t, db, `SELECT actor_principal_id FROM jobs WHERE job_id = $1`, rows.webhookJobID))
	require.Equal(t, octo, scalar(t, db, `SELECT actor_principal_id FROM workflow_instances WHERE workflow_id = $1`, rows.workflowID))
	require.Equal(t, systemPrincipalID, scalar(t, db, `SELECT actor_principal_id FROM jobs WHERE job_id = $1`, rows.scheduleJobID))
	require.Zero(t, count(t, db, `SELECT count(*) FROM jobs WHERE actor_principal_id IS NULL`))
	require.Equal(t, reviewer, scalar(t, db, `SELECT actor_principal_id FROM ci_approvals WHERE person = 'github/reviewer-1'`))
	require.Equal(t, rows.userID, scalar(t, db, `SELECT actor_principal_id FROM ci_approvals WHERE person LIKE 'user:%'`))
	require.Equal(t, rows.userID, scalar(t, db, `SELECT actor_principal_id FROM audit_events WHERE actor_user_id = $1`, rows.userID))
	require.Equal(t, rows.serviceTokenID, scalar(t, db, `SELECT actor_principal_id FROM audit_events WHERE actor_credential_id = $1`, rows.serviceTokenID))
	require.Equal(t, systemPrincipalID, scalar(t, db, `SELECT actor_principal_id FROM audit_events WHERE action = 'webhook'`))

	// Group members have the principal.
	require.Equal(t, rows.userID, scalar(t, db, `SELECT principal_id FROM group_members WHERE group_id = $1`, rows.groupID))
}

func TestPrincipalsMigrationTriggers(t *testing.T) {
	db := newPrincipalsMigrationDB(t)
	rows := insertLegacyRows(t, db)
	require.NoError(t, goose.UpTo(db, "migrations", principalsMigrationVersion))

	// A new user gets a human principal.
	var bob string
	require.NoError(t, db.QueryRow(`INSERT INTO users (username, email, password, salt)
		VALUES ('bob', 'bob@example.com', '\x00', '\x00') RETURNING user_id`).Scan(&bob))
	require.Equal(t, "human", scalar(t, db, `SELECT kind FROM principals WHERE principal_id = $1`, bob))
	_, err := db.Exec(`UPDATE users SET status = 'disabled' WHERE user_id = $1`, bob)
	require.NoError(t, err)
	require.Equal(t, "disabled", scalar(t, db, `SELECT status FROM principals WHERE principal_id = $1`, bob))

	// A LinkKeys login adds both identity forms.
	_, err = db.Exec(`INSERT INTO auth_identities (user_id, subject, handle, domain) VALUES ($1, 'bbbb@example.com', 'bob', 'example.com')`, bob)
	require.NoError(t, err)
	require.Equal(t, bob, scalar(t, db, `SELECT principal_id FROM principal_identities WHERE identity_type = 'linkkeys' AND identity_value = 'bob@example.com'`))

	// A new service token without a principal gets a service principal.
	var tokenID string
	require.NoError(t, db.QueryRow(`INSERT INTO api_tokens (token_hash, name, is_active, subject_type, owner_org_id)
		VALUES (gen_random_bytes(32), 'Release Bot', true, 'service_token', $1) RETURNING token_id`, rows.orgID).Scan(&tokenID))
	require.Equal(t, tokenID, scalar(t, db, `SELECT principal_id FROM api_tokens WHERE token_id = $1`, tokenID))
	require.Equal(t, "release-bot", scalar(t, db, `SELECT name FROM principals WHERE principal_id = $1`, tokenID))
	// A token issued for an existing principal keeps it.
	var boundID string
	require.NoError(t, db.QueryRow(`INSERT INTO api_tokens (token_hash, name, is_active, subject_type, owner_org_id, principal_id, intersect_roles)
		VALUES (gen_random_bytes(32), 'second', true, 'service_token', $1, $2, true) RETURNING token_id`, rows.orgID, tokenID).Scan(&boundID))
	require.Equal(t, tokenID, scalar(t, db, `SELECT principal_id FROM principal_identities WHERE identity_type = 'token' AND identity_value = $1`, boundID))

	// A job inherits the actor of its event, then of its parent job; with
	// neither it gets the system principal.
	var eventJob, childJob, lonelyJob string
	require.NoError(t, db.QueryRow(`INSERT INTO jobs (org_id, name, job_command, event_id) VALUES ($1, 'a', 'x', $2) RETURNING job_id`,
		rows.orgID, rows.webhookEventID).Scan(&eventJob))
	require.NoError(t, db.QueryRow(`INSERT INTO jobs (org_id, name, job_command, parent_job_id) VALUES ($1, 'b', 'x', $2) RETURNING job_id`,
		rows.orgID, eventJob).Scan(&childJob))
	require.NoError(t, db.QueryRow(`INSERT INTO jobs (org_id, name, job_command) VALUES ($1, 'c', 'x') RETURNING job_id`, rows.orgID).Scan(&lonelyJob))
	octo := scalar(t, db, `SELECT principal_id FROM principal_identities WHERE identity_type = 'vcs' AND identity_value = 'github/octo-cat'`)
	require.Equal(t, octo, scalar(t, db, `SELECT actor_principal_id FROM jobs WHERE job_id = $1`, eventJob))
	require.Equal(t, octo, scalar(t, db, `SELECT actor_principal_id FROM jobs WHERE job_id = $1`, childJob))
	require.Equal(t, "vcs:github/octo-cat", scalar(t, db, `SELECT actor_identity FROM jobs WHERE job_id = $1`, childJob))
	require.Equal(t, systemPrincipalID, scalar(t, db, `SELECT actor_principal_id FROM jobs WHERE job_id = $1`, lonelyJob))

	// An audit record of a job token gets the actor of the job.
	var jobToken string
	require.NoError(t, db.QueryRow(`INSERT INTO api_tokens (token_hash, name, is_active, subject_type, owner_org_id, bound_job_id)
		VALUES (gen_random_bytes(32), 'job', true, 'job_token', $1, $2) RETURNING token_id`, rows.orgID, eventJob).Scan(&jobToken))
	require.Equal(t, "", scalar(t, db, `SELECT principal_id FROM api_tokens WHERE token_id = $1`, jobToken))
	_, err = db.Exec(`INSERT INTO audit_events (action, subject_type, actor_credential_id) VALUES ('job.submit', 'job', $1)`, jobToken)
	require.NoError(t, err)
	require.Equal(t, octo, scalar(t, db, `SELECT actor_principal_id FROM audit_events WHERE action = 'job.submit'`))

	// Linking a login that only a webhook saw merges its principal into the
	// user.
	_, err = db.Exec(`INSERT INTO vcs_identity_links (provider, external_subject, user_id, verified_by) VALUES ('github', 'octo-cat', $1, 'admin')`, bob)
	require.NoError(t, err)
	require.Equal(t, bob, scalar(t, db, `SELECT principal_id FROM principal_identities WHERE identity_type = 'vcs' AND identity_value = 'github/octo-cat'`))
	require.Equal(t, bob, scalar(t, db, `SELECT merged_into FROM principals WHERE principal_id = $1`, octo))
	// Unlinking removes the identity from the user.
	_, err = db.Exec(`DELETE FROM vcs_identity_links WHERE external_subject = 'octo-cat'`)
	require.NoError(t, err)
	require.Zero(t, count(t, db, `SELECT count(*) FROM principal_identities WHERE identity_type = 'vcs' AND identity_value = 'github/octo-cat'`))

	// A principal role assignment is accepted.
	_, err = db.Exec(`INSERT INTO role_assignments (principal_type, principal_id, scope_type, scope_id, role)
		VALUES ('principal', $1, 'org', $2, 'member')`, tokenID, rows.orgID)
	require.NoError(t, err)
}

// A VCS identity link must not merge a principal that has authority (a
// service principal with org roles, or a human with a group membership)
// into a user, and must not move its identity: that is a cross-org
// privilege escalation.
func TestPrincipalsVCSLinkRefusesEscalation(t *testing.T) {
	db := newPrincipalsMigrationDB(t)
	rows := insertLegacyRows(t, db)
	require.NoError(t, goose.UpTo(db, "migrations", principalsMigrationVersion))
	var mallory string
	require.NoError(t, db.QueryRow(`INSERT INTO users (username, email, password, salt)
		VALUES ('mallory', 'mallory@example.com', '\x00', '\x00') RETURNING user_id`).Scan(&mallory))

	// The service principal of acme has an org role and a VCS identity.
	_, err := db.Exec(`INSERT INTO role_assignments (principal_type, principal_id, scope_type, scope_id, role)
		VALUES ('principal', $1, 'org', $2, 'admin')`, rows.serviceTokenID, rows.orgID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO principal_identities (identity_type, identity_value, principal_id, verified_by)
		VALUES ('vcs', 'github/acme-bot', $1, 'admin')`, rows.serviceTokenID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO vcs_identity_links (provider, external_subject, user_id, verified_by) VALUES ('github', 'acme-bot', $1, 'admin')`, mallory)
	require.Error(t, err, "a service principal must not merge into a user")
	require.Equal(t, rows.serviceTokenID, scalar(t, db, `SELECT principal_id FROM principal_identities WHERE identity_type = 'vcs' AND identity_value = 'github/acme-bot'`))
	require.Equal(t, "", scalar(t, db, `SELECT merged_into FROM principals WHERE principal_id = $1`, rows.serviceTokenID))
	require.Equal(t, 1, count(t, db, `SELECT count(*) FROM role_assignments WHERE principal_id = $1`, rows.serviceTokenID))
	require.Zero(t, count(t, db, `SELECT count(*) FROM role_assignments WHERE principal_id = $1`, mallory))
	require.Zero(t, count(t, db, `SELECT count(*) FROM vcs_identity_links WHERE external_subject = 'acme-bot'`))

	// A webhook principal that an admin put in a group is not webhook-only.
	octo := scalar(t, db, `SELECT principal_id FROM principal_identities WHERE identity_type = 'vcs' AND identity_value = 'github/octo-cat'`)
	_, err = db.Exec(`INSERT INTO group_members (group_id, principal_id) VALUES ($1, $2)`, rows.groupID, octo)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO vcs_identity_links (provider, external_subject, user_id, verified_by) VALUES ('github', 'octo-cat', $1, 'admin')`, mallory)
	require.Error(t, err, "a principal with a group membership must not merge")
	require.Equal(t, octo, scalar(t, db, `SELECT principal_id FROM principal_identities WHERE identity_type = 'vcs' AND identity_value = 'github/octo-cat'`))
	require.Zero(t, count(t, db, `SELECT count(*) FROM group_members WHERE principal_id = $1`, mallory))

	// The identity of another user is not moved either.
	_, err = db.Exec(`INSERT INTO vcs_identity_links (provider, external_subject, user_id, verified_by) VALUES ('github', 'alice-gh', $1, 'admin')`, mallory)
	require.Error(t, err)
	require.Equal(t, rows.userID, scalar(t, db, `SELECT principal_id FROM principal_identities WHERE identity_type = 'vcs' AND identity_value = 'github/alice-gh'`))

	// The rule function agrees.
	require.Equal(t, "false", scalar(t, db, `SELECT principals_webhook_only($1)::text`, rows.serviceTokenID))
	require.Equal(t, "false", scalar(t, db, `SELECT principals_webhook_only($1)::text`, octo))
	reviewer := scalar(t, db, `SELECT principal_id FROM principal_identities WHERE identity_type = 'vcs' AND identity_value = 'github/reviewer-1'`)
	require.Equal(t, "true", scalar(t, db, `SELECT principals_webhook_only($1)::text`, reviewer))

	// The actor indexes exist.
	require.Equal(t, 2, count(t, db, `SELECT count(*) FROM pg_indexes WHERE indexname IN ('ci_approvals_actor_principal_idx', 'audit_events_actor_principal_idx')`))
}

func TestPrincipalsMigrationDown(t *testing.T) {
	db := newPrincipalsMigrationDB(t)
	rows := insertLegacyRows(t, db)
	require.NoError(t, goose.UpTo(db, "migrations", principalsMigrationVersion))
	// A service principal member cannot exist in the old table; Down drops it.
	_, err := db.Exec(`INSERT INTO group_members (group_id, principal_id) VALUES ($1, $2)`, rows.groupID, rows.serviceTokenID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO role_assignments (principal_type, principal_id, scope_type, scope_id, role)
		VALUES ('principal', $1, 'org', $2, 'member')`, rows.serviceTokenID, rows.orgID)
	require.NoError(t, err)

	// A token issued for a principal intersects with its roles. Without
	// the column it would get its full capabilities, so Down revokes it.
	var issuedID string
	require.NoError(t, db.QueryRow(`INSERT INTO api_tokens (token_hash, name, is_active, subject_type, owner_org_id, principal_id, intersect_roles)
		VALUES (gen_random_bytes(32), 'issued', true, 'service_token', $1, $2, true) RETURNING token_id`, rows.orgID, rows.serviceTokenID).Scan(&issuedID))

	require.NoError(t, goose.DownTo(db, "migrations", principalsMigrationVersion-1))
	require.Equal(t, "false", scalar(t, db, `SELECT is_active::text FROM api_tokens WHERE token_id = $1`, issuedID))
	require.NotEmpty(t, scalar(t, db, `SELECT revoked_at::text FROM api_tokens WHERE token_id = $1`, issuedID))
	require.Equal(t, "true", scalar(t, db, `SELECT is_active::text FROM api_tokens WHERE token_id = $1`, rows.serviceTokenID), "old tokens keep working")
	require.Zero(t, count(t, db, `SELECT count(*) FROM information_schema.tables WHERE table_name IN ('principals', 'principal_identities')`))
	require.Zero(t, count(t, db, `SELECT count(*) FROM information_schema.columns WHERE column_name = 'actor_principal_id'`))
	require.Equal(t, 1, count(t, db, `SELECT count(*) FROM group_members WHERE group_id = $1`, rows.groupID))
	_, err = db.Exec(`INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)`, rows.groupID, rows.userID)
	require.Error(t, err, "the old primary key (group_id, user_id) is back")
	// Users and tokens still insert without the triggers.
	_, err = db.Exec(`INSERT INTO users (username, email, password, salt) VALUES ('carol', 'c@example.com', '\x00', '\x00')`)
	require.NoError(t, err)

	// And the migration applies again.
	require.NoError(t, goose.UpTo(db, "migrations", principalsMigrationVersion))
	require.Equal(t, 1, count(t, db, `SELECT count(*) FROM principals p JOIN users u ON u.user_id = p.principal_id WHERE u.username = 'carol'`))
}
