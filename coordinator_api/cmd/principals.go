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

type principalIdentity struct {
	IdentityType  string `json:"identity_type" yaml:"identity_type"`
	IdentityValue string `json:"identity_value" yaml:"identity_value"`
	VerifiedBy    string `json:"verified_by" yaml:"verified_by"`
}

type principalToken struct {
	TokenID      string     `json:"token_id" yaml:"token_id"`
	Name         string     `json:"name" yaml:"name"`
	Capabilities []string   `json:"capabilities" yaml:"capabilities"`
	CreatedAt    time.Time  `json:"created_at" yaml:"created_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
	IsActive     bool       `json:"is_active" yaml:"is_active"`
}

type principalDetail struct {
	PrincipalID  string              `json:"principal_id" yaml:"principal_id"`
	Kind         string              `json:"kind" yaml:"kind"`
	Organization string              `json:"organization,omitempty" yaml:"organization,omitempty"`
	Name         string              `json:"name" yaml:"name"`
	DisplayName  string              `json:"display_name" yaml:"display_name"`
	Status       string              `json:"status" yaml:"status"`
	Subjects     []string            `json:"subjects,omitempty" yaml:"subjects,omitempty"`
	Identities   []principalIdentity `json:"identities" yaml:"identities"`
	Groups       []string            `json:"groups,omitempty" yaml:"groups,omitempty"`
	Tokens       []principalToken    `json:"tokens,omitempty" yaml:"tokens,omitempty"`
}

type principalRole struct {
	AssignmentID string  `json:"assignment_id" yaml:"assignment_id"`
	ScopeType    string  `json:"scope_type" yaml:"scope_type"`
	ScopeID      *string `json:"scope_id,omitempty" yaml:"scope_id,omitempty"`
	Role         string  `json:"role" yaml:"role"`
}

func principalsPath(org string, parts ...string) string {
	path := "/api/v1/organizations/" + url.PathEscape(org) + "/principals"
	for _, part := range parts {
		path += "/" + url.PathEscape(part)
	}
	return path
}

func orgFlag() cli.Flag {
	return &cli.StringFlag{Name: "org", Required: true, Usage: "organization name"}
}

func renderPrincipal(format string, principal principalDetail) error {
	return render(format, principal, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "principal_id\t%s\n", principal.PrincipalID)
		fmt.Fprintf(w, "kind\t%s\n", principal.Kind)
		fmt.Fprintf(w, "name\t%s\n", principal.Name)
		fmt.Fprintf(w, "display_name\t%s\n", principal.DisplayName)
		fmt.Fprintf(w, "status\t%s\n", principal.Status)
		if principal.Organization != "" {
			fmt.Fprintf(w, "organization\t%s\n", principal.Organization)
		}
		for _, identity := range principal.Identities {
			fmt.Fprintf(w, "identity\t%s:%s (%s)\n", identity.IdentityType, identity.IdentityValue, identity.VerifiedBy)
		}
		for _, group := range principal.Groups {
			fmt.Fprintf(w, "group\t%s\n", group)
		}
		for _, token := range principal.Tokens {
			fmt.Fprintf(w, "token\t%s %s active=%t\n", token.TokenID, token.Name, token.IsActive)
		}
		for _, subject := range principal.Subjects {
			fmt.Fprintf(w, "subject\t%s\n", subject)
		}
	})
}

// principalAction runs one request for a principal and prints the result.
func principalAction(usage string, args int, method string, suffix func(ctx *cli.Context) []string,
	body func(ctx *cli.Context) interface{}, status int) cli.ActionFunc {
	return func(ctx *cli.Context) error {
		if err := requireArgs(ctx, args, usage); err != nil {
			return err
		}
		client, err := newAPIClient(ctx)
		if err != nil {
			return err
		}
		parts := []string{ctx.Args().First()}
		if suffix != nil {
			parts = append(parts, suffix(ctx)...)
		}
		var requestBody interface{}
		if body != nil {
			requestBody = body(ctx)
		}
		if status == http.StatusNoContent {
			if err := client.doJSON(method, principalsPath(ctx.String("org"), parts...), requestBody, status, nil); err != nil {
				return err
			}
			fmt.Println("Done")
			return nil
		}
		var response principalDetail
		if err := client.doJSON(method, principalsPath(ctx.String("org"), parts...), requestBody, status, &response); err != nil {
			return err
		}
		return renderPrincipal(ctx.String("format"), response)
	}
}

// PrincipalsCommand manages principals: service principals of an
// organization, their groups, roles, and tokens, and the identities of the
// principals of the organization. An org admin or the admin token runs it.
var PrincipalsCommand = &cli.Command{
	Name:  "principals",
	Usage: "Manage principals: service principals, identities, groups, roles, and tokens",
	Subcommands: []*cli.Command{
		{
			Name: "list", Usage: "List the principals of an organization",
			Flags: append(apiFlags(), orgFlag(), formatFlag(),
				&cli.StringFlag{Name: "kind", Usage: "human, service, or system"}),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				path := principalsPath(ctx.String("org"))
				if kind := strings.TrimSpace(ctx.String("kind")); kind != "" {
					path += "?kind=" + url.QueryEscape(kind)
				}
				var response struct {
					Principals []principalDetail `json:"principals"`
				}
				if err := client.doJSON(http.MethodGet, path, nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response.Principals, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "KIND\tNAME\tSTATUS\tPRINCIPAL ID")
					for _, principal := range response.Principals {
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", principal.Kind, principal.Name, principal.Status, principal.PrincipalID)
					}
				})
			},
		},
		{
			Name: "create", ArgsUsage: "<name>", Usage: "Create a service principal",
			Flags: append(apiFlags(), orgFlag(), formatFlag(), &cli.StringFlag{Name: "display-name"}),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "principals create --org <org> <name>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response principalDetail
				body := map[string]string{"name": ctx.Args().First(), "display_name": ctx.String("display-name")}
				if err := client.doJSON(http.MethodPost, principalsPath(ctx.String("org")), body, http.StatusCreated, &response); err != nil {
					return err
				}
				return renderPrincipal(ctx.String("format"), response)
			},
		},
		{
			Name: "show", ArgsUsage: "<principal>", Usage: "Show a principal (a service name or a principal UUID)",
			Flags:  append(apiFlags(), orgFlag(), formatFlag()),
			Action: principalAction("principals show --org <org> <principal>", 1, http.MethodGet, nil, nil, http.StatusOK),
		},
		{
			Name: "disable", ArgsUsage: "<principal>", Usage: "Disable a service principal (its tokens stop working)",
			Flags: append(apiFlags(), orgFlag(), formatFlag()),
			Action: principalAction("principals disable --org <org> <principal>", 1, http.MethodPost,
				func(*cli.Context) []string { return []string{"disable"} }, nil, http.StatusOK),
		},
		{
			Name: "enable", ArgsUsage: "<principal>", Usage: "Enable a service principal",
			Flags: append(apiFlags(), orgFlag(), formatFlag()),
			Action: principalAction("principals enable --org <org> <principal>", 1, http.MethodPost,
				func(*cli.Context) []string { return []string{"enable"} }, nil, http.StatusOK),
		},
		{
			Name: "link", ArgsUsage: "<principal>", Usage: "Link an identity to a principal (verified by an admin)",
			Flags: append(apiFlags(), orgFlag(), formatFlag(),
				&cli.StringFlag{Name: "type", Required: true, Usage: "vcs, linkkeys, or vcsstar"},
				&cli.StringFlag{Name: "value", Required: true, Usage: "for example github/octocat or alice@example.com"}),
			Action: principalAction("principals link --org <org> --type <type> --value <value> <principal>", 1, http.MethodPost,
				func(*cli.Context) []string { return []string{"identities"} },
				func(ctx *cli.Context) interface{} {
					return map[string]string{"identity_type": ctx.String("type"), "identity_value": ctx.String("value")}
				}, http.StatusOK),
		},
		{
			Name: "unlink", ArgsUsage: "<principal>", Usage: "Remove an identity from a principal",
			Flags: append(apiFlags(), orgFlag(), formatFlag(),
				&cli.StringFlag{Name: "type", Required: true}, &cli.StringFlag{Name: "value", Required: true}),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "principals unlink --org <org> --type <type> --value <value> <principal>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				path := principalsPath(ctx.String("org"), ctx.Args().First(), "identities") +
					"?type=" + url.QueryEscape(ctx.String("type")) + "&value=" + url.QueryEscape(ctx.String("value"))
				var response principalDetail
				if err := client.doJSON(http.MethodDelete, path, nil, http.StatusOK, &response); err != nil {
					return err
				}
				return renderPrincipal(ctx.String("format"), response)
			},
		},
		{
			Name: "add-group", ArgsUsage: "<principal> <group>", Usage: "Add a principal to a group",
			Flags: append(apiFlags(), orgFlag(), formatFlag()),
			Action: principalAction("principals add-group --org <org> <principal> <group>", 2, http.MethodPost,
				func(*cli.Context) []string { return []string{"groups"} },
				func(ctx *cli.Context) interface{} { return map[string]string{"group": ctx.Args().Get(1)} }, http.StatusOK),
		},
		{
			Name: "remove-group", ArgsUsage: "<principal> <group>", Usage: "Remove a principal from a group",
			Flags: append(apiFlags(), orgFlag()),
			Action: principalAction("principals remove-group --org <org> <principal> <group>", 2, http.MethodDelete,
				func(ctx *cli.Context) []string { return []string{"groups", ctx.Args().Get(1)} }, nil, http.StatusNoContent),
		},
		{
			Name: "roles", ArgsUsage: "<principal>", Usage: "List the direct roles of a principal",
			Flags: append(apiFlags(), orgFlag(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "principals roles --org <org> <principal>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					Roles []principalRole `json:"roles"`
				}
				if err := client.doJSON(http.MethodGet, principalsPath(ctx.String("org"), ctx.Args().First(), "roles"), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response.Roles, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "ASSIGNMENT ID\tSCOPE\tSCOPE ID\tROLE")
					for _, role := range response.Roles {
						scopeID := ""
						if role.ScopeID != nil {
							scopeID = *role.ScopeID
						}
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", role.AssignmentID, role.ScopeType, scopeID, role.Role)
					}
				})
			},
		},
		{
			Name: "grant-role", ArgsUsage: "<principal>", Usage: "Give a principal a role in the organization, a project, or one workflow",
			Flags: append(apiFlags(), orgFlag(),
				&cli.StringFlag{Name: "role", Required: true, Usage: "admin or member (organization); owner or member (project); owner (workflow)"},
				&cli.StringFlag{Name: "project", Usage: "project name; omit for an organization role"},
				&cli.StringFlag{Name: "workflow", Usage: "workflow security ID (the YAML id) for a workflow role; needs --project"}),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "principals grant-role --org <org> --role <role> [--project <project> [--workflow <workflow>]] <principal>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				if workflow := strings.TrimSpace(ctx.String("workflow")); workflow != "" {
					return grantPrincipalWorkflowRole(ctx, client, workflow)
				}
				var response principalRole
				body := map[string]string{"role": ctx.String("role"), "project": ctx.String("project")}
				if err := client.doJSON(http.MethodPost, principalsPath(ctx.String("org"), ctx.Args().First(), "roles"), body, http.StatusCreated, &response); err != nil {
					return err
				}
				fmt.Printf("Role %s granted at %s scope (assignment %s)\n", response.Role, response.ScopeType, response.AssignmentID)
				return nil
			},
		},
		{
			Name: "revoke-role", ArgsUsage: "<principal> <assignment-id>", Usage: "Remove a role of a principal",
			Flags: append(apiFlags(), orgFlag()),
			Action: principalAction("principals revoke-role --org <org> <principal> <assignment-id>", 2, http.MethodDelete,
				func(ctx *cli.Context) []string { return []string{"roles", ctx.Args().Get(1)} }, nil, http.StatusNoContent),
		},
		{
			Name: "tokens", Usage: "Manage the tokens of a service principal",
			Subcommands: []*cli.Command{
				{
					Name: "create", ArgsUsage: "<principal>",
					Usage: "Issue a token for a service principal (capabilities intersect the principal's roles)",
					Flags: append(apiFlags(), orgFlag(),
						&cli.StringFlag{Name: "name", Usage: "token name (default: the principal name)"},
						&cli.StringSliceFlag{Name: "capability", Usage: "capability; repeat it. Omit for every capability (admin token only); on a no-login instance this also gives all organizations"},
						&cli.StringFlag{Name: "expires-at", Usage: "RFC 3339 expiry time"}),
					Action: func(ctx *cli.Context) error {
						if err := requireArgs(ctx, 1, "principals tokens create --org <org> <principal>"); err != nil {
							return err
						}
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						body := map[string]interface{}{"name": ctx.String("name"), "capabilities": ctx.StringSlice("capability")}
						if value := strings.TrimSpace(ctx.String("expires-at")); value != "" {
							expires, err := time.Parse(time.RFC3339, value)
							if err != nil {
								return fmt.Errorf("--expires-at must be RFC 3339: %w", err)
							}
							body["expires_at"] = expires
						}
						var response struct {
							Token     string         `json:"token"`
							TokenInfo principalToken `json:"token_info"`
						}
						if err := client.doJSON(http.MethodPost, principalsPath(ctx.String("org"), ctx.Args().First(), "tokens"), body, http.StatusCreated, &response); err != nil {
							return err
						}
						fmt.Printf("Token ID: %s\n", response.TokenInfo.TokenID)
						fmt.Printf("Token: %s\n", response.Token)
						fmt.Println("Store the token now. It cannot be shown again.")
						return nil
					},
				},
				{
					Name: "list", ArgsUsage: "<principal>", Usage: "List the tokens of a service principal",
					Flags: append(apiFlags(), orgFlag(), formatFlag()),
					Action: func(ctx *cli.Context) error {
						if err := requireArgs(ctx, 1, "principals tokens list --org <org> <principal>"); err != nil {
							return err
						}
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						var response struct {
							Tokens []principalToken `json:"tokens"`
						}
						if err := client.doJSON(http.MethodGet, principalsPath(ctx.String("org"), ctx.Args().First(), "tokens"), nil, http.StatusOK, &response); err != nil {
							return err
						}
						return render(ctx.String("format"), response.Tokens, func(w *tabwriter.Writer) {
							fmt.Fprintln(w, "TOKEN ID\tNAME\tACTIVE\tCAPABILITIES")
							for _, token := range response.Tokens {
								fmt.Fprintf(w, "%s\t%s\t%t\t%s\n", token.TokenID, token.Name, token.IsActive, strings.Join(token.Capabilities, ","))
							}
						})
					},
				},
				{
					Name: "revoke", ArgsUsage: "<principal> <token-id>", Usage: "Revoke a token of a service principal",
					Flags: append(apiFlags(), orgFlag()),
					Action: principalAction("principals tokens revoke --org <org> <principal> <token-id>", 2, http.MethodDelete,
						func(ctx *cli.Context) []string { return []string{"tokens", ctx.Args().Get(1)} }, nil, http.StatusNoContent),
				},
			},
		},
	},
}

// ResolvePrincipalCommand maps one identity to its principal and every
// linked identity. With --org it asks as an org admin (the principal must be
// in the organization); without it, it needs the admin token.
var ResolvePrincipalCommand = &cli.Command{
	Name:  "resolve-principal",
	Usage: "Show the principal of an identity and all of its linked identities",
	Flags: append(apiFlags(), formatFlag(),
		&cli.StringFlag{Name: "org", Usage: "ask as an admin of this organization"},
		&cli.StringFlag{Name: "type", Required: true, Usage: "user, vcs, vcsstar, linkkeys, token, federated, or principal"},
		&cli.StringFlag{Name: "value", Required: true, Usage: "the identity value (for example github/octocat)"}),
	Action: func(ctx *cli.Context) error {
		client, err := newAPIClient(ctx)
		if err != nil {
			return err
		}
		query := "?type=" + url.QueryEscape(ctx.String("type")) + "&value=" + url.QueryEscape(ctx.String("value"))
		path := "/api/v1/principals/resolve" + query
		if org := strings.TrimSpace(ctx.String("org")); org != "" {
			path = principalsPath(org, "resolve") + query
		}
		var response struct {
			Identity  string          `json:"identity"`
			Principal principalDetail `json:"principal"`
		}
		if err := client.doJSON(http.MethodGet, path, nil, http.StatusOK, &response); err != nil {
			return err
		}
		return renderPrincipal(ctx.String("format"), response.Principal)
	},
}

// grantPrincipalWorkflowRole makes a principal an owner of one workflow
// (the workflow-roles route, docs/principals.md). The argument is a
// principal ID or name; a name is read to find the ID.
func grantPrincipalWorkflowRole(ctx *cli.Context, client *apiClient, workflow string) error {
	if ctx.String("role") != "owner" {
		return fmt.Errorf("a workflow role is owner")
	}
	if strings.TrimSpace(ctx.String("project")) == "" {
		return fmt.Errorf("--workflow needs --project")
	}
	var principal principalDetail
	if err := client.doJSON(http.MethodGet, principalsPath(ctx.String("org"), ctx.Args().First()), nil, http.StatusOK, &principal); err != nil {
		return err
	}
	body := map[string]string{"project": ctx.String("project"), "workflow": workflow, "principal_id": principal.PrincipalID}
	var response workflowRole
	if err := client.doJSON(http.MethodPost, orgPath(ctx.String("org"), "workflow-roles"), body, http.StatusCreated, &response); err != nil {
		return err
	}
	fmt.Printf("Workflow role %s granted on %s (assignment %s)\n", response.Role, response.Workflow, response.AssignmentID)
	return nil
}
