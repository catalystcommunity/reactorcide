package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
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
			Usage: "List the API tokens that you can manage, newest first, one page at a time",
			Flags: append(apiFlags(), formatFlag(),
				&cli.IntFlag{Name: "limit", Value: 100, Usage: "Number of tokens on a page (1 to 1000)"},
				&cli.IntFlag{Name: "offset", Usage: "Number of tokens to skip"},
				&cli.BoolFlag{Name: "all", Usage: "Get every page (ignores --offset)"},
				&cli.BoolFlag{Name: "exclude-expired", Usage: "Do not list tokens whose expiry is in the past (expired tokens are listed by default)"},
			),
			Action: func(ctx *cli.Context) error {
				limit := ctx.Int("limit")
				if limit < 1 || limit > 1000 {
					return fmt.Errorf("--limit must be 1 to 1000")
				}
				offset := ctx.Int("offset")
				if offset < 0 {
					return fmt.Errorf("--offset must be 0 or more")
				}
				if ctx.Bool("all") {
					offset = 0
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var tokens []tokenSummary
				total := 0
				for {
					query := url.Values{}
					query.Set("limit", strconv.Itoa(limit))
					query.Set("offset", strconv.Itoa(offset))
					if ctx.Bool("exclude-expired") {
						query.Set("exclude_expired", "true")
					}
					var resp listTokensResponse
					if err := client.doJSON(http.MethodGet, "/api/v1/tokens?"+query.Encode(), nil, http.StatusOK, &resp); err != nil {
						return err
					}
					tokens = append(tokens, resp.Tokens...)
					total = resp.Total
					offset += len(resp.Tokens)
					if !ctx.Bool("all") || len(resp.Tokens) == 0 || offset >= resp.Total {
						break
					}
				}
				first := offset - len(tokens)
				err = render(ctx.String("format"), tokens, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "TOKEN ID\tNAME\tACTIVE\tCREATED\tEXPIRES\tLAST USED")
					for _, t := range tokens {
						fmt.Fprintf(w, "%s\t%s\t%t\t%s\t%s\t%s\n",
							t.TokenID, t.Name, t.IsActive,
							t.CreatedAt.Format(time.RFC3339),
							timeOrDash(t.ExpiresAt), timeOrDash(t.LastUsedAt))
					}
				})
				if err == nil && ctx.String("format") == "table" {
					fmt.Fprintln(os.Stderr, tokenPageNote(first, len(tokens), total))
				}
				return err
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
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

// tokenPageNote describes the listed rows, for example
// "Tokens 101-200 of 3456. Next page: --offset 200".
func tokenPageNote(first, count, total int) string {
	if count == 0 {
		return fmt.Sprintf("No tokens on this page (%d in total).", total)
	}
	note := fmt.Sprintf("Tokens %d-%d of %d.", first+1, first+count, total)
	if first+count < total {
		note += fmt.Sprintf(" Next page: --offset %d", first+count)
	}
	return note
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
