package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v2"
)

// Report modes, the agreement report, the code owners import, and path
// acceptances (docs/reporting-and-ownership.md).

type reportingPathOwnerRule struct {
	Pattern string   `json:"pattern" yaml:"pattern"`
	Owners  []string `json:"owners" yaml:"owners"`
}

type reportingEffective struct {
	ReportMode                string                   `json:"report_mode" yaml:"report_mode"`
	ReportModeSource          string                   `json:"report_mode_source" yaml:"report_mode_source"`
	AgreementContexts         []string                 `json:"agreement_contexts" yaml:"agreement_contexts"`
	AgreementContextsSource   string                   `json:"agreement_contexts_source" yaml:"agreement_contexts_source"`
	CodeownersImport          string                   `json:"codeowners_import" yaml:"codeowners_import"`
	CodeownersImportSource    string                   `json:"codeowners_import_source" yaml:"codeowners_import_source"`
	CodeownersCacheTTLSeconds int                      `json:"codeowners_cache_ttl_seconds" yaml:"codeowners_cache_ttl_seconds"`
	PathAcceptance            string                   `json:"path_acceptance" yaml:"path_acceptance"`
	PathAcceptanceSource      string                   `json:"path_acceptance_source" yaml:"path_acceptance_source"`
	PathOwners                []reportingPathOwnerRule `json:"path_owners" yaml:"path_owners"`
	PathOwnersSource          string                   `json:"path_owners_source" yaml:"path_owners_source"`
}

type reportingSettingsResponse struct {
	Effective   reportingEffective     `json:"effective" yaml:"effective"`
	Project     map[string]interface{} `json:"project" yaml:"project"`
	Permissions map[string]bool        `json:"permissions" yaml:"permissions"`
}

type reportingOrgSettings struct {
	ReportMode                string                   `json:"report_mode" yaml:"report_mode"`
	AgreementContexts         []string                 `json:"agreement_contexts" yaml:"agreement_contexts"`
	CodeownersImport          string                   `json:"codeowners_import" yaml:"codeowners_import"`
	CodeownersCacheTTLSeconds int                      `json:"codeowners_cache_ttl_seconds" yaml:"codeowners_cache_ttl_seconds"`
	PathAcceptance            string                   `json:"path_acceptance" yaml:"path_acceptance"`
	PathOwners                []reportingPathOwnerRule `json:"path_owners" yaml:"path_owners"`
	CanManage                 bool                     `json:"can_manage,omitempty" yaml:"can_manage,omitempty"`
}

type agreementOtherResult struct {
	Name      string `json:"name" yaml:"name"`
	Kind      string `json:"kind" yaml:"kind"`
	State     string `json:"state" yaml:"state"`
	TargetURL string `json:"target_url,omitempty" yaml:"target_url,omitempty"`
}

type agreementRow struct {
	CommitSHA              string                 `json:"commit_sha" yaml:"commit_sha"`
	PRNumber               *int                   `json:"pr_number,omitempty" yaml:"pr_number,omitempty"`
	Branch                 string                 `json:"branch,omitempty" yaml:"branch,omitempty"`
	ReactorcideResult      string                 `json:"reactorcide_result" yaml:"reactorcide_result"`
	OtherResult            string                 `json:"other_result,omitempty" yaml:"other_result,omitempty"`
	OtherResults           []agreementOtherResult `json:"other_results" yaml:"other_results"`
	Agree                  *bool                  `json:"agree,omitempty" yaml:"agree,omitempty"`
	Status                 string                 `json:"status" yaml:"status"`
	ReactorcideCompletedAt time.Time              `json:"reactorcide_completed_at" yaml:"reactorcide_completed_at"`
}

type agreementSummary struct {
	Total          int            `json:"total" yaml:"total"`
	Compared       int            `json:"compared" yaml:"compared"`
	Agreed         int            `json:"agreed" yaml:"agreed"`
	Disagreed      int            `json:"disagreed" yaml:"disagreed"`
	Pending        int            `json:"pending" yaml:"pending"`
	NoOther        int            `json:"no_other" yaml:"no_other"`
	Rate           *float64       `json:"rate,omitempty" yaml:"rate,omitempty"`
	Disagreements  []agreementRow `json:"disagreements" yaml:"disagreements"`
	FalseFailures  int            `json:"reactorcide_failed_other_passed" yaml:"reactorcide_failed_other_passed"`
	MissedFailures int            `json:"reactorcide_passed_other_failed" yaml:"reactorcide_passed_other_failed"`
}

