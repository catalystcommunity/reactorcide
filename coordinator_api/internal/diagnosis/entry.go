package diagnosis

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/runshifts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// ValidationError is an invalid diagnosis input. REST answers 400 and CSIL
// "invalid_argument".
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

// IsValidationError reports whether err is a ValidationError.
func IsValidationError(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

// Confidences are the confidence values an entry can have. Empty is none.
var Confidences = []string{"low", "medium", "high"}

var categoryPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Input is a diagnosis entry as a principal posts it.
type Input struct {
	Title      string
	Body       string
	Confidence string
	Category   string
	Links      []models.DiagnosisLink
	// SuggestedShifts uses the keys of a re-run (internal/runshifts): cpu,
	// cpu_request, memory, storage, storage_request, timeout.
	SuggestedShifts map[string]any
}

// Clean is a validated entry. Title and Body are cleaned of control
// characters but are not escaped: the UI shows them as text, and the pull
// request comment sanitizes them (CommentSection).
type Clean struct {
	Title           string
	Body            string
	Confidence      string
	Category        string
	Links           []models.DiagnosisLink
	SuggestedShifts *models.RunShifts
}

// Validate checks in and returns the clean form.
func (in Input) Validate() (Clean, error) {
	var out Clean
	title := strings.Join(strings.Fields(cleanControl(in.Title)), " ")
	if title == "" {
		return out, invalid("the title is required")
	}
	if utf8.RuneCountInString(title) > MaxTitleRunes {
		return out, invalid("the title has more than %d characters", MaxTitleRunes)
	}
	out.Title = title

	if len(in.Body) > MaxBodyBytes {
		return out, invalid("the body has more than %d bytes", MaxBodyBytes)
	}
	out.Body = strings.TrimSpace(cleanControl(in.Body))
	// The sanitized form can be longer ("<" becomes "&lt;"). Refuse a body
	// whose comment form does not fit, so the stored entry always renders.
	if len(SanitizeMarkdown(out.Body)) > MaxBodyBytes {
		return out, invalid("the body is more than %d bytes after sanitization (each \"<\" becomes \"&lt;\")", MaxBodyBytes)
	}

	confidence := strings.ToLower(strings.TrimSpace(in.Confidence))
	if confidence != "" {
		known := false
		for _, value := range Confidences {
			known = known || value == confidence
		}
		if !known {
			return out, invalid("the confidence must be low, medium, or high")
		}
	}
	out.Confidence = confidence

	category := strings.ToLower(strings.TrimSpace(in.Category))
	if category != "" && !categoryPattern.MatchString(category) {
		return out, invalid("the category must have 1 to %d lowercase letters, digits, dots, underscores, or hyphens", MaxCategoryLength)
	}
	out.Category = category

	if len(in.Links) > MaxLinks {
		return out, invalid("an entry has at most %d links", MaxLinks)
	}
	out.Links = []models.DiagnosisLink{}
	for i, link := range in.Links {
		name := strings.Join(strings.Fields(cleanControl(link.Name)), " ")
		if name == "" || utf8.RuneCountInString(name) > MaxLinkNameRunes {
			return out, invalid("link %d: the name must have 1 to %d characters", i+1, MaxLinkNameRunes)
		}
		cleanURL, err := validateLinkURL(link.URL)
		if err != nil {
			return out, invalid("link %d: %v", i+1, err)
		}
		out.Links = append(out.Links, models.DiagnosisLink{Name: name, URL: cleanURL})
	}

	if len(in.SuggestedShifts) > 0 {
		shifts, err := runshifts.ParseMap(in.SuggestedShifts)
		if err != nil {
			return out, invalid("suggested shifts: %v", err)
		}
		out.SuggestedShifts = shifts.Ptr()
	}
	return out, nil
}

// validateLinkURL accepts an absolute http or https URL with a host and no
// user information.
func validateLinkURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxLinkURLLength {
		return "", fmt.Errorf("the URL must have 1 to %d characters", MaxLinkURLLength)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("the URL is not valid")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("the URL must use https or http")
	}
	if parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("the URL must have a host and no user information")
	}
	return parsed.String(), nil
}

// ShiftsText returns the suggested shifts in the form of a PR command
// argument list, for example "memory=8Gi timeout=2h0m0s".
func ShiftsText(shifts *models.RunShifts) string {
	if shifts == nil {
		return ""
	}
	return runshifts.Describe(*shifts)
}

