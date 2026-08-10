package cmd

import (
	"fmt"
	"net/http"
	"time"

	"github.com/urfave/cli/v2"
)

type approvalCreateRequest struct {
	Organization     string     `json:"organization"`
	Project          string     `json:"project"`
	PRNumber         int        `json:"pr_number"`
	HeadRepository   string     `json:"head_repository"`
	HeadSHA          string     `json:"head_sha"`
	BaseSHA          string     `json:"base_sha"`
	PolicyRevision   string     `json:"policy_revision"`
	WorkflowScope    string     `json:"workflow_scope"`
	ExecutionProfile string     `json:"execution_profile"`
	ApproverSubject  string     `json:"approver_subject"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

var ApprovalsCommand = &cli.Command{Name: "approvals", Usage: "Manage SHA-bound CI approvals", Subcommands: []*cli.Command{{
	Name: "create", Flags: append(apiFlags(),
		&cli.StringFlag{Name: "org", Required: true}, &cli.StringFlag{Name: "project", Required: true},
		&cli.IntFlag{Name: "pr", Required: true}, &cli.StringFlag{Name: "head-repository", Required: true},
		&cli.StringFlag{Name: "head-sha", Required: true}, &cli.StringFlag{Name: "base-sha", Required: true},
		&cli.StringFlag{Name: "policy-revision", Required: true}, &cli.StringFlag{Name: "workflow", Required: true},
		&cli.StringFlag{Name: "profile", Required: true}, &cli.StringFlag{Name: "subject", Required: true},
		&cli.TimestampFlag{Name: "expires-at", Layout: time.RFC3339}),
	Action: func(ctx *cli.Context) error {
		client, err := newAPIClient(ctx)
		if err != nil {
			return err
		}
		request := approvalCreateRequest{Organization: ctx.String("org"), Project: ctx.String("project"),
			PRNumber: ctx.Int("pr"), HeadRepository: ctx.String("head-repository"), HeadSHA: ctx.String("head-sha"),
			BaseSHA: ctx.String("base-sha"), PolicyRevision: ctx.String("policy-revision"), WorkflowScope: ctx.String("workflow"),
			ExecutionProfile: ctx.String("profile"), ApproverSubject: ctx.String("subject"), ExpiresAt: ctx.Timestamp("expires-at")}
		var response struct {
			ApprovalID string `json:"approval_id"`
		}
		if err := client.doJSON(http.MethodPost, "/api/v1/approvals", request, http.StatusCreated, &response); err != nil {
			return err
		}
		fmt.Printf("CI approval created: %s\n", response.ApprovalID)
		return nil
	},
}}}