type agreementResponse struct {
	Contexts []string         `json:"contexts" yaml:"contexts"`
	Summary  agreementSummary `json:"summary" yaml:"summary"`
	Rows     []agreementRow   `json:"rows" yaml:"rows"`
}

type pathAcceptanceRow struct {
	AcceptanceID       string     `json:"acceptance_id" yaml:"acceptance_id"`
	WorkflowSecurityID string     `json:"workflow_security_id" yaml:"workflow_security_id"`
	PathPattern        string     `json:"path_pattern" yaml:"path_pattern"`
	OwnerSubject       string     `json:"owner_subject" yaml:"owner_subject"`
	State              string     `json:"state" yaml:"state"`
	Reason             string     `json:"reason,omitempty" yaml:"reason,omitempty"`
	RequestedBy        *string    `json:"requested_by,omitempty" yaml:"requested_by,omitempty"`
	AcceptedBy         *string    `json:"accepted_by,omitempty" yaml:"accepted_by,omitempty"`
	AcceptedAt         *time.Time `json:"accepted_at,omitempty" yaml:"accepted_at,omitempty"`
	RevokedAt          *time.Time `json:"revoked_at,omitempty" yaml:"revoked_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at" yaml:"created_at"`
}

type pathAcceptancesResponse struct {
	Mode       string                   `json:"path_acceptance" yaml:"path_acceptance"`
	Source     string                   `json:"ownership_source" yaml:"ownership_source"`
	Rows       []pathAcceptanceRow      `json:"rows" yaml:"rows"`
	PathOwners []reportingPathOwnerRule `json:"path_owners" yaml:"path_owners"`
}

func reportingProjectPath(ctx *cli.Context, suffix string) string {
	return "/api/v1/projects/" + url.PathEscape(strings.TrimSpace(ctx.String("project"))) + "/" + suffix
}

// parsePathOwnerRules reads 'PATTERN=SUBJECT [SUBJECT...]' values. An empty
// right side means "no owner".
func parsePathOwnerRules(values []string) ([]reportingPathOwnerRule, error) {
	rules := []reportingPathOwnerRule{}
	for _, value := range values {
		pattern, owners, ok := strings.Cut(value, "=")
		pattern = strings.TrimSpace(pattern)
		if !ok || pattern == "" {
			return nil, fmt.Errorf("path owner %q must be 'PATTERN=SUBJECT [SUBJECT...]'", value)
		}
		rules = append(rules, reportingPathOwnerRule{Pattern: pattern, Owners: strings.Fields(owners)})
	}
	return rules, nil
}

// agreementContextsFrom reads --agreement-context values and the lines of
// --agreement-contexts-file. A comma splits a flag value, so a name with a
// comma goes in the file.
func agreementContextsFrom(ctx *cli.Context) ([]string, error) {
	contexts := append([]string{}, ctx.StringSlice("agreement-context")...)
	if file := ctx.String("agreement-contexts-file"); file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				contexts = append(contexts, line)
			}
		}
	}
	return contexts, nil
}

func reportingSettingFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "report-mode", Usage: "full, statuses_only, or silent"},
		&cli.StringSliceFlag{Name: "agreement-context", Usage: "status context or check name of the other CI (repeat; a glob is allowed; a comma splits flag values)"},
		&cli.StringFlag{Name: "agreement-contexts-file", Usage: "file with one agreement context on each line"},
		&cli.StringFlag{Name: "codeowners-import", Usage: "off or on"},
		&cli.IntFlag{Name: "codeowners-ttl", Usage: "cache TTL of a parsed code owners file, in seconds (10 to 86400)"},
		&cli.StringFlag{Name: "path-acceptance", Usage: "off, warn, or enforce"},
		&cli.StringSliceFlag{Name: "path-owner", Usage: "'PATTERN=SUBJECT [SUBJECT...]' registry rule (repeat; the last match wins)"},
	}
}

