package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v2"
)

// subscriptionView is the REST form of an outbound event subscription.
type subscriptionView struct {
	SubscriptionID     string    `json:"subscription_id" yaml:"subscription_id"`
	Name               string    `json:"name" yaml:"name"`
	Description        string    `json:"description" yaml:"description"`
	OwnerPrincipalID   string    `json:"owner_principal_id" yaml:"owner_principal_id"`
	OwnerActive        bool      `json:"owner_active" yaml:"owner_active"`
	Scope              string    `json:"scope" yaml:"scope"`
	ProjectID          *string   `json:"project_id,omitempty" yaml:"project_id,omitempty"`
	WorkflowSecurityID string    `json:"workflow_security_id,omitempty" yaml:"workflow_security_id,omitempty"`
	OwnerGroup         string    `json:"owner_group,omitempty" yaml:"owner_group,omitempty"`
	EventTypes         []string  `json:"event_types" yaml:"event_types"`
	RefClassesAny      []string  `json:"ref_classes_any" yaml:"ref_classes_any"`
	Results            []string  `json:"results" yaml:"results"`
	ActorKinds         []string  `json:"actor_kinds" yaml:"actor_kinds"`
	FailureClasses     []string  `json:"failure_classes" yaml:"failure_classes"`
	SinkType           string    `json:"sink_type" yaml:"sink_type"`
	WebhookURL         string    `json:"webhook_url" yaml:"webhook_url"`
	MaxAttempts        int       `json:"max_attempts" yaml:"max_attempts"`
	Enabled            bool      `json:"enabled" yaml:"enabled"`
	CreatedAt          time.Time `json:"created_at" yaml:"created_at"`
	SigningSecret      string    `json:"signing_secret,omitempty" yaml:"signing_secret,omitempty"`
}

type deliveryView struct {
	DeliveryID     string     `json:"delivery_id" yaml:"delivery_id"`
	EventID        string     `json:"event_id" yaml:"event_id"`
	EventType      string     `json:"event_type" yaml:"event_type"`
	OrderKey       string     `json:"order_key" yaml:"order_key"`
	Sequence       int64      `json:"sequence" yaml:"sequence"`
	Status         string     `json:"status" yaml:"status"`
	Attempts       int        `json:"attempts" yaml:"attempts"`
	LastHTTPStatus *int       `json:"last_http_status,omitempty" yaml:"last_http_status,omitempty"`
	LastLatencyMs  *int       `json:"last_latency_ms,omitempty" yaml:"last_latency_ms,omitempty"`
	LastError      string     `json:"last_error" yaml:"last_error"`
	NextAttemptAt  time.Time  `json:"next_attempt_at" yaml:"next_attempt_at"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty" yaml:"delivered_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at" yaml:"created_at"`
	AttemptLog     []struct {
		Attempt    int       `json:"attempt" yaml:"attempt"`
		StartedAt  time.Time `json:"started_at" yaml:"started_at"`
		Result     string    `json:"result" yaml:"result"`
		HTTPStatus *int      `json:"http_status,omitempty" yaml:"http_status,omitempty"`
		LatencyMs  int       `json:"latency_ms" yaml:"latency_ms"`
		Error      string    `json:"error" yaml:"error"`
	} `json:"attempt_log,omitempty" yaml:"attempt_log,omitempty"`
}

func subscriptionsPath(org string, parts ...string) string {
	path := "/api/v1/organizations/" + url.PathEscape(org) + "/subscriptions"
	for _, part := range parts {
		path += "/" + url.PathEscape(part)
	}
	return path
}

func intOrDash(value *int) string {
	if value == nil {
		return "-"
	}
	return strconv.Itoa(*value)
}

func listOrAll(values []string) string {
	if len(values) == 0 {
		return "*"
	}
	return strings.Join(values, ",")
}

