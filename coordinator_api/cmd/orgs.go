package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
)

type organizationSummary struct {
	Name        string `json:"name" yaml:"name"`
	DisplayName string `json:"display_name" yaml:"display_name"`
	IsPrivate   bool   `json:"is_private" yaml:"is_private"`
	Status      string `json:"status" yaml:"status"`
	IsDefault   bool   `json:"is_default" yaml:"is_default"`

	DuplicateEvents string `json:"duplicate_events,omitempty" yaml:"duplicate_events,omitempty"`
	DynamicJobs     string `json:"dynamic_jobs,omitempty" yaml:"dynamic_jobs,omitempty"`
	DraftPRs        string `json:"draft_prs,omitempty" yaml:"draft_prs,omitempty"`
	InfraRetries    *int   `json:"infra_retries,omitempty" yaml:"infra_retries,omitempty"`
	DefaultsProfile string `json:"defaults_profile,omitempty" yaml:"defaults_profile,omitempty"`
	ShedLevel       string `json:"shed_level,omitempty" yaml:"shed_level,omitempty"`
}

// organizationUpdate is the PUT body of orgs update. Optional settings are
// sent only when a flag sets them.
type organizationUpdate struct {
	DisplayName     string  `json:"display_name"`
	IsPrivate       bool    `json:"is_private"`
	Status          string  `json:"status"`
	DuplicateEvents *string `json:"duplicate_events,omitempty"`
	DynamicJobs     *string `json:"dynamic_jobs,omitempty"`
	DraftPRs        *string `json:"draft_prs,omitempty"`
	// InfraRetries: automatic retries of an infra.platform failure, 0-5.
	InfraRetries *int `json:"infra_retries,omitempty"`
}