func renderReportingSettings(w *tabwriter.Writer, e reportingEffective) {
	fmt.Fprintf(w, "report_mode\t%s\t(%s)\n", e.ReportMode, e.ReportModeSource)
	contexts := strings.Join(e.AgreementContexts, ", ")
	if contexts == "" {
		contexts = "- (no agreement report)"
	}
	fmt.Fprintf(w, "agreement_contexts\t%s\t(%s)\n", contexts, e.AgreementContextsSource)
	fmt.Fprintf(w, "codeowners_import\t%s\t(%s, cache %ds)\n", e.CodeownersImport, e.CodeownersImportSource, e.CodeownersCacheTTLSeconds)
	fmt.Fprintf(w, "path_acceptance\t%s\t(%s)\n", e.PathAcceptance, e.PathAcceptanceSource)
	for i, rule := range e.PathOwners {
		owners := strings.Join(rule.Owners, " ")
		if owners == "" {
			owners = "(no owner)"
		}
		fmt.Fprintf(w, "path_owner %d\t%s\t%s\n", i+1, rule.Pattern, owners)
	}
}

func renderPathAcceptances(w *tabwriter.Writer, rows []pathAcceptanceRow) {
	fmt.Fprintln(w, "ID\tWORKFLOW\tPATTERN\tOWNER\tSTATE\tACCEPTED BY\tACCEPTED AT\tREASON")
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", row.AcceptanceID, row.WorkflowSecurityID, row.PathPattern, row.OwnerSubject,
			row.State, principalText(row.AcceptedBy), timeText(row.AcceptedAt), row.Reason)
	}
}

