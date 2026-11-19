package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/runshifts"
	"github.com/urfave/cli/v2"
)

// defaultDiagnosisFailureClasses are the failure classes that a diagnosis
// tool can act on: the pipeline owner owns them (docs/runtime-behavior.md).
// Infrastructure classes are for the CI owner and retry on their own.
var defaultDiagnosisFailureClasses = []string{"job", "oom", "timeout"}

type diagnosisLinkView struct {
	Name string `json:"name" yaml:"name"`
	URL  string `json:"url" yaml:"url"`
}

type diagnosisView struct {
	DiagnosisID string  `json:"diagnosis_id" yaml:"diagnosis_id"`
	TargetType  string  `json:"target_type" yaml:"target_type"`
	JobID       *string `json:"job_id,omitempty" yaml:"job_id,omitempty"`
	WorkflowID  *string `json:"workflow_id,omitempty" yaml:"workflow_id,omitempty"`
	NodeName    string  `json:"node_name,omitempty" yaml:"node_name,omitempty"`
	Author      struct {
		PrincipalID string `json:"principal_id" yaml:"principal_id"`
		Kind        string `json:"kind" yaml:"kind"`
		DisplayName string `json:"display_name" yaml:"display_name"`
	} `json:"author" yaml:"author"`
	Title           string              `json:"title" yaml:"title"`
	Body            string              `json:"body" yaml:"body"`
	Confidence      string              `json:"confidence,omitempty" yaml:"confidence,omitempty"`
	Category        string              `json:"category,omitempty" yaml:"category,omitempty"`
	Links           []diagnosisLinkView `json:"links" yaml:"links"`
	SuggestedShifts map[string]any      `json:"suggested_shifts,omitempty" yaml:"suggested_shifts,omitempty"`
	Revision        int                 `json:"revision" yaml:"revision"`
	CreatedAt       time.Time           `json:"created_at" yaml:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at" yaml:"updated_at"`
	Created         *bool               `json:"created,omitempty" yaml:"created,omitempty"`
}

// diagnosisPath is the REST path of the entries of a job, a workflow node,
// or a workflow.
func diagnosisPath(ctx *cli.Context) (string, error) {
	job, workflow, node := ctx.String("job"), ctx.String("workflow"), ctx.String("node")
	switch {
	case job != "" && (workflow != "" || node != ""):
		return "", fmt.Errorf("use --job, or --workflow with an optional --node; not both")
	case job != "":
		return "/api/v1/jobs/" + url.PathEscape(job) + "/diagnosis", nil
	case workflow != "" && node != "":
		return "/api/v1/workflows/" + url.PathEscape(workflow) + "/nodes/" + url.PathEscape(node) + "/diagnosis", nil
	case workflow != "":
		return "/api/v1/workflows/" + url.PathEscape(workflow) + "/diagnosis", nil
	case node != "":
		return "", fmt.Errorf("--node needs --workflow")
	}
	return "", fmt.Errorf("--job or --workflow is required")
}

func diagnosisTargetFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "job", Usage: "Job ID"},
		&cli.StringFlag{Name: "workflow", Usage: "Workflow ID (alone: the whole workflow)"},
		&cli.StringFlag{Name: "node", Usage: "Node name in --workflow"},
	}
}

// parseDiagnosisLinks reads --link NAME=URL values.
func parseDiagnosisLinks(values []string) ([]diagnosisLinkView, error) {
	links := []diagnosisLinkView{}
	for _, value := range values {
		name, target, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(target) == "" {
			return nil, fmt.Errorf("--link %q: use NAME=URL", value)
		}
		links = append(links, diagnosisLinkView{Name: strings.TrimSpace(name), URL: strings.TrimSpace(target)})
	}
	return links, nil
}

// diagnosisShifts reads --shift KEY=VALUE values with the coordinator parser.
func diagnosisShifts(values []string) (map[string]any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if _, err := runshifts.ParseArgs(values); err != nil {
		return nil, fmt.Errorf("--shift: %w", err)
	}
	raw := map[string]any{}
	for _, value := range values {
		key, v, _ := strings.Cut(value, "=")
		raw[key] = v
	}
	return raw, nil
}

func shiftsText(shifts map[string]any) string {
	keys := make([]string, 0, len(shifts))
	for key := range shifts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", key, shifts[key]))
	}
	return strings.Join(parts, " ")
}

