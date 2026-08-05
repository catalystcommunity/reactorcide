package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

// apiClient is the shared HTTP client every coordinator-facing CLI command
// uses. It carries the bearer token and base URL resolved from flags or the
// REACTORCIDE_API_URL / REACTORCIDE_API_TOKEN environment variables.
type apiClient struct {
	apiURL string
	token  string
	client *http.Client
}

type apiError struct {
	StatusCode int
	Body       string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("API error (%d): %s", e.StatusCode, e.Body)
}

// apiFlags returns the connection flags shared by every command that talks to
// a coordinator. Declare them on both a parent command and its subcommands so
// lineageString resolves them from either position on the command line.
func apiFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "api-url",
			Aliases: []string{"u"},
			Usage:   "Coordinator API URL (e.g., http://localhost:6080)",
			EnvVars: []string{"REACTORCIDE_API_URL"},
		},
		&cli.StringFlag{
			Name:    "token",
			Aliases: []string{"t"},
			Usage:   "API token for authentication",
			EnvVars: []string{"REACTORCIDE_API_TOKEN"},
		},
	}
}

func formatFlag() cli.Flag {
	return &cli.StringFlag{
		Name:    "format",
		Aliases: []string{"f"},
		Value:   "table",
		Usage:   "Output format: table, json, yaml",
	}
}

// lineageString returns the first non-empty value for a flag across the
// command lineage. urfave/cli v2 stops at the innermost command that declares
// a flag, so a value given before the subcommand ("jobs --api-url X list") is
// otherwise shadowed by the subcommand's own empty copy.
func lineageString(ctx *cli.Context, name string) string {
	for _, c := range ctx.Lineage() {
		if v := strings.TrimSpace(c.String(name)); v != "" {
			return v
		}
	}
	return ""
}

func apiURLConfigured(ctx *cli.Context) bool {
	return lineageString(ctx, "api-url") != ""
}

func newAPIClient(ctx *cli.Context) (*apiClient, error) {
	apiURL := lineageString(ctx, "api-url")
	if apiURL == "" {
		return nil, fmt.Errorf("API URL is required (use --api-url or REACTORCIDE_API_URL)")
	}
	token := lineageString(ctx, "token")
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
	return &apiClient{
		apiURL: strings.TrimSuffix(apiURL, "/"),
		token:  token,
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (c *apiClient) doJSON(method, path string, requestBody interface{}, expectedStatus int, responseBody interface{}) error {
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
		return &apiError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if responseBody == nil {
		return nil
	}
	if err := json.Unmarshal(data, responseBody); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}
	return nil
}

// render writes v as JSON or YAML, or calls table for the human-readable form.
func render(format string, v interface{}, table func(w *tabwriter.Writer)) error {
	switch format {
	case "json":
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	case "yaml":
		data, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		fmt.Print(string(data))
	case "table":
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		table(w)
		return w.Flush()
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
	return nil
}

// pagedQuery builds the limit/offset query shared by the list endpoints,
// plus any endpoint-specific filters. Empty filters are omitted so the
// server applies its own defaults.
func pagedQuery(ctx *cli.Context, extra map[string]string) string {
	values := url.Values{}
	if limit := ctx.Int("limit"); limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	if offset := ctx.Int("offset"); offset > 0 {
		values.Set("offset", strconv.Itoa(offset))
	}
	for k, v := range extra {
		if strings.TrimSpace(v) != "" {
			values.Set(k, v)
		}
	}
	if len(values) == 0 {
		return ""
	}
	return "?" + values.Encode()
}

func paginationFlags() []cli.Flag {
	return []cli.Flag{
		&cli.IntFlag{Name: "limit", Usage: "Maximum number of results"},
		&cli.IntFlag{Name: "offset", Usage: "Number of results to skip"},
	}
}

func timeOrDash(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format(time.RFC3339)
}
