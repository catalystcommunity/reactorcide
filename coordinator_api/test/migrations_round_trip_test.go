package test

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

const (
	// roundTripBase is the last migration in the production release that the
	// 2026-11-16 work packages start from.
	roundTripBase int64 = 46
	// roundTripHead is the last migration of the 2026-11-16 work packages.
	roundTripHead int64 = 65
)

// TestMigrations047To065RoundTrip applies every work package migration in
// order, one at a time, on a database at 000046 that holds existing rows. It
// then runs each Down back to 000046, checks that the schema is the same as
// before, and applies all of the migrations again. This is the release path
// and the rollback path that production uses.
func TestMigrations047To065RoundTrip(t *testing.T) {
	db := newScratchDBAt(t, "wp_round_trip", roundTripBase)
	seedRoundTripRows(t, db)

	base := schemaSnapshot(t, db)

	stepUp := func(pass string) {
		t.Helper()
		for want := roundTripBase + 1; want <= roundTripHead; want++ {
			require.NoError(t, goose.UpByOne(db, "migrations"), "%s: up to %06d", pass, want)
			got, err := goose.GetDBVersion(db)
			require.NoError(t, err)
			require.Equal(t, want, got, "%s: migrations must apply one at a time in order", pass)
		}
	}

	stepUp("first up")
	head := schemaSnapshot(t, db)
	require.NotEqual(t, base, head, "the snapshot must see the schema changes of 000047-000065")

	for want := roundTripHead - 1; want >= roundTripBase; want-- {
		require.NoError(t, goose.Down(db, "migrations"), "down to %06d", want)
		got, err := goose.GetDBVersion(db)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	requireSameSchema(t, base, schemaSnapshot(t, db), "after Down to 000046")

	stepUp("second up")
	requireSameSchema(t, head, schemaSnapshot(t, db), "after the second Up to 000065")

	// The rows that existed before the release are still there.
	var jobs int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM jobs WHERE name LIKE 'round-trip-%'`).Scan(&jobs))
	require.Equal(t, 3, jobs)
}

// seedRoundTripRows adds rows like the production rows that the migrations
// backfill: an organization, a project, and finished and running jobs.
func seedRoundTripRows(t *testing.T, db *sql.DB) {
	t.Helper()
	var orgID string
	require.NoError(t, db.QueryRow(`INSERT INTO organizations (name, display_name) VALUES ('round-trip-org', 'Round Trip') RETURNING org_id`).Scan(&orgID))
	var projectID string
	require.NoError(t, db.QueryRow(`INSERT INTO projects (name, repo_url, org_id) VALUES ('round-trip', 'github.com/example/round-trip', $1) RETURNING project_id`, orgID).Scan(&projectID))
	two := 2
	for _, row := range []struct {
		name, status string
		exitCode     *int
		lastError    string
	}{
		{"round-trip-completed", "completed", nil, ""},
		{"round-trip-failed", "failed", &two, "job exited with code 2"},
		{"round-trip-running", "running", nil, ""},
	} {
		_, err := db.Exec(`INSERT INTO jobs (org_id, project_id, name, job_command, status, exit_code, last_error) VALUES ($1, $2, $3, 'true', $4, $5, $6)`,
			orgID, projectID, row.name, row.status, row.exitCode, row.lastError)
		require.NoError(t, err)
	}
}

// schemaSnapshot returns one sorted line for each column, constraint, index,
// trigger, function, and enum value in the public schema, except the goose
// version table.
func schemaSnapshot(t *testing.T, db *sql.DB) []string {
	t.Helper()
	queries := map[string]string{
		"column": `SELECT table_name || '.' || column_name || ' ' || data_type || ' null=' || is_nullable || ' default=' || COALESCE(column_default, '')
			FROM information_schema.columns WHERE table_schema = 'public' AND table_name <> 'goose_db_version'`,
		"constraint": `SELECT c.conrelid::regclass::text || ' ' || c.conname || ' ' || pg_get_constraintdef(c.oid)
			FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace
			WHERE n.nspname = 'public' AND c.conrelid <> 0 AND c.conrelid::regclass::text <> 'goose_db_version'`,
		"index": `SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename <> 'goose_db_version'`,
		"trigger": `SELECT event_object_table || ' ' || trigger_name || ' ' || event_manipulation || ' ' || action_statement
			FROM information_schema.triggers WHERE trigger_schema = 'public'`,
		"function": `SELECT p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')'
			FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'public'`,
		"enum": `SELECT t.typname || ' ' || e.enumlabel FROM pg_enum e JOIN pg_type t ON t.oid = e.enumtypid
			JOIN pg_namespace n ON n.oid = t.typnamespace WHERE n.nspname = 'public'`,
	}
	var out []string
	for kind, query := range queries {
		rows, err := db.Query(query)
		require.NoError(t, err, kind)
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line), kind)
			out = append(out, kind+": "+line)
		}
		require.NoError(t, rows.Err(), kind)
		require.NoError(t, rows.Close())
	}
	sort.Strings(out)
	return out
}

func requireSameSchema(t *testing.T, want, got []string, when string) {
	t.Helper()
	wantSet := map[string]bool{}
	for _, line := range want {
		wantSet[line] = true
	}
	gotSet := map[string]bool{}
	for _, line := range got {
		gotSet[line] = true
	}
	var diff []string
	for _, line := range want {
		if !gotSet[line] {
			diff = append(diff, "- "+line)
		}
	}
	for _, line := range got {
		if !wantSet[line] {
			diff = append(diff, "+ "+line)
		}
	}
	if len(diff) > 0 {
		t.Fatalf("schema changed %s (- missing, + extra):\n%s", when, strings.Join(diff, "\n"))
	}
	require.Equal(t, len(want), len(got), fmt.Sprintf("schema line count %s", when))
}