func renderDiagnoses(format string, entries []diagnosisView) error {
	return render(format, map[string]any{"diagnoses": entries}, func(w *tabwriter.Writer) {
		fmt.Fprintln(w, "DIAGNOSIS\tTARGET\tAUTHOR\tKIND\tCONFIDENCE\tTITLE")
		for _, entry := range entries {
			target := entry.TargetType
			if entry.NodeName != "" {
				target += ":" + entry.NodeName
			}
			name := entry.Author.DisplayName
			if name == "" {
				name = entry.Author.PrincipalID
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", entry.DiagnosisID, target, name, entry.Author.Kind,
				valueOrDash(entry.Confidence), entry.Title)
		}
	})
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func renderDiagnosis(format string, entry diagnosisView) error {
	return render(format, entry, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "diagnosis_id\t%s\n", entry.DiagnosisID)
		fmt.Fprintf(w, "target\t%s\n", entry.TargetType)
		if entry.JobID != nil {
			fmt.Fprintf(w, "job_id\t%s\n", *entry.JobID)
		}
		if entry.WorkflowID != nil {
			fmt.Fprintf(w, "workflow_id\t%s\n", *entry.WorkflowID)
		}
		if entry.NodeName != "" {
			fmt.Fprintf(w, "node\t%s\n", entry.NodeName)
		}
		fmt.Fprintf(w, "author\t%s (%s)\n", entry.Author.DisplayName, entry.Author.Kind)
		fmt.Fprintf(w, "title\t%s\n", entry.Title)
		fmt.Fprintf(w, "revision\t%d\n", entry.Revision)
		if entry.Created != nil {
			fmt.Fprintf(w, "created\t%t\n", *entry.Created)
		}
		if len(entry.SuggestedShifts) > 0 {
			fmt.Fprintf(w, "suggested_shifts\t%s\n", shiftsText(entry.SuggestedShifts))
		}
	})
}

