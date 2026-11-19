package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
)

// Groups linked to VCS teams (docs/principals.md, plan item B3).

type groupTeamLink struct {
	LinkID        string `json:"link_id" yaml:"link_id"`
	Provider      string `json:"provider" yaml:"provider"`
	Team          string `json:"team" yaml:"team"`
	Subject       string `json:"subject" yaml:"subject"`
	LastSyncedAt  string `json:"last_synced_at,omitempty" yaml:"last_synced_at,omitempty"`
	LastAttemptAt string `json:"last_attempt_at,omitempty" yaml:"last_attempt_at,omitempty"`
	NextSyncAt    string `json:"next_sync_at,omitempty" yaml:"next_sync_at,omitempty"`
	LastError     string `json:"last_error,omitempty" yaml:"last_error,omitempty"`
	FailureCount  int    `json:"failure_count" yaml:"failure_count"`
	MemberCount   int    `json:"member_count" yaml:"member_count"`
}

type groupTeamSyncResult struct {
	LinkID  string   `json:"link_id" yaml:"link_id"`
	Team    string   `json:"team" yaml:"team"`
	Members int      `json:"members" yaml:"members"`
	Added   []string `json:"added" yaml:"added"`
	Removed []string `json:"removed" yaml:"removed"`
	Error   string   `json:"error,omitempty" yaml:"error,omitempty"`
}

type groupMembershipChange struct {
	PrincipalID string `json:"principal_id" yaml:"principal_id"`
	GroupName   string `json:"group_name" yaml:"group_name"`
	Change      string `json:"change" yaml:"change"`
	Source      string `json:"source" yaml:"source"`
	ChangedAt   string `json:"changed_at" yaml:"changed_at"`
}

func renderGroupMembers(members []groupMember) func(w *tabwriter.Writer) {
	return func(w *tabwriter.Writer) {
		fmt.Fprintln(w, "MEMBER\tSOURCE\tTEAMS\tPRINCIPAL ID\tUSER ID")
		for _, member := range members {
			name := member.Username
			if name == "" {
				name = member.Name
			}
			source := member.Source
			if source == "" {
				source = "manual"
			}
			if member.Manual && len(member.Teams) > 0 {
				source = "manual+vcs_sync"
			}
			teams := strings.Join(member.Teams, ",")
			if teams == "" {
				teams = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, source, teams, member.PrincipalID, member.UserID)
		}
	}
}

func init() {
	GroupsCommand.Subcommands = append(GroupsCommand.Subcommands, groupTeamCommands...)
}