// ReportingCommand manages report modes, the agreement report, and the
// code owners and path ownership settings.
var ReportingCommand = &cli.Command{
	Name:  "reporting",
	Usage: "Manage report modes, the agreement report, the code owners import, and path ownership",
	Subcommands: []*cli.Command{
		{
			Name: "show", Usage: "Show the reporting settings of a project",
			Flags: append(apiFlags(), requiredSetProjectFlag(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response reportingSettingsResponse
				if err := client.doJSON(http.MethodGet, reportingProjectPath(ctx, "reporting"), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) { renderReportingSettings(w, response.Effective) })
			},
		},
		{
			Name: "set", Usage: "Change the reporting settings of a project (a value that is not given stays)",
			Flags: append(append(apiFlags(), requiredSetProjectFlag(), formatFlag(),
				&cli.StringSliceFlag{Name: "inherit", Usage: "use the organization value again (repeat): report_mode, agreement_contexts, codeowners_import, codeowners_cache_ttl_seconds, path_acceptance, path_owners"},
				&cli.BoolFlag{Name: "clear-agreement-contexts", Usage: "set no agreement context (turn the agreement report off)"},
				&cli.BoolFlag{Name: "clear-path-owners", Usage: "set an empty path owner registry"}),
				reportingSettingFlags()...),
			Action: func(ctx *cli.Context) error {
				body := map[string]interface{}{}
				if ctx.IsSet("report-mode") {
					body["report_mode"] = ctx.String("report-mode")
				}
				contexts, err := agreementContextsFrom(ctx)
				if err != nil {
					return err
				}
				if len(contexts) > 0 {
					body["agreement_contexts"] = contexts
				} else if ctx.Bool("clear-agreement-contexts") {
					body["agreement_contexts"] = []string{}
				}
				if ctx.IsSet("codeowners-import") {
					body["codeowners_import"] = ctx.String("codeowners-import")
				}
				if ctx.IsSet("codeowners-ttl") {
					body["codeowners_cache_ttl_seconds"] = ctx.Int("codeowners-ttl")
				}
				if ctx.IsSet("path-acceptance") {
					body["path_acceptance"] = ctx.String("path-acceptance")
				}
				rules, err := parsePathOwnerRules(ctx.StringSlice("path-owner"))
				if err != nil {
					return err
				}
				if len(rules) > 0 {
					body["path_owners"] = rules
				} else if ctx.Bool("clear-path-owners") {
					body["path_owners"] = []reportingPathOwnerRule{}
				}
				if inherit := ctx.StringSlice("inherit"); len(inherit) > 0 {
					body["inherit"] = inherit
				}
				if len(body) == 0 {
					return fmt.Errorf("give at least one setting to change")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response reportingSettingsResponse
				if err := client.doJSON(http.MethodPut, reportingProjectPath(ctx, "reporting"), body, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) { renderReportingSettings(w, response.Effective) })
			},
		},
		{
			Name: "org-show", Usage: "Show the reporting defaults of an organization",
			Flags: append(apiFlags(), formatFlag(), &cli.StringFlag{Name: "org", Required: true, Usage: "organization name"}),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response reportingOrgSettings
				path := "/api/v1/organizations/" + url.PathEscape(ctx.String("org")) + "/reporting-settings"
				if err := client.doJSON(http.MethodGet, path, nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					renderReportingSettings(w, reportingEffective{ReportMode: response.ReportMode, ReportModeSource: "org",
						AgreementContexts: response.AgreementContexts, AgreementContextsSource: "org", CodeownersImport: response.CodeownersImport,
						CodeownersImportSource: "org", CodeownersCacheTTLSeconds: response.CodeownersCacheTTLSeconds,
						PathAcceptance: response.PathAcceptance, PathAcceptanceSource: "org", PathOwners: response.PathOwners, PathOwnersSource: "org"})
				})
			},
		},
		{
			Name: "org-set", Usage: "Replace the reporting defaults of an organization (a value that is not given gets its default)",
			Flags: append(append(apiFlags(), formatFlag(), &cli.StringFlag{Name: "org", Required: true, Usage: "organization name"}),
				reportingSettingFlags()...),
			Action: func(ctx *cli.Context) error {
				contexts, err := agreementContextsFrom(ctx)
				if err != nil {
					return err
				}
				rules, err := parsePathOwnerRules(ctx.StringSlice("path-owner"))
				if err != nil {
					return err
				}
				body := reportingOrgSettings{ReportMode: ctx.String("report-mode"), AgreementContexts: contexts,
					CodeownersImport: ctx.String("codeowners-import"), CodeownersCacheTTLSeconds: ctx.Int("codeowners-ttl"),
					PathAcceptance: ctx.String("path-acceptance"), PathOwners: rules}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response reportingOrgSettings
				path := "/api/v1/organizations/" + url.PathEscape(ctx.String("org")) + "/reporting-settings"
				if err := client.doJSON(http.MethodPut, path, body, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "report_mode\t%s\ncodeowners_import\t%s\npath_acceptance\t%s\n", response.ReportMode, response.CodeownersImport, response.PathAcceptance)
				})
			},
		},
		{
			Name: "agreement", Usage: "Show the agreement of Reactorcide with another CI system, per commit",
			Flags: append(apiFlags(), requiredSetProjectFlag(), formatFlag(),
				&cli.StringFlag{Name: "since", Usage: "an RFC 3339 time or a duration back from now (7d, 72h)"},
				&cli.IntFlag{Name: "limit", Value: 500},
				&cli.BoolFlag{Name: "all", Usage: "list every commit, not only the disagreements"}),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				query := url.Values{}
				if since := ctx.String("since"); since != "" {
					query.Set("since", since)
				}
				query.Set("limit", fmt.Sprint(ctx.Int("limit")))
				var response agreementResponse
				if err := client.doJSON(http.MethodGet, reportingProjectPath(ctx, "agreement")+"?"+query.Encode(), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					s := response.Summary
					rate := "-"
					if s.Rate != nil {
						rate = fmt.Sprintf("%.1f%%", *s.Rate*100)
					}
					fmt.Fprintf(w, "contexts\t%s\n", strings.Join(response.Contexts, ", "))
					fmt.Fprintf(w, "agreement\t%s (%d of %d compared)\n", rate, s.Agreed, s.Compared)
					fmt.Fprintf(w, "reactorcide failed, other passed\t%d\n", s.FalseFailures)
					fmt.Fprintf(w, "reactorcide passed, other failed\t%d\n", s.MissedFailures)
					fmt.Fprintf(w, "pending\t%d\nno other result\t%d\n\n", s.Pending, s.NoOther)
					rows := s.Disagreements
					if ctx.Bool("all") {
						rows = response.Rows
					}
					fmt.Fprintln(w, "COMMIT\tPR\tREACTORCIDE\tOTHER\tSTATUS\tAGREE\tCOMPLETED")
					for _, row := range rows {
						pr := "-"
						if row.PRNumber != nil {
							pr = fmt.Sprint(*row.PRNumber)
						}
						agree := "-"
						if row.Agree != nil {
							agree = map[bool]string{true: "yes", false: "NO"}[*row.Agree]
						}
						other := row.OtherResult
						if other == "" {
							other = "-"
						}
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", shortSHA(row.CommitSHA), pr, row.ReactorcideResult, other, row.Status, agree,
							row.ReactorcideCompletedAt.UTC().Format(time.RFC3339))
					}
				})
			},
		},
	},
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func pathAcceptanceDecisionCommand(name, usage string) *cli.Command {
	return &cli.Command{
		Name: name, ArgsUsage: "<acceptance-id>", Usage: usage,
		Flags: append(apiFlags(), requiredSetProjectFlag(), formatFlag(), &cli.StringFlag{Name: "reason"}),
		Action: func(ctx *cli.Context) error {
			if err := requireArgs(ctx, 1, "path-acceptances "+name+" --project <project> <acceptance-id>"); err != nil {
				return err
			}
			client, err := newAPIClient(ctx)
			if err != nil {
				return err
			}
			var row pathAcceptanceRow
			path := reportingProjectPath(ctx, "path-acceptances") + "/" + url.PathEscape(ctx.Args().First()) + "/" + name
			if err := client.doJSON(http.MethodPost, path, map[string]string{"reason": ctx.String("reason")}, http.StatusOK, &row); err != nil {
				return err
			}
			return render(ctx.String("format"), row, func(w *tabwriter.Writer) { renderPathAcceptances(w, []pathAcceptanceRow{row}) })
		},
	}
}

