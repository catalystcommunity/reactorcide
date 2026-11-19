package test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// commandVCSClient is a GitHub client fake for pull request commands.
type commandVCSClient struct {
	mu       sync.Mutex
	subjects map[string][]string
	comments []string
}

func (c *commandVCSClient) ParseWebhook(*http.Request) (*vcs.WebhookEvent, error) {
	return nil, errors.New("unused")
}
func (c *commandVCSClient) ValidateWebhook(*http.Request, string) error { return nil }
func (c *commandVCSClient) UpdateCommitStatus(context.Context, string, vcs.StatusUpdate) error {
	return nil
}
func (c *commandVCSClient) UpdatePRComment(_ context.Context, _ string, _ int, comment string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.comments = append(c.comments, comment)
	return nil
}
func (c *commandVCSClient) UpsertPRCommentByMarker(context.Context, string, int, string, string) error {
	return nil
}
func (c *commandVCSClient) GetPRInfo(_ context.Context, _ string, prNumber int) (*vcs.PullRequestInfo, error) {
	return &vcs.PullRequestInfo{Number: prNumber, State: "open", HeadSHA: "head1", BaseRef: "main", HeadRef: "feature",
		AuthorLogin: "pr-author"}, nil
}

// ListPRCommitLogins lists the commit authors, so a demote from a comment
// can check separation of duties.
func (c *commandVCSClient) ListPRCommitLogins(context.Context, string, int) ([]string, error) {
	return []string{"committer"}, nil
}
func (c *commandVCSClient) GetProvider() vcs.Provider { return vcs.GitHub }
func (c *commandVCSClient) ResolveActorSubjects(_ context.Context, _ string, login string) ([]string, error) {
	return c.subjects[login], nil
}
func (c *commandVCSClient) lastComment() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.comments) == 0 {
		return ""
	}
	return c.comments[len(c.comments)-1]
}

func TestRequiredSetPullRequestCommands(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		org, project, event := requiredSetProject(t, ctx, tx, "rs-command")
		_, err := ps.SetRequiredSetEnabled(ctx, project.ProjectID, org.OrgID, true, nil)
		require.NoError(t, err)
		require.NoError(t, ps.UpsertOrgRequiredSetSettings(ctx, &models.OrgRequiredSetSettings{OrgID: org.OrgID,
			Approvers: []string{"repository_write"}, ApproverRules: []models.RequiredSetApproverRule{}, MaxQuarantineDays: 30}))

		client := &commandVCSClient{subjects: map[string][]string{"alice": {"repository_write"}}}
		h := handlers.NewWebhookHandler(ps, corndogs.NewMockClient())
		h.AddVCSClient(vcs.GitHub, client)
		run := func(login, name, workflow string) error {
			_, err := h.Execute(ctx, approvals.Request{Project: project, PRNumber: 7, Channel: models.CIApprovalChannelComment,
				Provider: "github", Login: login, Command: approvals.Command{Name: name, Workflow: workflow}})
			return err
		}

		// An approver (a live VCS subject) promotes at once.
		require.NoError(t, run("alice", approvals.CommandPromote, "lint"))
		assert.True(t, requiredset.ForEvent(ctx, ps, event).Classify("lint").Blocking(), "lint is required after the promotion")

		// A person with no role and no approver subject is refused.
		assert.ErrorIs(t, run("mallory", approvals.CommandDemote, "lint"), approvals.ErrNotAllowed)
		assert.True(t, requiredset.ForEvent(ctx, ps, event).Classify("lint").Blocking())

		// An invalid change is refused with a fixed reason.
		var refused *approvals.RefusedError
		require.ErrorAs(t, run("alice", approvals.CommandPromote, "lint"), &refused)
		assert.NotContains(t, refused.Reason, "lint")

		// An author of the pull request cannot demote from a comment, even
		// as an approver (separation of duties).
		client.subjects["pr-author"] = []string{"repository_write"}
		client.subjects["committer"] = []string{"repository_write"}
		for _, author := range []string{"pr-author", "committer"} {
			require.ErrorAs(t, run(author, approvals.CommandDemote, "lint"), &refused)
			assert.Contains(t, refused.Reason, "author")
			assert.True(t, requiredset.ForEvent(ctx, ps, event).Classify("lint").Blocking(), "an author did not demote lint")
		}

		// The approver demotes.
		require.NoError(t, run("alice", approvals.CommandDemote, "lint"))
		assert.False(t, requiredset.ForEvent(ctx, ps, event).Classify("lint").Blocking(), "lint is advisory after the demotion")
		assert.Empty(t, client.comments, "Execute itself does not reply; the comment path does")
	})
}
