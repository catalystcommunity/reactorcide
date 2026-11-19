package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/federation"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/transportsecurity"
	"github.com/urfave/cli/v2"
)

type federationIssuer struct {
	IssuerID         string    `json:"issuer_id" yaml:"issuer_id"`
	Name             string    `json:"name" yaml:"name"`
	IssuerURL        string    `json:"issuer_url" yaml:"issuer_url"`
	JWKSURL          string    `json:"jwks_url,omitempty" yaml:"jwks_url,omitempty"`
	Audiences        []string  `json:"audiences" yaml:"audiences"`
	Algorithms       []string  `json:"algorithms" yaml:"algorithms"`
	ClockSkewSeconds int       `json:"clock_skew_seconds" yaml:"clock_skew_seconds"`
	Status           string    `json:"status" yaml:"status"`
	CreatedAt        time.Time `json:"created_at" yaml:"created_at"`
}

type federationMapping struct {
	MappingID       string                       `json:"mapping_id" yaml:"mapping_id"`
	Issuer          string                       `json:"issuer" yaml:"issuer"`
	Name            string                       `json:"name" yaml:"name"`
	PrincipalID     string                       `json:"principal_id" yaml:"principal_id"`
	PrincipalName   string                       `json:"principal_name" yaml:"principal_name"`
	Conditions      []models.FederationCondition `json:"conditions" yaml:"conditions"`
	Capabilities    []string                     `json:"capabilities" yaml:"capabilities"`
	TokenTTLSeconds int                          `json:"token_ttl_seconds" yaml:"token_ttl_seconds"`
	Status          string                       `json:"status" yaml:"status"`
}

func federationPath(org string, parts ...string) string {
	path := "/api/v1/organizations/" + url.PathEscape(org) + "/federation/issuers"
	for _, part := range parts {
		path += "/" + url.PathEscape(part)
	}
	return path
}

func renderIssuer(format string, issuer federationIssuer) error {
	return render(format, issuer, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "issuer_id\t%s\n", issuer.IssuerID)
		fmt.Fprintf(w, "name\t%s\n", issuer.Name)
		fmt.Fprintf(w, "issuer_url\t%s\n", issuer.IssuerURL)
		jwks := issuer.JWKSURL
		if jwks == "" {
			jwks = "(discovery)"
		}
		fmt.Fprintf(w, "jwks_url\t%s\n", jwks)
		fmt.Fprintf(w, "audiences\t%s\n", strings.Join(issuer.Audiences, ","))
		fmt.Fprintf(w, "algorithms\t%s\n", strings.Join(issuer.Algorithms, ","))
		fmt.Fprintf(w, "clock_skew_seconds\t%d\n", issuer.ClockSkewSeconds)
		fmt.Fprintf(w, "status\t%s\n", issuer.Status)
	})
}

func conditionText(condition models.FederationCondition) string {
	if condition.Op == models.FederationOpGlob {
		return condition.Claim + "~" + condition.Value
	}
	return condition.Claim + "=" + condition.Value
}

func renderMapping(format string, mapping federationMapping) error {
	return render(format, mapping, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "mapping_id\t%s\n", mapping.MappingID)
		fmt.Fprintf(w, "issuer\t%s\n", mapping.Issuer)
		fmt.Fprintf(w, "name\t%s\n", mapping.Name)
		fmt.Fprintf(w, "principal\t%s (%s)\n", mapping.PrincipalName, mapping.PrincipalID)
		for _, condition := range mapping.Conditions {
			fmt.Fprintf(w, "condition\t%s\n", conditionText(condition))
		}
		fmt.Fprintf(w, "capabilities\t%s\n", strings.Join(mapping.Capabilities, ","))
		fmt.Fprintf(w, "token_ttl_seconds\t%d\n", mapping.TokenTTLSeconds)
		fmt.Fprintf(w, "status\t%s\n", mapping.Status)
	})
}

func issuerFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "issuer-url", Usage: "the exact \"iss\" value of the tokens (HTTPS)"},
		&cli.StringFlag{Name: "jwks-url", Usage: "the JWKS URL (default: read jwks_uri from the discovery document)"},
		&cli.StringSliceFlag{Name: "audience", Usage: "an allowed audience (repeat for more)"},
		&cli.StringSliceFlag{Name: "algorithm", Usage: "an allowed algorithm: RS256, ES256, EdDSA (default RS256 and ES256)"},
		&cli.IntFlag{Name: "clock-skew", Value: -1, Usage: "allowed clock skew in seconds (0-600, default 60)"},
		&cli.StringFlag{Name: "status", Usage: "active or disabled"},
	}
}

func issuerBody(ctx *cli.Context, name string) map[string]any {
	body := map[string]any{}
	if name != "" {
		body["name"] = name
	}
	if value := ctx.String("issuer-url"); value != "" {
		body["issuer_url"] = value
	}
	if ctx.IsSet("jwks-url") {
		body["jwks_url"] = ctx.String("jwks-url")
	}
	if values := ctx.StringSlice("audience"); len(values) > 0 {
		body["audiences"] = values
	}
	if values := ctx.StringSlice("algorithm"); len(values) > 0 {
		body["algorithms"] = values
	}
	if ctx.Int("clock-skew") >= 0 {
		body["clock_skew_seconds"] = ctx.Int("clock-skew")
	}
	if value := ctx.String("status"); value != "" {
		body["status"] = value
	}
	return body
}

func mappingFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "issuer", Required: true, Usage: "issuer name"},
		&cli.StringFlag{Name: "principal", Usage: "service principal name or UUID"},
		&cli.StringSliceFlag{Name: "condition", Usage: "CLAIM=VALUE (equals) or CLAIM~GLOB (glob); repeat for more; one must be on sub"},
		&cli.StringSliceFlag{Name: "capability", Usage: "a capability of the issued token (repeat for more)"},
		&cli.IntFlag{Name: "ttl", Usage: "issued token lifetime in seconds (60-3600, default 900)"},
		&cli.StringFlag{Name: "status", Usage: "active or disabled"},
	}
}

func mappingBody(ctx *cli.Context, name string) (map[string]any, error) {
	body := map[string]any{}
	if name != "" {
		body["name"] = name
	}
	if value := ctx.String("principal"); value != "" {
		body["principal"] = value
	}
	if values := ctx.StringSlice("condition"); len(values) > 0 {
		conditions := []models.FederationCondition{}
		for _, value := range values {
			condition, err := federation.ParseCondition(value)
			if err != nil {
				return nil, err
			}
			conditions = append(conditions, condition)
		}
		body["conditions"] = conditions
	}
	if values := ctx.StringSlice("capability"); len(values) > 0 {
		body["capabilities"] = values
	}
	if ttl := ctx.Int("ttl"); ttl > 0 {
		body["token_ttl_seconds"] = ttl
	}
	if value := ctx.String("status"); value != "" {
		body["status"] = value
	}
	return body, nil
}

// readSubjectToken reads the external token from a file or stdin ("-"). The
// token never goes on the command line, where other users can see it.
func readSubjectToken(path string) (string, error) {
	var reader io.Reader = os.Stdin
	if path != "" && path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer file.Close()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, 64<<10))
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("the token is empty")
	}
	return token, nil
}

