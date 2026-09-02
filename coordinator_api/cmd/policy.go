package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/google/uuid"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

var PolicyCommand = &cli.Command{
	Name:  "policy",
	Usage: "Manage coordinator CI admission policy",
	Flags: apiFlags(),
	Subcommands: []*cli.Command{
		{
			Name:  "get",
			Usage: "Get a project CI policy",
			Flags: []cli.Flag{policyProjectFlag(), formatFlag()},
			Action: func(ctx *cli.Context) error {
				client, projectID, err := policyClientAndProject(ctx)
				if err != nil {
					return err
				}
				resp, err := client.GetCiPolicy(ctx.Context, csilapi.GetCiPolicyRequest{ProjectId: projectID})
				if err != nil {
					return err
				}
				return printPolicy(ctx.String("format"), resp.Policy)
			},
		},
		{
			Name:  "set",
			Usage: "Create or replace a project CI policy",
			Flags: []cli.Flag{policyProjectFlag(), &cli.StringFlag{Name: "file", Aliases: []string{"f"}, Required: true, Usage: "Policy YAML or JSON file; use - for stdin"}, &cli.StringFlag{Name: "expected-revision", Usage: "Reject the update if the current revision differs"}, policyFormatFlag()},
			Action: func(ctx *cli.Context) error {
				document, err := readPolicyInput(ctx.String("file"))
				if err != nil {
					return err
				}
				// The wire type is structured, so the CLI parses the local
				// file and sends the parsed value. YAML stays a valid
				// AUTHORING format for operator files (~/.config/reactorcide/
				// policies/*.yaml and apply-all.sh keep working); it is simply
				// no longer what travels or what is stored.
				wireDocument, err := policyFileToWireDocument(document)
				if err != nil {
					return err
				}
				client, projectID, err := policyClientAndProject(ctx)
				if err != nil {
					return err
				}
				resp, err := client.PutCiPolicy(ctx.Context, csilapi.PutCiPolicyRequest{ProjectId: projectID, Document: wireDocument, ExpectedRevision: optionalText(ctx.String("expected-revision"))})
				if err != nil {
					return err
				}
				return printPolicy(ctx.String("format"), resp.Policy)
			},
		},
		{
			Name:  "delete",
			Usage: "Delete a project CI policy",
			Flags: []cli.Flag{policyProjectFlag(), &cli.StringFlag{Name: "expected-revision", Usage: "Reject the delete if the current revision differs"}},
			Action: func(ctx *cli.Context) error {
				client, projectID, err := policyClientAndProject(ctx)
				if err != nil {
					return err
				}
				_, err = client.DeleteCiPolicy(ctx.Context, csilapi.DeleteCiPolicyRequest{ProjectId: projectID, ExpectedRevision: optionalText(ctx.String("expected-revision"))})
				if err != nil {
					return err
				}
				fmt.Printf("CI policy deleted for project %s\n", projectID)
				return nil
			},
		},
		{
			Name:  "validate",
			Usage: "Validate a policy file without changing coordinator state",
			Flags: []cli.Flag{&cli.StringFlag{Name: "file", Aliases: []string{"f"}, Required: true}},
			Action: func(ctx *cli.Context) error {
				document, err := readPolicyInput(ctx.String("file"))
				if err != nil {
					return err
				}
				policy, err := cipolicy.ParseDocument(document)
				if err != nil {
					return err
				}
				fmt.Printf("Policy is valid. Revision: %s\n", policy.Revision)
				return nil
			},
		},
		{
			Name:  "explain",
			Usage: "Evaluate a local policy file against supplied facts",
			Flags: []cli.Flag{&cli.StringFlag{Name: "file", Aliases: []string{"f"}, Required: true},
				&cli.StringFlag{Name: "workflow"}, &cli.StringSliceFlag{Name: "changed-path"}, &cli.StringFlag{Name: "event", Value: "pull_request_updated"},
				&cli.StringFlag{Name: "base-branch"}, &cli.StringFlag{Name: "head-repository", Value: "same"}, &cli.StringSliceFlag{Name: "actor"}, &cli.StringSliceFlag{Name: "approval"}},
			Action: explainPolicy,
		},
	},
}

func policyProjectFlag() cli.Flag {
	return &cli.StringFlag{Name: "project", Required: true, Usage: "Project ID, name, or repository URL"}
}

func policyFormatFlag() cli.Flag {
	return &cli.StringFlag{Name: "format", Value: "table", Usage: "Output format: table, json, yaml"}
}

