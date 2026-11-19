package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v2"
)

// githubAppView is the REST form of a GitHub App (docs/github-app.md). It
// has no key and no webhook secret.
type githubAppView struct {
	AppID               int64   `json:"app_id" yaml:"app_id"`
	Slug                string  `json:"slug" yaml:"slug"`
	Name                string  `json:"name" yaml:"name"`
	ClientID            string  `json:"client_id" yaml:"client_id"`
	DefaultOrgID        *string `json:"default_org_id,omitempty" yaml:"default_org_id,omitempty"`
	OpenRegistration    bool    `json:"open_registration" yaml:"open_registration"`
	Enabled             bool    `json:"enabled" yaml:"enabled"`
	Headroom            float64 `json:"headroom" yaml:"headroom"`
	ActiveInstallations int     `json:"active_installations" yaml:"active_installations"`
	Repositories        int     `json:"repositories" yaml:"repositories"`
	InstallURL          string  `json:"install_url,omitempty" yaml:"install_url,omitempty"`
	Recommended         bool    `json:"recommended" yaml:"recommended"`
	RateLimit           struct {
		Limit      *int       `json:"limit,omitempty" yaml:"limit,omitempty"`
		Remaining  *int       `json:"remaining,omitempty" yaml:"remaining,omitempty"`
		Used       *int       `json:"used,omitempty" yaml:"used,omitempty"`
		ResetAt    *time.Time `json:"reset_at,omitempty" yaml:"reset_at,omitempty"`
		ObservedAt *time.Time `json:"observed_at,omitempty" yaml:"observed_at,omitempty"`
	} `json:"rate_limit" yaml:"rate_limit"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
}

type githubInstallationView struct {
	InstallationID      int64             `json:"installation_id" yaml:"installation_id"`
	AppID               int64             `json:"app_id" yaml:"app_id"`
	AccountLogin        string            `json:"account_login" yaml:"account_login"`
	AccountType         string            `json:"account_type" yaml:"account_type"`
	RepositorySelection string            `json:"repository_selection" yaml:"repository_selection"`
	Permissions         map[string]string `json:"permissions" yaml:"permissions"`
	Suspended           bool              `json:"suspended" yaml:"suspended"`
	DeletedAt           *time.Time        `json:"deleted_at,omitempty" yaml:"deleted_at,omitempty"`
	Headroom            float64           `json:"headroom" yaml:"headroom"`
	LastSeenAt          time.Time         `json:"last_seen_at" yaml:"last_seen_at"`
	Repositories        []struct {
		RepoFullName   string  `json:"repo_full_name" yaml:"repo_full_name"`
		Private        bool    `json:"private" yaml:"private"`
		ProjectID      *string `json:"project_id,omitempty" yaml:"project_id,omitempty"`
		AutoRegistered bool    `json:"auto_registered" yaml:"auto_registered"`
		// PendingApproval: the project was created disabled and waits
		// until an administrator enables it.
		PendingApproval bool `json:"pending_approval" yaml:"pending_approval"`
	} `json:"repositories" yaml:"repositories"`
}

func githubAppPath(parts ...string) string {
	path := "/api/v1/github-apps"
	for _, part := range parts {
		path += "/" + url.PathEscape(part)
	}
	return path
}

func headroomString(value float64) string {
	return strconv.FormatFloat(value*100, 'f', 0, 64) + "%"
}

func renderGitHubApp(format string, app githubAppView) error {
	return render(format, app, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "app_id\t%d\n", app.AppID)
		fmt.Fprintf(w, "slug\t%s\n", app.Slug)
		fmt.Fprintf(w, "name\t%s\n", app.Name)
		if app.ClientID != "" {
			fmt.Fprintf(w, "client_id\t%s\n", app.ClientID)
		}
		if app.DefaultOrgID != nil {
			fmt.Fprintf(w, "default_org_id\t%s\n", *app.DefaultOrgID)
		}
		fmt.Fprintf(w, "open_registration\t%t\n", app.OpenRegistration)
		fmt.Fprintf(w, "enabled\t%t\n", app.Enabled)
		fmt.Fprintf(w, "headroom\t%s\n", headroomString(app.Headroom))
		if app.RateLimit.Remaining != nil && app.RateLimit.Limit != nil {
			fmt.Fprintf(w, "app_rate_limit\t%d of %d left\n", *app.RateLimit.Remaining, *app.RateLimit.Limit)
		}
		fmt.Fprintf(w, "installations\t%d active\n", app.ActiveInstallations)
		fmt.Fprintf(w, "repositories\t%d\n", app.Repositories)
		if app.InstallURL != "" {
			fmt.Fprintf(w, "install_url\t%s\n", app.InstallURL)
		}
		fmt.Fprintf(w, "recommended\t%t\n", app.Recommended)
	})
}

func githubAppIDAction(usage string, run func(ctx *cli.Context, client *apiClient, id string) error) cli.ActionFunc {
	return func(ctx *cli.Context) error {
		if err := requireArgs(ctx, 1, usage); err != nil {
			return err
		}
		id := ctx.Args().First()
		if n, err := strconv.ParseInt(id, 10, 64); err != nil || n <= 0 {
			return fmt.Errorf("the App ID must be a positive number")
		}
		client, err := newAPIClient(ctx)
		if err != nil {
			return err
		}
		return run(ctx, client, id)
	}
}

// readPrivateKeyFile reads the App key. The key goes only into the request
// body; it is never printed.
func readPrivateKeyFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read --private-key-file: %w", err)
	}
	if !strings.Contains(string(data), "PRIVATE KEY") {
		return "", fmt.Errorf("--private-key-file is not a PEM private key")
	}
	return string(data), nil
}

// GitHubAppsCommand manages the GitHub App pool.
var GitHubAppsCommand = &cli.Command{
	Name:    "github-apps",
	Aliases: []string{"github-app"},
	Usage:   "Manage the GitHub Apps of the deployment (needs a global administrator, for example the admin API token)",
	Subcommands: []*cli.Command{
		{
			Name:  "add",
			Usage: "Register a GitHub App. The key and the webhook secret are read from files and never printed.",
			Flags: append(apiFlags(), formatFlag(),
				&cli.Int64Flag{Name: "app-id", Required: true, Usage: "The numeric GitHub App ID"},
				&cli.StringFlag{Name: "private-key-file", Required: true, Usage: "PEM private key of the App"},
				&cli.StringFlag{Name: "webhook-secret-file", Required: true, Usage: "File with the webhook secret of the App"},
				&cli.StringFlag{Name: "slug", Usage: "App slug (default: from GitHub)"},
				&cli.StringFlag{Name: "name", Usage: "App name (default: from GitHub)"},
				&cli.StringFlag{Name: "client-id", Usage: "App client ID (default: from GitHub)"},
				&cli.StringFlag{Name: "default-org", Usage: "Organization of new projects when no account mapping applies"},
				&cli.BoolFlag{Name: "open-registration", Usage: "Enable new projects that go into --default-org (no account mapping). Without it they are created disabled and wait for approval"},
				&cli.BoolFlag{Name: "no-verify", Usage: "Do not check the key with GitHub"},
				&cli.BoolFlag{Name: "replace", Usage: "Replace the key and secret of a registered App"},
			),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				key, err := readPrivateKeyFile(ctx.String("private-key-file"))
				if err != nil {
					return err
				}
				secretData, err := os.ReadFile(ctx.String("webhook-secret-file"))
				if err != nil {
					return fmt.Errorf("failed to read --webhook-secret-file: %w", err)
				}
				secret := strings.TrimSpace(string(secretData))
				if secret == "" {
					return fmt.Errorf("--webhook-secret-file is empty")
				}
				verify := !ctx.Bool("no-verify")
				body := map[string]any{"app_id": ctx.Int64("app-id"), "private_key": key, "webhook_secret": secret,
					"slug": ctx.String("slug"), "name": ctx.String("name"), "client_id": ctx.String("client-id"),
					"default_org": ctx.String("default-org"), "verify": verify, "replace": ctx.Bool("replace")}
				if ctx.IsSet("open-registration") {
					body["open_registration"] = ctx.Bool("open-registration")
				}
				expected := http.StatusCreated
				if ctx.Bool("replace") {
					expected = http.StatusOK
				}
				var response githubAppView
				if err := client.doJSON(http.MethodPost, githubAppPath(), body, expected, &response); err != nil {
					return err
				}
				return renderGitHubApp(ctx.String("format"), response)
			},
		},
		{
			Name:  "list",
			Usage: "List the Apps with their rate-limit headroom. The recommended App has the most headroom.",
			Flags: append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					Apps []githubAppView `json:"apps" yaml:"apps"`
				}
				if err := client.doJSON(http.MethodGet, githubAppPath(), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response.Apps, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "APP ID\tSLUG\tENABLED\tHEADROOM\tINSTALLATIONS\tREPOSITORIES\tRECOMMENDED")
					for _, app := range response.Apps {
						recommended := ""
						if app.Recommended {
							recommended = "yes"
						}
						fmt.Fprintf(w, "%d\t%s\t%t\t%s\t%d\t%d\t%s\n", app.AppID, app.Slug, app.Enabled, headroomString(app.Headroom),
							app.ActiveInstallations, app.Repositories, recommended)
					}
				})
			},
		},
		{
			Name: "show", Usage: "Show an App", ArgsUsage: "<app-id>",
			Flags: append(apiFlags(), formatFlag()),
			Action: githubAppIDAction("usage: reactorcide github-apps show <app-id>", func(ctx *cli.Context, client *apiClient, id string) error {
				var response githubAppView
				if err := client.doJSON(http.MethodGet, githubAppPath(id), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return renderGitHubApp(ctx.String("format"), response)
			}),
		},
		{
			Name: "recommended", Usage: "Show the App that new installations should use (the most headroom)",
			Flags: append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response githubAppView
				if err := client.doJSON(http.MethodGet, githubAppPath("recommended"), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return renderGitHubApp(ctx.String("format"), response)
			},
		},
		{
			Name: "disable", Usage: "Disable an App. Its projects use their tokens and commit statuses again.", ArgsUsage: "<app-id>",
			Flags: append(apiFlags(), formatFlag()),
			Action: githubAppIDAction("usage: reactorcide github-apps disable <app-id>", func(ctx *cli.Context, client *apiClient, id string) error {
				var response githubAppView
				if err := client.doJSON(http.MethodPost, githubAppPath(id, "disable"), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return renderGitHubApp(ctx.String("format"), response)
			}),
		},
		{
			Name: "enable", Usage: "Enable a disabled App", ArgsUsage: "<app-id>",
			Flags: append(apiFlags(), formatFlag()),
			Action: githubAppIDAction("usage: reactorcide github-apps enable <app-id>", func(ctx *cli.Context, client *apiClient, id string) error {
				var response githubAppView
				if err := client.doJSON(http.MethodPost, githubAppPath(id, "enable"), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return renderGitHubApp(ctx.String("format"), response)
			}),
		},
		{
			Name: "installations", Usage: "List the installations and repositories of an App", ArgsUsage: "<app-id>",
			Flags: append(apiFlags(), formatFlag()),
			Action: githubAppIDAction("usage: reactorcide github-apps installations <app-id>", func(ctx *cli.Context, client *apiClient, id string) error {
				var response struct {
					Installations []githubInstallationView `json:"installations" yaml:"installations"`
				}
				if err := client.doJSON(http.MethodGet, githubAppPath(id, "installations"), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response.Installations, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "INSTALLATION\tACCOUNT\tSELECTION\tSTATE\tHEADROOM\tREPOSITORIES")
					for _, inst := range response.Installations {
						state := "active"
						switch {
						case inst.DeletedAt != nil:
							state = "deleted"
						case inst.Suspended:
							state = "suspended"
						}
						names := make([]string, 0, len(inst.Repositories))
						for _, repo := range inst.Repositories {
							if repo.PendingApproval {
								names = append(names, repo.RepoFullName+" (pending approval)")
								continue
							}
							names = append(names, repo.RepoFullName)
						}
						fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", inst.InstallationID, inst.AccountLogin, inst.RepositorySelection,
							state, headroomString(inst.Headroom), strings.Join(names, ","))
					}
				})
			}),
		},
		{
			Name: "sync", Usage: "Read the installations and repositories of an App from GitHub and register projects", ArgsUsage: "<app-id>",
			Flags: append(apiFlags(), formatFlag()),
			Action: githubAppIDAction("usage: reactorcide github-apps sync <app-id>", func(ctx *cli.Context, client *apiClient, id string) error {
				var response struct {
					AppID         int64 `json:"app_id" yaml:"app_id"`
					Installations int   `json:"installations" yaml:"installations"`
					Repositories  []struct {
						Repository string `json:"repository" yaml:"repository"`
						ProjectID  string `json:"project_id,omitempty" yaml:"project_id,omitempty"`
						Action     string `json:"action" yaml:"action"`
						Detail     string `json:"detail,omitempty" yaml:"detail,omitempty"`
					} `json:"repositories" yaml:"repositories"`
					Errors []string `json:"errors,omitempty" yaml:"errors,omitempty"`
				}
				if err := client.doJSON(http.MethodPost, githubAppPath(id, "sync"), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "installations\t%d\n", response.Installations)
					fmt.Fprintln(w, "REPOSITORY\tACTION\tPROJECT\tDETAIL")
					for _, repo := range response.Repositories {
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", repo.Repository, repo.Action, repo.ProjectID, repo.Detail)
					}
					for _, e := range response.Errors {
						fmt.Fprintf(w, "error\t%s\n", e)
					}
				})
			}),
		},
		{
			Name: "map-account", Usage: "Register new projects of a GitHub account in an organization", ArgsUsage: "<account-login>",
			Flags: append(apiFlags(), formatFlag(), &cli.StringFlag{Name: "org", Required: true, Usage: "Organization name or ID"}),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "usage: reactorcide github-apps map-account --org ORG <account-login>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response map[string]any
				if err := client.doJSON(http.MethodPut, githubAppPath("account-orgs", ctx.Args().First()),
					map[string]string{"org": ctx.String("org")}, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "account\t%v\norg_id\t%v\n", response["account_login"], response["org_id"])
				})
			},
		},
		{
			Name: "unmap-account", Usage: "Remove an account mapping (new projects go to the default organization)", ArgsUsage: "<account-login>",
			Flags: apiFlags(),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "usage: reactorcide github-apps unmap-account <account-login>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				return client.doJSON(http.MethodDelete, githubAppPath("account-orgs", ctx.Args().First()), nil, http.StatusNoContent, nil)
			},
		},
		{
			Name: "accounts", Usage: "List the account mappings",
			Flags: append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					AccountOrgs []struct {
						AccountLogin string `json:"account_login" yaml:"account_login"`
						OrgID        string `json:"org_id" yaml:"org_id"`
					} `json:"account_orgs" yaml:"account_orgs"`
				}
				if err := client.doJSON(http.MethodGet, githubAppPath("account-orgs"), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response.AccountOrgs, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "ACCOUNT\tORG ID")
					for _, row := range response.AccountOrgs {
						fmt.Fprintf(w, "%s\t%s\n", row.AccountLogin, row.OrgID)
					}
				})
			},
		},
	},
}