// FederationCommand manages inbound federation: trusted external OIDC
// issuers and claim mappings to service principals (docs/federation.md).
var FederationCommand = &cli.Command{
	Name:  "federation",
	Usage: "Manage inbound federation: trusted OIDC issuers and claim mappings to service principals",
	Subcommands: []*cli.Command{
		{
			Name: "issuers", Usage: "Manage the trusted OIDC issuers of an organization",
			Subcommands: []*cli.Command{
				{
					Name: "list", Usage: "List issuers", Flags: append(apiFlags(), orgFlag(), formatFlag()),
					Action: func(ctx *cli.Context) error {
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						var response struct {
							Issuers []federationIssuer `json:"issuers" yaml:"issuers"`
						}
						if err := client.doJSON(http.MethodGet, federationPath(ctx.String("org")), nil, http.StatusOK, &response); err != nil {
							return err
						}
						return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
							fmt.Fprintln(w, "NAME\tISSUER URL\tAUDIENCES\tSTATUS")
							for _, issuer := range response.Issuers {
								fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", issuer.Name, issuer.IssuerURL, strings.Join(issuer.Audiences, ","), issuer.Status)
							}
						})
					},
				},
				{
					Name: "show", Usage: "Show an issuer", ArgsUsage: "NAME", Flags: append(apiFlags(), orgFlag(), formatFlag()),
					Action: func(ctx *cli.Context) error {
						if err := requireArgs(ctx, 1, "federation issuers show --org ORG NAME"); err != nil {
							return err
						}
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						var issuer federationIssuer
						if err := client.doJSON(http.MethodGet, federationPath(ctx.String("org"), ctx.Args().First()), nil, http.StatusOK, &issuer); err != nil {
							return err
						}
						return renderIssuer(ctx.String("format"), issuer)
					},
				},
				{
					Name: "create", Usage: "Register a trusted issuer", ArgsUsage: "NAME",
					Flags: append(append(apiFlags(), orgFlag(), formatFlag()), issuerFlags()...),
					Action: func(ctx *cli.Context) error {
						if err := requireArgs(ctx, 1, "federation issuers create --org ORG --issuer-url URL --audience AUD NAME"); err != nil {
							return err
						}
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						var issuer federationIssuer
						if err := client.doJSON(http.MethodPost, federationPath(ctx.String("org")), issuerBody(ctx, ctx.Args().First()),
							http.StatusCreated, &issuer); err != nil {
							return err
						}
						return renderIssuer(ctx.String("format"), issuer)
					},
				},
				{
					Name: "update", Usage: "Change an issuer (omitted flags keep their value)", ArgsUsage: "NAME",
					Flags: append(append(apiFlags(), orgFlag(), formatFlag()), issuerFlags()...),
					Action: func(ctx *cli.Context) error {
						if err := requireArgs(ctx, 1, "federation issuers update --org ORG NAME"); err != nil {
							return err
						}
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						var issuer federationIssuer
						if err := client.doJSON(http.MethodPut, federationPath(ctx.String("org"), ctx.Args().First()), issuerBody(ctx, ""),
							http.StatusOK, &issuer); err != nil {
							return err
						}
						return renderIssuer(ctx.String("format"), issuer)
					},
				},
				{
					Name: "delete", Usage: "Remove an issuer and its mappings", ArgsUsage: "NAME", Flags: append(apiFlags(), orgFlag()),
					Action: func(ctx *cli.Context) error {
						if err := requireArgs(ctx, 1, "federation issuers delete --org ORG NAME"); err != nil {
							return err
						}
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						if err := client.doJSON(http.MethodDelete, federationPath(ctx.String("org"), ctx.Args().First()), nil, http.StatusNoContent, nil); err != nil {
							return err
						}
						fmt.Println("Done")
						return nil
					},
				},
			},
		},
		{
			Name: "mappings", Usage: "Manage the claim mappings of an issuer",
			Subcommands: []*cli.Command{
				{
					Name: "list", Usage: "List the mappings of an issuer",
					Flags: append(apiFlags(), orgFlag(), formatFlag(), &cli.StringFlag{Name: "issuer", Required: true, Usage: "issuer name"}),
					Action: func(ctx *cli.Context) error {
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						var response struct {
							Mappings []federationMapping `json:"mappings" yaml:"mappings"`
						}
						if err := client.doJSON(http.MethodGet, federationPath(ctx.String("org"), ctx.String("issuer"), "mappings"), nil, http.StatusOK, &response); err != nil {
							return err
						}
						return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
							fmt.Fprintln(w, "NAME\tPRINCIPAL\tCONDITIONS\tCAPABILITIES\tSTATUS")
							for _, mapping := range response.Mappings {
								conditions := []string{}
								for _, condition := range mapping.Conditions {
									conditions = append(conditions, conditionText(condition))
								}
								fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", mapping.Name, mapping.PrincipalName, strings.Join(conditions, " "),
									strings.Join(mapping.Capabilities, ","), mapping.Status)
							}
						})
					},
				},
				{
					Name: "show", Usage: "Show a mapping", ArgsUsage: "NAME",
					Flags: append(apiFlags(), orgFlag(), formatFlag(), &cli.StringFlag{Name: "issuer", Required: true, Usage: "issuer name"}),
					Action: func(ctx *cli.Context) error {
						if err := requireArgs(ctx, 1, "federation mappings show --org ORG --issuer ISSUER NAME"); err != nil {
							return err
						}
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						var mapping federationMapping
						if err := client.doJSON(http.MethodGet, federationPath(ctx.String("org"), ctx.String("issuer"), "mappings", ctx.Args().First()),
							nil, http.StatusOK, &mapping); err != nil {
							return err
						}
						return renderMapping(ctx.String("format"), mapping)
					},
				},
				{
					Name: "create", Usage: "Map claim conditions to a service principal", ArgsUsage: "NAME",
					Flags: append(append(apiFlags(), orgFlag(), formatFlag()), mappingFlags()...),
					Action: func(ctx *cli.Context) error {
						if err := requireArgs(ctx, 1, "federation mappings create --org ORG --issuer ISSUER --principal P --condition sub=... --capability C NAME"); err != nil {
							return err
						}
						body, err := mappingBody(ctx, ctx.Args().First())
						if err != nil {
							return err
						}
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						var mapping federationMapping
						if err := client.doJSON(http.MethodPost, federationPath(ctx.String("org"), ctx.String("issuer"), "mappings"), body,
							http.StatusCreated, &mapping); err != nil {
							return err
						}
						return renderMapping(ctx.String("format"), mapping)
					},
				},
				{
					Name: "update", Usage: "Change a mapping (omitted flags keep their value; --condition replaces all conditions)", ArgsUsage: "NAME",
					Flags: append(append(apiFlags(), orgFlag(), formatFlag()), mappingFlags()...),
					Action: func(ctx *cli.Context) error {
						if err := requireArgs(ctx, 1, "federation mappings update --org ORG --issuer ISSUER NAME"); err != nil {
							return err
						}
						body, err := mappingBody(ctx, "")
						if err != nil {
							return err
						}
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						var mapping federationMapping
						if err := client.doJSON(http.MethodPut, federationPath(ctx.String("org"), ctx.String("issuer"), "mappings", ctx.Args().First()),
							body, http.StatusOK, &mapping); err != nil {
							return err
						}
						return renderMapping(ctx.String("format"), mapping)
					},
				},
				{
					Name: "delete", Usage: "Remove a mapping", ArgsUsage: "NAME",
					Flags: append(apiFlags(), orgFlag(), &cli.StringFlag{Name: "issuer", Required: true, Usage: "issuer name"}),
					Action: func(ctx *cli.Context) error {
						if err := requireArgs(ctx, 1, "federation mappings delete --org ORG --issuer ISSUER NAME"); err != nil {
							return err
						}
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						if err := client.doJSON(http.MethodDelete, federationPath(ctx.String("org"), ctx.String("issuer"), "mappings", ctx.Args().First()),
							nil, http.StatusNoContent, nil); err != nil {
							return err
						}
						fmt.Println("Done")
						return nil
					},
				},
			},
		},
		{
			Name: "exchange",
			Usage: "Exchange an external OIDC token for a short-lived Reactorcide token. " +
				"Prints only the token to stdout, for piping.",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "api-url", Aliases: []string{"u"}, EnvVars: []string{"REACTORCIDE_API_URL"}, Usage: "Coordinator API URL"},
				&cli.BoolFlag{Name: "allow-insecure-transport", Usage: "Allow a transport without TLS (development only)"},
				orgFlag(),
				&cli.StringFlag{Name: "token-file", Value: "-", Usage: "file that holds the external token (\"-\" is stdin)"},
				&cli.StringFlag{Name: "principal", Usage: "service principal name or UUID, when more than one mapping can match"},
				&cli.StringSliceFlag{Name: "capability", Usage: "request only these capabilities of the mapping"},
				&cli.BoolFlag{Name: "json", Usage: "print the full JSON response instead of the token only"},
			},
			Action: func(ctx *cli.Context) error {
				apiURL := lineageString(ctx, "api-url")
				if apiURL == "" {
					return fmt.Errorf("API URL is required (use --api-url or REACTORCIDE_API_URL)")
				}
				allowInsecure := lineageBool(ctx, "allow-insecure-transport")
				if err := transportsecurity.ValidateURL(apiURL, allowInsecure, "coordinator API"); err != nil {
					return err
				}
				subject, err := readSubjectToken(ctx.String("token-file"))
				if err != nil {
					return err
				}
				client := &apiClient{apiURL: strings.TrimSuffix(apiURL, "/"), allowInsecureTransport: allowInsecure,
					client: transportsecurity.HTTPClient(&http.Client{Timeout: 30 * time.Second}, allowInsecure, "coordinator API")}
				body := map[string]any{"organization": ctx.String("org"), "token": subject}
				if value := ctx.String("principal"); value != "" {
					body["principal"] = value
				}
				if values := ctx.StringSlice("capability"); len(values) > 0 {
					body["capabilities"] = values
				}
				var response map[string]any
				if err := client.doJSON(http.MethodPost, "/api/v1/federation/token", body, http.StatusOK, &response); err != nil {
					return err
				}
				if ctx.Bool("json") {
					data, _ := json.MarshalIndent(response, "", "  ")
					fmt.Println(string(data))
					return nil
				}
				token, _ := response["access_token"].(string)
				fmt.Println(token)
				return nil
			},
		},
	},
}