func renderSubscription(format string, s subscriptionView) error {
	return render(format, s, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "subscription_id\t%s\n", s.SubscriptionID)
		fmt.Fprintf(w, "name\t%s\n", s.Name)
		if s.Description != "" {
			fmt.Fprintf(w, "description\t%s\n", s.Description)
		}
		fmt.Fprintf(w, "scope\t%s\n", s.Scope)
		if s.ProjectID != nil {
			fmt.Fprintf(w, "project_id\t%s\n", *s.ProjectID)
		}
		if s.WorkflowSecurityID != "" {
			fmt.Fprintf(w, "workflow\t%s\n", s.WorkflowSecurityID)
		}
		if s.OwnerGroup != "" {
			fmt.Fprintf(w, "owner_group\t%s\n", s.OwnerGroup)
		}
		fmt.Fprintf(w, "owner\t%s (active=%t)\n", s.OwnerPrincipalID, s.OwnerActive)
		fmt.Fprintf(w, "event_types\t%s\n", listOrAll(s.EventTypes))
		fmt.Fprintf(w, "ref_classes_any\t%s\n", listOrAll(s.RefClassesAny))
		fmt.Fprintf(w, "results\t%s\n", listOrAll(s.Results))
		fmt.Fprintf(w, "actor_kinds\t%s\n", listOrAll(s.ActorKinds))
		fmt.Fprintf(w, "failure_classes\t%s\n", listOrAll(s.FailureClasses))
		fmt.Fprintf(w, "sink\t%s %s\n", s.SinkType, s.WebhookURL)
		fmt.Fprintf(w, "max_attempts\t%d\n", s.MaxAttempts)
		fmt.Fprintf(w, "enabled\t%t\n", s.Enabled)
		if s.SigningSecret != "" {
			fmt.Fprintf(w, "signing_secret\t%s\n", s.SigningSecret)
			fmt.Fprintf(w, "\tThis is the only time the secret is shown. Keep it in a secret store now.\n")
		}
	})
}

// readSecretFile reads a signing secret from a file, so it is not in the
// shell history or the process list.
func readSecretFile(ctx *cli.Context) (string, error) {
	path := ctx.String("secret-file")
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read --secret-file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func subscriptionIDAction(usage string, run func(ctx *cli.Context, client *apiClient, id string) error) cli.ActionFunc {
	return func(ctx *cli.Context) error {
		if err := requireArgs(ctx, 1, usage); err != nil {
			return err
		}
		client, err := newAPIClient(ctx)
		if err != nil {
			return err
		}
		return run(ctx, client, ctx.Args().First())
	}
}

func patchSubscription(ctx *cli.Context, client *apiClient, id string, body map[string]any) error {
	var response subscriptionView
	if err := client.doJSON(http.MethodPatch, subscriptionsPath(ctx.String("org"), id), body, http.StatusOK, &response); err != nil {
		return err
	}
	return renderSubscription(ctx.String("format"), response)
}

func filterFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringSliceFlag{Name: "event-type", Usage: "Event type to send (repeat). Default: every type"},
		&cli.StringSliceFlag{Name: "ref-class", Usage: "Send only events with one of these ref classes (repeat)"},
		&cli.StringSliceFlag{Name: "result", Usage: "Send only events with one of these results (repeat), for example failed"},
		&cli.StringSliceFlag{Name: "actor-kind", Usage: "Send only events of these actor kinds (repeat): human, service, system"},
		&cli.StringSliceFlag{Name: "failure-class", Usage: "Send only events with one of these failure classes (repeat), for example job, oom, timeout"},
	}
}

