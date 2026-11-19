// Package diagnosis holds the primitives for failure diagnosis by principals
// (plan item F6, docs/diagnosis.md): validation and sanitization of a
// diagnosis entry, the rendering of the pull request comment section, and
// the service that REST and CSIL use after they authorize the caller.
//
// Reactorcide does not diagnose. An external diagnosis tool is a service
// principal. It reads logs with its own grants and posts an entry here.
package diagnosis

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Size limits of an entry. The body limit is also a database CHECK.
const (
	MaxTitleRunes     = 200
	MaxBodyBytes      = 16384
	MaxCategoryLength = 64
	MaxLinks          = 10
	MaxLinkNameRunes  = 100
	MaxLinkURLLength  = 2048
	// MaxCommentBodyRunes is the part of the body that the pull request
	// comment shows. The job page shows the full body.
	MaxCommentBodyRunes = 4000
)

// zeroWidthSpace breaks a mention without a visible change.
const zeroWidthSpace = "​"

var (
	// imagePattern finds the start of a Markdown image: "![".
	imagePattern = regexp.MustCompile(`!\[`)
	// backlinkPattern finds "#123" and "GH-123": GitHub links them to an
	// issue or a pull request and adds a cross-reference there.
	backlinkPattern = regexp.MustCompile(`(#|\b[Gg][Hh]-)([0-9])`)
)

// cleanControl removes control characters except a newline and a tab, and
// makes each line end "\n". It also replaces invalid UTF-8.
func cleanControl(text string) string {
	text = strings.ToValidUTF8(text, "\uFFFD")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		// Bidirectional overrides can hide text in a review.
		switch r {
		case '\u202a', '\u202b', '\u202c', '\u202d', '\u202e', '\u2066', '\u2067', '\u2068', '\u2069':
			return -1
		}
		return r
	}, text)
}

// neutralizeInline makes one piece of text that is not code safe for a pull
// request comment:
//   - "&" becomes "&amp;": no character reference ("&#64;", "&commat;")
//     can make an "@", a "<", or another character after the sanitizer.
//   - "<" becomes "&lt;": no HTML tag, no HTML comment, no <img>.
//   - "-->" becomes "--&gt;": the text cannot close a report marker.
//   - "![" becomes "[": an image becomes a plain link.
//   - every "@" gets a zero-width space after it: no mention pings a user or
//     a team, whatever character is before it ("_@org/team_", "`@x").
//   - "#123" and "GH-123" get a zero-width space before the digits: no
//     cross-reference on another issue or pull request.
func neutralizeInline(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, "-->", "--&gt;")
	text = imagePattern.ReplaceAllString(text, "[")
	text = strings.ReplaceAll(text, "@", "@"+zeroWidthSpace)
	return backlinkPattern.ReplaceAllString(text, "${1}"+zeroWidthSpace+"${2}")
}

// neutralizeCode makes code text safe. GitHub does not render HTML, images,
// mentions, or references in code. But the sanitizer and GitHub can disagree
// about where code starts and ends (a list item, a block quote, a lazy
// continuation line). Thus code also gets neutralized, with invisible
// changes only: a zero-width space after each "@", "<", and "&", and before
// the digits of "#123". A "<" followed by a zero-width space is not a tag, a
// comment, or a report marker, and "&" followed by it is not a character
// reference. The code looks the same.
func neutralizeCode(text string) string {
	replacer := strings.NewReplacer("@", "@"+zeroWidthSpace, "<", "<"+zeroWidthSpace, "&", "&"+zeroWidthSpace,
		"-->", "--"+zeroWidthSpace+">", "![", "!"+zeroWidthSpace+"[")
	text = replacer.Replace(text)
	return backlinkPattern.ReplaceAllString(text, "${1}"+zeroWidthSpace+"${2}")
}

// SanitizeMarkdown returns body in a form that is safe for the rolling pull
// request comment and for the UI:
//   - no HTML (each "<" outside code is escaped, each "&" too),
//   - no images (an image becomes a link),
//   - no mentions that ping a user or a team, and no issue references,
//   - no control or bidirectional override characters,
//   - no open fenced code block at the end: the next section of the comment
//     always renders as text.
//
// Fenced code blocks follow the CommonMark rules (fenceOpen, fenceCloses).
// Code keeps its visible text (neutralizeCode).
func SanitizeMarkdown(body string) string {
	body = cleanControl(body)
	lines := strings.Split(body, "\n")
	var open *fence
	for i, line := range lines {
		if open != nil {
			if open.closes(line) {
				open = nil
			}
			lines[i] = neutralizeCode(line)
			continue
		}
		if f, ok := fenceOpen(line); ok {
			open = &f
			lines[i] = neutralizeCode(line)
			continue
		}
		lines[i] = sanitizeLine(line)
	}
	out := strings.TrimSpace(strings.Join(lines, "\n"))
	if open != nil {
		out += "\n" + open.closing()
	}
	return out
}

