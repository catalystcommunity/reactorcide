package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v2"
)

// requiredSetWorkflow is one row of GET .../required-set.
type requiredSetWorkflow struct {
	WorkflowSecurityID string     `json:"workflow_security_id" yaml:"workflow_security_id"`
	Name               string     `json:"name,omitempty" yaml:"name,omitempty"`
	BranchPattern      string     `json:"branch_pattern" yaml:"branch_pattern"`
	State              string     `json:"state" yaml:"state"`
	InSet              bool       `json:"in_set" yaml:"in_set"`
	Source             string     `json:"source,omitempty" yaml:"source,omitempty"`
	AddedBy            *string    `json:"added_by,omitempty" yaml:"added_by,omitempty"`
	AddedAt            *time.Time `json:"added_at,omitempty" yaml:"added_at,omitempty"`
	Reason             string     `json:"reason,omitempty" yaml:"reason,omitempty"`
	QuarantinedUntil   *time.Time `json:"quarantined_until,omitempty" yaml:"quarantined_until,omitempty"`
	QuarantineReason   string     `json:"quarantine_reason,omitempty" yaml:"quarantine_reason,omitempty"`
}

type requiredSetProposal struct {
	ProposalID         string     `json:"proposal_id" yaml:"proposal_id"`
	WorkflowSecurityID string     `json:"workflow_security_id" yaml:"workflow_security_id"`
	BranchPattern      string     `json:"branch_pattern" yaml:"branch_pattern"`
	Action             string     `json:"action" yaml:"action"`
	State              string     `json:"state" yaml:"state"`
	Reason             string     `json:"reason,omitempty" yaml:"reason,omitempty"`
	ProposedBy         *string    `json:"proposed_by,omitempty" yaml:"proposed_by,omitempty"`
	ProposedAt         time.Time  `json:"proposed_at" yaml:"proposed_at"`
	DecidedBy          *string    `json:"decided_by,omitempty" yaml:"decided_by,omitempty"`
	DecidedAt          *time.Time `json:"decided_at,omitempty" yaml:"decided_at,omitempty"`
}

type requiredSetApproverRule struct {
	Branches  []string `json:"branches" yaml:"branches"`
	Approvers []string `json:"approvers" yaml:"approvers"`
}

type requiredSetResponse struct {
	ProjectID         string                    `json:"project_id" yaml:"project_id"`
	Enabled           bool                      `json:"enabled" yaml:"enabled"`
	EnabledAt         *time.Time                `json:"enabled_at,omitempty" yaml:"enabled_at,omitempty"`
	Workflows         []requiredSetWorkflow     `json:"workflows" yaml:"workflows"`
	Proposals         []requiredSetProposal     `json:"proposals" yaml:"proposals"`
	Approvers         []string                  `json:"approvers" yaml:"approvers"`
	ApproversSource   string                    `json:"approvers_source" yaml:"approvers_source"`
	ApproverRules     []requiredSetApproverRule `json:"approver_rules" yaml:"approver_rules"`
	MaxQuarantineDays int                       `json:"max_quarantine_days" yaml:"max_quarantine_days"`
}

func requiredSetProjectFlag() cli.Flag {
	return &cli.StringFlag{Name: "project", Required: true, Usage: "project ID or name"}
}

func requiredSetBase(ctx *cli.Context) string {
	return "/api/v1/projects/" + url.PathEscape(strings.TrimSpace(ctx.String("project"))) + "/required-set"
}

func branchText(pattern string) string {
	if pattern == "" {
		return "*all*"
	}
	return pattern
}

func timeText(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func principalText(value *string) string {
	if value == nil || *value == "" {
		return "-"
	}
	return *value
}

func renderRequiredSetProposals(w *tabwriter.Writer, proposals []requiredSetProposal) {
	fmt.Fprintln(w, "PROPOSAL\tACTION\tWORKFLOW\tBRANCH\tSTATE\tBY\tAT\tREASON")
	for _, p := range proposals {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", p.ProposalID, p.Action, p.WorkflowSecurityID, branchText(p.BranchPattern),
			p.State, principalText(p.ProposedBy), p.ProposedAt.UTC().Format(time.RFC3339), p.Reason)
	}
}

