package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/urfave/cli/v2"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/provenance"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/transportsecurity"
)

// WP15 CLI: job artifacts, test results, test quarantine, and provenance
// (docs/artifacts-and-tests.md, docs/provenance.md). All commands work with
// the admin API token.

type artifactView struct {
	ArtifactID   string `json:"artifact_id" yaml:"artifact_id"`
	Path         string `json:"path" yaml:"path"`
	Kind         string `json:"kind" yaml:"kind"`
	MediaType    string `json:"media_type" yaml:"media_type"`
	SizeBytes    int64  `json:"size_bytes" yaml:"size_bytes"`
	SHA256       string `json:"sha256,omitempty" yaml:"sha256,omitempty"`
	Status       string `json:"status" yaml:"status"`
	StatusReason string `json:"status_reason,omitempty" yaml:"status_reason,omitempty"`
}

type artifactList struct {
	JobID     string         `json:"job_id" yaml:"job_id"`
	Artifacts []artifactView `json:"artifacts" yaml:"artifacts"`
}

func jobPath(jobID, suffix string) string {
	return "/api/v1/jobs/" + url.PathEscape(jobID) + suffix
}

// download sends an authenticated GET and returns the response. It has no
// overall timeout: an artifact can be large.
func (c *apiClient) download(path string) (*http.Response, error) {
	if err := transportsecurity.ValidateURL(c.apiURL, c.allowInsecureTransport, "coordinator API"); err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, c.apiURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	client := transportsecurity.HTTPClient(&http.Client{}, c.allowInsecureTransport, "coordinator API")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, &apiError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	return resp, nil
}

func listJobArtifacts(client *apiClient, jobID string) (artifactList, error) {
	var list artifactList
	err := client.doJSON(http.MethodGet, jobPath(jobID, "/artifacts"), nil, http.StatusOK, &list)
	return list, err
}

// ArtifactsCommand lists and downloads job artifacts.
var ArtifactsCommand = &cli.Command{
	Name:  "artifacts",
	Usage: "List and download the artifacts of a job",
	Flags: apiFlags(),
	Subcommands: []*cli.Command{
		{
			Name: "list", Usage: "List the artifacts of a job", ArgsUsage: "JOB_ID",
			Flags: append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() != 1 {
					return fmt.Errorf("give one JOB_ID argument")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				list, err := listJobArtifacts(client, ctx.Args().First())
				if err != nil {
					return err
				}
				return render(ctx.String("format"), list, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "ID\tPATH\tKIND\tSIZE\tSHA256\tSTATUS")
					for _, a := range list.Artifacts {
						digest := a.SHA256
						if len(digest) > 16 {
							digest = digest[:16] + "…"
						}
						fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", a.ArtifactID, a.Path, a.Kind, a.SizeBytes, digest, a.Status)
					}
				})
			},
		},
		{
			Name: "download", Usage: "Download one artifact and check its SHA-256 digest", ArgsUsage: "JOB_ID ARTIFACT_ID_OR_PATH",
			Flags: append(apiFlags(), &cli.StringFlag{Name: "output", Aliases: []string{"o"}, Usage: "Output file (default: the base name of the artifact path; '-' for stdout)"}),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() != 2 {
					return fmt.Errorf("give JOB_ID and ARTIFACT_ID_OR_PATH")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				jobID, ref := ctx.Args().Get(0), ctx.Args().Get(1)
				list, err := listJobArtifacts(client, jobID)
				if err != nil {
					return err
				}
				var found *artifactView
				for i := range list.Artifacts {
					if list.Artifacts[i].ArtifactID == ref || list.Artifacts[i].Path == ref {
						found = &list.Artifacts[i]
						break
					}
				}
				if found == nil {
					return fmt.Errorf("job %s has no artifact %q", jobID, ref)
				}
				resp, err := client.download(jobPath(jobID, "/artifacts/"+url.PathEscape(found.ArtifactID)))
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				output := ctx.String("output")
				if output == "" {
					output = filepath.Base(filepath.FromSlash(found.Path))
				}
				var dest io.Writer = os.Stdout
				var file *os.File
				if output != "-" {
					file, err = os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
					if err != nil {
						return err
					}
					dest = file
				}
				hash := sha256.New()
				_, copyErr := io.Copy(io.MultiWriter(dest, hash), resp.Body)
				if file != nil {
					if err := file.Close(); err != nil && copyErr == nil {
						copyErr = err
					}
				}
				if copyErr != nil {
					return copyErr
				}
				digest := hex.EncodeToString(hash.Sum(nil))
				if found.SHA256 != "" && digest != found.SHA256 {
					return fmt.Errorf("the digest of the download is sha256:%s, the coordinator recorded sha256:%s", digest, found.SHA256)
				}
				if output != "-" {
					fmt.Fprintf(os.Stderr, "wrote %s (sha256:%s)\n", output, digest)
				}
				return nil
			},
		},
	},
}