// SubscriptionsCommand manages outbound event subscriptions.
var SubscriptionsCommand = &cli.Command{
	Name:    "subscriptions",
	Aliases: []string{"subscription"},
	Usage:   "Manage outbound event subscriptions (webhooks for phase events)",
	Subcommands: []*cli.Command{
		{
			Name:  "create",
			Usage: "Create a subscription. The answer shows the signing secret one time.",
			Flags: append(append(apiFlags(), orgFlag(), formatFlag(),
				&cli.StringFlag{Name: "name", Required: true, Usage: "Name, unique in the organization"},
				&cli.StringFlag{Name: "description"},
				&cli.StringFlag{Name: "scope", Value: "org", Usage: "org, project, workflow, or owner_group"},
				&cli.StringFlag{Name: "project", Usage: "Project ID (project and workflow scopes)"},
				&cli.StringFlag{Name: "workflow", Usage: "Workflow security ID (workflow scope)"},
				&cli.StringFlag{Name: "owner-group", Usage: "Owner group (owner_group scope)"},
				&cli.StringFlag{Name: "url", Required: true, Usage: "HTTPS URL of the webhook"},
				&cli.IntFlag{Name: "max-attempts", Usage: "Attempts before dead_letter (0: coordinator default)"},
				&cli.StringFlag{Name: "owner", Usage: "Owner principal ID (default: the caller)"},
				&cli.StringFlag{Name: "secret-file", Usage: "File with the signing secret (default: a random secret)"},
				&cli.BoolFlag{Name: "disabled", Usage: "Create the subscription disabled"},
			), filterFlags()...),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				secret, err := readSecretFile(ctx)
				if err != nil {
					return err
				}
				enabled := !ctx.Bool("disabled")
				body := map[string]any{
					"name": ctx.String("name"), "description": ctx.String("description"), "scope": ctx.String("scope"),
					"workflow_security_id": ctx.String("workflow"), "owner_group": ctx.String("owner-group"),
					"webhook_url": ctx.String("url"), "max_attempts": ctx.Int("max-attempts"), "enabled": enabled,
					"event_types": ctx.StringSlice("event-type"), "ref_classes_any": ctx.StringSlice("ref-class"),
					"results": ctx.StringSlice("result"), "actor_kinds": ctx.StringSlice("actor-kind"),
					"failure_classes":    ctx.StringSlice("failure-class"),
					"owner_principal_id": ctx.String("owner"), "signing_secret": secret,
				}
				if project := ctx.String("project"); project != "" {
					body["project_id"] = project
				}
				var response subscriptionView
				if err := client.doJSON(http.MethodPost, subscriptionsPath(ctx.String("org")), body, http.StatusCreated, &response); err != nil {
					return err
				}
				return renderSubscription(ctx.String("format"), response)
			},
		},
		{
			Name:  "list",
			Usage: "List the subscriptions of an organization, or of one project",
			Flags: append(apiFlags(), orgFlag(), formatFlag(), &cli.StringFlag{Name: "project", Usage: "Project ID"}),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				path := subscriptionsPath(ctx.String("org"))
				if project := ctx.String("project"); project != "" {
					path += "?project=" + url.QueryEscape(project)
				}
				var response struct {
					Subscriptions []subscriptionView `json:"subscriptions" yaml:"subscriptions"`
				}
				if err := client.doJSON(http.MethodGet, path, nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response.Subscriptions, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "ID\tNAME\tSCOPE\tEVENT TYPES\tURL\tENABLED")
					for _, s := range response.Subscriptions {
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%t\n", s.SubscriptionID, s.Name, s.Scope, listOrAll(s.EventTypes), s.WebhookURL, s.Enabled)
					}
				})
			},
		},
		{
			Name: "show", Usage: "Show a subscription", ArgsUsage: "<subscription-id>",
			Flags: append(apiFlags(), orgFlag(), formatFlag()),
			Action: subscriptionIDAction("usage: reactorcide subscriptions show --org ORG <subscription-id>", func(ctx *cli.Context, client *apiClient, id string) error {
				var response subscriptionView
				if err := client.doJSON(http.MethodGet, subscriptionsPath(ctx.String("org"), id), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return renderSubscription(ctx.String("format"), response)
			}),
		},
		{
			Name: "update", Usage: "Change a subscription. Only the flags you give change.", ArgsUsage: "<subscription-id>",
			Flags: append(append(apiFlags(), orgFlag(), formatFlag(),
				&cli.StringFlag{Name: "description"},
				&cli.StringFlag{Name: "url", Usage: "HTTPS URL of the webhook"},
				&cli.IntFlag{Name: "max-attempts"},
				&cli.BoolFlag{Name: "rotate-secret", Usage: "Make a new signing secret and show it one time"},
				&cli.StringFlag{Name: "secret-file", Usage: "File with the new signing secret"},
				&cli.StringSliceFlag{Name: "clear", Usage: "Clear a filter (repeat): event-type, ref-class, result, actor-kind, failure-class"},
			), filterFlags()...),
			Action: subscriptionIDAction("usage: reactorcide subscriptions update --org ORG <subscription-id> [flags]", func(ctx *cli.Context, client *apiClient, id string) error {
				body := map[string]any{}
				for flag, field := range map[string]string{"description": "description", "url": "webhook_url"} {
					if ctx.IsSet(flag) {
						body[field] = ctx.String(flag)
					}
				}
				if ctx.IsSet("max-attempts") {
					body["max_attempts"] = ctx.Int("max-attempts")
				}
				filters := map[string]string{"event-type": "event_types", "ref-class": "ref_classes_any", "result": "results",
					"actor-kind": "actor_kinds", "failure-class": "failure_classes"}
				for flag, field := range filters {
					if ctx.IsSet(flag) {
						body[field] = ctx.StringSlice(flag)
					}
				}
				for _, name := range ctx.StringSlice("clear") {
					field, ok := filters[name]
					if !ok {
						return fmt.Errorf("unknown filter %q for --clear", name)
					}
					body[field] = []string{}
				}
				secret, err := readSecretFile(ctx)
				if err != nil {
					return err
				}
				if secret != "" {
					body["signing_secret"] = secret
				}
				if ctx.Bool("rotate-secret") {
					body["rotate_secret"] = true
				}
				if len(body) == 0 {
					return fmt.Errorf("nothing to change")
				}
				return patchSubscription(ctx, client, id, body)
			}),
		},
		{
			Name: "enable", Usage: "Enable a subscription", ArgsUsage: "<subscription-id>",
			Flags: append(apiFlags(), orgFlag(), formatFlag()),
			Action: subscriptionIDAction("usage: reactorcide subscriptions enable --org ORG <subscription-id>", func(ctx *cli.Context, client *apiClient, id string) error {
				return patchSubscription(ctx, client, id, map[string]any{"enabled": true})
			}),
		},
		{
			Name: "disable", Usage: "Disable a subscription. Its open deliveries go to dead_letter.", ArgsUsage: "<subscription-id>",
			Flags: append(apiFlags(), orgFlag(), formatFlag()),
			Action: subscriptionIDAction("usage: reactorcide subscriptions disable --org ORG <subscription-id>", func(ctx *cli.Context, client *apiClient, id string) error {
				return patchSubscription(ctx, client, id, map[string]any{"enabled": false})
			}),
		},
		{
			Name: "delete", Usage: "Delete a subscription and its delivery log", ArgsUsage: "<subscription-id>",
			Flags: append(apiFlags(), orgFlag()),
			Action: subscriptionIDAction("usage: reactorcide subscriptions delete --org ORG <subscription-id>", func(ctx *cli.Context, client *apiClient, id string) error {
				if err := client.doJSON(http.MethodDelete, subscriptionsPath(ctx.String("org"), id), nil, http.StatusNoContent, nil); err != nil {
					return err
				}
				fmt.Printf("Subscription %s deleted\n", id)
				return nil
			}),
		},
		{
			Name: "deliveries", Usage: "List the newest deliveries of a subscription, or show one with its attempts",
			ArgsUsage: "<subscription-id> [delivery-id]",
			Flags: append(apiFlags(), orgFlag(), formatFlag(),
				&cli.StringFlag{Name: "status", Usage: "pending, queued, sending, delivered, or dead_letter"},
				&cli.IntFlag{Name: "limit", Value: 50, Usage: "Number of deliveries (1-500)"}),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() < 1 || ctx.NArg() > 2 {
					return fmt.Errorf("usage: reactorcide subscriptions deliveries --org ORG <subscription-id> [delivery-id]")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				id := ctx.Args().First()
				if ctx.NArg() == 2 {
					var d deliveryView
					if err := client.doJSON(http.MethodGet, subscriptionsPath(ctx.String("org"), id, "deliveries", ctx.Args().Get(1)), nil, http.StatusOK, &d); err != nil {
						return err
					}
					return render(ctx.String("format"), d, func(w *tabwriter.Writer) {
						fmt.Fprintf(w, "delivery_id\t%s\nevent\t%s %s\nstatus\t%s\nattempts\t%d\n", d.DeliveryID, d.EventType, d.EventID, d.Status, d.Attempts)
						fmt.Fprintln(w, "ATTEMPT\tSTARTED\tRESULT\tHTTP\tLATENCY MS\tERROR")
						for _, a := range d.AttemptLog {
							fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d\t%s\n", a.Attempt, a.StartedAt.Format(time.RFC3339), a.Result, intOrDash(a.HTTPStatus), a.LatencyMs, a.Error)
						}
					})
				}
				query := url.Values{}
				query.Set("limit", strconv.Itoa(ctx.Int("limit")))
				if status := ctx.String("status"); status != "" {
					query.Set("status", status)
				}
				var response struct {
					Deliveries []deliveryView `json:"deliveries" yaml:"deliveries"`
				}
				if err := client.doJSON(http.MethodGet, subscriptionsPath(ctx.String("org"), id, "deliveries")+"?"+query.Encode(), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response.Deliveries, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "DELIVERY\tEVENT TYPE\tORDER KEY\tSEQ\tSTATUS\tATTEMPTS\tHTTP\tLATENCY MS\tERROR")
					for _, d := range response.Deliveries {
						fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%d\t%s\t%s\t%s\n", d.DeliveryID, d.EventType, d.OrderKey, d.Sequence, d.Status,
							d.Attempts, intOrDash(d.LastHTTPStatus), intOrDash(d.LastLatencyMs), d.LastError)
					}
				})
			},
		},
		{
			Name: "replay", Usage: "Send a delivered or dead_letter delivery again, or every dead_letter delivery",
			ArgsUsage: "<subscription-id> [delivery-id]",
			Flags:     append(apiFlags(), orgFlag(), &cli.BoolFlag{Name: "all-dead-letters", Usage: "Replay every dead_letter delivery"}),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() < 1 || ctx.NArg() > 2 || (ctx.NArg() == 2) == ctx.Bool("all-dead-letters") {
					return fmt.Errorf("usage: reactorcide subscriptions replay --org ORG <subscription-id> (<delivery-id> | --all-dead-letters)")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				id := ctx.Args().First()
				if ctx.Bool("all-dead-letters") {
					var response struct {
						Replayed int64 `json:"replayed"`
					}
					if err := client.doJSON(http.MethodPost, subscriptionsPath(ctx.String("org"), id, "replay-dead-letters"), nil, http.StatusOK, &response); err != nil {
						return err
					}
					fmt.Printf("Replayed %d deliveries\n", response.Replayed)
					return nil
				}
				var d deliveryView
				if err := client.doJSON(http.MethodPost, subscriptionsPath(ctx.String("org"), id, "deliveries", ctx.Args().Get(1), "replay"), nil, http.StatusOK, &d); err != nil {
					return err
				}
				fmt.Printf("Delivery %s is %s\n", d.DeliveryID, d.Status)
				return nil
			},
		},
		{
			Name: "test", Usage: "Send one test.ping event to the sink now", ArgsUsage: "<subscription-id>",
			Flags: append(apiFlags(), orgFlag(), formatFlag()),
			Action: subscriptionIDAction("usage: reactorcide subscriptions test --org ORG <subscription-id>", func(ctx *cli.Context, client *apiClient, id string) error {
				var response struct {
					Delivered  bool   `json:"delivered" yaml:"delivered"`
					HTTPStatus int    `json:"http_status,omitempty" yaml:"http_status,omitempty"`
					LatencyMs  int64  `json:"latency_ms" yaml:"latency_ms"`
					Error      string `json:"error,omitempty" yaml:"error,omitempty"`
					Refused    bool   `json:"refused,omitempty" yaml:"refused,omitempty"`
				}
				if err := client.doJSON(http.MethodPost, subscriptionsPath(ctx.String("org"), id, "test"), nil, http.StatusOK, &response); err != nil {
					return err
				}
				if err := render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "delivered\t%t\nhttp_status\t%d\nlatency_ms\t%d\n", response.Delivered, response.HTTPStatus, response.LatencyMs)
					if response.Error != "" {
						fmt.Fprintf(w, "error\t%s\n", response.Error)
					}
				}); err != nil {
					return err
				}
				if !response.Delivered {
					return cli.Exit("", 1)
				}
				return nil
			}),
		},
	},
}