func requiredSetDecisionCommand(name, usage string) *cli.Command {
	return &cli.Command{
		Name: name, ArgsUsage: "<proposal-id>", Usage: usage,
		Flags: append(apiFlags(), requiredSetProjectFlag(), formatFlag(), &cli.StringFlag{Name: "reason"}),
		Action: func(ctx *cli.Context) error {
			if err := requireArgs(ctx, 1, "required-set "+name+" --project <project> <proposal-id>"); err != nil {
				return err
			}
			client, err := newAPIClient(ctx)
			if err != nil {
				return err
			}
			var proposal requiredSetProposal
			path := requiredSetBase(ctx) + "/proposals/" + url.PathEscape(ctx.Args().First()) + "/" + name
			if err := client.doJSON(http.MethodPost, path, map[string]string{"reason": ctx.String("reason")}, http.StatusOK, &proposal); err != nil {
				return err
			}
			return render(ctx.String("format"), proposal, func(w *tabwriter.Writer) {
				renderRequiredSetProposals(w, []requiredSetProposal{proposal})
			})
		},
	}
}

func requiredSetProposeCommand(name, action, usage string) *cli.Command {
	return &cli.Command{
		Name: name, ArgsUsage: "<workflow-id>", Usage: usage,
		Flags: append(apiFlags(), requiredSetProjectFlag(), formatFlag(),
			&cli.StringFlag{Name: "branch", Usage: "target branch glob of the entry (empty: every branch)"},
			&cli.StringFlag{Name: "reason"},
			&cli.BoolFlag{Name: "approve", Usage: "approve at once (the caller must be an approver)"}),
		Action: func(ctx *cli.Context) error {
			if err := requireArgs(ctx, 1, "required-set "+name+" --project <project> <workflow-id>"); err != nil {
				return err
			}
			client, err := newAPIClient(ctx)
			if err != nil {
				return err
			}
			body := map[string]interface{}{"action": action, "workflow": ctx.Args().First(), "branch_pattern": ctx.String("branch"),
				"reason": ctx.String("reason"), "approve": ctx.Bool("approve")}
			var proposal requiredSetProposal
			if err := client.doJSON(http.MethodPost, requiredSetBase(ctx)+"/proposals", body, http.StatusCreated, &proposal); err != nil {
				return err
			}
			return render(ctx.String("format"), proposal, func(w *tabwriter.Writer) {
				renderRequiredSetProposals(w, []requiredSetProposal{proposal})
			})
		},
	}
}

func requiredSetToggleCommand(name string) *cli.Command {
	usage := "Turn the required set of a project on. Every workflow the project knows goes into the set."
	if name == "disable" {
		usage = "Turn the required set of a project off. Every workflow blocks again."
	}
	return &cli.Command{
		Name: name, Usage: usage,
		Flags: append(apiFlags(), requiredSetProjectFlag(), formatFlag()),
		Action: func(ctx *cli.Context) error {
			client, err := newAPIClient(ctx)
			if err != nil {
				return err
			}
			var response struct {
				Enabled bool     `json:"enabled" yaml:"enabled"`
				Seeded  []string `json:"seeded" yaml:"seeded"`
			}
			if err := client.doJSON(http.MethodPost, requiredSetBase(ctx)+"/"+name, nil, http.StatusOK, &response); err != nil {
				return err
			}
			return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "enabled\t%t\n", response.Enabled)
				if response.Enabled {
					fmt.Fprintf(w, "added\t%s\n", strings.Join(response.Seeded, ", "))
				}
			})
		},
	}
}

