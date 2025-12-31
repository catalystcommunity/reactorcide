package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/urfave/cli/v2"
	"golang.org/x/term"
)

// getPassword gets the secrets password from REACTORCIDE_SECRETS_PASSWORD env var or prompts.
// This is a convenience wrapper around promptForSecret for the secrets password specifically.
func getPassword(prompt string) (string, error) {
	return promptForSecret("REACTORCIDE_SECRETS_PASSWORD", prompt)
}

// getPasswordConfirm gets and confirms a password.
func getPasswordConfirm() (string, error) {
	pw, err := getPassword("Secrets password: ")
	if err != nil {
		return "", err
	}

	confirm, err := getPassword("Confirm password: ")
	if err != nil {
		return "", err
	}

	if pw != confirm {
		return "", fmt.Errorf("passwords do not match")
	}

	return pw, nil
}

var SecretsCommand = &cli.Command{
	Name:  "secrets",
	Usage: "Manage local encrypted secrets storage",
	Subcommands: []*cli.Command{
		{
			Name:  "init",
			Usage: "Initialize secrets storage",
			Flags: []cli.Flag{
				&cli.BoolFlag{
					Name:  "force",
					Usage: "Reinitialize even if already exists",
				},
			},
			Action: func(ctx *cli.Context) error {
				storage := secrets.NewStorage()

				pw, err := getPasswordConfirm()
				if err != nil {
					return err
				}

				if err := storage.Init(pw, ctx.Bool("force")); err != nil {
					return err
				}

				fmt.Println("Secrets storage initialized")
				return nil
			},
		},
		{
			Name:      "set",
			Usage:     "Set a secret value",
			ArgsUsage: "<path> <key>",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:    "value",
					Aliases: []string{"v"},
					Usage:   "Secret value (prompts if not provided)",
				},
				&cli.BoolFlag{
					Name:  "stdin",
					Usage: "Read value from stdin",
				},
			},
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() < 2 {
					return fmt.Errorf("usage: reactorcide secrets set <path> <key>")
				}

				path := ctx.Args().Get(0)
				key := ctx.Args().Get(1)

				storage := secrets.NewStorage()

				pw, err := getPassword("Secrets password: ")
				if err != nil {
					return err
				}

				var value string
				if ctx.Bool("stdin") {
					// Read from stdin
					reader := bufio.NewReader(os.Stdin)
					content, err := reader.ReadString('\n')
					if err != nil && err.Error() != "EOF" {
						// Try reading all if no newline
						allContent, _ := os.ReadFile("/dev/stdin")
						content = string(allContent)
					}
					value = strings.TrimSpace(content)
				} else if ctx.String("value") != "" {
					value = ctx.String("value")
				} else {
					// Prompt for value
					fmt.Fprint(os.Stderr, "Secret value: ")
					if term.IsTerminal(int(os.Stdin.Fd())) {
						valueBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
						fmt.Fprintln(os.Stderr)
						if err != nil {
							return fmt.Errorf("failed to read secret value: %w", err)
						}
						value = string(valueBytes)
					} else {
						reader := bufio.NewReader(os.Stdin)
						v, err := reader.ReadString('\n')
						if err != nil {
							return fmt.Errorf("failed to read secret value: %w", err)
						}
						value = strings.TrimSpace(v)
					}
				}

				if err := storage.Set(path, key, value, pw); err != nil {
					return err
				}

				fmt.Printf("Secret set: %s:%s\n", path, key)
				return nil
			},
		},
		{
			Name:      "get",
			Usage:     "Get a secret value (outputs only the value for scripting)",
			ArgsUsage: "<path> <key>",
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() < 2 {
					return fmt.Errorf("usage: reactorcide secrets get <path> <key>")
				}

				path := ctx.Args().Get(0)
				key := ctx.Args().Get(1)

				storage := secrets.NewStorage()

				pw, err := getPassword("Secrets password: ")
				if err != nil {
					return err
				}

				value, err := storage.Get(path, key, pw)
				if err != nil {
					return err
				}

				if value == "" {
					return fmt.Errorf("secret not found: %s:%s", path, key)
				}

				// Output value without newline for clean piping
				fmt.Print(value)
				return nil
			},
		},
		{
			Name:      "delete",
			Usage:     "Delete a secret",
			ArgsUsage: "<path> <key>",
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() < 2 {
					return fmt.Errorf("usage: reactorcide secrets delete <path> <key>")
				}

				path := ctx.Args().Get(0)
				key := ctx.Args().Get(1)

				storage := secrets.NewStorage()

				pw, err := getPassword("Secrets password: ")
				if err != nil {
					return err
				}

				deleted, err := storage.Delete(path, key, pw)
				if err != nil {
					return err
				}

				if deleted {
					fmt.Printf("Secret deleted: %s:%s\n", path, key)
				} else {
					return fmt.Errorf("secret not found: %s:%s", path, key)
				}
				return nil
			},
		},
		{
			Name:      "list",
			Usage:     "List all keys in a path (values NOT shown)",
			ArgsUsage: "<path>",
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() < 1 {
					return fmt.Errorf("usage: reactorcide secrets list <path>")
				}

				path := ctx.Args().Get(0)

				storage := secrets.NewStorage()

				pw, err := getPassword("Secrets password: ")
				if err != nil {
					return err
				}

				keys, err := storage.ListKeys(path, pw)
				if err != nil {
					return err
				}

				sort.Strings(keys)
				for _, k := range keys {
					fmt.Println(k)
				}
				return nil
			},
		},
		{
			Name:      "get-multi",
			Usage:     "Get multiple secrets at once (key derivation happens once)",
			ArgsUsage: "<path:key> [path:key...]",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:    "format",
					Aliases: []string{"f"},
					Usage:   "Output format: env, json, or lines (default: env)",
					Value:   "env",
				},
			},
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() < 1 {
					return fmt.Errorf("usage: reactorcide secrets get-multi <path:key> [path:key...]")
				}

				storage := secrets.NewStorage()

				pw, err := getPassword("Secrets password: ")
				if err != nil {
					return err
				}

				// Parse all path:key pairs into SecretRefs
				refs := make([]secrets.SecretRef, 0, ctx.NArg())
				for i := 0; i < ctx.NArg(); i++ {
					arg := ctx.Args().Get(i)
					parts := strings.SplitN(arg, ":", 2)
					if len(parts) != 2 {
						return fmt.Errorf("invalid format %q, expected path:key", arg)
					}
					refs = append(refs, secrets.SecretRef{Path: parts[0], Key: parts[1]})
				}

				// Get all secrets with single key derivation
				results, err := storage.GetMulti(refs, pw)
				if err != nil {
					return err
				}

				// Check for missing secrets
				for _, ref := range refs {
					if results[ref.Key] == "" {
						return fmt.Errorf("secret not found: %s:%s", ref.Path, ref.Key)
					}
				}

				// Output in requested format
				format := ctx.String("format")
				switch format {
				case "env":
					for _, ref := range refs {
						fmt.Printf("%s=%s\n", ref.Key, results[ref.Key])
					}
				case "json":
					jsonBytes, _ := json.MarshalIndent(results, "", "  ")
					fmt.Println(string(jsonBytes))
				case "lines":
					for _, ref := range refs {
						fmt.Println(results[ref.Key])
					}
				default:
					return fmt.Errorf("unknown format: %s", format)
				}

				return nil
			},
		},
		{
			Name:  "list-paths",
			Usage: "List all paths that have secrets",
			Action: func(ctx *cli.Context) error {
				storage := secrets.NewStorage()

				pw, err := getPassword("Secrets password: ")
				if err != nil {
					return err
				}

				paths, err := storage.ListPaths(pw)
				if err != nil {
					return err
				}

				sort.Strings(paths)
				for _, p := range paths {
					fmt.Println(p)
				}
				return nil
			},
		},
	},
}
