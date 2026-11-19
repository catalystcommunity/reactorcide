package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
)

// Workflow-scoped roles and workflow owners (plan B5, B2;
// docs/organizations-and-ci-policy.md "Workflow owners and workflow roles").

type workflowRole struct {
	AssignmentID  string `json:"assignment_id" yaml:"assignment_id"`
	PrincipalType string `json:"principal_type" yaml:"principal_type"`
	PrincipalID   string `json:"principal_id" yaml:"principal_id"`
	PrincipalName string `json:"principal_name,omitempty" yaml:"principal_name,omitempty"`
	ProjectID     string `json:"project_id" yaml:"project_id"`
	Workflow      string `json:"workflow" yaml:"workflow"`
	Role          string `json:"role" yaml:"role"`
	CreatedAt     string `json:"created_at" yaml:"created_at"`
}

type workflowOwner struct {
	Workflow string   `json:"workflow" yaml:"workflow"`
	Group    string   `json:"group,omitempty" yaml:"group,omitempty"`
	Subjects []string `json:"subjects,omitempty" yaml:"subjects,omitempty"`
	Contact  *struct {
		Channel string `json:"channel,omitempty" yaml:"channel,omitempty"`
		HelpURL string `json:"help_url,omitempty" yaml:"help_url,omitempty"`
		Email   string `json:"email,omitempty" yaml:"email,omitempty"`
	} `json:"contact,omitempty" yaml:"contact,omitempty"`
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
	CISHA  string `json:"ci_sha,omitempty" yaml:"ci_sha,omitempty"`
}

func orgPath(org string, parts ...string) string {
	path := "/api/v1/organizations/" + url.PathEscape(org)
	for _, part := range parts {
		path += "/" + url.PathEscape(part)
	}
	return path
}

// RolesCommand manages workflow-scoped roles. A workflow owner (a holder of
// the role, or a member of the owner group that the trusted workflow
// definition names) can cancel, retry and re-run, quarantine, and propose
// required-set changes for that one workflow.
var RolesCommand = &cli.Command{
	Name:  "roles",
	Usage: "Manage workflow-scoped roles (owner of one workflow in a project)",
	Subcommands: []*cli.Command{
		{
			Name: "list", Usage: "List the workflow roles of a project",
			Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}, &cli.StringFlag{Name: "project", Required: true},
				&cli.StringFlag{Name: "workflow", Usage: "workflow security ID (the YAML id); omit for every workflow"}, formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				query := url.Values{"project": {ctx.String("project")}}
				if ctx.String("workflow") != "" {
					query.Set("workflow", ctx.String("workflow"))
				}
				var response struct {
					Roles []workflowRole `json:"roles"`
				}
				if err := client.doJSON(http.MethodGet, orgPath(ctx.String("org"), "workflow-roles")+"?"+query.Encode(), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response.Roles, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "ASSIGNMENT ID\tWORKFLOW\tROLE\tPRINCIPAL TYPE\tPRINCIPAL")
					for _, role := range response.Roles {
						name := role.PrincipalName
						if name == "" {
							name = role.PrincipalID
						}
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", role.AssignmentID, role.Workflow, role.Role, role.PrincipalType, name)
					}
				})
			},
		},
		{
			Name: "assign", Usage: "Make a group, user, or principal an owner of one workflow",
			Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}, &cli.StringFlag{Name: "project", Required: true},
				&cli.StringFlag{Name: "workflow", Required: true, Usage: "workflow security ID (the YAML id)"},
				&cli.StringFlag{Name: "role", Value: "owner", Usage: "owner (the only workflow role)"},
				&cli.StringFlag{Name: "group", Usage: "group name in the organization"},
				&cli.StringFlag{Name: "user", Usage: "Reactorcide username"},
				&cli.StringFlag{Name: "principal", Usage: "principal ID (for example a service principal)"}),
			Action: func(ctx *cli.Context) error {
				if ctx.String("role") != "owner" {
					return fmt.Errorf("a workflow role is owner")
				}
				set := 0
				for _, name := range []string{"group", "user", "principal"} {
					if strings.TrimSpace(ctx.String(name)) != "" {
						set++
					}
				}
				if set != 1 {
					return fmt.Errorf("give exactly one of --group, --user, --principal")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				body := map[string]string{"project": ctx.String("project"), "workflow": ctx.String("workflow"),
					"group": ctx.String("group"), "username": ctx.String("user"), "principal_id": ctx.String("principal")}
				var response workflowRole
				if err := client.doJSON(http.MethodPost, orgPath(ctx.String("org"), "workflow-roles"), body, http.StatusCreated, &response); err != nil {
					return err
				}
				fmt.Printf("Workflow role %s granted on %s (assignment %s)\n", response.Role, response.Workflow, response.AssignmentID)
				return nil
			},
		},
		{
			Name: "revoke", ArgsUsage: "<assignment-id>", Usage: "Remove a workflow role",
			Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "roles revoke --org <org> <assignment-id>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				if err := client.doJSON(http.MethodDelete, orgPath(ctx.String("org"), "workflow-roles", ctx.Args().First()), nil, http.StatusNoContent, nil); err != nil {
					return err
				}
				fmt.Println("Workflow role revoked")
				return nil
			},
		},
		{
			Name: "owners", Usage: "List the trusted owner and contact of every workflow of a project",
			Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}, &cli.StringFlag{Name: "project", Required: true}, formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					Owners []workflowOwner `json:"owners"`
				}
				query := url.Values{"project": {ctx.String("project")}}
				if err := client.doJSON(http.MethodGet, orgPath(ctx.String("org"), "workflow-owners")+"?"+query.Encode(), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response.Owners, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "WORKFLOW\tGROUP\tSUBJECTS\tCHANNEL\tSOURCE")
					for _, owner := range response.Owners {
						channel := ""
						if owner.Contact != nil {
							channel = owner.Contact.Channel
						}
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", owner.Workflow, owner.Group, strings.Join(owner.Subjects, ","), channel, owner.Source)
					}
				})
			},
		},
	},
}
