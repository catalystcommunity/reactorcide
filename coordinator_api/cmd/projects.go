package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

var ProjectsCommand = &cli.Command{
	Name:  "projects",
	Usage: "Manage projects (repositories) on a Reactorcide coordinator",
	Flags: apiFlags(),
	Subcommands: []*cli.Command{
		{
			Name:  "list",
			Usage: "List projects",
			Flags: append(append(apiFlags(), paginationFlags()...), formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var resp listProjectsResponse
				if err := client.doJSON(http.MethodGet, "/api/v1/projects"+pagedQuery(ctx, nil), nil, http.StatusOK, &resp); err != nil {
					return err
				}
				return render(ctx.String("format"), resp.Projects, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "PROJECT ID\tNAME\tREPO URL\tENABLED\tQUEUE")
					for _, p := range resp.Projects {
						fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%s\n",
							p.ProjectID, p.Name, p.RepoURL, p.Enabled, p.DefaultQueueName)
					}
				})
			},
		},
		{
			Name:      "get",
			Usage:     "Get a project by ID",
			ArgsUsage: "<project-id>",
			Flags:     append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				projectID, client, err := projectTarget(ctx)
				if err != nil {
					return err
				}
				var project projectResponse
				if err := client.doJSON(http.MethodGet, "/api/v1/projects/"+url.PathEscape(projectID), nil, http.StatusOK, &project); err != nil {
					return err
				}
				return renderProject(ctx.String("format"), &project)
			},
		},
		{
			Name:  "create",
			Usage: "Create a project",
			Description: "Supply the project either with flags or with --file, a YAML or JSON " +
				"document holding the same fields as the API request body. Flags override the file.",
			Flags: append(append(apiFlags(), projectSpecFlags()...),
				formatFlag(),
				&cli.StringFlag{Name: "file", Usage: "YAML or JSON file holding the project definition"},
			),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				spec, err := loadProjectSpec(ctx)
				if err != nil {
					return err
				}
				if spec.Name == "" || spec.RepoURL == "" {
					return fmt.Errorf("--name and --repo-url are required")
				}
				var project projectResponse
				if err := client.doJSON(http.MethodPost, "/api/v1/projects", spec, http.StatusCreated, &project); err != nil {
					return err
				}
				return renderProject(ctx.String("format"), &project)
			},
		},
		{
			Name:      "update",
			Usage:     "Update a project",
			ArgsUsage: "<project-id>",
			Description: "Only the fields you supply are changed. Use --file for a YAML or JSON " +
				"document holding the same fields as the API request body.",
			Flags: append(append(apiFlags(), projectSpecFlags()...),
				formatFlag(),
				&cli.StringFlag{Name: "file", Usage: "YAML or JSON file holding the fields to update"},
			),
			Action: func(ctx *cli.Context) error {
				projectID, client, err := projectTarget(ctx)
				if err != nil {
					return err
				}
				spec, err := loadProjectSpec(ctx)
				if err != nil {
					return err
				}
				var project projectResponse
				if err := client.doJSON(http.MethodPut, "/api/v1/projects/"+url.PathEscape(projectID), spec, http.StatusOK, &project); err != nil {
					return err
				}
				return renderProject(ctx.String("format"), &project)
			},
		},
		{
			Name:      "delete",
			Usage:     "Delete a project",
			ArgsUsage: "<project-id>",
			Flags:     apiFlags(),
			Action: func(ctx *cli.Context) error {
				projectID, client, err := projectTarget(ctx)
				if err != nil {
					return err
				}
				if err := client.doJSON(http.MethodDelete, "/api/v1/projects/"+url.PathEscape(projectID), nil, http.StatusNoContent, nil); err != nil {
					return err
				}
				fmt.Printf("Project deleted: %s\n", projectID)
				return nil
			},
		},
	},
}

