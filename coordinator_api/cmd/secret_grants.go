package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

var SecretGrantsCommand = &cli.Command{
	Name:  "secret-grants",
	Usage: "Manage API secret grants",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "api-url",
			Aliases: []string{"u"},
			Usage:   "Coordinator API URL",
			EnvVars: []string{"REACTORCIDE_API_URL"},
		},
		&cli.StringFlag{
			Name:    "token",
			Aliases: []string{"t"},
			Usage:   "API token for authentication",
			EnvVars: []string{"REACTORCIDE_API_TOKEN"},
		},
		&cli.StringFlag{
			Name:  "project",
			Usage: "Project scope by ID, name, or repo URL. Omit for org/global grants",
		},
	},
	Subcommands: []*cli.Command{
		{
			Name:  "list",
			Usage: "List secret grants",
			Flags: []cli.Flag{secretGrantFormatFlag()},
			Action: func(ctx *cli.Context) error {
				client, err := newSecretGrantsAPIClient(ctx)
				if err != nil {
					return err
				}
				grants, err := client.List(ctx.String("project"))
				if err != nil {
					return err
				}
				return printSecretGrants(ctx.String("format"), grants)
			},
		},
		{
			Name:      "get",
			Usage:     "Get a secret grant by name or ID",
			ArgsUsage: "<name-or-id>",
			Flags:     []cli.Flag{secretGrantFormatFlag()},
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() < 1 {
					return fmt.Errorf("usage: reactorcide secret-grants get <name-or-id>")
				}
				client, err := newSecretGrantsAPIClient(ctx)
				if err != nil {
					return err
				}
				grant, err := client.Get(ctx.Args().Get(0), ctx.String("project"))
				if err != nil {
					return err
				}
				return printSecretGrants(ctx.String("format"), []models.SecretGrant{*grant})
			},
		},
		{
			Name:      "set",
			Usage:     "Create or update a named secret grant",
			ArgsUsage: "<name>",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "secret-path", Usage: "Secret path pattern to grant"},
				&cli.StringFlag{Name: "secret-match", Value: models.SecretGrantMatchPrefix, Usage: "Secret path match: exact, prefix, glob, regex"},
				&cli.StringFlag{Name: "job-name", Usage: "Job name pattern. Omit to match any job name"},
				&cli.StringFlag{Name: "job-match", Value: models.SecretGrantMatchAny, Usage: "Job name match: any, exact, prefix, glob, regex"},
				&cli.StringFlag{Name: "description", Usage: "Grant description"},
			},
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() < 1 {
					return fmt.Errorf("usage: reactorcide secret-grants set <name> --secret-path <pattern>")
				}
				if strings.TrimSpace(ctx.String("secret-path")) == "" {
					return fmt.Errorf("--secret-path is required")
				}
				req := secretGrantAPIRequest{
					Name:              ctx.Args().Get(0),
					Project:           ctx.String("project"),
					SecretPathMatch:   ctx.String("secret-match"),
					SecretPathPattern: ctx.String("secret-path"),
					JobNameMatch:      ctx.String("job-match"),
					JobNamePattern:    ctx.String("job-name"),
					Description:       ctx.String("description"),
				}
				if req.JobNamePattern != "" && req.JobNameMatch == models.SecretGrantMatchAny {
					req.JobNameMatch = models.SecretGrantMatchExact
				}
				client, err := newSecretGrantsAPIClient(ctx)
				if err != nil {
					return err
				}
				grant, err := client.Upsert(req)
				if err != nil {
					return err
				}
				return printSecretGrants("table", []models.SecretGrant{*grant})
			},
		},
		{
			Name:      "delete",
			Usage:     "Delete a secret grant by name or ID",
			ArgsUsage: "<name-or-id>",
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() < 1 {
					return fmt.Errorf("usage: reactorcide secret-grants delete <name-or-id>")
				}
				client, err := newSecretGrantsAPIClient(ctx)
				if err != nil {
					return err
				}
				if err := client.Delete(ctx.Args().Get(0), ctx.String("project")); err != nil {
					return err
				}
				fmt.Printf("Secret grant deleted: %s\n", ctx.Args().Get(0))
				return nil
			},
		},
		{
			Name:  "apply",
			Usage: "Apply secret grants from a YAML file",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "file", Aliases: []string{"f"}, Usage: "YAML file to apply"},
				&cli.BoolFlag{Name: "dry-run", Usage: "Compute changes without writing them"},
				&cli.BoolFlag{Name: "prune", Usage: "Delete grants omitted from scopes present in the file"},
				secretGrantFormatFlag(),
			},
			Action: func(ctx *cli.Context) error {
				path := ctx.String("file")
				if path == "" {
					return fmt.Errorf("--file is required")
				}
				req, err := loadSecretGrantApplyFile(path)
				if err != nil {
					return err
				}
				req.DryRun = req.DryRun || ctx.Bool("dry-run")
				req.Prune = req.Prune || ctx.Bool("prune")
				client, err := newSecretGrantsAPIClient(ctx)
				if err != nil {
					return err
				}
				resp, err := client.Apply(req)
				if err != nil {
					return err
				}
				return printSecretGrantApplyResponse(ctx.String("format"), resp)
			},
		},
	},
}

