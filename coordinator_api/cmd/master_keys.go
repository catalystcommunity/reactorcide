package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
)

// masterKeysCommand covers /api/v1/admin/secrets. Every subcommand needs an
// admin token. The key material itself never travels over the API: the
// coordinator reads it from REACTORCIDE_MASTER_KEYS, and these commands only
// manage which key is registered, primary, or decommissioned.
var masterKeysCommand = &cli.Command{
	Name:  "master-keys",
	Usage: "Manage secret master keys (admin)",
	Flags: apiFlags(),
	Subcommands: []*cli.Command{
		{
			Name:  "list",
			Usage: "List master keys",
			Flags: append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var resp masterKeysListResponse
				if err := client.doJSON(http.MethodGet, "/api/v1/admin/secrets/master-keys", nil, http.StatusOK, &resp); err != nil {
					return err
				}
				return render(ctx.String("format"), resp.MasterKeys, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "KEY ID\tNAME\tACTIVE\tPRIMARY\tCREATED\tDESCRIPTION")
					for _, k := range resp.MasterKeys {
						fmt.Fprintf(w, "%s\t%s\t%t\t%t\t%s\t%s\n",
							k.KeyID, k.Name, k.IsActive, k.IsPrimary, k.CreatedAt, k.Description)
					}
				})
			},
		},
		{
			Name:      "create",
			Usage:     "Register a master key that already exists in REACTORCIDE_MASTER_KEYS",
			ArgsUsage: "<name>",
			Flags: append(apiFlags(),
				formatFlag(),
				&cli.StringFlag{Name: "description", Usage: "Description for the key"},
			),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() < 1 {
					return fmt.Errorf("usage: reactorcide secrets master-keys create <name>")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				body := map[string]string{"name": ctx.Args().Get(0)}
				if description := ctx.String("description"); description != "" {
					body["description"] = description
				}
				var key masterKeyResponse
				if err := client.doJSON(http.MethodPost, "/api/v1/admin/secrets/master-keys", body, http.StatusCreated, &key); err != nil {
					return err
				}
				return render(ctx.String("format"), key, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "key_id\t%s\nname\t%s\nactive\t%t\nprimary\t%t\n", key.KeyID, key.Name, key.IsActive, key.IsPrimary)
				})
			},
		},
		{
			Name:      "rotate",
			Usage:     "Make a master key primary and re-encrypt org keys onto it",
			ArgsUsage: "<name>",
			Flags:     apiFlags(),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() < 1 {
					return fmt.Errorf("usage: reactorcide secrets master-keys rotate <name>")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				name := ctx.Args().Get(0)
				var resp rotateResponse
				path := "/api/v1/admin/secrets/master-keys/" + url.PathEscape(name) + "/rotate"
				if err := client.doJSON(http.MethodPost, path, nil, http.StatusOK, &resp); err != nil {
					return err
				}
				fmt.Printf("%s: %s\n", resp.Status, resp.KeyName)
				return nil
			},
		},
		{
			Name:      "decommission",
			Usage:     "Mark a master key inactive",
			ArgsUsage: "<name>",
			Flags:     apiFlags(),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() < 1 {
					return fmt.Errorf("usage: reactorcide secrets master-keys decommission <name>")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				name := ctx.Args().Get(0)
				path := "/api/v1/admin/secrets/master-keys/" + url.PathEscape(name)
				if err := client.doJSON(http.MethodDelete, path, nil, http.StatusOK, nil); err != nil {
					return err
				}
				fmt.Printf("Master key decommissioned: %s\n", name)
				return nil
			},
		},
		{
			Name:  "sync-primary",
			Usage: "Register the primary key from REACTORCIDE_MASTER_KEYS with the database",
			Flags: apiFlags(),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var resp struct {
					Status  string `json:"status"`
					Primary string `json:"primary"`
				}
				if err := client.doJSON(http.MethodPost, "/api/v1/admin/secrets/sync-primary", nil, http.StatusOK, &resp); err != nil {
					return err
				}
				fmt.Printf("%s: %s\n", resp.Status, resp.Primary)
				return nil
			},
		},
	},
}

type masterKeyResponse struct {
	KeyID       string `json:"key_id" yaml:"key_id"`
	Name        string `json:"name" yaml:"name"`
	IsActive    bool   `json:"is_active" yaml:"is_active"`
	IsPrimary   bool   `json:"is_primary" yaml:"is_primary"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	CreatedAt   string `json:"created_at" yaml:"created_at"`
}

type masterKeysListResponse struct {
	MasterKeys []masterKeyResponse `json:"master_keys"`
}

type rotateResponse struct {
	Status  string `json:"status"`
	KeyName string `json:"key_name"`
}
