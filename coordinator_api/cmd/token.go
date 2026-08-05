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
					Name:    "user-id",
					Aliases: []string{"u"},
					Usage:   "User ID to associate with the token (defaults to REACTORCIDE_DEFAULT_USER_ID)",
					EnvVars: []string{"REACTORCIDE_DEFAULT_USER_ID"},
				},
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

				if err := store.AppStore.EnsureDefaultUser(); err != nil {
					return fmt.Errorf("failed to ensure default user: %w", err)
				}

				tokenName := ctx.String("name")
				userID := ctx.String("user-id")

				if userID == "" {
					return fmt.Errorf("user-id is required (set REACTORCIDE_DEFAULT_USER_ID or use --user-id)")
				}

				tokenBytes := make([]byte, 32)
				if _, err := rand.Read(tokenBytes); err != nil {
					return fmt.Errorf("failed to generate token: %w", err)
				}
				tokenString := hex.EncodeToString(tokenBytes)

				tokenHash := checkauth.HashAPIToken(tokenString)

				apiToken := &models.APIToken{
					UserID:    userID,
					TokenHash: tokenHash,
					Name:      tokenName,
					IsActive:  true,
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
	TokenID    string     `json:"token_id" yaml:"token_id"`
	Name       string     `json:"name" yaml:"name"`
	IsActive   bool       `json:"is_active" yaml:"is_active"`
	CreatedAt  time.Time  `json:"created_at" yaml:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty" yaml:"last_used_at,omitempty"`
}
