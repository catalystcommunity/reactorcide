// Package testresults ingests the JUnit reports of a job, detects flaky
// tests, and applies test quarantine (WP15, plan items F5 and A4,
// docs/artifacts-and-tests.md).
package testresults

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/artifacts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/testreports"
)

// TestIDLength is the number of hex characters of a test ID.
const TestIDLength = 32

// TestID is the stable ID of a test: a hash of the project, the suite, the
// class, and the name. The same test in another project has another ID.
func TestID(projectID, suite, classname, name string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{"reactorcide-test-v1", projectID, suite, classname, name}, "\x00")))
	return hex.EncodeToString(sum[:])[:TestIDLength]
}

// ValidTestID reports whether value has the form of a test ID.
func ValidTestID(value string) bool {
	if len(value) != TestIDLength {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

// DefaultFlakyWindow is the detection window of flaky tests.
const DefaultFlakyWindow = 30 * 24 * time.Hour

// FlakyWindowFromEnv reads REACTORCIDE_FLAKY_WINDOW_DAYS (default 30).
func FlakyWindowFromEnv() time.Duration {
	if value := strings.TrimSpace(os.Getenv("REACTORCIDE_FLAKY_WINDOW_DAYS")); value != "" {
		if days, err := strconv.Atoi(value); err == nil && days > 0 && days <= 3650 {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	return DefaultFlakyWindow
}

// MaxCasesPerJob bounds the stored results of one job, over all reports.
const MaxCasesPerJob = 50000

// Ingestor reads the test reports of a finished job.
type Ingestor struct {
	Results     store.TestResultStore
	Quarantines store.TestQuarantineStore
	Artifacts   *artifacts.Service
	Limits      testreports.Limits
	FlakyWindow time.Duration
	// Mask removes secret values from the text fields of a result (suite,
	// class, name, file, and message). Nil keeps them.
	Mask func(string) string
	// TestExit is the test_exit value of the workflow output of the job
	// (DeclaredTestExit). The quarantine override applies only when it is
	// true.
	TestExit bool
	Now      func() time.Time
}

func (in *Ingestor) now() time.Time {
	if in.Now != nil {
		return in.Now().UTC()
	}
	return time.Now().UTC()
}

// Ingest parses the complete test_report artifacts of job, stores the
// results and the summary, and updates the flakiness records. It returns
// nil and no error when the job has no report. quarantinePolicy is the
// quarantine_policy of the workflow node of the job ("" for none).
func (in *Ingestor) Ingest(ctx context.Context, job *models.Job, list []models.JobArtifact, quarantinePolicy string) (*models.JobTestSummary, error) {
	if in == nil || in.Results == nil || in.Artifacts == nil || job == nil {
		return nil, nil
	}
	reports := []models.JobArtifact{}
	for _, artifact := range list {
		if artifact.Kind == models.ArtifactKindTestReport && artifact.Status == models.ArtifactStatusComplete {
			reports = append(reports, artifact)
		}
	}
	if len(reports) == 0 {
		return nil, nil
	}
	projectID := ""
	if job.ProjectID != nil {
		projectID = *job.ProjectID
	}
	commitSHA := ""
	if job.CommitSHA != nil {
		commitSHA = *job.CommitSHA
	}
	summary := &models.JobTestSummary{JobID: job.JobID, Reports: len(reports), ParseErrors: []string{}, QuarantinePolicy: quarantinePolicy,
		TestExit: in.TestExit}
	results := []models.TestResult{}
	for i := range reports {
		report := &reports[i]
		remaining := MaxCasesPerJob - len(results)
		if remaining <= 0 {
			summary.Truncated = true
			break
		}
		limits := in.Limits
		if limits.MaxCases <= 0 || limits.MaxCases > remaining {
			limits.MaxCases = remaining
		}
		cases, truncated, err := in.parse(ctx, report, limits)
		if truncated {
			summary.Truncated = true
		}
		if err != nil {
			summary.ParseErrors = append(summary.ParseErrors, truncate(fmt.Sprintf("%s: %s", report.Path, err.Error()), 500))
		}
		artifactID := report.ArtifactID
		for _, c := range cases {
			// A secret value in a test name is masked like a secret in the
			// message. The test ID is a hash of the masked names, so the ID
			// is not a hash of a secret either.
			suite, classname, name, file := in.mask(c.Suite, 1024), in.mask(c.Classname, 1024), in.mask(c.Name, 1024), in.mask(c.File, 1024)
			result := models.TestResult{
				JobID: job.JobID, ProjectID: job.ProjectID, ArtifactID: &artifactID,
				TestID: TestID(projectID, suite, classname, name),
				Suite:  suite, Classname: classname, Name: name, Status: c.Status,
				DurationMs: c.Duration.Milliseconds(), Message: in.mask(c.Message, 4096), File: file, Line: c.Line, CommitSHA: commitSHA,
			}
			results = append(results, result)
		}
	}
	quarantined := map[string]bool{}
	if in.Quarantines != nil && projectID != "" {
		active, err := in.Quarantines.ListTestQuarantines(ctx, projectID, false, in.now())
		if err != nil {
			return nil, fmt.Errorf("read test quarantines: %w", err)
		}
		for _, q := range active {
			quarantined[q.TestID] = true
		}
	}
	for _, result := range results {
		summary.Total++
		switch result.Status {
		case models.TestStatusPass:
			summary.Passed++
		case models.TestStatusFail:
			summary.Failed++
		case models.TestStatusError:
			summary.Errored++
		case models.TestStatusSkip:
			summary.Skipped++
		}
		if result.Failed() && quarantined[result.TestID] {
			summary.QuarantinedFailures++
		}
	}
	summary.QuarantineOverride = QuarantineOverride(job, summary)
	if err := in.Results.ReplaceJobTestResults(ctx, job.JobID, results, summary); err != nil {
		return nil, fmt.Errorf("store test results: %w", err)
	}
	if projectID != "" {
		window := in.FlakyWindow
		if window <= 0 {
			window = DefaultFlakyWindow
		}
		flaky, err := in.Results.RecordTestRuns(ctx, projectID, commitSHA, results, window)
		if err != nil {
			return summary, fmt.Errorf("record test runs: %w", err)
		}
		if len(flaky) > 0 {
			summary.Flaky = len(flaky)
			if err := in.Results.SaveJobTestSummary(ctx, summary); err != nil {
				return summary, fmt.Errorf("store test summary: %w", err)
			}
		}
	}
	return summary, nil
}

func (in *Ingestor) parse(ctx context.Context, report *models.JobArtifact, limits testreports.Limits) ([]testreports.Case, bool, error) {
	reader, err := in.Artifacts.Open(ctx, report)
	if err != nil {
		return nil, false, err
	}
	defer reader.Close()
	parsed, err := testreports.Parse(reader, limits)
	if parsed == nil {
		return nil, false, err
	}
	return parsed.Cases, parsed.Truncated, err
}

// mask applies Mask to one text field and keeps it in limit bytes. The
// parser already bounds the fields; the limit only applies after masking.
func (in *Ingestor) mask(value string, limit int) string {
	if in.Mask == nil || value == "" {
		return value
	}
	return truncate(in.Mask(value), limit)
}

type testExitDocument struct {
	TestExit *bool `json:"test_exit"`
}

// DeclaredTestExit reads `test_exit` of a workflow output document. Only a
// JSON true counts.
func DeclaredTestExit(workflowOutput []byte) bool {
	if len(workflowOutput) == 0 {
		return false
	}
	var doc testExitDocument
	if err := json.Unmarshal(workflowOutput, &doc); err != nil || doc.TestExit == nil {
		return false
	}
	return *doc.TestExit
}

// QuarantineOverride reports whether the workflow node of job counts as a
// success although the job failed. All of these must be true:
//   - the node has quarantine_policy ignore_quarantined,
//   - the workflow output of the job declared test_exit: true: the job
//     states that its non-zero exit came from the test runner,
//   - the job failed with the class "job" (its own exit code),
//   - the reports were read in full, there are failed tests, and every
//     failed test is quarantined.
//
// A timeout, an OOM kill, or an infrastructure failure is never overridden.
//
// The policy trusts the report of the job: Reactorcide cannot see why a
// process exited. A job step that fails after the tests (a lint, a build, a
// push) and still declares test_exit: true is hidden. Thus the override is
// an opt-in of the workflow owner, who writes the trusted CI definition: a
// job that does not declare test_exit keeps its failure.
func QuarantineOverride(job *models.Job, summary *models.JobTestSummary) bool {
	if job == nil || summary == nil || summary.QuarantinePolicy != models.QuarantinePolicyIgnoreQuarantined {
		return false
	}
	if !summary.TestExit {
		return false
	}
	if job.Status != "failed" || job.FailureClassValue() != models.FailureClassJob {
		return false
	}
	if len(summary.ParseErrors) > 0 || summary.Truncated {
		// An unread part of a report can hold a failure that is not
		// quarantined.
		return false
	}
	failures := summary.Failed + summary.Errored
	return failures > 0 && summary.QuarantinedFailures == failures
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && (value[cut]&0xC0) == 0x80 {
		cut--
	}
	return value[:cut]
}