// PathAcceptancesCommand manages owner acceptances of path dependencies.
var PathAcceptancesCommand = &cli.Command{
	Name:  "path-acceptances",
	Usage: "Request, accept, reject, and revoke owner acceptances of workflow path dependencies",
	Subcommands: []*cli.Command{
		{
			Name: "list", Usage: "List the path acceptances of a project",
			Flags: append(apiFlags(), requiredSetProjectFlag(), formatFlag(),
				&cli.StringFlag{Name: "state", Usage: "requested, accepted, rejected, or revoked"}),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				path := reportingProjectPath(ctx, "path-acceptances")
				if state := ctx.String("state"); state != "" {
					path += "?state=" + url.QueryEscape(state)
				}
				var response pathAcceptancesResponse
				if err := client.doJSON(http.MethodGet, path, nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "path_acceptance\t%s\nownership\t%s\n\n", response.Mode, response.Source)
					renderPathAcceptances(w, response.Rows)
				})
			},
		},
		{
			Name: "request", ArgsUsage: "<workflow-id>", Usage: "Ask the owner of a path area to accept a path dependency of a workflow",
			Flags: append(apiFlags(), requiredSetProjectFlag(), formatFlag(),
				&cli.StringFlag{Name: "pattern", Required: true, Usage: "path pattern (code owners syntax), for example /payments/"},
				&cli.StringFlag{Name: "owner", Required: true, Usage: "owner subject, for example vcs_team:acme/payments"},
				&cli.StringFlag{Name: "reason"},
				&cli.BoolFlag{Name: "accept", Usage: "accept at once (the caller must be the owner or an org admin)"}),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "path-acceptances request --project <project> --pattern <pattern> --owner <subject> <workflow-id>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				body := map[string]interface{}{"workflow_security_id": ctx.Args().First(), "path_pattern": ctx.String("pattern"),
					"owner_subject": ctx.String("owner"), "reason": ctx.String("reason"), "accept": ctx.Bool("accept")}
				var row pathAcceptanceRow
				if err := client.doJSON(http.MethodPost, reportingProjectPath(ctx, "path-acceptances"), body, http.StatusCreated, &row); err != nil {
					return err
				}
				return render(ctx.String("format"), row, func(w *tabwriter.Writer) { renderPathAcceptances(w, []pathAcceptanceRow{row}) })
			},
		},
		pathAcceptanceDecisionCommand("accept", "Accept a requested path dependency (an owner of the path)"),
		pathAcceptanceDecisionCommand("reject", "Reject a requested path dependency (an owner of the path)"),
		pathAcceptanceDecisionCommand("revoke", "Revoke a request or an acceptance"),
	},
}