type testSummaryView struct {
	Reports             int      `json:"reports" yaml:"reports"`
	Total               int      `json:"total" yaml:"total"`
	Passed              int      `json:"passed" yaml:"passed"`
	Failed              int      `json:"failed" yaml:"failed"`
	Errored             int      `json:"errored" yaml:"errored"`
	Skipped             int      `json:"skipped" yaml:"skipped"`
	QuarantinedFailures int      `json:"quarantined_failures" yaml:"quarantined_failures"`
	Flaky               int      `json:"flaky" yaml:"flaky"`
	Truncated           bool     `json:"truncated" yaml:"truncated"`
	ParseErrors         []string `json:"parse_errors" yaml:"parse_errors"`
	QuarantinePolicy    string   `json:"quarantine_policy,omitempty" yaml:"quarantine_policy,omitempty"`
	QuarantineOverride  bool     `json:"quarantine_override" yaml:"quarantine_override"`
}

type testResultView struct {
	TestID      string `json:"test_id" yaml:"test_id"`
	Suite       string `json:"suite" yaml:"suite"`
	Classname   string `json:"classname" yaml:"classname"`
	Name        string `json:"name" yaml:"name"`
	Status      string `json:"status" yaml:"status"`
	DurationMs  int64  `json:"duration_ms" yaml:"duration_ms"`
	Message     string `json:"message,omitempty" yaml:"message,omitempty"`
	File        string `json:"file,omitempty" yaml:"file,omitempty"`
	Line        *int   `json:"line,omitempty" yaml:"line,omitempty"`
	Quarantined bool   `json:"quarantined" yaml:"quarantined"`
	Flaky       bool   `json:"flaky" yaml:"flaky"`
}

type quarantineView struct {
	QuarantineID     string     `json:"quarantine_id" yaml:"quarantine_id"`
	TestID           string     `json:"test_id" yaml:"test_id"`
	Suite            string     `json:"suite" yaml:"suite"`
	Classname        string     `json:"classname" yaml:"classname"`
	Name             string     `json:"name" yaml:"name"`
	OwnerPrincipalID string     `json:"owner_principal_id" yaml:"owner_principal_id"`
	Reason           string     `json:"reason" yaml:"reason"`
	ExpiresAt        time.Time  `json:"expires_at" yaml:"expires_at"`
	RemovedAt        *time.Time `json:"removed_at,omitempty" yaml:"removed_at,omitempty"`
}

func testDisplayName(suite, classname, name string) string {
	parts := []string{}
	for _, p := range []string{suite, classname, name} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " › ")
}

func projectFlag() cli.Flag {
	return &cli.StringFlag{Name: "project", Usage: "Project ID", Required: true}
}

func projectPath(projectID, suffix string) string {
	return "/api/v1/projects/" + url.PathEscape(projectID) + suffix
}

