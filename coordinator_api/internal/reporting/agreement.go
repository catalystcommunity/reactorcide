package reporting

import (
	"path"
	"sort"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// The agreement report (plan item A3). For a project with agreement
// contexts, the coordinator stores the Reactorcide result of each commit when
// the event of the commit has a final result. A background pass on one
// replica (lease AgreementLeaseName) then reads the commit statuses and the
// check runs of the commit from the VCS and compares them. The other CI
// system can finish later, so the pass reads again with a backoff until every
// configured context has a final result, or until the window closes.

// AgreementLeaseName is the lease of the background pass.
const AgreementLeaseName = "ci-agreement"

// Timings of the background pass.
const (
	AgreementFirstDelay = time.Minute
	AgreementMaxDelay   = time.Hour
	// AgreementWindow is how long the pass waits for the other CI system
	// after the Reactorcide result.
	AgreementWindow = 24 * time.Hour
)

// Kinds of an other result.
const (
	OtherKindStatus = "status"
	OtherKindCheck  = "check"
)

// IsOwnContext reports whether a status context or check name belongs to
// Reactorcide. The agreement report never compares Reactorcide with itself.
func IsOwnContext(name string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "reactorcide")
}

func validGlob(pattern string) bool {
	_, err := path.Match(pattern, "")
	return err == nil
}

func isGlob(pattern string) bool { return strings.ContainsAny(pattern, "*?[") }