// OIDCCommand manages the Reactorcide OIDC issuer for jobs (docs/oidc.md).
var OIDCCommand = &cli.Command{
	Name:  "oidc",
	Usage: "Manage the Reactorcide OIDC issuer for jobs: signing keys and org or project settings",
	Subcommands: []*cli.Command{
		{
			Name: "keys", Usage: "Manage the signing keys (needs global authority with secrets:manage)",
			Subcommands: []*cli.Command{
				{
					Name: "list", Usage: "List the signing keys", Flags: append(apiFlags(), formatFlag()),
					Action: func(ctx *cli.Context) error { return oidcKeysAction(ctx, http.MethodGet, "", nil) },
				},
				{
					Name: "rotate", Usage: "Add a new signing key. It is published at once and signs after one hour.",
					Flags: append(apiFlags(), formatFlag(),
						&cli.BoolFlag{Name: "immediate", Usage: "sign with the new key at once (for a compromised key; consumers with a cached JWKS refuse tokens until they refresh)"}),
					Action: func(ctx *cli.Context) error {
						return oidcKeysAction(ctx, http.MethodPost, "/rotate", map[string]any{"immediate": ctx.Bool("immediate")})
					},
				},
				{
					Name: "retire", Usage: "Remove a key from the JWKS now (a compromised key)", ArgsUsage: "KID",
					Flags: append(apiFlags(), formatFlag()),
					Action: func(ctx *cli.Context) error {
						if err := requireArgs(ctx, 1, "oidc keys retire KID"); err != nil {
							return err
						}
						return oidcKeysAction(ctx, http.MethodPost, "/"+url.PathEscape(ctx.Args().First())+"/retire", map[string]any{})
					},
				},
			},
		},
		{
			Name: "settings", Usage: "Show or change the OIDC issuer settings of an organization or project",
			Subcommands: []*cli.Command{
				{
					Name: "show", Usage: "Show the settings",
					Flags:  append(apiFlags(), orgFlag(), formatFlag(), &cli.StringFlag{Name: "project", Usage: "project name"}),
					Action: func(ctx *cli.Context) error { return oidcSettingsAction(ctx, http.MethodGet, nil) },
				},
				{
					Name: "set", Usage: "Change the settings",
					Flags: append(apiFlags(), orgFlag(), formatFlag(),
						&cli.StringFlag{Name: "project", Usage: "project name"},
						&cli.StringFlag{Name: "issuer", Usage: "allow or deny (inherit for a project)"},
						&cli.StringFlag{Name: "claims-file", Usage: "JSON file: {\"AUDIENCE\": {\"CLAIM\": \"VALUE\"}, \"*\": {...}}"}),
					Action: func(ctx *cli.Context) error {
						body := map[string]any{}
						if value := ctx.String("issuer"); value != "" {
							body["issuer"] = value
						}
						if path := ctx.String("claims-file"); path != "" {
							data, err := os.ReadFile(path)
							if err != nil {
								return err
							}
							var claims map[string]map[string]string
							if err := json.Unmarshal(data, &claims); err != nil {
								return fmt.Errorf("claims file: %w", err)
							}
							body["audience_claims"] = claims
						}
						return oidcSettingsAction(ctx, http.MethodPut, body)
					},
				},
			},
		},
	},
}

