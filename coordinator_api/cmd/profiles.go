package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

func profilePath(org string, name ...string) string {
	path := "/api/v1/organizations/" + url.PathEscape(org) + "/profiles"
	if len(name) > 0 {
		path += "/" + url.PathEscape(name[0])
	}
	return path
}

var ProfilesCommand = &cli.Command{Name: "profiles", Usage: "Manage organization execution profiles", Subcommands: []*cli.Command{
	{Name: "list", Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}, formatFlag()), Action: func(ctx *cli.Context) error {
		client, err := newAPIClient(ctx)
		if err != nil {
			return err
		}
		var response struct {
			Profiles []models.ExecutionProfile `json:"profiles" yaml:"profiles"`
		}
		if err := client.doJSON(http.MethodGet, profilePath(ctx.String("org")), nil, http.StatusOK, &response); err != nil {
			return err
		}
		return render(ctx.String("format"), response.Profiles, func(w *tabwriter.Writer) {
			fmt.Fprintln(w, "NAME\tDENY SECRETS\tROOT\tWORKER CLASSES")
			for _, profile := range response.Profiles {
				fmt.Fprintf(w, "%s\t%t\t%t\t%v\n", profile.Name, profile.DenySecrets, profile.MayRunAsRoot, []string(profile.AllowedWorkerClasses))
			}
		})
	}},
	{Name: "get", ArgsUsage: "<name>", Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}, formatFlag()), Action: func(ctx *cli.Context) error {
		if ctx.NArg() != 1 {
			return fmt.Errorf("usage: reactorcide profiles get --org NAME <name>")
		}
		client, err := newAPIClient(ctx)
		if err != nil {
			return err
		}
		var profile models.ExecutionProfile
		if err := client.doJSON(http.MethodGet, profilePath(ctx.String("org"), ctx.Args().First()), nil, http.StatusOK, &profile); err != nil {
			return err
		}
		return render(ctx.String("format"), profile, func(w *tabwriter.Writer) { fmt.Fprintf(w, "%s\n", profile.Name) })
	}},
	{Name: "apply", ArgsUsage: "<profile.yaml>", Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}), Action: func(ctx *cli.Context) error {
		if ctx.NArg() != 1 {
			return fmt.Errorf("usage: reactorcide profiles apply --org NAME <profile.yaml>")
		}
		data, err := os.ReadFile(ctx.Args().First())
		if err != nil {
			return err
		}
		var profile models.ExecutionProfile
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&profile); err != nil {
			return fmt.Errorf("parse execution profile: %w", err)
		}
		if profile.Name == "" {
			return fmt.Errorf("profile name is required")
		}
		client, err := newAPIClient(ctx)
		if err != nil {
			return err
		}
		method, path, expected := http.MethodPost, profilePath(ctx.String("org")), http.StatusCreated
		var existing models.ExecutionProfile
		getErr := client.doJSON(http.MethodGet, profilePath(ctx.String("org"), profile.Name), nil, http.StatusOK, &existing)
		if getErr == nil {
			method, path, expected = http.MethodPut, profilePath(ctx.String("org"), profile.Name), http.StatusOK
		} else {
			var apiErr *apiError
			if !errors.As(getErr, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
				return getErr
			}
		}
		var response models.ExecutionProfile
		if err := client.doJSON(method, path, profile, expected, &response); err != nil {
			return err
		}
		fmt.Printf("Execution profile applied: %s\n", response.Name)
		return nil
	}},
	{Name: "delete", ArgsUsage: "<name>", Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}), Action: func(ctx *cli.Context) error {
		if ctx.NArg() != 1 {
			return fmt.Errorf("usage: reactorcide profiles delete --org NAME <name>")
		}
		client, err := newAPIClient(ctx)
		if err != nil {
			return err
		}
		if err := client.doJSON(http.MethodDelete, profilePath(ctx.String("org"), ctx.Args().First()), nil, http.StatusNoContent, nil); err != nil {
			return err
		}
		fmt.Printf("Execution profile deleted: %s\n", ctx.Args().First())
		return nil
	}},
}}
