package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
)

type groupSummary struct {
	GroupID     string `json:"group_id" yaml:"group_id"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// PriorityShift moves the priority of the members' work (docs/priority.md).
	PriorityShift int `json:"priority_shift" yaml:"priority_shift"`
}

type groupMember struct {
	UserID   string `json:"user_id" yaml:"user_id"`
	Username string `json:"username" yaml:"username"`
	// The fields below come from the principal of the member. A member that
	// a linked VCS team gives can be a principal with no user.
	PrincipalID string   `json:"principal_id,omitempty" yaml:"principal_id,omitempty"`
	Kind        string   `json:"kind,omitempty" yaml:"kind,omitempty"`
	Name        string   `json:"name,omitempty" yaml:"name,omitempty"`
	Source      string   `json:"source,omitempty" yaml:"source,omitempty"`
	Manual      bool     `json:"manual,omitempty" yaml:"manual,omitempty"`
	Teams       []string `json:"teams,omitempty" yaml:"teams,omitempty"`
	VCSLogins   []string `json:"vcs_logins,omitempty" yaml:"vcs_logins,omitempty"`
}

func groupsPath(org string, parts ...string) string {
	path := "/api/v1/organizations/" + url.PathEscape(org) + "/groups"
	for _, part := range parts {
		path += "/" + url.PathEscape(part)
	}
	return path
}

func requireArgs(ctx *cli.Context, count int, usage string) error {
	if ctx.NArg() != count {
		return fmt.Errorf("usage: reactorcide %s", usage)
	}
	return nil
}

// GroupsCommand manages organization groups. A group member with a verified
// VCS identity link gets the approver subject reactorcide_group:<name> on CI
// approvals, from any VCS provider that has a link for that user.
var GroupsCommand = &cli.Command{
	Name: "groups", Usage: "Manage organization groups (approver subject reactorcide_group:<name>)",
	Subcommands: []*cli.Command{
		{
			Name: "create", ArgsUsage: "<name>", Usage: "Create a group",
			Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}, &cli.StringFlag{Name: "description"}),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "groups create --org <org> <name>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response groupSummary
				body := map[string]string{"name": ctx.Args().First(), "description": ctx.String("description")}
				if err := client.doJSON(http.MethodPost, groupsPath(ctx.String("org")), body, http.StatusCreated, &response); err != nil {
					return err
				}
				fmt.Printf("Group created: %s (approver subject reactorcide_group:%s)\n", response.Name, response.Name)
				return nil
			},
		},
		{
			Name: "list", Usage: "List the groups of an organization",
			Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}, formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					Groups []groupSummary `json:"groups"`
				}
				if err := client.doJSON(http.MethodGet, groupsPath(ctx.String("org")), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response.Groups, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "NAME\tGROUP ID\tPRIORITY SHIFT\tDESCRIPTION")
					for _, group := range response.Groups {
						fmt.Fprintf(w, "%s\t%s\t%+d\t%s\n", group.Name, group.GroupID, group.PriorityShift, group.Description)
					}
				})
			},
		},
		{
			Name: "members", ArgsUsage: "<group>", Usage: "List the members of a group",
			Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}, formatFlag()),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "groups members --org <org> <group>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					Members []groupMember `json:"members"`
				}
				if err := client.doJSON(http.MethodGet, groupsPath(ctx.String("org"), ctx.Args().First(), "members"), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response.Members, renderGroupMembers(response.Members))
			},
		},
		{
			Name: "add-member", ArgsUsage: "<group> <username>", Usage: "Add a Reactorcide user to a group",
			Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 2, "groups add-member --org <org> <group> <username>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				body := map[string]string{"username": ctx.Args().Get(1)}
				var response groupMember
				if err := client.doJSON(http.MethodPost, groupsPath(ctx.String("org"), ctx.Args().First(), "members"), body, http.StatusCreated, &response); err != nil {
					return err
				}
				fmt.Printf("Added %s to group %s\n", response.Username, ctx.Args().First())
				return nil
			},
		},
		{
			Name: "remove-member", ArgsUsage: "<group> <username>", Usage: "Remove a user from a group",
			Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 2, "groups remove-member --org <org> <group> <username>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				if err := client.doJSON(http.MethodDelete, groupsPath(ctx.String("org"), ctx.Args().First(), "members", ctx.Args().Get(1)), nil, http.StatusNoContent, nil); err != nil {
					return err
				}
				fmt.Printf("Removed %s from group %s\n", ctx.Args().Get(1), ctx.Args().First())
				return nil
			},
		},
	},
}

type vcsIdentityLink struct {
	LinkID          string `json:"link_id" yaml:"link_id"`
	Provider        string `json:"provider" yaml:"provider"`
	ExternalSubject string `json:"external_subject" yaml:"external_subject"`
	UserID          string `json:"user_id" yaml:"user_id"`
	VerifiedBy      string `json:"verified_by" yaml:"verified_by"`
}

// IdentitiesCommand manages verified VCS identity links. A link maps a VCS
// login to a Reactorcide user, so that user's groups and project ownership
// apply to approvals and actor facts from that provider.
var IdentitiesCommand = &cli.Command{
	Name: "identities", Usage: "Manage verified VCS identity links",
	Subcommands: []*cli.Command{
		{
			Name: "link", Usage: "Link a VCS login to a Reactorcide user (verified by an admin)",
			Flags: append(apiFlags(),
				&cli.StringFlag{Name: "org", Required: true, Usage: "organization whose admin verifies the link"},
				&cli.StringFlag{Name: "provider", Required: true, Usage: "VCS provider, for example github"},
				&cli.StringFlag{Name: "login", Required: true, Usage: "VCS login"},
				&cli.StringFlag{Name: "username", Required: true, Usage: "Reactorcide username"}),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				body := map[string]string{"organization": ctx.String("org"), "provider": ctx.String("provider"),
					"external_subject": ctx.String("login"), "username": ctx.String("username")}
				var response vcsIdentityLink
				if err := client.doJSON(http.MethodPost, "/api/v1/vcs-identities", body, http.StatusCreated, &response); err != nil {
					return err
				}
				fmt.Printf("Linked %s/%s to %s: %s\n", response.Provider, response.ExternalSubject, ctx.String("username"), response.LinkID)
				return nil
			},
		},
		{
			Name: "list", Usage: "List VCS identity links (needs an all-organizations token)",
			Flags: append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					Links []vcsIdentityLink `json:"links"`
				}
				if err := client.doJSON(http.MethodGet, "/api/v1/vcs-identities", nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response.Links, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "LINK ID\tPROVIDER\tLOGIN\tUSER ID\tVERIFIED BY")
					for _, link := range response.Links {
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", link.LinkID, link.Provider, link.ExternalSubject, link.UserID, link.VerifiedBy)
					}
				})
			},
		},
		{
			Name: "unlink", ArgsUsage: "<link-id>", Usage: "Remove a VCS identity link (needs an all-organizations token)",
			Flags: apiFlags(),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "identities unlink <link-id>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				if err := client.doJSON(http.MethodDelete, "/api/v1/vcs-identities/"+url.PathEscape(ctx.Args().First()), nil, http.StatusNoContent, nil); err != nil {
					return err
				}
				fmt.Printf("Removed identity link %s\n", ctx.Args().First())
				return nil
			},
		},
	},
}