func oidcKeysAction(ctx *cli.Context, method, suffix string, body any) error {
	client, err := newAPIClient(ctx)
	if err != nil {
		return err
	}
	var response struct {
		Issuer string `json:"issuer" yaml:"issuer"`
		Keys   []struct {
			KID         string     `json:"kid" yaml:"kid"`
			Algorithm   string     `json:"algorithm" yaml:"algorithm"`
			ActivatesAt time.Time  `json:"activates_at" yaml:"activates_at"`
			RetiresAt   *time.Time `json:"retires_at,omitempty" yaml:"retires_at,omitempty"`
			State       string     `json:"state" yaml:"state"`
		} `json:"keys" yaml:"keys"`
	}
	if err := client.doJSON(method, "/api/v1/oidc/keys"+suffix, body, http.StatusOK, &response); err != nil {
		return err
	}
	return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "issuer\t%s\n", response.Issuer)
		fmt.Fprintln(w, "KID\tALG\tSTATE\tACTIVATES\tRETIRES")
		for _, key := range response.Keys {
			retires := ""
			if key.RetiresAt != nil {
				retires = key.RetiresAt.Format(time.RFC3339)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", key.KID, key.Algorithm, key.State, key.ActivatesAt.Format(time.RFC3339), retires)
		}
	})
}

