package testreports

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseNestedSuites(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<testsuites name="all">
  <testsuite name="outer" file="pkg/outer_test.go">
    <testsuite name="inner">
      <testcase classname="pkg.A" name="passes" time="0.25"/>
      <testcase classname="pkg.A" name="fails" time="1,234.5" line="42">
        <failure message="expected 1, got 2">assert.go:10
second line &amp; more</failure>
        <system-out>lots of output</system-out>
      </testcase>
      <testcase classname="pkg.A" name="errors"><error message="panic">stack</error></testcase>
      <testcase classname="pkg.A" name="skipped"><skipped message="not on linux"/></testcase>
    </testsuite>
    <testcase name="top" />
  </testsuite>
</testsuites>`
	report, err := Parse(strings.NewReader(doc), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Truncated || len(report.Cases) != 5 {
		t.Fatalf("cases %+v", report.Cases)
	}
	pass, fail, errored, skip, top := report.Cases[0], report.Cases[1], report.Cases[2], report.Cases[3], report.Cases[4]
	if pass.Suite != "outer/inner" || pass.Status != StatusPass || pass.Duration != 250*time.Millisecond {
		t.Fatalf("pass %+v", pass)
	}
	if fail.Status != StatusFail || fail.Line == nil || *fail.Line != 42 || fail.File != "pkg/outer_test.go" {
		t.Fatalf("fail %+v", fail)
	}
	if fail.Duration != time.Duration(1234.5*float64(time.Second)) {
		t.Fatalf("duration %v", fail.Duration)
	}
	if !strings.HasPrefix(fail.Message, "expected 1, got 2\nassert.go:10\nsecond line & more") || strings.Contains(fail.Message, "lots of output") {
		t.Fatalf("message %q", fail.Message)
	}
	if errored.Status != StatusError || skip.Status != StatusSkip || skip.Message != "not on linux" {
		t.Fatalf("error/skip %+v %+v", errored, skip)
	}
	if top.Suite != "outer" || top.Classname != "" {
		t.Fatalf("top %+v", top)
	}
}

func TestParseSingleSuiteRoot(t *testing.T) {
	report, err := Parse(strings.NewReader(`<testsuite name="pytest"><testcase classname="t" name="x"/></testsuite>`), Limits{})
	if err != nil || len(report.Cases) != 1 || report.Cases[0].Suite != "pytest" {
		t.Fatalf("%v %+v", err, report)
	}
}

func TestParseRefusesEntities(t *testing.T) {
	xxe := `<?xml version="1.0"?>
<!DOCTYPE foo [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>
<testsuite name="s"><testcase name="x"><failure message="&xxe;"/></testcase></testsuite>`
	_, err := Parse(strings.NewReader(xxe), Limits{})
	if err == nil || !strings.Contains(err.Error(), "entities") {
		t.Fatalf("XXE must be refused: %v", err)
	}
	// An undeclared entity is an error too (no expansion).
	_, err = Parse(strings.NewReader(`<testsuite><testcase name="&lol;"/></testsuite>`), Limits{})
	if err == nil {
		t.Fatal("an unknown entity must fail")
	}
}

func TestParseInvalidAndNotJUnit(t *testing.T) {
	report, err := Parse(strings.NewReader(`<testsuite name="s"><testcase name="a"/><testcase name="b">`), Limits{})
	if err == nil {
		t.Fatal("unclosed XML must fail")
	}
	if len(report.Cases) != 1 {
		t.Fatalf("cases before the error stay: %+v", report.Cases)
	}
	if _, err := Parse(strings.NewReader(`<html><body/></html>`), Limits{}); !errors.Is(err, ErrNotJUnit) {
		t.Fatalf("not junit: %v", err)
	}
	if _, err := Parse(strings.NewReader(``), Limits{}); !errors.Is(err, ErrNotJUnit) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := Parse(strings.NewReader(`<?xml version="1.0" encoding="UTF-16"?><testsuite/>`), Limits{}); err == nil {
		t.Fatal("a non UTF-8 encoding must fail")
	}
}

func TestParseHugeReportIsCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<testsuite name="big">`)
	for i := 0; i < 5000; i++ {
		b.WriteString(`<testcase classname="c" name="t`)
		b.WriteString(strings.Repeat("x", 50))
		b.WriteString(`"><failure message="m">`)
		b.WriteString(strings.Repeat("y", 200))
		b.WriteString(`</failure></testcase>`)
	}
	b.WriteString(`</testsuite>`)
	report, err := Parse(strings.NewReader(b.String()), Limits{MaxBytes: 64 << 10})
	if err == nil || !report.Truncated {
		t.Fatalf("a capped report must say so: %v", err)
	}
	if len(report.Cases) == 0 || len(report.Cases) >= 5000 {
		t.Fatalf("cases %d", len(report.Cases))
	}
	report, err = Parse(strings.NewReader(b.String()), Limits{MaxCases: 10})
	if err != nil || !report.Truncated || len(report.Cases) != 10 {
		t.Fatalf("case limit: %v %v %d", err, report.Truncated, len(report.Cases))
	}
	report, err = Parse(strings.NewReader(b.String()), Limits{MaxMessageBytes: 20})
	if err != nil || len(report.Cases[0].Message) > 20 {
		t.Fatalf("message limit: %v %q", err, report.Cases[0].Message)
	}
}

func TestParseDepthLimit(t *testing.T) {
	doc := "<testsuites>" + strings.Repeat("<testsuite>", 100) + strings.Repeat("</testsuite>", 100) + "</testsuites>"
	if _, err := Parse(strings.NewReader(doc), Limits{}); err == nil || !strings.Contains(err.Error(), "levels") {
		t.Fatalf("depth: %v", err)
	}
}

func TestFirstLinesAndConvention(t *testing.T) {
	lines := FirstLines("\n first\n\nsecond "+strings.Repeat("z", 300)+"\nthird\nfourth", 3, 20)
	if len(lines) != 3 || lines[0] != " first" || !strings.HasSuffix(lines[1], "…") {
		t.Fatalf("%q", lines)
	}
	for path, want := range map[string]bool{
		"junit.xml": true, "reports/TEST-junit-unit.xml": true, "a/JUnit.XML": true,
		"junit.json": false, "results.xml": false, "junit/results.xml": false,
	} {
		if LooksLikeReport(path) != want {
			t.Errorf("%s: want %v", path, want)
		}
	}
}
