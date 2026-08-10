package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/urfave/cli/v2"
)

func loadPolicy(root string) (*cipolicy.Policy, error) {
	files := map[string][]byte{}
	mainPath := filepath.Join(root, ".reactorcide", "policy.yaml")
	data, err := os.ReadFile(mainPath)
	if err != nil {
		return nil, err
	}
	files[".reactorcide/policy.yaml"] = data
	fragments, err := filepath.Glob(filepath.Join(root, ".reactorcide", "policies", "*.yaml"))
	if err != nil {
		return nil, err
	}
	for _, name := range fragments {
		data, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		files[filepath.ToSlash(strings.TrimPrefix(name, root+string(filepath.Separator)))] = data
	}
	return cipolicy.Parse(files)
}

var PolicyCommand = &cli.Command{Name: "policy", Usage: "Inspect trusted CI policy", Subcommands: []*cli.Command{
	{Name: "validate", Flags: []cli.Flag{&cli.StringFlag{Name: "project"}, &cli.StringFlag{Name: "path", Value: "."}}, Action: func(ctx *cli.Context) error {
		policy, err := loadPolicy(ctx.String("path"))
		if err != nil {
			return err
		}
		fmt.Printf("Policy is valid. Revision: %s\n", policy.Revision)
		return nil
	}},
	{Name: "explain", Flags: []cli.Flag{&cli.StringFlag{Name: "project"}, &cli.IntFlag{Name: "pr"}, &cli.StringFlag{Name: "path", Value: "."},
		&cli.StringFlag{Name: "workflow"}, &cli.StringSliceFlag{Name: "changed-path"}, &cli.StringFlag{Name: "event", Value: "pull_request_updated"},
		&cli.StringFlag{Name: "base-branch"}, &cli.StringFlag{Name: "head-repository", Value: "same"}, &cli.StringSliceFlag{Name: "actor"}, &cli.StringSliceFlag{Name: "approval"}},
		Action: func(ctx *cli.Context) error {
			policy, err := loadPolicy(ctx.String("path"))
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
			fmt.Printf("Project: %s\nPull request: %d\nBase policy revision: %s\n", ctx.String("project"), ctx.Int("pr"), policy.Revision)
			fmt.Printf("Changed CI paths: %s\n", strings.Join(ctx.StringSlice("changed-path"), ", "))
			for _, workflowID := range workflowIDs {
				decision, err := cipolicy.Decide(policy, cipolicy.Facts{WorkflowID: workflowID, ChangedCIPaths: ctx.StringSlice("changed-path"), Event: ctx.String("event"), BaseBranch: ctx.String("base-branch"), HeadRepositoryRelation: ctx.String("head-repository"), ActorSubjects: actors, ApprovalSubjects: approvals})
				if err != nil {
					return err
				}
				fmt.Printf("\nWorkflow: %s\nCI source: %s\nProfile: %s\nWorker class: %s\nRule: %s\nAllowed head CI: %t\n", workflowID, decision.CISource, decision.Profile, decision.WorkerClass, decision.RuleID, decision.Allowed)
				for _, reason := range decision.Reasons {
					fmt.Printf("Reason: %s\n", reason)
				}
			}
			return nil
		}},
}}