var groupTeamCommands = []*cli.Command{
	{
		Name: "link-team", ArgsUsage: "<group> <owner/slug>",
		Usage: "Link a group to a VCS team. The team members become group members after a sync.",
		Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true},
			&cli.StringFlag{Name: "provider", Value: "github", Usage: "VCS provider (only github)"}),
		Action: func(ctx *cli.Context) error {
			if err := requireArgs(ctx, 2, "groups link-team --org <org> <group> <owner/slug>"); err != nil {
				return err
			}
			client, err := newAPIClient(ctx)
			if err != nil {
				return err
			}
			body := map[string]string{"provider": ctx.String("provider"), "team": ctx.Args().Get(1)}
			var response groupTeamLink
			if err := client.doJSON(http.MethodPost, groupsPath(ctx.String("org"), ctx.Args().First(), "teams"), body, http.StatusCreated, &response); err != nil {
				return err
			}
			fmt.Printf("Linked group %s to %s (%s). The next sync adds the team members.\n", ctx.Args().First(), response.Subject, response.LinkID)
			return nil
		},
	},
	{
		Name: "unlink-team", ArgsUsage: "<group> <owner/slug | link-id>",
		Usage: "Remove a team link. The members that only this team gives leave the group.",
		Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}),
		Action: func(ctx *cli.Context) error {
			if err := requireArgs(ctx, 2, "groups unlink-team --org <org> <group> <owner/slug | link-id>"); err != nil {
				return err
			}
			client, err := newAPIClient(ctx)
			if err != nil {
				return err
			}
			path := groupsPath(ctx.String("org"), ctx.Args().First(), "teams")
			for _, part := range strings.Split(strings.TrimPrefix(ctx.Args().Get(1), "vcs_team:"), "/") {
				path += "/" + url.PathEscape(part)
			}
			if err := client.doJSON(http.MethodDelete, path, nil, http.StatusNoContent, nil); err != nil {
				return err
			}
			fmt.Printf("Removed team link %s from group %s\n", ctx.Args().Get(1), ctx.Args().First())
			return nil
		},
	},
	{
		Name: "teams", ArgsUsage: "<group>", Usage: "List the linked teams of a group with their sync status",
		Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}, formatFlag()),
		Action: func(ctx *cli.Context) error {
			if err := requireArgs(ctx, 1, "groups teams --org <org> <group>"); err != nil {
				return err
			}
			client, err := newAPIClient(ctx)
			if err != nil {
				return err
			}
			var response struct {
				Teams []groupTeamLink `json:"teams"`
			}
			if err := client.doJSON(http.MethodGet, groupsPath(ctx.String("org"), ctx.Args().First(), "teams"), nil, http.StatusOK, &response); err != nil {
				return err
			}
			return render(ctx.String("format"), response.Teams, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "TEAM\tMEMBERS\tLAST SYNC\tNEXT SYNC\tERROR\tLINK ID")
				for _, team := range response.Teams {
					fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n", team.Subject, team.MemberCount, groupDash(team.LastSyncedAt),
						groupDash(team.NextSyncAt), groupDash(team.LastError), team.LinkID)
				}
			})
		},
	},
	{
		Name: "sync", ArgsUsage: "<group>", Usage: "Sync the linked teams of a group now",
		Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}, formatFlag()),
		Action: func(ctx *cli.Context) error {
			if err := requireArgs(ctx, 1, "groups sync --org <org> <group>"); err != nil {
				return err
			}
			client, err := newAPIClient(ctx)
			if err != nil {
				return err
			}
			var response struct {
				Results   []groupTeamSyncResult `json:"results"`
				Requested *int                  `json:"requested"`
			}
			if err := client.doJSON(http.MethodPost, groupsPath(ctx.String("org"), ctx.Args().First(), "sync"), nil, http.StatusOK, &response); err != nil {
				return err
			}
			if response.Requested != nil {
				fmt.Printf("Requested a sync of %d linked teams. The sync loop runs it soon.\n", *response.Requested)
				return nil
			}
			return render(ctx.String("format"), response.Results, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "TEAM\tMEMBERS\tADDED\tREMOVED\tERROR")
				for _, result := range response.Results {
					fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\n", result.Team, result.Members, len(result.Added), len(result.Removed), groupDash(result.Error))
				}
			})
		},
	},
	{
		Name: "history", ArgsUsage: "<group>", Usage: "Show the membership changes of a group, newest first",
		Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true},
			&cli.StringFlag{Name: "principal", Usage: "only this principal UUID"},
			&cli.StringFlag{Name: "since", Usage: "RFC 3339 time"},
			&cli.IntFlag{Name: "limit", Value: 100}, formatFlag()),
		Action: func(ctx *cli.Context) error {
			if err := requireArgs(ctx, 1, "groups history --org <org> <group>"); err != nil {
				return err
			}
			client, err := newAPIClient(ctx)
			if err != nil {
				return err
			}
			query := url.Values{}
			if value := ctx.String("principal"); value != "" {
				query.Set("principal", value)
			}
			if value := ctx.String("since"); value != "" {
				query.Set("since", value)
			}
			query.Set("limit", fmt.Sprint(ctx.Int("limit")))
			var response struct {
				History []groupMembershipChange `json:"history"`
			}
			path := groupsPath(ctx.String("org"), ctx.Args().First(), "history") + "?" + query.Encode()
			if err := client.doJSON(http.MethodGet, path, nil, http.StatusOK, &response); err != nil {
				return err
			}
			return render(ctx.String("format"), response.History, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "TIME\tCHANGE\tSOURCE\tPRINCIPAL ID")
				for _, change := range response.History {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", change.ChangedAt, change.Change, change.Source, change.PrincipalID)
				}
			})
		},
	},
}

func groupDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
