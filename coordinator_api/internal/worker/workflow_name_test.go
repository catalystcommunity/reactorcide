package worker

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// TestRepoBasename guards the default workflow-name qualifier: the repo
// basename is the last path component of the source repo URL, with ".git"
// stripped and casing preserved (never the full URL). Used for the
// backward-compat "Reactorcide Jobs, repo: <name>" default.
func TestRepoBasename(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		name string
		job  *models.Job
		want string
	}{
		{"https .git", &models.Job{CISourceURL: s("https://github.com/catalystcommunity/reactorcide.git")}, "reactorcide"},
		{"casing preserved", &models.Job{CISourceURL: s("https://github.com/catalystcommunity/Corndogs")}, "Corndogs"},
		{"scp-like", &models.Job{CISourceURL: s("git@github.com:foo/bar.git")}, "bar"},
		{"trailing slash", &models.Job{CISourceURL: s("https://example.com/x/repo/")}, "repo"},
		{"falls back to source_url", &models.Job{SourceURL: s("https://github.com/o/ichoi.git")}, "ichoi"},
		{"none", &models.Job{}, ""},
	}
	for _, c := range cases {
		if got := repoBasename(c.job); got != c.want {
			t.Errorf("%s: repoBasename = %q, want %q", c.name, got, c.want)
		}
	}
}
