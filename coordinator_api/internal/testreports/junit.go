// Package testreports reads JUnit XML test reports (WP15,
// docs/artifacts-and-tests.md). JUnit XML is a generic format that most test
// tools can write; Reactorcide knows no test tool (owner decision D6).
//
// The parser streams the document with encoding/xml. It is safe for an
// untrusted report:
//
//   - It reads at most Limits.MaxBytes bytes and at most Limits.MaxCases test
//     cases. A larger report gives the cases before the limit and
//     Truncated=true.
//   - It refuses a DTD that declares entities. encoding/xml never reads an
//     external entity and expands only the five XML entities, so no
//     external file or URL is read and an entity expansion attack is not
//     possible.
//   - It accepts UTF-8 (and US-ASCII) only.
//   - It limits the element depth and the size of each failure message.
package testreports

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Case states. They agree with models.TestStatus*.
const (
	StatusPass  = "pass"
	StatusFail  = "fail"
	StatusError = "error"
	StatusSkip  = "skip"
)

// Case is one test case.
type Case struct {
	// Suite is the path of the testsuite names, joined with "/".
	Suite     string
	Classname string
	Name      string
	Status    string
	Duration  time.Duration
	// Message is the failure, error, or skip message: the message
	// attribute, then the element text. It has at most
	// Limits.MaxMessageBytes bytes.
	Message string
	File    string
	Line    *int
}

// Report is the result of a parse.
type Report struct {
	Cases []Case
	// Truncated is true when the report was larger than a limit. Cases has
	// the cases before the limit.
	Truncated bool
}

// Limits bound one parse.
type Limits struct {
	MaxBytes        int64
	MaxCases        int
	MaxMessageBytes int
	MaxDepth        int
	MaxNameBytes    int
}

// DefaultLimits are the limits of the coordinator.
var DefaultLimits = Limits{MaxBytes: 32 << 20, MaxCases: 20000, MaxMessageBytes: 4096, MaxDepth: 64, MaxNameBytes: 1024}

func (l Limits) withDefaults() Limits {
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultLimits.MaxBytes
	}
	if l.MaxCases <= 0 {
		l.MaxCases = DefaultLimits.MaxCases
	}
	if l.MaxMessageBytes <= 0 {
		l.MaxMessageBytes = DefaultLimits.MaxMessageBytes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultLimits.MaxDepth
	}
	if l.MaxNameBytes <= 0 {
		l.MaxNameBytes = DefaultLimits.MaxNameBytes
	}
	return l
}

// ErrNotJUnit is returned when the root element is not testsuites or
// testsuite.
var ErrNotJUnit = errors.New("the document is not a JUnit XML report (the root element must be testsuites or testsuite)")

var errSizeLimit = errors.New("size limit")

// cappedReader returns errSizeLimit after limit bytes.
type cappedReader struct {
	r       io.Reader
	left    int64
	tripped bool
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		c.tripped = true
		return 0, errSizeLimit
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// Parse reads one JUnit XML report. A report that is not valid XML gives an
// error, with the cases before the error in the report.
func Parse(r io.Reader, limits Limits) (*Report, error) {
	limits = limits.withDefaults()
	capped := &cappedReader{r: r, left: limits.MaxBytes}
	decoder := xml.NewDecoder(capped)
	decoder.Strict = true
	decoder.CharsetReader = func(label string, input io.Reader) (io.Reader, error) {
		switch strings.ToLower(strings.TrimSpace(label)) {
		case "utf-8", "utf8", "us-ascii", "ascii":
			return input, nil
		}
		return nil, fmt.Errorf("the encoding %q is not supported (use UTF-8)", label)
	}
	p := &parser{limits: limits, report: &Report{}}
	err := p.run(decoder)
	if capped.tripped {
		p.report.Truncated = true
		return p.report, fmt.Errorf("the report is larger than %d bytes; only the first part was read", limits.MaxBytes)
	}
	if err != nil {
		return p.report, err
	}
	return p.report, nil
}

type parser struct {
	limits Limits
	report *Report

	depth      int
	sawRoot    bool
	suites     []string
	suiteFiles []string
	current    *Case
	// inMessage is the depth of the failure/error/skipped element whose text
	// goes to the message; 0 means none.
	inMessage int
	message   bytes.Buffer
}

func (p *parser) run(decoder *xml.Decoder) error {
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if !p.sawRoot {
				return ErrNotJUnit
			}
			return nil
		}
		if err != nil {
			if errors.Is(err, errSizeLimit) {
				return err
			}
			return fmt.Errorf("the report is not valid XML: %w", err)
		}
		switch t := token.(type) {
		case xml.Directive:
			if bytes.Contains(bytes.ToUpper(t), []byte("ENTITY")) {
				return errors.New("the report declares XML entities; a DTD with entities is not permitted")
			}
		case xml.StartElement:
			p.depth++
			if p.depth > p.limits.MaxDepth {
				return fmt.Errorf("the report has more than %d levels of elements", p.limits.MaxDepth)
			}
			if done := p.start(t); done {
				if !p.sawRoot {
					return ErrNotJUnit
				}
				return nil
			}
		case xml.EndElement:
			p.end(t)
			p.depth--
		case xml.CharData:
			if p.inMessage > 0 && p.message.Len() < p.limits.MaxMessageBytes {
				room := p.limits.MaxMessageBytes - p.message.Len()
				if len(t) > room {
					t = t[:room]
				}
				p.message.Write(t)
			}
		}
	}
}

