package owners

import (
	"context"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFromSpecNoBlocksIsKnownWithNoOwner(t *testing.T) {
	snapshot, err := FromSpec(nil, nil, "abc")
	require.NoError(t, err)
	assert.True(t, snapshot.Known())
	assert.False(t, snapshot.HasOwner())
	assert.Equal(t, "abc", snapshot.CISHA)
	assert.Nil(t, Subjects(snapshot), "no owner gives no subjects, so workflow_owner matches nothing")
}

func TestFromSpecNormalizes(t *testing.T) {
	snapshot, err := FromSpec(&Spec{Group: "reactorcide_group:payments-ci", Subjects: []string{"vcs_user:github/alice", "vcs_user:github/alice"}},
		&ContactSpec{Channel: " #pay ", HelpURL: "https://wiki.example/pay", Email: "pay@example.com"}, "sha")
	require.NoError(t, err)
	assert.Equal(t, "payments-ci", snapshot.GroupName())
	assert.Equal(t, []string{"vcs_user:github/alice"}, snapshot.Subjects)
	assert.Equal(t, &models.WorkflowContact{Channel: "#pay", HelpURL: "https://wiki.example/pay", Email: "pay@example.com"}, snapshot.Contact)
	assert.Equal(t, []string{"reactorcide_group:payments-ci", "vcs_user:github/alice"}, Subjects(snapshot))

	team, err := FromSpec(&Spec{Group: "vcs_team:Acme/Pay"}, nil, "")
	require.NoError(t, err)
	assert.Equal(t, "vcs_team:acme/pay", team.GroupName())
	assert.Equal(t, []string{"vcs_team:acme/pay"}, Subjects(team))
}

func TestFromSpecRejects(t *testing.T) {
	cases := []struct {
		owner   *Spec
		contact *ContactSpec
	}{
		{owner: &Spec{Group: "Payments CI"}},
		{owner: &Spec{Group: "vcs_team:acme"}},
		{owner: &Spec{Group: strings.Repeat("a", 201)}},
		{owner: &Spec{Subjects: []string{"*"}}},
		{owner: &Spec{Subjects: []string{"repository_write"}}},
		{owner: &Spec{Subjects: []string{"workflow_owner"}}},
		{owner: &Spec{Subjects: []string{"principal_kind:human"}}},
		{owner: &Spec{Subjects: make([]string, MaxSubjects+1)}},
		{contact: &ContactSpec{Channel: strings.Repeat("c", MaxChannelLength+1)}},
		{contact: &ContactSpec{Channel: "a\nb"}},
		{contact: &ContactSpec{HelpURL: "javascript:alert(1)"}},
		{contact: &ContactSpec{HelpURL: "https://user:pw@host/x"}},
		{contact: &ContactSpec{Email: "Pay <pay@example.com>"}},
		{contact: &ContactSpec{Email: "not-an-email"}},
	}
	for i, c := range cases {
		_, err := FromSpec(c.owner, c.contact, "")
		assert.Error(t, err, "case %d", i)
	}
}

func TestExpand(t *testing.T) {
	assert.Equal(t, []string{"project_owner", "reactorcide_group:pay"},
		Expand([]string{"project_owner", "workflow_owner", "project_owner"}, []string{"reactorcide_group:pay"}))
	assert.Equal(t, []string{"project_owner"}, Expand([]string{"project_owner", "workflow_owner"}, nil))
}

type fakeStore struct {
	snapshot *models.WorkflowOwnership
	err      error
}

func (f fakeStore) LatestWorkflowOwnership(context.Context, string, string) (*models.WorkflowOwnership, error) {
	return f.snapshot, f.err
}

func TestForWorkflow(t *testing.T) {
	group := "pay"
	got, err := ForWorkflow(context.Background(), fakeStore{snapshot: &models.WorkflowOwnership{Group: &group}}, "p", "w")
	require.NoError(t, err)
	assert.Equal(t, "pay", got.GroupName())

	got, err = ForWorkflow(context.Background(), fakeStore{err: store.ErrNotFound}, "p", "w")
	require.NoError(t, err)
	assert.False(t, got.Known(), "no trusted snapshot is unknown, not an error")

	got, err = ForWorkflow(context.Background(), struct{}{}, "p", "w")
	require.NoError(t, err)
	assert.False(t, got.Known())
}