// ReportEntryKey is the key of the entry of a diagnosis in the rolling pull
// request comment: one key for each (target, author).
func ReportEntryKey(report *models.DiagnosisReport) string {
	target := ""
	if report.JobID != nil {
		target = *report.JobID
	} else if report.WorkflowID != nil {
		target = *report.WorkflowID
	}
	return "diagnosis-" + target + "-" + report.AuthorPrincipalID
}

// AuthorLabel is the label of the author: the display name and the kind,
// for example "Diag Bot (service)". It is not sanitized.
func AuthorLabel(report *models.DiagnosisReport) string {
	name := strings.TrimSpace(report.AuthorDisplayName)
	if name == "" {
		name = report.AuthorPrincipalID
	}
	kind := report.AuthorKind
	if kind == "" {
		kind = "unknown"
	}
	return fmt.Sprintf("%s (%s)", name, kind)
}

// CommentSection returns the title, the status, and the Markdown body of the
// section of a diagnosis in the rolling pull request comment. subject is the
// name of the job, the node, or the workflow. pageURL is the job or workflow
// page, or "". Every text from the entry is sanitized.
func CommentSection(report *models.DiagnosisReport, subject, pageURL string) (title, status, body string) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		subject = report.TargetType
	}
	title = "Diagnosis of " + SanitizeInline(subject) + ": " + SanitizeInline(report.Title)
	status = "by " + SanitizeInline(AuthorLabel(report))

	var out strings.Builder
	meta := []string{}
	if report.Confidence != "" {
		meta = append(meta, "**Confidence:** "+report.Confidence)
	}
	if report.Category != "" {
		meta = append(meta, "**Category:** `"+report.Category+"`")
	}
	if len(meta) > 0 {
		out.WriteString(strings.Join(meta, " · ") + "\n\n")
	}
	text, cut := truncateMarkdown(SanitizeMarkdown(report.Body), MaxCommentBodyRunes)
	if text != "" {
		out.WriteString(text + "\n")
	}
	if cut {
		if pageURL != "" {
			fmt.Fprintf(&out, "\n_The diagnosis is longer. [Read all of it](%s)._\n", markdownURL(pageURL))
		} else {
			out.WriteString("\n_The diagnosis is longer. Read all of it on the Reactorcide page._\n")
		}
	}
	if shifts := ShiftsText(report.SuggestedShifts); shifts != "" {
		fmt.Fprintf(&out, "\n**Suggested re-run shifts:** `%s`\n", shifts)
	}
	if len(report.Links) > 0 {
		parts := make([]string, 0, len(report.Links))
		for _, link := range report.Links {
			parts = append(parts, fmt.Sprintf("[%s](%s)", SanitizeInline(link.Name), markdownURL(link.URL)))
		}
		out.WriteString("\n**Links:** " + strings.Join(parts, " · ") + "\n")
	}
	fmt.Fprintf(&out, "\n_Posted by the %s principal %s. Reactorcide does not check the content of a diagnosis._\n",
		SanitizeInline(kindOrUnknown(report.AuthorKind)), SanitizeInline(strings.TrimSpace(report.AuthorDisplayName)))
	return title, status, strings.TrimSpace(out.String())
}

func kindOrUnknown(kind string) string {
	if kind == "" {
		return "unknown"
	}
	return kind
}

// markdownURL keeps a URL inside the parentheses of a Markdown link.
func markdownURL(value string) string {
	replacer := strings.NewReplacer("(", "%28", ")", "%29", " ", "%20", "<", "%3C", ">", "%3E", "\n", "", "\r", "")
	return replacer.Replace(value)
}

// truncateMarkdown cuts sanitized Markdown to at most n runes at a line end
// when it can, and closes a fenced code block that the cut leaves open, so
// that the next sections of the comment render as normal text.
func truncateMarkdown(text string, n int) (string, bool) {
	cut, truncated := Truncate(text, n)
	if !truncated {
		return text, false
	}
	cut = strings.TrimSuffix(cut, "…")
	if index := strings.LastIndex(cut, "\n"); index > n/2 {
		cut = cut[:index]
	}
	cut = strings.TrimRight(cut, " \t\n") + " …"
	if fence := openFence(cut); fence != "" {
		cut += "\n" + fence
	}
	return cut, true
}

// openFence returns the closing fence of a fenced code block that is still
// open at the end of text, or "". It uses the same CommonMark rules as
// SanitizeMarkdown.
func openFence(text string) string {
	var open *fence
	for _, line := range strings.Split(text, "\n") {
		if open != nil {
			if open.closes(line) {
				open = nil
			}
			continue
		}
		if f, ok := fenceOpen(line); ok {
			open = &f
		}
	}
	if open == nil {
		return ""
	}
	return open.closing()
}
