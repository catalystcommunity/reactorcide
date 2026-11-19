package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
)

// WP14 CLI: environment defaults, the organization image policy, and job
// links. All commands work with the admin API token.

type envDefaultRow struct {
	Key       string `json:"key" yaml:"key"`
	Value     string `json:"value" yaml:"value"`
	UpdatedAt string `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
}

type envDefaultsList struct {
	Scope       string          `json:"scope" yaml:"scope"`
	WorkerClass string          `json:"worker_class,omitempty" yaml:"worker_class,omitempty"`
	ProjectID   string          `json:"project_id,omitempty" yaml:"project_id,omitempty"`
	EnvDefaults []envDefaultRow `json:"env_defaults" yaml:"env_defaults"`
}

func envDefaultsScopeFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "org", Usage: "Organization name (organization or worker class scope)"},
		&cli.StringFlag{Name: "worker-class", Usage: "Worker class name (worker class scope; needs --org)"},
		&cli.StringFlag{Name: "project", Usage: "Project ID (project scope)"},
	}
}

// envDefaultsPath returns the REST path of the scope of the flags.
func envDefaultsPath(ctx *cli.Context, key string) (string, error) {
	org, class, project := ctx.String("org"), ctx.String("worker-class"), ctx.String("project")
	var base string
	switch {
	case project != "" && (org != "" || class != ""):
		return "", fmt.Errorf("give --project alone, or --org with an optional --worker-class")
	case project != "":
		base = "/api/v1/projects/" + url.PathEscape(project) + "/env-defaults"
	case org == "":
		return "", fmt.Errorf("give --org (and --worker-class), or --project")
	case class != "":
		base = "/api/v1/organizations/" + url.PathEscape(org) + "/worker-classes/" + url.PathEscape(class) + "/env-defaults"
	default:
		base = "/api/v1/organizations/" + url.PathEscape(org) + "/env-defaults"
	}
	if key != "" {
		base += "/" + url.PathEscape(key)
	}
	return base, nil
}

// EnvDefaultsCommand manages non-secret environment defaults.
var EnvDefaultsCommand = &cli.Command{
	Name:  "env-defaults",
	Usage: "Manage non-secret job environment defaults (organization, worker class, project)",
	Description: "Precedence in the job environment: job > project > worker class > organization. " +
		"REACTORCIDE_* and RC_WF* keys are reserved. Values are not secret; use secret grants for secrets.",
	Flags: apiFlags(),
	Subcommands: []*cli.Command{
		{
			Name: "list", Usage: "List the environment defaults of one scope",
			Flags: append(append(apiFlags(), formatFlag()), envDefaultsScopeFlags()...),
			Action: func(ctx *cli.Context) error {
				path, err := envDefaultsPath(ctx, "")
				if err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var list envDefaultsList
				if err := client.doJSON(http.MethodGet, path, nil, http.StatusOK, &list); err != nil {
					return err
				}
				return render(ctx.String("format"), list, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "KEY\tVALUE\tUPDATED")
					for _, row := range list.EnvDefaults {
						fmt.Fprintf(w, "%s\t%s\t%s\n", row.Key, row.Value, row.UpdatedAt)
					}
				})
			},
		},
		{
			Name: "set", Usage: "Set one environment default: env-defaults set --org ORG KEY=VALUE",
			ArgsUsage: "KEY=VALUE",
			Flags:     append(append(apiFlags(), formatFlag()), envDefaultsScopeFlags()...),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() != 1 {
					return fmt.Errorf("give one KEY=VALUE argument")
				}
				key, value, ok := strings.Cut(ctx.Args().First(), "=")
				if !ok || key == "" {
					return fmt.Errorf("the argument must be KEY=VALUE")
				}
				path, err := envDefaultsPath(ctx, key)
				if err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var row envDefaultRow
				if err := client.doJSON(http.MethodPut, path, map[string]string{"value": value}, http.StatusOK, &row); err != nil {
					return err
				}
				return render(ctx.String("format"), row, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "KEY\tVALUE")
					fmt.Fprintf(w, "%s\t%s\n", row.Key, row.Value)
				})
			},
		},
		{
			Name: "unset", Usage: "Remove one environment default", ArgsUsage: "KEY",
			Flags: append(apiFlags(), envDefaultsScopeFlags()...),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() != 1 {
					return fmt.Errorf("give one KEY argument")
				}
				path, err := envDefaultsPath(ctx, ctx.Args().First())
				if err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				if err := client.doJSON(http.MethodDelete, path, nil, http.StatusNoContent, nil); err != nil {
					return err
				}
				fmt.Fprintf(ctx.App.Writer, "Removed %s\n", ctx.Args().First())
				return nil
			},
		},
	},
}

type imagePolicyView struct {
	Enabled         bool     `json:"enabled" yaml:"enabled"`
	AllowedPrefixes []string `json:"allowed_prefixes" yaml:"allowed_prefixes"`
	RequireDigest   bool     `json:"require_digest" yaml:"require_digest"`
}

func renderImagePolicy(ctx *cli.Context, policy imagePolicyView) error {
	return render(ctx.String("format"), policy, func(w *tabwriter.Writer) {
		fmt.Fprintln(w, "ENABLED\tREQUIRE DIGEST\tALLOWED PREFIXES")
		fmt.Fprintf(w, "%t\t%t\t%s\n", policy.Enabled, policy.RequireDigest, strings.Join(policy.AllowedPrefixes, ","))
	})
}

func imagePolicyPath(org string) string {
	return "/api/v1/organizations/" + url.PathEscape(org) + "/image-policy"
}

// ImagePolicyCommand reads and sets the organization image policy.
var ImagePolicyCommand = &cli.Command{
	Name:  "image-policy",
	Usage: "Read and set the job image policy of an organization (allowed registry prefixes, digest pinning)",
	Flags: apiFlags(),
	Subcommands: []*cli.Command{
		{
			Name: "get", Usage: "Show the image policy",
			Flags: append(apiFlags(), formatFlag(), &cli.StringFlag{Name: "org", Required: true, Usage: "Organization name"}),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var policy imagePolicyView
				if err := client.doJSON(http.MethodGet, imagePolicyPath(ctx.String("org")), nil, http.StatusOK, &policy); err != nil {
					return err
				}
				return renderImagePolicy(ctx, policy)
			},
		},
		{
			Name: "set", Usage: "Replace the image policy",
			Flags: append(apiFlags(), formatFlag(),
				&cli.StringFlag{Name: "org", Required: true, Usage: "Organization name"},
				&cli.BoolFlag{Name: "enabled", Usage: "Turn the policy on (off without this flag)"},
				&cli.StringSliceFlag{Name: "allow", Usage: "Allowed image reference prefix, for example ghcr.io/example/ (repeat)"},
				&cli.BoolFlag{Name: "require-digest", Usage: "Refuse images with no @sha256 digest"}),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				prefixes := ctx.StringSlice("allow")
				if prefixes == nil {
					prefixes = []string{}
				}
				body := imagePolicyView{Enabled: ctx.Bool("enabled"), AllowedPrefixes: prefixes, RequireDigest: ctx.Bool("require-digest")}
				var policy imagePolicyView
				if err := client.doJSON(http.MethodPut, imagePolicyPath(ctx.String("org")), body, http.StatusOK, &policy); err != nil {
					return err
				}
				return renderImagePolicy(ctx, policy)
			},
		},
	},
}

type jobLinkView struct {
	Name string `json:"name" yaml:"name"`
	URL  string `json:"url" yaml:"url"`
}

// JobLinksCommand lists the named links that a job published.
var JobLinksCommand = &cli.Command{
	Name: "job-links", Usage: "List the named links that a job published in its workflow output",
	ArgsUsage: "JOB_ID",
	Flags:     append(apiFlags(), formatFlag()),
	Action: func(ctx *cli.Context) error {
		if ctx.NArg() != 1 {
			return fmt.Errorf("give one JOB_ID argument")
		}
		client, err := newAPIClient(ctx)
		if err != nil {
			return err
		}
		var response struct {
			JobID string        `json:"job_id" yaml:"job_id"`
			Links []jobLinkView `json:"links" yaml:"links"`
		}
		if err := client.doJSON(http.MethodGet, "/api/v1/jobs/"+url.PathEscape(ctx.Args().First())+"/links", nil, http.StatusOK, &response); err != nil {
			return err
		}
		return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
			fmt.Fprintln(w, "NAME\tURL")
			for _, link := range response.Links {
				fmt.Fprintf(w, "%s\t%s\n", link.Name, link.URL)
			}
		})
	},
}
