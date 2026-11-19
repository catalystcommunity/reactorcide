// Package codeowners parses a GitHub CODEOWNERS file and finds the owners of
// a path with the GitHub semantics (plan item A2, owner decision D8;
// docs/reporting-and-ownership.md).
//
// The rules are the rules of the GitHub documentation "About code owners":
//
//   - Each line is a pattern, then zero or more owners. A "#" at the start of
//     a line, or after white space, starts a comment. "\#" at the start of a
//     pattern is a literal "#".
//   - The patterns use most gitignore rules. A pattern with no slash, or with
//     only a trailing slash, matches at any level. A pattern with a slash at
//     the start or in the middle is relative to the repository root. A
//     trailing slash matches a directory and everything in it. A pattern that
//     matches a directory also matches everything in it, except when its last
//     segment is "*" ("docs/*" matches "docs/a.md", not "docs/b/c.md").
//   - "*" matches any text but "/", "?" matches one character but "/", and
//     "**" matches zero or more directories.
//   - These gitignore features do not work: "!" (negation) and "[ ]"
//     (character ranges). A line that starts with "!" is invalid and is
//     skipped; brackets are literal characters.
//   - The last matching pattern wins. A matching line with no owners means
//     that the path has no owner.
//   - A line with invalid syntax is skipped. The other lines still apply.
//
// Owners are "@user", "@org/team", or an email address.
package codeowners

import (
	"fmt"
	"regexp"
	"strings"
)

// Locations are the GitHub locations of the file, in the order GitHub reads
// them. The first file found is the code owners file.
var Locations = []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

// MaxFileBytes is the GitHub size limit of a code owners file. GitHub does not
// load a larger file.
const MaxFileBytes = 3 * 1024 * 1024

// Owner kinds.
const (
	OwnerUser  = "user"
	OwnerTeam  = "team"
	OwnerEmail = "email"
)

// Owner is one owner of a rule.
type Owner struct {
	Kind string
	// Value is the login (user), "org/team" (team), or the address (email),
	// with no "@" prefix.
	Value string
}

// String returns the owner as the file writes it.
func (o Owner) String() string {
	if o.Kind == OwnerEmail {
		return o.Value
	}
	return "@" + o.Value
}

// Rule is one valid line of the file.
type Rule struct {
	Line    int
	Pattern string
	Owners  []Owner
	re      *regexp.Regexp
}

// Matches reports whether the rule pattern matches a path (a file path
// relative to the repository root, with "/" separators).
func (r *Rule) Matches(path string) bool {
	return r.re.MatchString(strings.TrimPrefix(path, "/"))
}

// Warning is a line that the parser skipped, or a problem with the file.
type Warning struct {
	Line    int
	Message string
}

func (w Warning) String() string {
	if w.Line > 0 {
		return fmt.Sprintf("line %d: %s", w.Line, w.Message)
	}
	return w.Message
}

// File is a parsed code owners file.
type File struct {
	Rules    []Rule
	Warnings []Warning
}

// Match returns the last rule that matches the path, or nil when no rule
// matches. A rule with no owners means that the path has no owner.
func (f *File) Match(path string) *Rule {
	if f == nil {
		return nil
	}
	for i := len(f.Rules) - 1; i >= 0; i-- {
		if f.Rules[i].Matches(path) {
			return &f.Rules[i]
		}
	}
	return nil
}

// Owners returns the owners of a path: the owners of the last matching rule.
// nil means no owner.
func (f *File) Owners(path string) []Owner {
	if rule := f.Match(path); rule != nil {
		return rule.Owners
	}
	return nil
}

