package models

import "strings"

// SourceBranch is the branch (or tag) of the code that a job ran. For an
// opened or updated pull request it is the head branch
// (REACTORCIDE_HEAD_REF). For every other event (push, merge, tag, manual
// run) it is REACTORCIDE_BRANCH: the pushed branch, the base branch after a
// merge, or the tag name. Empty when the job has neither.
//
// postgres_store.sourceBranchSQL is the same rule in SQL. Change both.
func (j *Job) SourceBranch() string {
	if j == nil || j.JobEnvVars == nil {
		return ""
	}
	env := func(key string) string {
		value, _ := j.JobEnvVars[key].(string)
		return strings.TrimSpace(value)
	}
	switch env("REACTORCIDE_EVENT_TYPE") {
	case "pull_request_opened", "pull_request_updated":
		if head := env("REACTORCIDE_HEAD_REF"); head != "" {
			return head
		}
	}
	return env("REACTORCIDE_BRANCH")
}