// TestsCommand shows test results, flaky tests, and test quarantines.
var TestsCommand = &cli.Command{
	Name:  "tests",
	Usage: "Show the test results of a job, the flaky tests of a project, and manage test quarantine",
	Flags: apiFlags(),
	Subcommands: []*cli.Command{
		{
			Name: "results", Usage: "Show the test results of a job (failures first)", ArgsUsage: "JOB_ID",
			Flags: append(apiFlags(), formatFlag(),
				&cli.BoolFlag{Name: "failed", Usage: "Show only failed and errored tests"},
				&cli.IntFlag{Name: "limit", Value: 200, Usage: "Most results"}),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() != 1 {
					return fmt.Errorf("give one JOB_ID argument")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				query := url.Values{"limit": {fmt.Sprint(ctx.Int("limit"))}}
				if ctx.Bool("failed") {
					query.Set("status", "fail,error")
				}
				var response struct {
					JobID   string           `json:"job_id" yaml:"job_id"`
					Summary *testSummaryView `json:"summary" yaml:"summary"`
					Results []testResultView `json:"results" yaml:"results"`
				}
				if err := client.doJSON(http.MethodGet, jobPath(ctx.Args().First(), "/tests?"+query.Encode()), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					if response.Summary == nil {
						fmt.Fprintln(w, "The job has no test reports.")
						return
					}
					s := response.Summary
					fmt.Fprintf(w, "%d tests: %d passed, %d failed, %d errors, %d skipped (%d failures quarantined, %d flaky)\n",
						s.Total, s.Passed, s.Failed, s.Errored, s.Skipped, s.QuarantinedFailures, s.Flaky)
					if s.QuarantineOverride {
						fmt.Fprintln(w, "The workflow node counts as a success: every failed test is quarantined (quarantine_policy: ignore_quarantined).")
					}
					for _, e := range s.ParseErrors {
						fmt.Fprintf(w, "report error: %s\n", e)
					}
					fmt.Fprintln(w, "STATUS\tTEST\tTEST ID\tFLAGS\tFIRST LINE")
					for _, r := range response.Results {
						flags := []string{}
						if r.Quarantined {
							flags = append(flags, "quarantined")
						}
						if r.Flaky {
							flags = append(flags, "flaky")
						}
						first := ""
						if lines := strings.SplitN(strings.TrimSpace(r.Message), "\n", 2); len(lines) > 0 {
							first = lines[0]
						}
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Status, testDisplayName(r.Suite, r.Classname, r.Name), r.TestID, strings.Join(flags, ","), first)
					}
				})
			},
		},
		{
			Name: "flaky", Usage: "List the flaky tests of a project",
			Flags: append(apiFlags(), formatFlag(), projectFlag(), &cli.IntFlag{Name: "days", Usage: "Detection window in days (default: the coordinator window)"}),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				path := projectPath(ctx.String("project"), "/tests/flaky")
				if days := ctx.Int("days"); days > 0 {
					path += fmt.Sprintf("?days=%d", days)
				}
				var response struct {
					ProjectID  string `json:"project_id" yaml:"project_id"`
					WindowDays int    `json:"window_days" yaml:"window_days"`
					Tests      []struct {
						TestID      string     `json:"test_id" yaml:"test_id"`
						Suite       string     `json:"suite" yaml:"suite"`
						Classname   string     `json:"classname" yaml:"classname"`
						Name        string     `json:"name" yaml:"name"`
						FlakyCount  int        `json:"flaky_count" yaml:"flaky_count"`
						TotalRuns   int        `json:"total_runs" yaml:"total_runs"`
						LastFlakyAt *time.Time `json:"last_flaky_at" yaml:"last_flaky_at"`
					} `json:"tests" yaml:"tests"`
				}
				if err := client.doJSON(http.MethodGet, path, nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "TEST\tTEST ID\tFLAKY COMMITS\tRUNS\tLAST FLAKY")
					for _, t := range response.Tests {
						fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\n", testDisplayName(t.Suite, t.Classname, t.Name), t.TestID, t.FlakyCount, t.TotalRuns, timeOrDash(t.LastFlakyAt))
					}
				})
			},
		},
		{
			Name:  "quarantine",
			Usage: "Manage test quarantine (an owner, a reason, and an expiry are required)",
			Flags: apiFlags(),
			Subcommands: []*cli.Command{
				{
					Name: "list", Usage: "List the test quarantines of a project",
					Flags: append(apiFlags(), formatFlag(), projectFlag(), &cli.BoolFlag{Name: "all", Usage: "Also show ended and expired quarantines"}),
					Action: func(ctx *cli.Context) error {
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						path := projectPath(ctx.String("project"), "/test-quarantines")
						if ctx.Bool("all") {
							path += "?all=true"
						}
						var response struct {
							ProjectID   string           `json:"project_id" yaml:"project_id"`
							Quarantines []quarantineView `json:"quarantines" yaml:"quarantines"`
						}
						if err := client.doJSON(http.MethodGet, path, nil, http.StatusOK, &response); err != nil {
							return err
						}
						return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
							fmt.Fprintln(w, "ID\tTEST\tTEST ID\tOWNER\tEXPIRES\tREASON")
							for _, q := range response.Quarantines {
								expires := q.ExpiresAt.UTC().Format(time.RFC3339)
								if q.RemovedAt != nil {
									expires += " (ended)"
								}
								fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", q.QuarantineID, testDisplayName(q.Suite, q.Classname, q.Name), q.TestID, q.OwnerPrincipalID, expires, q.Reason)
							}
						})
					},
				},
				{
					Name: "add", Usage: "Quarantine one test",
					Flags: append(apiFlags(), formatFlag(), projectFlag(),
						&cli.StringFlag{Name: "test-id", Usage: "Test ID (or give --suite, --class, and --name)"},
						&cli.StringFlag{Name: "suite", Usage: "Test suite path"},
						&cli.StringFlag{Name: "class", Usage: "Test class name"},
						&cli.StringFlag{Name: "name", Usage: "Test name"},
						&cli.StringFlag{Name: "reason", Usage: "Why the test is quarantined", Required: true},
						&cli.StringFlag{Name: "until", Usage: "Expiry as an RFC 3339 time"},
						&cli.StringFlag{Name: "for", Usage: "Expiry as a duration, for example 72h or 7d"},
						&cli.StringFlag{Name: "owner", Usage: "Owner principal ID (default: your principal; required with the admin token)"},
						&cli.StringFlag{Name: "workflow", Usage: "Workflow security ID, so that an owner of that workflow can quarantine"}),
					Action: func(ctx *cli.Context) error {
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						body := map[string]string{"test_id": ctx.String("test-id"), "suite": ctx.String("suite"), "classname": ctx.String("class"),
							"name": ctx.String("name"), "reason": ctx.String("reason"), "until": ctx.String("until"), "duration": ctx.String("for"),
							"owner_principal_id": ctx.String("owner"), "workflow_security_id": ctx.String("workflow")}
						var row quarantineView
						if err := client.doJSON(http.MethodPost, projectPath(ctx.String("project"), "/test-quarantines"), body, http.StatusCreated, &row); err != nil {
							return err
						}
						return render(ctx.String("format"), row, func(w *tabwriter.Writer) {
							fmt.Fprintf(w, "quarantined %s (%s) until %s\n", testDisplayName(row.Suite, row.Classname, row.Name), row.TestID, row.ExpiresAt.UTC().Format(time.RFC3339))
							fmt.Fprintf(w, "quarantine ID: %s\n", row.QuarantineID)
						})
					},
				},
				{
					Name: "remove", Usage: "End a test quarantine before its expiry", ArgsUsage: "QUARANTINE_ID",
					Flags: append(apiFlags(), projectFlag(), &cli.StringFlag{Name: "workflow", Usage: "Workflow security ID (for a workflow owner)"}),
					Action: func(ctx *cli.Context) error {
						if ctx.NArg() != 1 {
							return fmt.Errorf("give one QUARANTINE_ID argument")
						}
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						path := projectPath(ctx.String("project"), "/test-quarantines/"+url.PathEscape(ctx.Args().First()))
						if workflow := ctx.String("workflow"); workflow != "" {
							path += "?workflow_security_id=" + url.QueryEscape(workflow)
						}
						if err := client.doJSON(http.MethodDelete, path, nil, http.StatusNoContent, nil); err != nil {
							return err
						}
						fmt.Println("quarantine ended")
						return nil
					},
				},
			},
		},
	},
}

