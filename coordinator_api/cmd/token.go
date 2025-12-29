package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

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
				// Initialize store
				store.AppStore = postgres_store.PostgresStore
				if _, err := store.AppStore.Initialize(); err != nil {
					return fmt.Errorf("failed to initialize database: %w", err)
				}

				// Ensure default user exists
				if err := store.AppStore.EnsureDefaultUser(); err != nil {
					return fmt.Errorf("failed to ensure default user: %w", err)
				}

				tokenName := ctx.String("name")
				userID := ctx.String("user-id")

				if userID == "" {
					return fmt.Errorf("user-id is required (set REACTORCIDE_DEFAULT_USER_ID or use --user-id)")
				}

				// Generate token
				tokenBytes := make([]byte, 32)
				if _, err := rand.Read(tokenBytes); err != nil {
					return fmt.Errorf("failed to generate token: %w", err)
				}
				tokenString := hex.EncodeToString(tokenBytes)

				// Hash for storage
				tokenHash := checkauth.HashAPIToken(tokenString)

				// Create token
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
	},
}