type secretGrantAPIClient struct {
	*secretsAPIClient
}

type secretGrantAPIRequest struct {
	Name              string `json:"name,omitempty" yaml:"name,omitempty"`
	ProjectID         string `json:"project_id,omitempty" yaml:"project_id,omitempty"`
	Project           string `json:"project,omitempty" yaml:"project,omitempty"`
	SecretPathMatch   string `json:"secret_path_match,omitempty" yaml:"secret_path_match,omitempty"`
	SecretPathPattern string `json:"secret_path_pattern,omitempty" yaml:"secret_path_pattern,omitempty"`
	JobNameMatch      string `json:"job_name_match,omitempty" yaml:"job_name_match,omitempty"`
	JobNamePattern    string `json:"job_name_pattern,omitempty" yaml:"job_name_pattern,omitempty"`
	Description       string `json:"description,omitempty" yaml:"description,omitempty"`
	State             string `json:"state,omitempty" yaml:"state,omitempty"`
}

type secretGrantListResponse struct {
	Grants []models.SecretGrant `json:"grants"`
	Total  int                  `json:"total"`
}

type secretGrantApplyRequest struct {
	DryRun bool                    `json:"dry_run,omitempty" yaml:"dry_run,omitempty"`
	Prune  bool                    `json:"prune,omitempty" yaml:"prune,omitempty"`
	Grants []secretGrantAPIRequest `json:"grants" yaml:"grants"`
}

type secretGrantApplyResponse struct {
	DryRun    bool                 `json:"dry_run" yaml:"dry_run"`
	Created   []models.SecretGrant `json:"created,omitempty" yaml:"created,omitempty"`
	Updated   []models.SecretGrant `json:"updated,omitempty" yaml:"updated,omitempty"`
	Deleted   []models.SecretGrant `json:"deleted,omitempty" yaml:"deleted,omitempty"`
	Unchanged []models.SecretGrant `json:"unchanged,omitempty" yaml:"unchanged,omitempty"`
}

type secretGrantResourceFile struct {
	APIVersion string                  `yaml:"apiVersion"`
	Kind       string                  `yaml:"kind"`
	DryRun     bool                    `yaml:"dry_run"`
	Prune      bool                    `yaml:"prune"`
	Grants     []secretGrantAPIRequest `yaml:"grants"`
	Items      []secretGrantResource   `yaml:"items"`
}

type secretGrantResource struct {
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Scope struct {
			ProjectID string `yaml:"project_id"`
			Project   string `yaml:"project"`
		} `yaml:"scope"`
		Secret struct {
			Path  string `yaml:"path"`
			Match string `yaml:"match"`
		} `yaml:"secret"`
		Subject struct {
			JobName struct {
				Value string `yaml:"value"`
				Match string `yaml:"match"`
			} `yaml:"jobName"`
		} `yaml:"subject"`
		Description string `yaml:"description"`
		State       string `yaml:"state"`
	} `yaml:"spec"`
}

func secretGrantFormatFlag() cli.Flag {
	return &cli.StringFlag{Name: "format", Aliases: []string{"f"}, Value: "table", Usage: "Output format: table, json, yaml"}
}

func newSecretGrantsAPIClient(ctx *cli.Context) (*secretGrantAPIClient, error) {
	client, err := newSecretsAPIClient(ctx)
	if err != nil {
		return nil, err
	}
	return &secretGrantAPIClient{secretsAPIClient: client}, nil
}

func (c *secretGrantAPIClient) List(project string) ([]models.SecretGrant, error) {
	var response secretGrantListResponse
	if err := c.doJSON(http.MethodGet, "/api/v1/secret-grants"+secretGrantProjectQuery(project), nil, http.StatusOK, &response); err != nil {
		return nil, err
	}
	return response.Grants, nil
}