// projectSpec mirrors handlers.CreateProjectRequest. Pointer and slice fields
// stay nil when unset so an update leaves those columns alone.
type projectSpec struct {
	Name         string `json:"name,omitempty" yaml:"name,omitempty"`
	Description  string `json:"description,omitempty" yaml:"description,omitempty"`
	RepoURL      string `json:"repo_url,omitempty" yaml:"repo_url,omitempty"`
	Organization string `json:"org,omitempty" yaml:"org,omitempty"`

	Enabled           *bool    `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	TargetBranches    []string `json:"target_branches,omitempty" yaml:"target_branches,omitempty"`
	AllowedEventTypes []string `json:"allowed_event_types,omitempty" yaml:"allowed_event_types,omitempty"`

	DefaultCISourceType string `json:"default_ci_source_type,omitempty" yaml:"default_ci_source_type,omitempty"`
	DefaultCISourceURL  string `json:"default_ci_source_url,omitempty" yaml:"default_ci_source_url,omitempty"`
	DefaultCISourceRef  string `json:"default_ci_source_ref,omitempty" yaml:"default_ci_source_ref,omitempty"`

	DefaultRunnerImage    string  `json:"default_runner_image,omitempty" yaml:"default_runner_image,omitempty"`
	DefaultJobCommand     string  `json:"default_job_command,omitempty" yaml:"default_job_command,omitempty"`
	DefaultTimeoutSeconds *int    `json:"default_timeout_seconds,omitempty" yaml:"default_timeout_seconds,omitempty"`
	DefaultQueueName      string  `json:"default_queue_name,omitempty" yaml:"default_queue_name,omitempty"`
	CheckoutMode          *string `json:"checkout_mode,omitempty" yaml:"checkout_mode,omitempty"`

	// Secret-bearing fields hold references (path:key), never values.
	VCSTokenSecret       string            `json:"vcs_token_secret,omitempty" yaml:"vcs_token_secret,omitempty"`
	VCSCredentialSecrets map[string]string `json:"vcs_token_secrets,omitempty" yaml:"vcs_token_secrets,omitempty"`
	WebhookSecret        string            `json:"webhook_secret,omitempty" yaml:"webhook_secret,omitempty"`
	WebhookSecrets       map[string]string `json:"webhook_secrets,omitempty" yaml:"webhook_secrets,omitempty"`
}

type projectResponse struct {
	projectSpec `yaml:",inline"`
	ProjectID   string  `json:"project_id" yaml:"project_id"`
	UserID      *string `json:"user_id,omitempty" yaml:"user_id,omitempty"`
}

type listProjectsResponse struct {
	Projects []projectListItem `json:"projects"`
	Total    int               `json:"total"`
	Limit    int               `json:"limit"`
	Offset   int               `json:"offset"`
}

type projectListItem struct {
	ProjectID        string `json:"project_id" yaml:"project_id"`
	Name             string `json:"name" yaml:"name"`
	Description      string `json:"description,omitempty" yaml:"description,omitempty"`
	RepoURL          string `json:"repo_url" yaml:"repo_url"`
	Enabled          bool   `json:"enabled" yaml:"enabled"`
	DefaultQueueName string `json:"default_queue_name" yaml:"default_queue_name"`
}

func projectSpecFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "name", Usage: "Project name"},
		&cli.StringFlag{Name: "description", Usage: "Project description"},
		&cli.StringFlag{Name: "repo-url", Usage: "Repository clone URL"},
		&cli.StringFlag{Name: "org", Usage: "Organization name"},
		&cli.BoolFlag{Name: "enabled", Usage: "Enable webhook-triggered jobs for this project"},
		&cli.StringSliceFlag{Name: "target-branch", Usage: "Branch that may trigger jobs (repeatable)"},
		&cli.StringSliceFlag{Name: "allowed-event-type", Usage: "VCS event type that may trigger jobs (repeatable)"},
		&cli.StringFlag{Name: "ci-source-type", Usage: "Default CI source type"},
		&cli.StringFlag{Name: "ci-source-url", Usage: "Default trusted CI source URL"},
		&cli.StringFlag{Name: "ci-source-ref", Usage: "Default trusted CI source ref"},
		&cli.StringFlag{Name: "runner-image", Usage: "Default runner image"},
		&cli.StringFlag{Name: "job-command", Usage: "Default job command"},
		&cli.IntFlag{Name: "timeout-seconds", Usage: "Default job timeout in seconds"},
		&cli.StringFlag{Name: "queue-name", Usage: "Default queue name"},
		&cli.StringFlag{Name: "checkout-mode", Usage: "Runnerlib checkout mode: isolated, shared, or empty to use the coordinator default"},
		&cli.StringFlag{Name: "vcs-token-secret", Usage: "Secret reference (path:key) for the VCS token"},
		&cli.StringFlag{Name: "webhook-secret", Usage: "Secret reference (path:key) for the webhook secret"},
	}
}

// loadProjectSpec reads the optional --file document, then applies any flags
// the user set on top of it.
func loadProjectSpec(ctx *cli.Context) (*projectSpec, error) {
	spec := &projectSpec{}
	if path := ctx.String("file"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", path, err)
		}
		if err := yaml.Unmarshal(data, spec); err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", path, err)
		}
	}

	setString(ctx, "name", &spec.Name)
	setString(ctx, "description", &spec.Description)
	setString(ctx, "repo-url", &spec.RepoURL)
	setString(ctx, "org", &spec.Organization)
	setString(ctx, "ci-source-type", &spec.DefaultCISourceType)
	setString(ctx, "ci-source-url", &spec.DefaultCISourceURL)
	setString(ctx, "ci-source-ref", &spec.DefaultCISourceRef)
	setString(ctx, "runner-image", &spec.DefaultRunnerImage)
	setString(ctx, "job-command", &spec.DefaultJobCommand)
	setString(ctx, "queue-name", &spec.DefaultQueueName)
	if ctx.IsSet("checkout-mode") {
		mode := ctx.String("checkout-mode")
		spec.CheckoutMode = &mode
	}
	setString(ctx, "vcs-token-secret", &spec.VCSTokenSecret)
	setString(ctx, "webhook-secret", &spec.WebhookSecret)

	if ctx.IsSet("enabled") {
		enabled := ctx.Bool("enabled")
		spec.Enabled = &enabled
	}
	if ctx.IsSet("timeout-seconds") {
		timeout := ctx.Int("timeout-seconds")
		spec.DefaultTimeoutSeconds = &timeout
	}
	if ctx.IsSet("target-branch") {
		spec.TargetBranches = ctx.StringSlice("target-branch")
	}
	if ctx.IsSet("allowed-event-type") {
		spec.AllowedEventTypes = ctx.StringSlice("allowed-event-type")
	}
	return spec, nil
}

func setString(ctx *cli.Context, flag string, target *string) {
	if ctx.IsSet(flag) {
		*target = ctx.String(flag)
	}
}

func projectTarget(ctx *cli.Context) (string, *apiClient, error) {
	if ctx.NArg() < 1 {
		return "", nil, fmt.Errorf("usage: reactorcide projects %s <project-id>", ctx.Command.Name)
	}
	client, err := newAPIClient(ctx)
	if err != nil {
		return "", nil, err
	}
	return ctx.Args().Get(0), client, nil
}

func renderProject(format string, p *projectResponse) error {
	return render(format, p, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "project_id\t%s\n", p.ProjectID)
		fmt.Fprintf(w, "name\t%s\n", p.Name)
		fmt.Fprintf(w, "repo_url\t%s\n", p.RepoURL)
		if p.Description != "" {
			fmt.Fprintf(w, "description\t%s\n", p.Description)
		}
		if p.Enabled != nil {
			fmt.Fprintf(w, "enabled\t%t\n", *p.Enabled)
		}
		fmt.Fprintf(w, "default_queue_name\t%s\n", p.DefaultQueueName)
		fmt.Fprintf(w, "default_runner_image\t%s\n", p.DefaultRunnerImage)
		if p.DefaultCISourceURL != "" {
			fmt.Fprintf(w, "default_ci_source_url\t%s\n", p.DefaultCISourceURL)
		}
		if len(p.TargetBranches) > 0 {
			fmt.Fprintf(w, "target_branches\t%v\n", p.TargetBranches)
		}
		if len(p.AllowedEventTypes) > 0 {
			fmt.Fprintf(w, "allowed_event_types\t%v\n", p.AllowedEventTypes)
		}
	})
}
