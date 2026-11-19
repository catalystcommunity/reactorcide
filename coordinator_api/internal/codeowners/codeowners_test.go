package codeowners

import (
	"strings"
	"testing"
)

// githubExample is the example file of the GitHub documentation "About code
// owners" (the comments are shortened).
const githubExample = `# This is a comment.
*       @global-owner1 @global-owner2

# Order is important; the last matching pattern takes the most precedence.
*.js    @js-owner #This is an inline comment.

*.go docs@example.com

*.txt @octo-org/octocats

/build/logs/ @doctocat

docs/*  docs@example.com

apps/ @octocat

/docs/ @doctocat

/scripts/ @doctocat @octocat

**/logs @octocat

/apps/ @octocat
/apps/github
`

func owners(file *File, path string) string {
	rule := file.Match(path)
	if rule == nil {
		return "<no match>"
	}
	values := []string{}
	for _, owner := range rule.Owners {
		values = append(values, owner.String())
	}
	if len(values) == 0 {
		return "<no owner>"
	}
	return strings.Join(values, " ")
}

func TestGitHubExampleFileLastMatchWins(t *testing.T) {
	file := Parse([]byte(githubExample))
	if len(file.Warnings) != 0 {
		t.Fatalf("warnings: %v", file.Warnings)
	}
	cases := map[string]string{
		"README.md":                          "@global-owner1 @global-owner2",
		"src/index.js":                       "@js-owner",
		"cmd/main.go":                        "docs@example.com",
		"notes/todo.txt":                     "@octo-org/octocats",
		"build/logs/today.log":               "@octocat", // **/logs is after /build/logs/
		"build/logs/deep/today.log":          "@octocat",
		"docs/getting-started.md":            "@doctocat", // /docs/ is after docs/*
		"docs/build-app/troubleshooting.md":  "@doctocat",
		"scripts/deploy.sh":                  "@doctocat @octocat",
		"scripts/logs/run.log":               "@octocat",
		"deeply/nested/logs/x.log":           "@octocat",
		"apps/web/index.html":                "@octocat",
		"apps/github/main.rb":                "<no owner>",
		"apps/github":                        "<no owner>",
		"lib/apps/thing.rb":                  "@octocat", // apps/ at any level
		"lib/apps/thing.js":                  "@octocat",
		"web/scripts/deploy.sh":              "@global-owner1 @global-owner2", // /scripts/ is anchored
		"web/docs/readme.md":                 "@global-owner1 @global-owner2",
		"subdir/build/logs/x.txt":            "@octocat",
		"apps.js":                            "@js-owner",
		"apps":                               "@global-owner1 @global-owner2", // apps/ needs a directory
		"build/logs":                         "@octocat",                      // **/logs matches the file named logs
		"documentation/x.md":                 "@global-owner1 @global-owner2",
		"docs.md":                            "@global-owner1 @global-owner2",
		"notes/archive/2020/minutes.txt.bak": "@global-owner1 @global-owner2",
	}
	for path, want := range cases {
		if got := owners(file, path); got != want {
			t.Errorf("%s: got %s, want %s", path, got, want)
		}
	}
}