var (
	userPattern  = regexp.MustCompile(`^@[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	teamPattern  = regexp.MustCompile(`^@[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// ParseOwner parses one owner token.
func ParseOwner(token string) (Owner, bool) {
	switch {
	case teamPattern.MatchString(token):
		return Owner{Kind: OwnerTeam, Value: token[1:]}, true
	case userPattern.MatchString(token):
		return Owner{Kind: OwnerUser, Value: token[1:]}, true
	case emailPattern.MatchString(token):
		return Owner{Kind: OwnerEmail, Value: token}, true
	}
	return Owner{}, false
}

// Parse parses the content of a code owners file. It never fails: a line
// with invalid syntax is skipped with a warning, as GitHub does.
func Parse(content []byte) *File {
	file := &File{}
	if len(content) > MaxFileBytes {
		file.Warnings = append(file.Warnings, Warning{Message: fmt.Sprintf("the file is larger than %d bytes; GitHub does not load it", MaxFileBytes)})
		return file
	}
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	for index, raw := range lines {
		number := index + 1
		tokens := tokenize(raw)
		if len(tokens) == 0 {
			continue
		}
		pattern := tokens[0]
		if strings.HasPrefix(pattern, "!") {
			file.Warnings = append(file.Warnings, Warning{Line: number, Message: "a negation pattern (\"!\") is not supported; the line is skipped"})
			continue
		}
		re, err := compilePattern(pattern)
		if err != nil {
			file.Warnings = append(file.Warnings, Warning{Line: number, Message: err.Error() + "; the line is skipped"})
			continue
		}
		rule := Rule{Line: number, Pattern: pattern, re: re, Owners: []Owner{}}
		valid := true
		for _, token := range tokens[1:] {
			owner, ok := ParseOwner(token)
			if !ok {
				file.Warnings = append(file.Warnings, Warning{Line: number, Message: fmt.Sprintf("owner %q is not a user, a team, or an email address; the line is skipped", token)})
				valid = false
				break
			}
			rule.Owners = append(rule.Owners, owner)
		}
		if valid {
			file.Rules = append(file.Rules, rule)
		}
	}
	return file
}

// tokenize splits a line into white-space separated tokens. It stops at a
// comment, and keeps an escaped character ("\#", "\ ") in a token.
func tokenize(line string) []string {
	tokens := []string{}
	var current strings.Builder
	inToken := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && i+1 < len(line):
			// Keep the escape; compilePattern reads it.
			current.WriteByte(c)
			current.WriteByte(line[i+1])
			i++
			inToken = true
		case c == '#' && !inToken:
			// A comment at the start of the line or after white space.
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
			}
			return tokens
		case c == ' ' || c == '\t':
			if inToken {
				tokens = append(tokens, current.String())
				current.Reset()
				inToken = false
			}
		default:
			current.WriteByte(c)
			inToken = true
		}
	}
	if inToken {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// compilePattern turns a pattern into a regular expression for a path
// relative to the repository root.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	if strings.Contains(pattern, "***") {
		return nil, fmt.Errorf("pattern %q has \"***\"", pattern)
	}
	anchored := strings.HasPrefix(pattern, "/")
	body := strings.TrimPrefix(pattern, "/")
	dirOnly := strings.HasSuffix(body, "/") && !strings.HasSuffix(body, "\\/")
	body = strings.TrimSuffix(body, "/")
	if body == "" {
		return nil, fmt.Errorf("pattern %q matches no file", pattern)
	}
	if strings.Contains(body, "/") {
		// A slash at the start or in the middle: relative to the root.
		anchored = true
	}
	segments := strings.Split(body, "/")
	for _, segment := range segments {
		if segment == "" {
			return nil, fmt.Errorf("pattern %q has an empty path segment", pattern)
		}
	}
	var re strings.Builder
	re.WriteString(`\A`)
	if !anchored {
		re.WriteString(`(?:.*/)?`)
	}
	last := len(segments) - 1
	for i, segment := range segments {
		if segment == "**" {
			switch {
			case i == 0 && i == last:
				re.WriteString(`.*`)
			case i == last:
				// "dir/**": everything in dir. The slash came from the
				// segment before.
				re.WriteString(`.+`)
			default:
				// A leading or middle "**": zero or more directories.
				re.WriteString(`(?:[^/]+/)*`)
			}
			continue
		}
		re.WriteString(segmentRegex(segment))
		if i != last {
			re.WriteString(`/`)
		}
	}
	switch {
	case segments[last] == "**":
		re.WriteString(`\z`)
	case dirOnly:
		re.WriteString(`/`)
	case segments[last] == "*":
		// GitHub: "docs/*" matches the files in docs, not the files in its
		// subdirectories.
		re.WriteString(`\z`)
	default:
		// The path itself, or everything in a directory with that path.
		re.WriteString(`(?:\z|/)`)
	}
	return regexp.Compile(re.String())
}

// segmentRegex converts one path segment with "*", "?", and "\" escapes.
// Brackets are literal: GitHub does not support character ranges.
func segmentRegex(segment string) string {
	var out strings.Builder
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		switch {
		case c == '\\' && i+1 < len(segment):
			out.WriteString(regexp.QuoteMeta(string(segment[i+1])))
			i++
		case c == '*':
			out.WriteString(`[^/]*`)
		case c == '?':
			out.WriteString(`[^/]`)
		default:
			out.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return out.String()
}
