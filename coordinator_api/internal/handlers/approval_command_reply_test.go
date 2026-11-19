package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/ratelimit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetCommandReplyLimiters(t *testing.T, perPR, perProject *ratelimit.Limiter) {
	t.Helper()
	commandReplyLimiterOnce.Do(func() {})
	oldPR, oldProject := commandReplyPRLimiter, commandReplyProjLimiter
	commandReplyPRLimiter, commandReplyProjLimiter = perPR, perProject
	t.Cleanup(func() { commandReplyPRLimiter, commandReplyProjLimiter = oldPR, oldProject })
}

// A refused or invalid command gets a fixed reply that never repeats the
// comment, and replies are rate limited per pull request.
func TestCommandReplies(t *testing.T) {
	resetCommandReplyLimiters(t, ratelimit.New(1, 2), ratelimit.New(100, 100))
	replies := []string{}
	h := newApprovalHarness(t, map[string][]string{"maintainer": {"repository_write"}})
	h.client.UpdatePRCommentFunc = func(_ context.Context, _ string, _ int, comment string) error {
		replies = append(replies, comment)
		return nil
	}

	// An unknown command with unsafe text.
	h.comment(t, "someone", "/reactorcide frobnicate @everyone <img src=x>", 1)
	require.Len(t, replies, 1)
	assert.Contains(t, replies[0], "not a valid command")
	assert.NotContains(t, replies[0], "@everyone")
	assert.NotContains(t, replies[0], "<img")
	assert.Contains(t, replies[0], commandReplyMarker)
	assert.False(t, strings.HasPrefix(replies[0], "/reactorcide"), "a reply is never a command")

	// A refused command.
	h.comment(t, "drive-by", "/reactorcide approve-ci", 2)
	require.Len(t, replies, 2)
	assert.Contains(t, replies[1], "not allowed")

	// The third reply on the same pull request is over the limit.
	h.comment(t, "drive-by", "/reactorcide approve-ci", 3)
	assert.Len(t, replies, 2)

	// A normal comment gets no reply.
	resetCommandReplyLimiters(t, ratelimit.New(100, 100), ratelimit.New(100, 100))
	h.comment(t, "someone", "looks right to me", 4)
	assert.Len(t, replies, 2)

	// Invalid shift arguments: the reply does not repeat them.
	h.comment(t, "maintainer", "/reactorcide retry memory=@everyone", 5)
	require.Len(t, replies, 3)
	assert.NotContains(t, replies[2], "@everyone")

	// promote without a required set store: refused, with a reply.
	h.comment(t, "maintainer", "/reactorcide promote lint", 6)
	require.Len(t, replies, 4)
	assert.Contains(t, replies[3], "not available")
	assert.NotContains(t, replies[3], "lint")
}

// Separation of duties: an author of the pull request cannot demote a
// workflow from a comment on it. The pull request author, a commit author,
// and the person who pushed the head are authors. Another person passes this
// rule (here the harness has no required set store, so it gets "not
// available" next). Promote is not changed.
func TestDemoteCommand_RefusedForPullRequestAuthors(t *testing.T) {
	resetCommandReplyLimiters(t, ratelimit.New(100, 100), ratelimit.New(100, 100))
	replies := []string{}
	h := newApprovalHarness(t, map[string][]string{
		"author": {"repository_write"}, "committer": {"repository_write"}, "maintainer": {"repository_write"}})
	h.client.commitLogins = []string{"Committer"}
	h.client.UpdatePRCommentFunc = func(_ context.Context, _ string, _ int, comment string) error {
		replies = append(replies, comment)
		return nil
	}

	h.comment(t, "author", "/reactorcide demote lint", 1)
	require.Len(t, replies, 1)
	assert.Contains(t, replies[0], "an author of the pull request cannot demote")
	assert.NotContains(t, replies[0], "lint")

	h.comment(t, "committer", "/reactorcide demote lint", 2)
	require.Len(t, replies, 2)
	assert.Contains(t, replies[1], "an author of the pull request cannot demote")

	h.comment(t, "maintainer", "/reactorcide demote lint", 3)
	require.Len(t, replies, 3)
	assert.Contains(t, replies[2], "not available")

	// An author can still propose a promotion.
	h.comment(t, "author", "/reactorcide promote lint", 4)
	require.Len(t, replies, 4)
	assert.Contains(t, replies[3], "not available")
}

// When the commit authors cannot be listed, a demote from a comment fails
// closed.
func TestDemoteCommand_RefusedWhenAuthorsUnverified(t *testing.T) {
	resetCommandReplyLimiters(t, ratelimit.New(100, 100), ratelimit.New(100, 100))
	replies := []string{}
	h := newApprovalHarness(t, map[string][]string{"maintainer": {"repository_write"}})
	h.client.commitLoginsErr = errors.New("listing failed")
	h.client.UpdatePRCommentFunc = func(_ context.Context, _ string, _ int, comment string) error {
		replies = append(replies, comment)
		return nil
	}
	h.comment(t, "maintainer", "/reactorcide demote lint", 1)
	require.Len(t, replies, 1)
	assert.Contains(t, replies[0], "could not be verified")
}