func TestSingleRulePatterns(t *testing.T) {
	type check struct {
		path  string
		match bool
	}
	cases := []struct {
		pattern string
		checks  []check
	}{
		{"*", []check{{"a", true}, {"a/b/c.go", true}}},
		{"*.js", []check{{"a.js", true}, {"x/y/a.js", true}, {"a.jsx", false}, {"a.js/inner", true}}},
		{"/build/logs/", []check{{"build/logs/a", true}, {"build/logs/a/b", true}, {"x/build/logs/a", false}, {"build/logs", false}}},
		{"docs/*", []check{{"docs/getting-started.md", true}, {"docs/build-app/troubleshooting.md", false}, {"x/docs/a.md", false}}},
		{"apps/", []check{{"apps/a", true}, {"x/apps/a/b", true}, {"apps", false}, {"myapps/a", false}}},
		{"/docs/", []check{{"docs/a", true}, {"docs/a/b", true}, {"x/docs/a", false}}},
		{"**/logs", []check{{"logs/a", true}, {"build/logs/a", true}, {"deeply/nested/logs/x", true}, {"logs", true}, {"blogs/a", false}}},
		{"/apps/github", []check{{"apps/github", true}, {"apps/github/a/b", true}, {"apps/githubber", false}, {"x/apps/github", false}}},
		{"foo/**", []check{{"foo/a", true}, {"foo/a/b", true}, {"foo", false}, {"x/foo/a", false}}},
		{"a/**/b", []check{{"a/b", true}, {"a/x/b", true}, {"a/x/y/b", true}, {"a/x/y/b/c", true}, {"a/xb", false}}},
		{"src/*.go", []check{{"src/a.go", true}, {"src/x/a.go", false}}},
		{"file?.txt", []check{{"file1.txt", true}, {"file12.txt", false}, {"dir/file1.txt", true}}},
		{"/README.md", []check{{"README.md", true}, {"docs/README.md", false}}},
		{"README.md", []check{{"README.md", true}, {"docs/README.md", true}}},
		{"[ab].txt", []check{{"[ab].txt", true}, {"a.txt", false}}},
		{`\#notes`, []check{{"#notes", true}, {"notes", false}}},
		{"/*", []check{{"top.go", true}, {"dir/top.go", false}}},
		{"**", []check{{"a", true}, {"a/b", true}}},
	}
	for _, tc := range cases {
		file := Parse([]byte(tc.pattern + " @owner\n"))
		if len(file.Rules) != 1 {
			t.Fatalf("%q: %d rules, warnings %v", tc.pattern, len(file.Rules), file.Warnings)
		}
		for _, c := range tc.checks {
			if got := file.Rules[0].Matches(c.path); got != c.match {
				t.Errorf("pattern %q path %q: got %v, want %v", tc.pattern, c.path, got, c.match)
			}
		}
	}
}

func TestLaterRuleWithOwnerOverrides(t *testing.T) {
	file := Parse([]byte("/apps/ @octocat\n/apps/github @doctocat\n"))
	if got := owners(file, "apps/github/x"); got != "@doctocat" {
		t.Fatalf("got %s", got)
	}
	if got := owners(file, "apps/other/x"); got != "@octocat" {
		t.Fatalf("got %s", got)
	}
	if got := owners(file, "lib/x"); got != "<no match>" {
		t.Fatalf("got %s", got)
	}
}

func TestInvalidLinesAreSkipped(t *testing.T) {
	file := Parse([]byte("* @default\n!secret.txt @nobody\n*.md @ok not-an-owner\n/ @root\n*** @x\n*.go @go-team\n"))
	if len(file.Rules) != 2 {
		t.Fatalf("rules %d: %+v", len(file.Rules), file.Rules)
	}
	if len(file.Warnings) != 4 {
		t.Fatalf("warnings: %v", file.Warnings)
	}
	// The invalid *.md line does not apply: the default owner stays.
	if got := owners(file, "README.md"); got != "@default" {
		t.Fatalf("got %s", got)
	}
	if got := owners(file, "main.go"); got != "@go-team" {
		t.Fatalf("got %s", got)
	}
}

func TestOwnerKinds(t *testing.T) {
	file := Parse([]byte("* @alice @acme/platform-team dev@example.com\r\n"))
	if len(file.Rules) != 1 {
		t.Fatalf("warnings %v", file.Warnings)
	}
	got := file.Rules[0].Owners
	want := []Owner{{OwnerUser, "alice"}, {OwnerTeam, "acme/platform-team"}, {OwnerEmail, "dev@example.com"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("owner %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCommentsAndBlankLines(t *testing.T) {
	file := Parse([]byte("\n   \n# all\n\t# indented comment\n*.js @a # trailing\n"))
	if len(file.Rules) != 1 || len(file.Rules[0].Owners) != 1 || file.Rules[0].Line != 5 {
		t.Fatalf("rules: %+v warnings %v", file.Rules, file.Warnings)
	}
}

func TestTooLargeFile(t *testing.T) {
	file := Parse([]byte(strings.Repeat("a", MaxFileBytes+1)))
	if len(file.Rules) != 0 || len(file.Warnings) != 1 {
		t.Fatalf("got %+v", file)
	}
}