// ProvenanceCommand reads and verifies signed job provenance.
var ProvenanceCommand = &cli.Command{
	Name:  "provenance",
	Usage: "Get and verify the signed provenance of a job",
	Flags: apiFlags(),
	Subcommands: []*cli.Command{
		{
			Name: "get", Usage: "Write the DSSE provenance envelope of a job", ArgsUsage: "JOB_ID",
			Flags: append(apiFlags(), &cli.StringFlag{Name: "output", Aliases: []string{"o"}, Usage: "Output file (default: stdout)"}),
			Action: func(ctx *cli.Context) error {
				if ctx.NArg() != 1 {
					return fmt.Errorf("give one JOB_ID argument")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				data, err := fetchProvenance(client, ctx.Args().First())
				if err != nil {
					return err
				}
				if output := ctx.String("output"); output != "" {
					return os.WriteFile(output, data, 0o644)
				}
				_, err = os.Stdout.Write(append(data, '\n'))
				return err
			},
		},
		{
			Name:      "verify",
			Usage:     "Verify the signature of a provenance envelope and print its facts",
			ArgsUsage: "FILE",
			Description: "Verifies a DSSE envelope (a FILE, or --job JOB_ID from the coordinator) with the provenance keys " +
				"(--keys FILE, or GET /api/v1/provenance/keys of --api-url). The command exits with an error when the " +
				"signature does not verify or when a --require-* condition is not met.",
			Flags: append(apiFlags(), formatFlag(),
				&cli.StringFlag{Name: "job", Usage: "Read the provenance of this job from the coordinator"},
				&cli.StringFlag{Name: "keys", Usage: "A JWKS file with the provenance keys (default: read them from the coordinator)"},
				&cli.StringSliceFlag{Name: "require-ref-class", Usage: "Require this ref class (repeatable), for example production"},
				&cli.StringFlag{Name: "require-ci-origin", Usage: "Require this CI origin, for example base"},
				&cli.StringFlag{Name: "require-builder", Usage: "Require this builder ID"},
				&cli.StringFlag{Name: "require-repository", Usage: "Require this repository, as the verify output shows it, for example acme/app"},
				&cli.StringFlag{Name: "require-project", Usage: "Require this project ID (or the project name, with --require-org)"},
				&cli.StringFlag{Name: "require-org", Usage: "Require this organization name"},
				&cli.StringFlag{Name: "require-workflow", Usage: "Require this workflow security ID"},
				&cli.BoolFlag{Name: "require-coordinator-hashed", Usage: "Require that the --subject digest (or every subject) is coordinator_hashed: an uploaded artifact, not a digest that the job declared"},
				&cli.BoolFlag{Name: "allow-any-repository", Usage: "Let --subject pass with no --require-repository or --require-project (any project can attest a digest it declares)"},
				&cli.StringFlag{Name: "subject", Usage: "Require a subject with this digest (sha256:HEX). Needs --require-repository or --require-project"}),
			Action: func(ctx *cli.Context) error {
				var data []byte
				var err error
				var client *apiClient
				jobID := ctx.String("job")
				switch {
				case jobID != "" && ctx.NArg() > 0:
					return fmt.Errorf("give FILE or --job, not both")
				case jobID != "":
					if client, err = newAPIClient(ctx); err != nil {
						return err
					}
					if data, err = fetchProvenance(client, jobID); err != nil {
						return err
					}
				case ctx.NArg() == 1:
					if data, err = os.ReadFile(ctx.Args().First()); err != nil {
						return err
					}
				default:
					return fmt.Errorf("give FILE or --job JOB_ID")
				}
				keys, err := provenanceKeys(ctx, client)
				if err != nil {
					return err
				}
				envelope, err := provenance.ParseEnvelope(data)
				if err != nil {
					return err
				}
				payload, kid, err := provenance.Verify(envelope, keys)
				if err != nil {
					return err
				}
				statement, err := provenance.ParseStatement(envelope.PayloadType, payload)
				if err != nil {
					return err
				}
				req := provenance.Requirements{RefClasses: ctx.StringSlice("require-ref-class"), CIOrigin: ctx.String("require-ci-origin"),
					BuilderID: ctx.String("require-builder"), SubjectSHA256: ctx.String("subject"),
					Repository: ctx.String("require-repository"), Project: ctx.String("require-project"),
					Organization: ctx.String("require-org"), WorkflowSecurityID: ctx.String("require-workflow"),
					CoordinatorHashed: ctx.Bool("require-coordinator-hashed"), AllowAnyRepository: ctx.Bool("allow-any-repository")}
				checkErr := provenance.Check(statement, req)
				result := map[string]any{"verified": true, "key_id": kid, "statement": statement, "requirements_met": checkErr == nil}
				if checkErr != nil {
					result["requirement_error"] = checkErr.Error()
				}
				if err := render(ctx.String("format"), result, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "signature\tverified (key %s)\n", kid)
					for _, row := range provenance.Facts(statement) {
						fmt.Fprintf(w, "%s\t%s\n", row[0], row[1])
					}
				}); err != nil {
					return err
				}
				if checkErr != nil {
					return fmt.Errorf("requirement not met: %w", checkErr)
				}
				return nil
			},
		},
	},
}