// fence is an open fenced code block.
type fence struct {
	char  byte
	count int
}

func (f fence) closing() string { return strings.Repeat(string(f.char), f.count) }

// fenceIndent returns the line without its indentation of 0 to 3 spaces.
// ok is false for 4 or more columns (an indented code block or text) and
// for a tab in the indentation (a tab is at least to column 4).
func fenceIndent(line string) (string, bool) {
	n := 0
	for n < len(line) && line[n] == ' ' {
		n++
	}
	if n > 3 || (n < len(line) && line[n] == '\t') {
		return "", false
	}
	return line[n:], true
}

// fenceOpen reports whether line opens a fenced code block (CommonMark
// 4.5): 0 to 3 spaces of indentation, then 3 or more backticks or tildes.
// The info string after backticks must not contain a backtick ("```x`y" is
// text with a code span, not a fence).
func fenceOpen(line string) (fence, bool) {
	rest, ok := fenceIndent(line)
	if !ok || len(rest) < 3 || (rest[0] != '`' && rest[0] != '~') {
		return fence{}, false
	}
	char := rest[0]
	n := 0
	for n < len(rest) && rest[n] == char {
		n++
	}
	if n < 3 {
		return fence{}, false
	}
	if char == '`' && strings.IndexByte(rest[n:], '`') >= 0 {
		return fence{}, false
	}
	return fence{char: char, count: n}, true
}

// closes reports whether line closes the fence (CommonMark 4.5): 0 to 3
// spaces of indentation, at least as many fence characters of the same kind
// as the opening fence, then only spaces or tabs.
func (f fence) closes(line string) bool {
	rest, ok := fenceIndent(line)
	if !ok {
		return false
	}
	n := 0
	for n < len(rest) && rest[n] == f.char {
		n++
	}
	return n >= f.count && strings.Trim(rest[n:], " \t") == ""
}

// sanitizeLine neutralizes one line that is not in a fenced block. The text
// in inline code spans (CommonMark 6.1: a run of backticks, closed by a run
// of the same length) is code.
func sanitizeLine(line string) string {
	var out strings.Builder
	for {
		start := strings.IndexByte(line, '`')
		if start < 0 {
			out.WriteString(neutralizeInline(line))
			return out.String()
		}
		run := 1
		for start+run < len(line) && line[start+run] == '`' {
			run++
		}
		end := closingBackticks(line[start+run:], run)
		if end < 0 {
			// No closing run: the backticks are text. Continue after them.
			out.WriteString(neutralizeInline(line[:start+run]))
			line = line[start+run:]
			continue
		}
		ticks := line[start : start+run]
		out.WriteString(neutralizeInline(line[:start]))
		out.WriteString(ticks)
		out.WriteString(neutralizeCode(line[start+run : start+run+end]))
		out.WriteString(ticks)
		line = line[start+run+end+run:]
	}
}

// closingBackticks returns the index in text of a run of exactly n
// backticks, or -1.
func closingBackticks(text string, n int) int {
	for i := 0; i < len(text); {
		if text[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(text) && text[j] == '`' {
			j++
		}
		if j-i == n {
			return i
		}
		i = j
	}
	return -1
}

// SanitizeInline returns one line of text (a title, a display name, or a
// link name) that is safe in a heading or a link text of the comment: one
// line, no HTML, no mention, no Markdown link or emphasis syntax.
func SanitizeInline(text string) string {
	text = cleanControl(text)
	text = strings.Join(strings.Fields(text), " ")
	text = neutralizeInline(text)
	replacer := strings.NewReplacer("[", "\\[", "]", "\\]", "`", "'", "*", "\\*", "_", "\\_", "|", "\\|")
	return replacer.Replace(text)
}

// Truncate returns text with at most n runes. A cut text ends with an
// ellipsis. It never cuts in the middle of a rune.
func Truncate(text string, n int) (string, bool) {
	if utf8.RuneCountInString(text) <= n {
		return text, false
	}
	runes := []rune(text)
	return string(runes[:n]) + "…", true
}