// RequiredSetCommand manages the required set of a project
// (docs/required-set.md).
var RequiredSetCommand = &cli.Command{
	Name:  "required-set",
	Usage: "Manage the workflows that block a merge (required set), promotions, and quarantines",
	Subcommands: []*cli.Command{
		requiredSetToggleCommand("enable"),
		requiredSetToggleCommand("disable"),
		{
			Name: "list", Usage: "Show the workflows of a project and their state: required, advisory, or quarantined",
			Flags: append(apiFlags(), requiredSetProjectFlag(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response requiredSetResponse
				if err := client.doJSON(http.MethodGet, requiredSetBase(ctx), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "required set\t%s\n", map[bool]string{true: "on", false: "off (every workflow is required)"}[response.Enabled])
					approvers := strings.Join(response.Approvers, ", ")
					if approvers == "" {
						approvers = "org admins"
					}
					fmt.Fprintf(w, "approvers\t%s\n", approvers)
					for i, rule := range response.ApproverRules {
						fmt.Fprintf(w, "approver rule %d\tbranches %s: %s\n", i+1, strings.Join(rule.Branches, ", "), strings.Join(rule.Approvers, ", "))
					}
					fmt.Fprintln(w)
					fmt.Fprintln(w, "WORKFLOW\tBRANCH\tSTATE\tSOURCE\tADDED BY\tADDED AT\tQUARANTINED UNTIL\tREASON")
					for _, row := range response.Workflows {
						reason := row.Reason
						if row.State == "quarantined" {
							reason = row.QuarantineReason
						}
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", row.WorkflowSecurityID, branchText(row.BranchPattern), row.State,
							orDash(row.Source), principalText(row.AddedBy), timeText(row.AddedAt), timeText(row.QuarantinedUntil), reason)
					}
					pending := []requiredSetProposal{}
					for _, proposal := range response.Proposals {
						if proposal.State == "pending" {
							pending = append(pending, proposal)
						}
					}
					if len(pending) > 0 {
						fmt.Fprintln(w)
						renderRequiredSetProposals(w, pending)
					}
				})
			},
		},
		{
			Name: "proposals", Usage: "List the proposals of a project (all states)",
			Flags: append(apiFlags(), requiredSetProjectFlag(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					Proposals []requiredSetProposal `json:"proposals" yaml:"proposals"`
				}
				if err := client.doJSON(http.MethodGet, requiredSetBase(ctx)+"/proposals", nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					renderRequiredSetProposals(w, response.Proposals)
				})
			},
		},
		requiredSetProposeCommand("propose", "promote", "Propose to add a workflow to the required set (promotion)"),
		requiredSetProposeCommand("remove", "demote", "Propose to remove a workflow from the required set (demotion); the same approvers decide"),
		requiredSetDecisionCommand("approve", "Approve a pending proposal"),
		requiredSetDecisionCommand("reject", "Reject a pending proposal"),
		requiredSetDecisionCommand("withdraw", "Withdraw a pending proposal (the proposer or an approver)"),
		{
			Name: "quarantine", ArgsUsage: "<workflow-id>",
			Usage: "Remove a required workflow from the set until an expiry; it is required again at the expiry",
			Flags: append(apiFlags(), requiredSetProjectFlag(), formatFlag(),
				&cli.StringFlag{Name: "branch", Usage: "branch glob of the entry (empty: the entry for every branch)"},
				&cli.StringFlag{Name: "for", Usage: "duration, for example 72h or 7d"},
				&cli.StringFlag{Name: "until", Usage: "expiry as an RFC 3339 time"},
				&cli.StringFlag{Name: "reason", Required: true}),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "required-set quarantine --project <project> (--for 7d | --until TIME) --reason TEXT <workflow-id>"); err != nil {
					return err
				}
				if ctx.String("for") == "" && ctx.String("until") == "" {
					return fmt.Errorf("--for or --until is required: a quarantine always has an expiry")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				body := map[string]string{"workflow": ctx.Args().First(), "branch_pattern": ctx.String("branch"),
					"duration": ctx.String("for"), "until": ctx.String("until"), "reason": ctx.String("reason")}
				var entry requiredSetWorkflow
				if err := client.doJSON(http.MethodPost, requiredSetBase(ctx)+"/quarantine", body, http.StatusOK, &entry); err != nil {
					return err
				}
				return render(ctx.String("format"), entry, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "%s\t%s\tquarantined until %s\n", entry.WorkflowSecurityID, branchText(entry.BranchPattern), timeText(entry.QuarantinedUntil))
				})
			},
		},
		{
			Name: "unquarantine", ArgsUsage: "<workflow-id>", Usage: "End a quarantine before its expiry",
			Flags: append(apiFlags(), requiredSetProjectFlag(),
				&cli.StringFlag{Name: "branch", Usage: "branch glob of the entry (empty: the entry for every branch)"}),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "required-set unquarantine --project <project> <workflow-id>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				body := map[string]string{"workflow": ctx.Args().First(), "branch_pattern": ctx.String("branch")}
				if err := client.doJSON(http.MethodPost, requiredSetBase(ctx)+"/unquarantine", body, http.StatusNoContent, nil); err != nil {
					return err
				}
				fmt.Println("Done")
				return nil
			},
		},
		{
			Name: "approvers", Usage: "Set the required set approvers of a project (an org admin)",
			Flags: append(apiFlags(), requiredSetProjectFlag(), formatFlag(),
				&cli.StringSliceFlag{Name: "approver", Usage: "approver subject (repeat), for example reactorcide_group:ci-owners"},
				&cli.BoolFlag{Name: "inherit", Usage: "use the organization approvers and rules"},
				&cli.StringSliceFlag{Name: "rule", Usage: "'GLOB [GLOB...]=SUBJECT [SUBJECT...]' (repeat; the first match wins; a comma splits flag values, so use spaces)"}),
			Action: func(ctx *cli.Context) error {
				body := map[string]interface{}{"approvers": nil, "approver_rules": nil}
				if !ctx.Bool("inherit") {
					approvers := ctx.StringSlice("approver")
					if approvers == nil {
						approvers = []string{}
					}
					rules, err := parseApproverRules(ctx.StringSlice("rule"))
					if err != nil {
						return err
					}
					body["approvers"], body["approver_rules"] = approvers, rules
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response requiredSetResponse
				if err := client.doJSON(http.MethodPut, requiredSetBase(ctx)+"/approvers", body, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "approvers\t%s (%s)\n", strings.Join(response.Approvers, ", "), response.ApproversSource)
					for i, rule := range response.ApproverRules {
						fmt.Fprintf(w, "approver rule %d\tbranches %s: %s\n", i+1, strings.Join(rule.Branches, ", "), strings.Join(rule.Approvers, ", "))
					}
				})
			},
		},
		{
			Name: "org-settings", Usage: "Set the organization defaults: approvers, approver rules, and the longest quarantine",
			Flags: append(apiFlags(), formatFlag(),
				&cli.StringFlag{Name: "org", Required: true, Usage: "organization name"},
				&cli.StringSliceFlag{Name: "approver", Usage: "approver subject (repeat); none: org admins approve"},
				&cli.StringSliceFlag{Name: "rule", Usage: "'GLOB [GLOB...]=SUBJECT [SUBJECT...]' (repeat)"},
				&cli.IntFlag{Name: "max-quarantine-days", Value: 30}),
			Action: func(ctx *cli.Context) error {
				rules, err := parseApproverRules(ctx.StringSlice("rule"))
				if err != nil {
					return err
				}
				approvers := ctx.StringSlice("approver")
				if approvers == nil {
					approvers = []string{}
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					Approvers         []string                  `json:"approvers" yaml:"approvers"`
					ApproverRules     []requiredSetApproverRule `json:"approver_rules" yaml:"approver_rules"`
					MaxQuarantineDays int                       `json:"max_quarantine_days" yaml:"max_quarantine_days"`
				}
				body := map[string]interface{}{"approvers": approvers, "approver_rules": rules, "max_quarantine_days": ctx.Int("max-quarantine-days")}
				path := "/api/v1/organizations/" + url.PathEscape(ctx.String("org")) + "/required-set-settings"
				if err := client.doJSON(http.MethodPut, path, body, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "approvers\t%s\n", strings.Join(response.Approvers, ", "))
					fmt.Fprintf(w, "max quarantine days\t%d\n", response.MaxQuarantineDays)
				})
			},
		},
	},
}

// parseApproverRules reads "glob glob=subject subject" values. A comma
// splits the values of a slice flag, so the items of one rule are separated
// by spaces (or "|").
func parseApproverRules(values []string) ([]requiredSetApproverRule, error) {
	rules := []requiredSetApproverRule{}
	for _, value := range values {
		branches, approvers, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(branches) == "" || strings.TrimSpace(approvers) == "" {
			return nil, fmt.Errorf("--rule %q must be 'GLOB [GLOB...]=SUBJECT [SUBJECT...]'", value)
		}
		rules = append(rules, requiredSetApproverRule{Branches: splitTrim(branches), Approvers: splitTrim(approvers)})
	}
	return rules, nil
}

func splitTrim(value string) []string {
	result := []string{}
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == '|' || r == ' ' || r == '\t' }) {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
