package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

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
	Usage: "Manage secrets locally or through a Reactorcide coordinator API",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "api-url",
			Aliases: []string{"u"},
			Usage:   "Coordinator API URL. When set, secrets commands use the API instead of local storage",
			EnvVars: []string{"REACTORCIDE_API_URL"},
		},
		&cli.StringFlag{
			Name:    "token",
			Aliases: []string{"t"},
			Usage:   "API token for authentication when using --api-url",
			EnvVars: []string{"REACTORCIDE_API_TOKEN"},
		},
	},
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
				if secretsAPIEnabled(ctx) {
					client, err := newSecretsAPIClient(ctx)
					if err != nil {
						return err
					}
					if err := client.Init(); err != nil {
						return err
					}
					fmt.Println("Secrets storage initialized")
					return nil
				}

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

				var value string
				if ctx.Bool("stdin") {
					content, err := io.ReadAll(os.Stdin)
					if err != nil {
						return fmt.Errorf("failed to read secret value from stdin: %w", err)
					}
					value = string(content)
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

				if secretsAPIEnabled(ctx) {
					client, err := newSecretsAPIClient(ctx)
					if err != nil {
						return err
					}
					if err := client.Set(path, key, value); err != nil {
						return err
					}
					fmt.Printf("Secret set: %s:%s\n", path, key)
					return nil
				}

				storage := secrets.NewStorage()

				pw, err := getPassword("Secrets password: ")
				if err != nil {
					return err
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

				value, err := getSecretValue(ctx, path, key)
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

				deleted, err := deleteSecretValue(ctx, path, key)
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

				keys, err := listSecretKeys(ctx, path)
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

				results, err := getMultiSecrets(ctx, refs)
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
				paths, err := listSecretPaths(ctx)
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

type secretsAPIClient struct {
	apiURL string
	token  string
	client *http.Client
}

type secretValueAPIResponse struct {
	Value string `json:"value"`
}

type listKeysAPIResponse struct {
	Keys []string `json:"keys"`
}

type listPathsAPIResponse struct {
	Paths []string `json:"paths"`
}

type batchGetAPIResponse struct {
	Secrets map[string]string `json:"secrets"`
}

type secretsAPIError struct {
	StatusCode int
	Body       string
}

func (e *secretsAPIError) Error() string {
	return fmt.Sprintf("API error (%d): %s", e.StatusCode, e.Body)
}

func secretsAPIEnabled(ctx *cli.Context) bool {
	return strings.TrimSpace(ctx.String("api-url")) != ""
}

func newSecretsAPIClient(ctx *cli.Context) (*secretsAPIClient, error) {
	apiURL := strings.TrimSpace(ctx.String("api-url"))
	if apiURL == "" {
		return nil, fmt.Errorf("API URL is required (use --api-url or REACTORCIDE_API_URL)")
	}
	token := strings.TrimSpace(ctx.String("token"))
	var err error
	if token == "" {
		token, err = promptForSecret("REACTORCIDE_API_TOKEN", "API token: ")
		if err != nil {
			return nil, err
		}
	}
	if token == "" {
		return nil, fmt.Errorf("API token is required (use --token or REACTORCIDE_API_TOKEN)")
	}
	return &secretsAPIClient{
		apiURL: strings.TrimSuffix(apiURL, "/"),
		token:  token,
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func getSecretValue(ctx *cli.Context, path, key string) (string, error) {
	if secretsAPIEnabled(ctx) {
		client, err := newSecretsAPIClient(ctx)
		if err != nil {
			return "", err
		}
		return client.Get(path, key)
	}

	storage := secrets.NewStorage()
	pw, err := getPassword("Secrets password: ")
	if err != nil {
		return "", err
	}
	return storage.Get(path, key, pw)
}

func deleteSecretValue(ctx *cli.Context, path, key string) (bool, error) {
	if secretsAPIEnabled(ctx) {
		client, err := newSecretsAPIClient(ctx)
		if err != nil {
			return false, err
		}
		return client.Delete(path, key)
	}

	storage := secrets.NewStorage()
	pw, err := getPassword("Secrets password: ")
	if err != nil {
		return false, err
	}
	return storage.Delete(path, key, pw)
}

func listSecretKeys(ctx *cli.Context, path string) ([]string, error) {
	if secretsAPIEnabled(ctx) {
		client, err := newSecretsAPIClient(ctx)
		if err != nil {
			return nil, err
		}
		return client.ListKeys(path)
	}

	storage := secrets.NewStorage()
	pw, err := getPassword("Secrets password: ")
	if err != nil {
		return nil, err
	}
	return storage.ListKeys(path, pw)
}

func listSecretPaths(ctx *cli.Context) ([]string, error) {
	if secretsAPIEnabled(ctx) {
		client, err := newSecretsAPIClient(ctx)
		if err != nil {
			return nil, err
		}
		return client.ListPaths()
	}

	storage := secrets.NewStorage()
	pw, err := getPassword("Secrets password: ")
	if err != nil {
		return nil, err
	}
	return storage.ListPaths(pw)
}

func getMultiSecrets(ctx *cli.Context, refs []secrets.SecretRef) (map[string]string, error) {
	if secretsAPIEnabled(ctx) {
		client, err := newSecretsAPIClient(ctx)
		if err != nil {
			return nil, err
		}
		return client.GetMulti(refs)
	}

	storage := secrets.NewStorage()
	pw, err := getPassword("Secrets password: ")
	if err != nil {
		return nil, err
	}
	return storage.GetMulti(refs, pw)
}

func (c *secretsAPIClient) Init() error {
	return c.doJSON(http.MethodPost, "/api/v1/secrets/init", nil, http.StatusCreated, nil)
}

func (c *secretsAPIClient) Set(path, key, value string) error {
	body := map[string]string{"value": value}
	return c.doJSON(http.MethodPut, "/api/v1/secrets/value?"+secretQuery(path, key), body, http.StatusOK, nil)
}

func (c *secretsAPIClient) Get(path, key string) (string, error) {
	var response secretValueAPIResponse
	if err := c.doJSON(http.MethodGet, "/api/v1/secrets/value?"+secretQuery(path, key), nil, http.StatusOK, &response); err != nil {
		return "", err
	}
	return response.Value, nil
}

func (c *secretsAPIClient) Delete(path, key string) (bool, error) {
	err := c.doJSON(http.MethodDelete, "/api/v1/secrets/value?"+secretQuery(path, key), nil, http.StatusOK, nil)
	if err != nil {
		var apiErr *secretsAPIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (c *secretsAPIClient) ListKeys(path string) ([]string, error) {
	values := url.Values{}
	values.Set("path", path)
	var response listKeysAPIResponse
	if err := c.doJSON(http.MethodGet, "/api/v1/secrets?"+values.Encode(), nil, http.StatusOK, &response); err != nil {
		return nil, err
	}
	return response.Keys, nil
}

func (c *secretsAPIClient) ListPaths() ([]string, error) {
	var response listPathsAPIResponse
	if err := c.doJSON(http.MethodGet, "/api/v1/secrets/paths", nil, http.StatusOK, &response); err != nil {
		return nil, err
	}
	return response.Paths, nil
}

func (c *secretsAPIClient) GetMulti(refs []secrets.SecretRef) (map[string]string, error) {
	body := map[string][]secrets.SecretRef{"refs": refs}
	var response batchGetAPIResponse
	if err := c.doJSON(http.MethodPost, "/api/v1/secrets/batch/get", body, http.StatusOK, &response); err != nil {
		return nil, err
	}

	results := make(map[string]string, len(refs))
	for _, ref := range refs {
		if value, ok := response.Secrets[ref.Key]; ok {
			results[ref.Key] = value
			continue
		}
		results[ref.Key] = response.Secrets[fmt.Sprintf("%s:%s", ref.Path, ref.Key)]
	}
	return results, nil
}

func (c *secretsAPIClient) doJSON(method, path string, requestBody interface{}, expectedStatus int, responseBody interface{}) error {
	var body io.Reader
	if requestBody != nil {
		data, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("failed to marshal request: %w", err)
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.apiURL+path, body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != expectedStatus {
		return &secretsAPIError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if responseBody == nil {
		return nil
	}
	if err := json.Unmarshal(data, responseBody); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}
	return nil
}

func secretQuery(path, key string) string {
	values := url.Values{}
	values.Set("path", path)
	values.Set("key", key)
	return values.Encode()
}