// ContextMatches reports whether a configured context (a name or a glob)
// matches a status context or a check name.
func ContextMatches(pattern, name string) bool {
	if pattern == name {
		return true
	}
	if !isGlob(pattern) {
		return false
	}
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

// NormalizeStatusState maps a commit status state to success, failure, or
// pending.
func NormalizeStatusState(state string) string {
	switch strings.ToLower(state) {
	case "success":
		return models.AgreementResultSuccess
	case "failure", "error":
		return models.AgreementResultFailure
	}
	return "pending"
}

// NormalizeCheckState maps a check run status and conclusion to success,
// failure, or pending. Neutral and skipped count as success, as in branch
// protection.
func NormalizeCheckState(status, conclusion string) string {
	if strings.ToLower(status) != "completed" {
		return "pending"
	}
	switch strings.ToLower(conclusion) {
	case "success", "neutral", "skipped":
		return models.AgreementResultSuccess
	case "failure", "cancelled", "timed_out", "action_required", "startup_failure":
		return models.AgreementResultFailure
	}
	return "pending"
}

// ReactorcideResult maps the state of "Reactorcide Checks" to the result of
// the agreement report. ok is false while the event has no final result.
func ReactorcideResult(state string) (string, bool) {
	switch strings.ToLower(state) {
	case "success":
		return models.AgreementResultSuccess, true
	case "failure", "error", "cancelled":
		return models.AgreementResultFailure, true
	}
	return "", false
}

// SelectOtherResults keeps the results whose name matches a configured
// context. Reactorcide contexts never count. A name that the VCS gives more
// than one time keeps its first entry (the VCS lists the newest first).
func SelectOtherResults(observed []models.AgreementOtherResult, patterns []string) []models.AgreementOtherResult {
	out := []models.AgreementOtherResult{}
	seen := map[string]bool{}
	for _, result := range observed {
		if IsOwnContext(result.Name) {
			continue
		}
		key := result.Kind + "\x00" + result.Name
		if seen[key] {
			continue
		}
		for _, pattern := range patterns {
			if ContextMatches(pattern, result.Name) {
				seen[key] = true
				out = append(out, result)
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// allPatternsPresent reports whether each configured name has a result. A
// glob needs at least one match.
func allPatternsPresent(results []models.AgreementOtherResult, patterns []string) bool {
	for _, pattern := range patterns {
		found := false
		for _, result := range results {
			if ContextMatches(pattern, result.Name) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// NextDelay is the backoff after a number of reads.
func NextDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := AgreementFirstDelay
	for i := 1; i < attempts && delay < AgreementMaxDelay; i++ {
		delay *= 2
	}
	if delay > AgreementMaxDelay {
		delay = AgreementMaxDelay
	}
	return delay
}

// Observe applies one read of the other CI system to a row. It returns the
// changed row. The row is final (status complete or no_other) when every
// configured context has a final result, or when the window closed.
func Observe(row models.CIAgreement, observed []models.AgreementOtherResult, patterns []string, now time.Time) models.CIAgreement {
	results := SelectOtherResults(observed, patterns)
	row.OtherResults = results
	row.Attempts++
	row.ObservedAt = &now
	row.LastError = ""
	windowClosed := !now.Before(row.ReactorcideCompletedAt.Add(AgreementWindow))

	final := []models.AgreementOtherResult{}
	pending := false
	for _, result := range results {
		if result.State == "pending" {
			pending = true
			continue
		}
		final = append(final, result)
	}
	other := ""
	if len(final) > 0 {
		other = models.AgreementResultSuccess
		for _, result := range final {
			if result.State == models.AgreementResultFailure {
				other = models.AgreementResultFailure
			}
		}
	}
	row.OtherResult = other
	done := (!pending && len(final) > 0 && allPatternsPresent(results, patterns)) || windowClosed
	if !done {
		row.Status = models.AgreementStatusPending
		row.Agree = nil
		next := now.Add(NextDelay(row.Attempts))
		row.NextCheckAt = &next
		return row
	}
	row.NextCheckAt = nil
	if other == "" {
		row.Status = models.AgreementStatusNoOther
		row.Agree = nil
		return row
	}
	row.Status = models.AgreementStatusComplete
	agree := other == row.ReactorcideResult
	row.Agree = &agree
	return row
}

// ObserveError records a failed read: the row stays pending with a backoff,
// until the window closes.
func ObserveError(row models.CIAgreement, err error, now time.Time) models.CIAgreement {
	row.Attempts++
	if err != nil {
		row.LastError = truncate(err.Error(), 500)
	}
	if !now.Before(row.ReactorcideCompletedAt.Add(AgreementWindow)) {
		row.NextCheckAt = nil
		if row.OtherResult == "" {
			row.Status = models.AgreementStatusNoOther
		} else {
			row.Status = models.AgreementStatusComplete
			agree := row.OtherResult == row.ReactorcideResult
			row.Agree = &agree
		}
		return row
	}
	next := now.Add(NextDelay(row.Attempts))
	row.NextCheckAt = &next
	return row
}

// Close ends the comparison of a row with the results that it has (for
// example when the agreement contexts were removed).
func Close(row models.CIAgreement) models.CIAgreement {
	row.NextCheckAt = nil
	if row.OtherResult == "" {
		row.Status = models.AgreementStatusNoOther
		row.Agree = nil
		return row
	}
	row.Status = models.AgreementStatusComplete
	agree := row.OtherResult == row.ReactorcideResult
	row.Agree = &agree
	return row
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// Summary is the agreement summary of a project.
type Summary struct {
	Total     int `json:"total"`
	Compared  int `json:"compared"`
	Agreed    int `json:"agreed"`
	Disagreed int `json:"disagreed"`
	Pending   int `json:"pending"`
	NoOther   int `json:"no_other"`
	// Rate is Agreed / Compared, or nil when nothing was compared.
	Rate *float64 `json:"rate,omitempty"`
	// Disagreements are the compared rows that do not agree, newest first.
	Disagreements []models.CIAgreement `json:"disagreements"`
	// FalseFailures: Reactorcide failed, the other CI passed.
	FalseFailures int `json:"reactorcide_failed_other_passed"`
	// MissedFailures: Reactorcide passed, the other CI failed.
	MissedFailures int `json:"reactorcide_passed_other_failed"`
}

// Summarize counts the rows.
func Summarize(rows []models.CIAgreement) Summary {
	summary := Summary{Disagreements: []models.CIAgreement{}}
	for _, row := range rows {
		summary.Total++
		switch row.Status {
		case models.AgreementStatusPending:
			summary.Pending++
			continue
		case models.AgreementStatusNoOther:
			summary.NoOther++
			continue
		}
		if row.Agree == nil {
			continue
		}
		summary.Compared++
		if *row.Agree {
			summary.Agreed++
			continue
		}
		summary.Disagreed++
		summary.Disagreements = append(summary.Disagreements, row)
		if row.ReactorcideResult == models.AgreementResultFailure {
			summary.FalseFailures++
		} else {
			summary.MissedFailures++
		}
	}
	if summary.Compared > 0 {
		rate := float64(summary.Agreed) / float64(summary.Compared)
		summary.Rate = &rate
	}
	return summary
}