func fetchProvenance(client *apiClient, jobID string) ([]byte, error) {
	resp, err := client.download(jobPath(jobID, "/provenance"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// provenanceKeys reads the JWKS from --keys or from the coordinator. The
// keys route is public, so a token is not needed for it.
func provenanceKeys(ctx *cli.Context, client *apiClient) (*jose.JSONWebKeySet, error) {
	var data []byte
	if path := ctx.String("keys"); path != "" {
		file, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		data = file
	} else {
		apiURL := lineageString(ctx, "api-url")
		if apiURL == "" {
			return nil, fmt.Errorf("give --keys FILE, or --api-url to read the provenance keys from the coordinator")
		}
		allowInsecure := lineageBool(ctx, "allow-insecure-transport")
		if err := transportsecurity.ValidateURL(apiURL, allowInsecure, "coordinator API"); err != nil {
			return nil, err
		}
		httpClient := transportsecurity.HTTPClient(&http.Client{Timeout: 30 * time.Second}, allowInsecure, "coordinator API")
		resp, err := httpClient.Get(strings.TrimSuffix(apiURL, "/") + "/api/v1/provenance/keys")
		if err != nil {
			return nil, fmt.Errorf("read the provenance keys: %w", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode != http.StatusOK {
			return nil, &apiError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
		}
		data = body
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("the keys are not a JWKS: %w", err)
	}
	if len(set.Keys) == 0 {
		return nil, fmt.Errorf("the JWKS has no keys")
	}
	return &set, nil
}