var OrgsCommand = &cli.Command{
	Name: "orgs", Usage: "Manage organizations", Flags: apiFlags(),
	Subcommands: []*cli.Command{
		{
			Name: "create", ArgsUsage: "<name>", Flags: append(apiFlags(),
				&cli.StringFlag{Name: "display-name"}, &cli.BoolFlag{Name: "private"},
				&cli.StringFlag{Name: "defaults-profile", Usage: "Defaults of the new organization: secure (default) or legacy"}),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() != 1 {
					return fmt.Errorf("usage: reactorcide orgs create <name>")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				body := organizationSummary{Name: ctx.Args().First(), DisplayName: ctx.String("display-name"), IsPrivate: ctx.Bool("private"), Status: "active",
					DefaultsProfile: ctx.String("defaults-profile")}
				var response organizationSummary
				if err := client.doJSON(http.MethodPost, "/api/v1/organizations", body, http.StatusCreated, &response); err != nil {
					return err
				}
				fmt.Printf("Organization created: %s\n", response.Name)
				return nil
			},
		},
		{
			Name: "list", Flags: append(apiFlags(), formatFlag()), Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					Organizations []organizationSummary `json:"organizations"`
				}
				if err := client.doJSON(http.MethodGet, "/api/v1/organizations", nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response.Organizations, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "NAME\tDISPLAY NAME\tSTATUS\tPRIVATE\tDEFAULT")
					for _, org := range response.Organizations {
						fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%t\n", org.Name, org.DisplayName, org.Status, org.IsPrivate, org.IsDefault)
					}
				})
			},
		},
		{
			Name: "get", ArgsUsage: "<name>", Flags: append(apiFlags(), formatFlag()), Action: func(ctx *cli.Context) error {
				if ctx.NArg() != 1 {
					return fmt.Errorf("usage: reactorcide orgs get <name>")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response organizationSummary
				if err := client.doJSON(http.MethodGet, "/api/v1/organizations/"+url.PathEscape(ctx.Args().First()), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "NAME\tDISPLAY NAME\tSTATUS\tPRIVATE\tDEFAULT\tDEFAULTS\tDRAFT PRS\tDYNAMIC JOBS\tSHED")
					fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%t\t%s\t%s\t%s\t%s\n", response.Name, response.DisplayName, response.Status, response.IsPrivate, response.IsDefault,
						response.DefaultsProfile, response.DraftPRs, response.DynamicJobs, response.ShedLevel)
				})
			},
		},
		{
			Name: "set-default", Usage: "Make an organization the default: it gets each project or direct job that names no organization (needs global organizations:manage)", ArgsUsage: "<name>", Flags: apiFlags(), Action: func(ctx *cli.Context) error {
				if ctx.NArg() != 1 {
					return fmt.Errorf("usage: reactorcide orgs set-default <name>")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response organizationSummary
				if err := client.doJSON(http.MethodPut, "/api/v1/organizations/"+url.PathEscape(ctx.Args().First())+"/default", nil, http.StatusOK, &response); err != nil {
					return err
				}
				fmt.Printf("Default organization: %s\n", response.Name)
				return nil
			},
		},
		{
			Name: "update", Aliases: []string{"set"}, ArgsUsage: "<name>", Flags: append(apiFlags(),
				&cli.IntFlag{Name: "infra-retries", Usage: "Automatic retries of a job that failed with infra.platform before its main container started (0-5)"},
				&cli.StringFlag{Name: "display-name", Usage: "Display name (default: keep the current value)"},
				&cli.StringFlag{Name: "status", Usage: "active, suspended, or disabled (default: keep the current value)"},
				&cli.BoolFlag{Name: "private", Usage: "Make the organization private (default: keep the current value)"},
				&cli.StringFlag{Name: "draft-prs", Usage: "Draft pull requests: run, skip, or skip_when_shedding"},
				&cli.StringFlag{Name: "dynamic-jobs", Usage: "Runtime job creation: allow or deny"},
				&cli.StringFlag{Name: "duplicate-events", Usage: "Duplicate deliveries: run or skip"}),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() != 1 {
					return fmt.Errorf("usage: reactorcide orgs update <name>")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				// The API replaces display name, privacy and status. Keep the
				// current values for each flag that is not set.
				var current organizationSummary
				if err := client.doJSON(http.MethodGet, "/api/v1/organizations/"+url.PathEscape(ctx.Args().First()), nil, http.StatusOK, &current); err != nil {
					return err
				}
				body := organizationUpdate{DisplayName: current.DisplayName, Status: current.Status, IsPrivate: current.IsPrivate}
				if ctx.IsSet("display-name") {
					body.DisplayName = ctx.String("display-name")
				}
				if ctx.IsSet("status") {
					body.Status = ctx.String("status")
				}
				if ctx.IsSet("private") {
					body.IsPrivate = ctx.Bool("private")
				}
				for flag, target := range map[string]**string{"draft-prs": &body.DraftPRs, "dynamic-jobs": &body.DynamicJobs, "duplicate-events": &body.DuplicateEvents} {
					if ctx.IsSet(flag) {
						value := ctx.String(flag)
						*target = &value
					}
				}
				if ctx.IsSet("infra-retries") {
					value := ctx.Int("infra-retries")
					if value < 0 || value > 5 {
						return fmt.Errorf("--infra-retries must be 0-5")
					}
					body.InfraRetries = &value
				}
				var response organizationSummary
				if err := client.doJSON(http.MethodPut, "/api/v1/organizations/"+url.PathEscape(ctx.Args().First()), body, http.StatusOK, &response); err != nil {
					return err
				}
				fmt.Printf("Organization updated: %s\n", response.Name)
				return nil
			},
		},
		{
			Name: "set-defaults", ArgsUsage: "<name>", Usage: "Apply a defaults profile: secure or legacy",
			Description: "secure writes run_gate [outside_contributors] and dynamic_jobs deny, and gives a project with no stored CI policy " +
				"the built-in policy (CI from base; head CI only through override-ci or a head_ci rule). legacy writes run_gate [] and " +
				"dynamic_jobs allow, and a project with no stored policy runs the CI of a same-repository pull request head.",
			Flags: append(apiFlags(), &cli.StringFlag{Name: "profile", Required: true, Usage: "secure or legacy"}),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() != 1 {
					return fmt.Errorf("usage: reactorcide orgs set-defaults <name> --profile secure|legacy")
				}
				profile := ctx.String("profile")
				if profile != "secure" && profile != "legacy" {
					return fmt.Errorf("--profile must be secure or legacy")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					Organization  string   `json:"organization"`
					Profile       string   `json:"profile"`
					RunGate       []string `json:"run_gate"`
					DynamicJobs   string   `json:"dynamic_jobs"`
					BuiltInPolicy bool     `json:"builtin_policy"`
				}
				body := map[string]string{"profile": profile}
				if err := client.doJSON(http.MethodPut, "/api/v1/organizations/"+url.PathEscape(ctx.Args().First())+"/defaults-profile", body, http.StatusOK, &response); err != nil {
					return err
				}
				fmt.Printf("Organization %s uses the %s defaults: run_gate %v, dynamic_jobs %s, built-in CI policy %t\n",
					response.Organization, response.Profile, response.RunGate, response.DynamicJobs, response.BuiltInPolicy)
				return nil
			},
		},
		orgsVCSOwnersCommand(),
		{
			Name: "delete", Usage: "Delete an organization, and all resources and credentials that it owns, after --replacement becomes the default. This cannot be undone", ArgsUsage: "<name>", Flags: append(apiFlags(),
				&cli.StringFlag{Name: "replacement", Required: true, Usage: "Organization that becomes the default"},
				&cli.BoolFlag{Name: "yes", Usage: "Confirm deletion of the organization and all resources that it owns"}),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() != 1 {
					return fmt.Errorf("usage: reactorcide orgs delete <name> --replacement <name> --yes")
				}
				if !ctx.Bool("yes") {
					return fmt.Errorf("organization deletion requires --yes")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				body := struct {
					Replacement string `json:"replacement"`
					Confirm     bool   `json:"confirm"`
				}{Replacement: ctx.String("replacement"), Confirm: true}
				if err := client.doJSON(http.MethodDelete, "/api/v1/organizations/"+url.PathEscape(ctx.Args().First()), body, http.StatusNoContent, nil); err != nil {
					return err
				}
				fmt.Printf("Organization deleted: %s\n", ctx.Args().First())
				return nil
			},
		},
	},
}