func policyClientAndProject(ctx *cli.Context) (*csilapi.ReactorcideUiClient, string, error) {
	client, err := managementClient(ctx)
	if err != nil {
		return nil, "", err
	}
	wanted := strings.TrimSpace(ctx.String("project"))
	if _, err := uuid.Parse(wanted); err == nil {
		return client, wanted, nil
	}
	projects, err := client.ListProjects(ctx.Context, csilapi.ListProjectsRequest{})
	if err != nil {
		return nil, "", err
	}
	var matches []string
	for _, project := range projects.Projects {
		if project.ProjectId == wanted || project.Name == wanted || project.RepoUrl == wanted {
			matches = append(matches, project.ProjectId)
		}
	}
	if len(matches) == 0 {
		return nil, "", fmt.Errorf("project not found: %s", wanted)
	}
	if len(matches) > 1 {
		return nil, "", fmt.Errorf("project reference is ambiguous: %s", wanted)
	}
	return client, matches[0], nil
}

func readPolicyInput(name string) ([]byte, error) {
	if name == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(name)
}

// policyFileToWireDocument parses an authored policy file (YAML or JSON) and
// converts it to the structured wire type.
//
// It round-trips through JSON on purpose: cipolicy.Policy and
// csilapi.CiPolicyDocument carry identical `json:` tags, so this cannot drop a
// field that both sides name, whereas a hand-written field copy is a third
// place to forget one. cipolicy.ParseDocument still runs first, so an invalid
// policy is rejected locally with its real parse error before any request is
// made.
func policyFileToWireDocument(document []byte) (csilapi.CiPolicyDocument, error) {
	parsed, err := cipolicy.ParseDocument(document)
	if err != nil {
		return csilapi.CiPolicyDocument{}, err
	}
	canonical, err := cipolicy.CanonicalDocument(parsed)
	if err != nil {
		return csilapi.CiPolicyDocument{}, err
	}
	var wire csilapi.CiPolicyDocument
	if err := json.Unmarshal(canonical, &wire); err != nil {
		return csilapi.CiPolicyDocument{}, err
	}
	return wire, nil
}

func printPolicy(format string, policy csilapi.CiPolicyDetail) error {
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(policy)
	}
	if format != "yaml" && format != "table" {
		return fmt.Errorf("unsupported format %q", format)
	}
	encoded, err := yaml.Marshal(policy.Document)
	if err != nil {
		return err
	}
	if format == "table" {
		fmt.Printf("Project: %s\nRevision: %s\nUpdated: %s\n\n", policy.ProjectId, policy.Revision, policy.UpdatedAt)
	}
	_, err = os.Stdout.Write(encoded)
	return err
}

func explainPolicy(ctx *cli.Context) error {
	document, err := readPolicyInput(ctx.String("file"))
	if err != nil {
		return err
	}
	policy, err := cipolicy.ParseDocument(document)
	if err != nil {
		return err
	}
	actors, approvals := map[string]bool{}, map[string]bool{}
	for _, item := range ctx.StringSlice("actor") {
		actors[item] = true
	}
	for _, item := range ctx.StringSlice("approval") {
		approvals[item] = true
	}
	workflowIDs := []string{ctx.String("workflow")}
	if workflowIDs[0] == "" {
		workflowIDs = nil
		seen := map[string]bool{}
		for _, rule := range policy.HeadCI {
			for _, workflowID := range rule.Workflows {
				if !seen[workflowID] {
					seen[workflowID] = true
					workflowIDs = append(workflowIDs, workflowID)
				}
			}
		}
	}
	fmt.Printf("Policy revision: %s\nChanged CI paths: %s\n", policy.Revision, strings.Join(ctx.StringSlice("changed-path"), ", "))
	for _, workflowID := range workflowIDs {
		decision, err := cipolicy.Decide(policy, cipolicy.Facts{WorkflowID: workflowID, ChangedCIPaths: ctx.StringSlice("changed-path"), Event: ctx.String("event"), BaseBranch: ctx.String("base-branch"), HeadRepositoryRelation: ctx.String("head-repository"), ActorSubjects: actors, ApprovalSubjects: approvals})
		if err != nil {
			return err
		}
		fmt.Printf("\nWorkflow: %s\nCI source: %s\nProfile: %s\nWorker class: %s\nRule: %s\nAllowed head CI: %t\n", workflowID, decision.CISource, decision.Profile, decision.WorkerClass, decision.RuleID, decision.Allowed)
		if len(decision.BaseNodes) > 0 {
			names := make([]string, 0, len(decision.BaseNodes))
			for name := range decision.BaseNodes {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				grant := decision.BaseNodes[name]
				fmt.Printf("Base node: %s (ci_source %s, profile %s, workers %s)\n", name, grant.CISource, grant.Profile, grant.WorkerClass)
			}
		}
		for _, reason := range decision.Reasons {
			fmt.Printf("Reason: %s\n", reason)
		}
	}
	return nil
}