// DiagnosisCommand holds the primitives for failure diagnosis by service
// principals (docs/diagnosis.md).
var DiagnosisCommand = &cli.Command{
	Name:  "diagnosis",
	Usage: "Set up diagnosis tools, and post or list diagnosis entries",
	Subcommands: []*cli.Command{
		{
			Name: "setup",
			Usage: "Create the outbound subscription of a diagnosis tool: node.post_complete events with a failure " +
				"class of job, oom, or timeout. The answer shows the signing secret one time.",
			Flags: append(apiFlags(), orgFlag(), formatFlag(),
				&cli.StringFlag{Name: "principal", Required: true, Usage: "Service principal of the tool (name or ID). It owns the subscription."},
				&cli.StringFlag{Name: "url", Required: true, Usage: "HTTPS URL of the tool webhook"},
				&cli.StringFlag{Name: "name", Usage: "Subscription name (default: diagnosis-PRINCIPAL)"},
				&cli.StringFlag{Name: "scope", Value: "org", Usage: "org or project"},
				&cli.StringFlag{Name: "project", Usage: "Project ID (project scope)"},
				&cli.StringSliceFlag{Name: "failure-class", Usage: "Failure class to send (repeat). Default: job, oom, timeout"},
				&cli.StringFlag{Name: "secret-file", Usage: "File with the signing secret (default: a random secret)"},
			),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				secret, err := readSecretFile(ctx)
				if err != nil {
					return err
				}
				var principal principalDetail
				if err := client.doJSON(http.MethodGet, principalsPath(ctx.String("org"), ctx.String("principal")), nil, http.StatusOK, &principal); err != nil {
					return fmt.Errorf("reading the principal %q: %w", ctx.String("principal"), err)
				}
				if principal.Kind != "service" {
					return fmt.Errorf("the principal %q is a %s principal; a diagnosis tool is a service principal", ctx.String("principal"), principal.Kind)
				}
				classes := ctx.StringSlice("failure-class")
				if len(classes) == 0 {
					classes = defaultDiagnosisFailureClasses
				}
				name := ctx.String("name")
				if name == "" {
					name = "diagnosis-" + principal.Name
				}
				scope := ctx.String("scope")
				if scope != "org" && scope != "project" {
					return fmt.Errorf("--scope must be org or project")
				}
				body := map[string]any{
					"name": name, "description": "Failed jobs for the diagnosis tool " + principal.Name,
					"scope": scope, "webhook_url": ctx.String("url"), "enabled": true,
					"event_types":        []string{"node.post_complete"},
					"failure_classes":    classes,
					"owner_principal_id": principal.PrincipalID, "signing_secret": secret,
				}
				if project := ctx.String("project"); project != "" {
					body["project_id"] = project
				} else if scope == "project" {
					return fmt.Errorf("--scope project needs --project")
				}
				var response subscriptionView
				if err := client.doJSON(http.MethodPost, subscriptionsPath(ctx.String("org")), body, http.StatusCreated, &response); err != nil {
					return err
				}
				if err := renderSubscription(ctx.String("format"), response); err != nil {
					return err
				}
				if ctx.String("format") == "table" {
					fmt.Fprintf(os.Stderr, "\nNext: give the principal a role and a token, for example:\n"+
						"  reactorcide principals grant-role --org %s --role member %s\n"+
						"  reactorcide principals tokens create --org %s %s --capability jobs:read --capability logs:read "+
						"--capability workflows:read --capability diagnosis:write\n"+
						"A retry with shifts also needs jobs:retry, which a service principal keeps only with an org admin role.\n",
						ctx.String("org"), principal.Name, ctx.String("org"), principal.Name)
				}
				return nil
			},
		},
		{
			Name:  "post",
			Usage: "Post the diagnosis entry of the caller's principal for a job, a workflow node, or a workflow. A new post replaces it.",
			Flags: append(append(apiFlags(), formatFlag(),
				&cli.StringFlag{Name: "title", Required: true, Usage: "Short title (at most 200 characters)"},
				&cli.StringFlag{Name: "body", Usage: "Markdown body (at most 16 KiB)"},
				&cli.StringFlag{Name: "body-file", Usage: "File with the Markdown body ('-' reads standard input)"},
				&cli.StringFlag{Name: "confidence", Usage: "low, medium, or high"},
				&cli.StringFlag{Name: "category", Usage: "Category, for example dependency or flaky-test"},
				&cli.StringSliceFlag{Name: "link", Usage: "Link NAME=URL (repeat, at most 10)"},
				&cli.StringSliceFlag{Name: "shift", Usage: "Suggested re-run shift KEY=VALUE (repeat), for example memory=8Gi"},
			), diagnosisTargetFlags()...),
			Action: func(ctx *cli.Context) error {
				path, err := diagnosisPath(ctx)
				if err != nil {
					return err
				}
				body := ctx.String("body")
				if file := ctx.String("body-file"); file != "" {
					if body != "" {
						return fmt.Errorf("use --body or --body-file, not both")
					}
					var data []byte
					if file == "-" {
						data, err = io.ReadAll(os.Stdin)
					} else {
						data, err = os.ReadFile(file)
					}
					if err != nil {
						return fmt.Errorf("failed to read --body-file: %w", err)
					}
					body = string(data)
				}
				links, err := parseDiagnosisLinks(ctx.StringSlice("link"))
				if err != nil {
					return err
				}
				shifts, err := diagnosisShifts(ctx.StringSlice("shift"))
				if err != nil {
					return err
				}
				request := map[string]any{"title": ctx.String("title"), "body": body, "confidence": ctx.String("confidence"),
					"category": ctx.String("category"), "links": links}
				if shifts != nil {
					request["suggested_shifts"] = shifts
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response diagnosisView
				if err := client.doJSON(http.MethodPut, path, request, http.StatusOK, &response); err != nil {
					return err
				}
				return renderDiagnosis(ctx.String("format"), response)
			},
		},
		{
			Name:  "delete",
			Usage: "Remove the diagnosis entry of the caller's principal for a job, a workflow node, or a workflow, and its pull request section",
			Flags: append(apiFlags(), diagnosisTargetFlags()...),
			Action: func(ctx *cli.Context) error {
				path, err := diagnosisPath(ctx)
				if err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					DiagnosisID string `json:"diagnosis_id"`
				}
				if err := client.doJSON(http.MethodDelete, path, nil, http.StatusOK, &response); err != nil {
					return err
				}
				fmt.Printf("Diagnosis entry deleted: %s\n", response.DiagnosisID)
				return nil
			},
		},
		{
			Name:  "list",
			Usage: "List the diagnosis entries of a job, or of a workflow and its jobs",
			Flags: append(append(apiFlags(), formatFlag()), diagnosisTargetFlags()...),
			Action: func(ctx *cli.Context) error {
				path, err := diagnosisPath(ctx)
				if err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					Diagnoses []diagnosisView `json:"diagnoses"`
				}
				if err := client.doJSON(http.MethodGet, path, nil, http.StatusOK, &response); err != nil {
					return err
				}
				return renderDiagnoses(ctx.String("format"), response.Diagnoses)
			},
		},
	},
}