// orgsVCSOwnersCommand reads and sets the VCS owner allowlist of an
// organization: the VCS owners for which the group VCS sync may use the
// global VCS credential. Only a global admin (or the admin API token) can use
// it.
func orgsVCSOwnersCommand() *cli.Command {
	path := func(ctx *cli.Context) string {
		return "/api/v1/organizations/" + url.PathEscape(ctx.Args().First()) + "/vcs-owner-allowlist"
	}
	type allowlist struct {
		Owners []string `json:"owners"`
	}
	print := func(org string, list allowlist) {
		if len(list.Owners) == 0 {
			fmt.Printf("Organization %s: no VCS owner may use the global VCS credential\n", org)
			return
		}
		fmt.Printf("Organization %s: the global VCS credential may read the teams of %s\n", org, strings.Join(list.Owners, ", "))
	}
	return &cli.Command{
		Name:  "vcs-owners",
		Usage: "VCS owners whose teams the group sync may read with the global VCS credential (global admin)",
		Description: "Without an entry, the group VCS sync of the organization uses only a project or organization VCS " +
			"credential. Add an owner only when the organization owns that VCS organization.",
		Subcommands: []*cli.Command{
			{
				Name: "get", ArgsUsage: "<org>", Flags: apiFlags(),
				Action: func(ctx *cli.Context) error {
					if ctx.NArg() != 1 {
						return fmt.Errorf("usage: reactorcide orgs vcs-owners get <org>")
					}
					client, err := newAPIClient(ctx)
					if err != nil {
						return err
					}
					var list allowlist
					if err := client.doJSON(http.MethodGet, path(ctx), nil, http.StatusOK, &list); err != nil {
						return err
					}
					print(ctx.Args().First(), list)
					return nil
				},
			},
			{
				Name: "set", ArgsUsage: "<org>", Usage: "Replace the list (no --owner clears it)",
				Flags: append(apiFlags(), &cli.StringSliceFlag{Name: "owner", Usage: "VCS owner (GitHub org login); repeat for more"}),
				Action: func(ctx *cli.Context) error {
					if ctx.NArg() != 1 {
						return fmt.Errorf("usage: reactorcide orgs vcs-owners set <org> [--owner LOGIN ...]")
					}
					client, err := newAPIClient(ctx)
					if err != nil {
						return err
					}
					body := allowlist{Owners: ctx.StringSlice("owner")}
					if body.Owners == nil {
						body.Owners = []string{}
					}
					var list allowlist
					if err := client.doJSON(http.MethodPut, path(ctx), body, http.StatusOK, &list); err != nil {
						return err
					}
					print(ctx.Args().First(), list)
					return nil
				},
			},
		},
	}
}
