package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
)

type organizationSummary struct {
	Name        string `json:"name" yaml:"name"`
	DisplayName string `json:"display_name" yaml:"display_name"`
	IsPrivate   bool   `json:"is_private" yaml:"is_private"`
	Status      string `json:"status" yaml:"status"`
	IsDefault   bool   `json:"is_default" yaml:"is_default"`
}

var OrgsCommand = &cli.Command{
	Name: "orgs", Usage: "Manage organizations", Flags: apiFlags(),
	Subcommands: []*cli.Command{
		{
			Name: "create", ArgsUsage: "<name>", Flags: append(apiFlags(),
				&cli.StringFlag{Name: "display-name"}, &cli.BoolFlag{Name: "private"}),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() != 1 {
					return fmt.Errorf("usage: reactorcide orgs create <name>")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				body := organizationSummary{Name: ctx.Args().First(), DisplayName: ctx.String("display-name"), IsPrivate: ctx.Bool("private"), Status: "active"}
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
					fmt.Fprintln(w, "NAME\tDISPLAY NAME\tSTATUS\tPRIVATE\tDEFAULT")
					fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%t\n", response.Name, response.DisplayName, response.Status, response.IsPrivate, response.IsDefault)
				})
			},
		},
		{
			Name: "set-default", ArgsUsage: "<name>", Flags: apiFlags(), Action: func(ctx *cli.Context) error {
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
			Name: "update", ArgsUsage: "<name>", Flags: append(apiFlags(),
				&cli.StringFlag{Name: "display-name", Required: true}, &cli.StringFlag{Name: "status", Value: "active"}, &cli.BoolFlag{Name: "private"}),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() != 1 {
					return fmt.Errorf("usage: reactorcide orgs update <name>")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				body := organizationSummary{DisplayName: ctx.String("display-name"), Status: ctx.String("status"), IsPrivate: ctx.Bool("private")}
				var response organizationSummary
				if err := client.doJSON(http.MethodPut, "/api/v1/organizations/"+url.PathEscape(ctx.Args().First()), body, http.StatusOK, &response); err != nil {
					return err
				}
				fmt.Printf("Organization updated: %s\n", response.Name)
				return nil
			},
		},
		{
			Name: "delete", ArgsUsage: "<name>", Flags: append(apiFlags(),
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