// start handles a start element. It returns true when the case limit is
// reached.
func (p *parser) start(t xml.StartElement) bool {
	name := t.Name.Local
	if p.depth == 1 {
		if name != "testsuites" && name != "testsuite" {
			// Not a report; the caller sees ErrNotJUnit after the loop ends
			// only when no root was seen, so stop here.
			p.sawRoot = false
			return true
		}
		p.sawRoot = true
	}
	switch name {
	case "testsuite":
		p.suites = append(p.suites, p.text(attr(t, "name")))
		p.suiteFiles = append(p.suiteFiles, p.text(attr(t, "file")))
	case "testcase":
		if p.current != nil {
			return false
		}
		if len(p.report.Cases) >= p.limits.MaxCases {
			p.report.Truncated = true
			return true
		}
		c := &Case{
			Suite:     p.suitePath(),
			Classname: p.text(attr(t, "classname")),
			Name:      p.text(attr(t, "name")),
			Status:    StatusPass,
			Duration:  parseSeconds(attr(t, "time")),
			File:      p.text(attr(t, "file")),
		}
		for i := len(p.suiteFiles) - 1; c.File == "" && i >= 0; i-- {
			c.File = p.suiteFiles[i]
		}
		if line, err := strconv.Atoi(strings.TrimSpace(attr(t, "line"))); err == nil && line > 0 {
			c.Line = &line
		}
		if c.Name == "" {
			c.Name = "(unnamed)"
		}
		p.current = c
	case "failure", "error", "skipped":
		if p.current == nil || p.inMessage > 0 {
			return false
		}
		switch {
		case name == "error" && p.current.Status != StatusFail:
			p.current.Status = StatusError
		case name == "failure":
			p.current.Status = StatusFail
		case name == "skipped" && p.current.Status == StatusPass:
			p.current.Status = StatusSkip
		default:
			return false
		}
		p.inMessage = p.depth
		p.message.Reset()
		if message := attr(t, "message"); message != "" {
			p.message.WriteString(truncateBytes(message, p.limits.MaxMessageBytes))
			p.message.WriteString("\n")
		}
	}
	return false
}

func (p *parser) end(t xml.EndElement) {
	switch t.Name.Local {
	case "testsuite":
		if len(p.suites) > 0 {
			p.suites = p.suites[:len(p.suites)-1]
			p.suiteFiles = p.suiteFiles[:len(p.suiteFiles)-1]
		}
	case "failure", "error", "skipped":
		if p.inMessage == p.depth && p.current != nil {
			p.current.Message = cleanMessage(p.message.String(), p.limits.MaxMessageBytes)
			p.inMessage = 0
		}
	case "testcase":
		if p.current != nil {
			p.report.Cases = append(p.report.Cases, *p.current)
			p.current = nil
		}
	}
}

func (p *parser) suitePath() string {
	parts := make([]string, 0, len(p.suites))
	for _, name := range p.suites {
		if name != "" {
			parts = append(parts, name)
		}
	}
	return p.text(strings.Join(parts, "/"))
}

func (p *parser) text(value string) string {
	return truncateBytes(strings.TrimSpace(value), p.limits.MaxNameBytes)
}

func attr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// parseSeconds reads a JUnit time attribute: seconds with a decimal point.
// Some tools write a thousands separator ("1,234.5"). A bad value is 0.
func parseSeconds(value string) time.Duration {
	value = strings.ReplaceAll(strings.TrimSpace(value), ",", "")
	if value == "" {
		return 0
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || seconds < 0 || seconds > 1e9 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// cleanMessage trims a message and removes control characters other than
// newline and tab.
func cleanMessage(value string, limit int) string {
	value = strings.ToValidUTF8(value, "")
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r == '\r' {
			return -1
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	return truncateBytes(strings.TrimSpace(value), limit)
}

// truncateBytes cuts value to at most limit bytes on a rune boundary.
func truncateBytes(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

// FirstLines returns the first n non-empty lines of a message, each at most
// width runes.
func FirstLines(message string, n, width int) []string {
	out := []string{}
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if utf8.RuneCountInString(line) > width {
			runes := []rune(line)
			line = string(runes[:width]) + "…"
		}
		out = append(out, line)
		if len(out) >= n {
			break
		}
	}
	return out
}

// LooksLikeReport reports whether a relative artifact path follows the
// test report convention: a file name that contains "junit" and ends with
// ".xml" (case-insensitive), in any directory.
func LooksLikeReport(path string) bool {
	base := path
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(base)
	return strings.HasSuffix(base, ".xml") && strings.Contains(base, "junit")
}
