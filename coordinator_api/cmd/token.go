package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"text/tabwriter"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/urfave/cli/v2"
)

var TokenCommand = &cli.Command{
	Name:  "token",
	Usage: "Manage API tokens",
	Subcommands: []*cli.Command{
		{
			Name:  "create",
			Usage: "Create a new API token",
			Description: "Writes straight to the database, so it works before any token exists. " +
				"It needs database access; the other token subcommands use the API.",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:     "name",
					Aliases:  []string{"n"},
					Usage:    "Name for the token",
					Required: true,
				},
				&cli.StringFlag{
					Name:  "as-user",
					Usage: "Delegate the token to this username",
				},
				&cli.StringSliceFlag{Name: "org", Usage: "Limit the token to this organization; repeat for more organizations"},
				&cli.StringSliceFlag{Name: "capability", Usage: "Limit the token to this capability; repeat for more capabilities"},
				&cli.StringFlag{
					Name:        "db-uri",
					Aliases:     []string{"db"},
					Usage:       "Database connection URI",
					Destination: &config.DbUri,
					EnvVars:     []string{"REACTORCIDE_DB_URI", "DB_URI"},
				},
			},
			Action: func(ctx *cli.Context) error {
				store.AppStore = postgres_store.PostgresStore
				if _, err := store.AppStore.Initialize(); err != nil {
					return fmt.Errorf("failed to initialize database: %w", err)
				}

				adminStore, ok := store.AppStore.(interface {
					EnsureDefaultOrganization(context.Context) error
					GetOrganizationByName(context.Context, string) (*models.Organization, error)
					GetUserByUsername(context.Context, string) (*models.User, error)
				})
				if !ok {
					return fmt.Errorf("configured store does not support organization token bootstrap")
				}
				if err := adminStore.EnsureDefaultOrganization(context.Background()); err != nil {
					return fmt.Errorf("failed to ensure default organization: %w", err)
				}

				tokenName := ctx.String("name")
				orgNames := ctx.StringSlice("org")
				capabilityNames := ctx.StringSlice("capability")
				capabilitySet, err := tokencaps.New(capabilityNames...)
				if err != nil {
					return err
				}
				orgIDs := make([]string, 0, len(orgNames))
				for _, name := range orgNames {
					org, err := adminStore.GetOrganizationByName(context.Background(), name)
					if err != nil {
						return fmt.Errorf("organization %q: %w", name, err)
					}
					orgIDs = append(orgIDs, org.OrgID)
				}
				subjectType := "instance_token"
				var ownerOrgID *string
				var userID string
				if username := ctx.String("as-user"); username != "" {
					user, err := adminStore.GetUserByUsername(context.Background(), username)
					if err != nil {
						return fmt.Errorf("user %q: %w", username, err)
					}
					userID = user.UserID
					subjectType = "user_token"
				} else if len(orgIDs) > 0 {
					subjectType = "service_token"
					ownerOrgID = &orgIDs[0]
				}

				tokenBytes := make([]byte, 32)
				if _, err := rand.Read(tokenBytes); err != nil {
					return fmt.Errorf("failed to generate token: %w", err)
				}
				tokenString := hex.EncodeToString(tokenBytes)

				tokenHash := checkauth.HashAPIToken(tokenString)

				apiToken := &models.APIToken{
					UserID: userID, TokenHash: tokenHash, Name: tokenName, IsActive: true,
					SubjectType: subjectType, OwnerOrgID: ownerOrgID,
					AllOrganizations: len(orgIDs) == 0, OrganizationIDs: orgIDs,
					AllCapabilities: len(capabilityNames) == 0, Capabilities: capabilitySet.Slice(),
				}

				if err := store.AppStore.CreateAPIToken(context.Background(), apiToken); err != nil {
					return fmt.Errorf("failed to create token: %w", err)
				}

				fmt.Printf("Token created successfully!\n")
				fmt.Printf("Token ID: %s\n", apiToken.TokenID)
				fmt.Printf("Token: %s\n", tokenString)
				fmt.Printf("\nSave this token - it cannot be retrieved again!\n")

				return nil
			},
		},
		{
			Name:  "list",
			Usage: "List the API tokens of the authenticated user",
			Flags: append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var resp listTokensResponse
				if err := client.doJSON(http.MethodGet, "/api/v1/tokens", nil, http.StatusOK, &resp); err != nil {
					return err
				}
				return render(ctx.String("format"), resp.Tokens, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "TOKEN ID\tNAME\tACTIVE\tCREATED\tEXPIRES\tLAST USED")
					for _, t := range resp.Tokens {
						fmt.Fprintf(w, "%s\t%s\t%t\t%s\t%s\t%s\n",
							t.TokenID, t.Name, t.IsActive,
							t.CreatedAt.Format(time.RFC3339),
							timeOrDash(t.ExpiresAt), timeOrDash(t.LastUsedAt))
					}
				})
			},
		},
		{
			Name:      "delete",
			Usage:     "Delete an API token",
			ArgsUsage: "<token-id>",
			Flags:     apiFlags(),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() < 1 {
					return fmt.Errorf("usage: reactorcide token delete <token-id>")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				tokenID := ctx.Args().Get(0)
				if err := client.doJSON(http.MethodDelete, "/api/v1/tokens/"+url.PathEscape(tokenID), nil, http.StatusNoContent, nil); err != nil {
					return err
				}
				fmt.Printf("Token deleted: %s\n", tokenID)
				return nil
			},
		},
	},
}

type listTokensResponse struct {
	Tokens []tokenSummary `json:"tokens"`
	Total  int            `json:"total"`
}

type tokenSummary struct {
	TokenID       string     `json:"token_id" yaml:"token_id"`
	Name          string     `json:"name" yaml:"name"`
	IsActive      bool       `json:"is_active" yaml:"is_active"`
	CreatedAt     time.Time  `json:"created_at" yaml:"created_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty" yaml:"last_used_at,omitempty"`
	SubjectType   string     `json:"subject_type" yaml:"subject_type"`
	Owner         string     `json:"owner,omitempty" yaml:"owner,omitempty"`
	Organizations []string   `json:"organizations" yaml:"organizations"`
	Capabilities  []string   `json:"capabilities" yaml:"capabilities"`
}