func (c *secretGrantAPIClient) Get(ref, project string) (*models.SecretGrant, error) {
	var response models.SecretGrant
	if err := c.doJSON(http.MethodGet, "/api/v1/secret-grants/"+url.PathEscape(ref)+secretGrantProjectQuery(project), nil, http.StatusOK, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *secretGrantAPIClient) Upsert(req secretGrantAPIRequest) (*models.SecretGrant, error) {
	projectRef := req.Project
	if projectRef == "" {
		projectRef = req.ProjectID
	}
	existing, err := c.Get(req.Name, projectRef)
	if err == nil {
		var response models.SecretGrant
		if err := c.doJSON(http.MethodPatch, "/api/v1/secret-grants/"+url.PathEscape(existing.Name)+secretGrantProjectQuery(projectRef), req, http.StatusOK, &response); err != nil {
			return nil, err
		}
		return &response, nil
	}
	var apiErr *secretsAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		return nil, err
	}
	var response models.SecretGrant
	if err := c.doJSON(http.MethodPost, "/api/v1/secret-grants", req, http.StatusCreated, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *secretGrantAPIClient) Delete(ref, project string) error {
	return c.doJSON(http.MethodDelete, "/api/v1/secret-grants/"+url.PathEscape(ref)+secretGrantProjectQuery(project), nil, http.StatusNoContent, nil)
}

func (c *secretGrantAPIClient) Apply(req secretGrantApplyRequest) (*secretGrantApplyResponse, error) {
	var response secretGrantApplyResponse
	if err := c.doJSON(http.MethodPost, "/api/v1/secret-grants/apply", req, http.StatusOK, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func secretGrantProjectQuery(project string) string {
	if strings.TrimSpace(project) == "" {
		return ""
	}
	values := url.Values{}
	values.Set("project", project)
	return "?" + values.Encode()
}

func loadSecretGrantApplyFile(path string) (secretGrantApplyRequest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return secretGrantApplyRequest{}, fmt.Errorf("failed to read %s: %w", path, err)
	}
	var file secretGrantResourceFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return secretGrantApplyRequest{}, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	req := secretGrantApplyRequest{DryRun: file.DryRun, Prune: file.Prune, Grants: file.Grants}
	for _, item := range file.Items {
		grant := secretGrantAPIRequest{
			Name:              item.Metadata.Name,
			ProjectID:         item.Spec.Scope.ProjectID,
			Project:           item.Spec.Scope.Project,
			SecretPathMatch:   item.Spec.Secret.Match,
			SecretPathPattern: item.Spec.Secret.Path,
			JobNameMatch:      item.Spec.Subject.JobName.Match,
			JobNamePattern:    item.Spec.Subject.JobName.Value,
			Description:       item.Spec.Description,
			State:             item.Spec.State,
		}
		if grant.SecretPathMatch == "" {
			grant.SecretPathMatch = models.SecretGrantMatchPrefix
		}
		if grant.JobNameMatch == "" {
			grant.JobNameMatch = models.SecretGrantMatchAny
		}
		req.Grants = append(req.Grants, grant)
	}
	return req, nil
}

func printSecretGrants(format string, grants []models.SecretGrant) error {
	sort.Slice(grants, func(i, j int) bool {
		return grants[i].Name < grants[j].Name
	})
	switch format {
	case "json":
		data, _ := json.MarshalIndent(grants, "", "  ")
		fmt.Println(string(data))
	case "yaml":
		data, _ := yaml.Marshal(grants)
		fmt.Print(string(data))
	case "table":
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSCOPE\tSECRET\tJOB\tDESCRIPTION")
		for _, grant := range grants {
			scope := "global"
			if grant.ProjectID != nil && *grant.ProjectID != "" {
				scope = "project:" + *grant.ProjectID
			}
			secret := grant.SecretPathMatch + ":" + grant.SecretPathPattern
			job := grant.JobNameMatch
			if grant.JobNamePattern != "" {
				job += ":" + grant.JobNamePattern
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", grant.Name, scope, secret, job, grant.Description)
		}
		return w.Flush()
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
	return nil
}

func printSecretGrantApplyResponse(format string, resp *secretGrantApplyResponse) error {
	switch format {
	case "json":
		data, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(data))
	case "yaml":
		data, _ := yaml.Marshal(resp)
		fmt.Print(string(data))
	case "table":
		fmt.Printf("dry_run: %t\n", resp.DryRun)
		fmt.Printf("created: %d\n", len(resp.Created))
		fmt.Printf("updated: %d\n", len(resp.Updated))
		fmt.Printf("deleted: %d\n", len(resp.Deleted))
		fmt.Printf("unchanged: %d\n", len(resp.Unchanged))
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
	return nil
}