func oidcSettingsAction(ctx *cli.Context, method string, body any) error {
	client, err := newAPIClient(ctx)
	if err != nil {
		return err
	}
	path := "/api/v1/organizations/" + url.PathEscape(ctx.String("org")) + "/oidc-settings"
	if project := ctx.String("project"); project != "" {
		path += "/projects/" + url.PathEscape(project)
	}
	var response struct {
		Organization     string                       `json:"organization" yaml:"organization"`
		Project          string                       `json:"project,omitempty" yaml:"project,omitempty"`
		Issuer           string                       `json:"issuer" yaml:"issuer"`
		AudienceClaims   map[string]map[string]string `json:"audience_claims" yaml:"audience_claims"`
		EffectiveEnabled bool                         `json:"effective_enabled" yaml:"effective_enabled"`
		IssuerURL        string                       `json:"issuer_url,omitempty" yaml:"issuer_url,omitempty"`
	}
	if err := client.doJSON(method, path, body, http.StatusOK, &response); err != nil {
		return err
	}
	return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "organization\t%s\n", response.Organization)
		if response.Project != "" {
			fmt.Fprintf(w, "project\t%s\n", response.Project)
		}
		fmt.Fprintf(w, "issuer\t%s\n", response.Issuer)
		fmt.Fprintf(w, "effective_enabled\t%t\n", response.EffectiveEnabled)
		if response.IssuerURL != "" {
			fmt.Fprintf(w, "issuer_url\t%s\n", response.IssuerURL)
		}
		for audience, claims := range response.AudienceClaims {
			for name, value := range claims {
				fmt.Fprintf(w, "claim\t%s: %s=%s\n", audience, name, value)
			}
		}
	})
}
