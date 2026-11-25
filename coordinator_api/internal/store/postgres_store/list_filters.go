package postgres_store

import (
	"fmt"
	"strings"
)

// sourceBranchSQL is models.Job.SourceBranch in SQL for the jobs row alias.
// Change both together.
func sourceBranchSQL(alias string) string {
	return fmt.Sprintf(`(CASE WHEN %[1]s.job_env_vars->>'REACTORCIDE_EVENT_TYPE' IN ('pull_request_opened', 'pull_request_updated')
		AND btrim(COALESCE(%[1]s.job_env_vars->>'REACTORCIDE_HEAD_REF', '')) <> ''
	THEN btrim(%[1]s.job_env_vars->>'REACTORCIDE_HEAD_REF')
	ELSE btrim(COALESCE(%[1]s.job_env_vars->>'REACTORCIDE_BRANCH', '')) END)`, alias)
}

// likePattern turns a list filter into an ILIKE pattern. `*` matches any
// text and `?` one character; every other character is literal. A pattern
// with no wildcard matches the whole value, or (substring) any part of it.
func likePattern(pattern string, substring bool) string {
	var b strings.Builder
	wildcard := false
	for _, r := range pattern {
		switch r {
		case '\\', '%', '_':
			b.WriteRune('\\')
			b.WriteRune(r)
		case '*':
			b.WriteRune('%')
			wildcard = true
		case '?':
			b.WriteRune('_')
			wildcard = true
		default:
			b.WriteRune(r)
		}
	}
	if substring && !wildcard {
		return "%" + b.String() + "%"
	}
	return b.String()
}
