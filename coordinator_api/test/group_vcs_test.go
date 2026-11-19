package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcsteams"
)

// fakeTeamAPI is a GitHub API with one team. Members are served 2 per page
// with a Link header, so the sync must follow pagination.
type fakeTeamAPI struct {
	mu      sync.Mutex
	members []string
	gone    bool
	server  *httptest.Server
}

func newFakeTeamAPI(t *testing.T, owner, slug string, members ...string) *fakeTeamAPI {
	api := &fakeTeamAPI{members: members}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		base := "/orgs/" + owner + "/teams/" + slug
		if api.gone || !strings.HasPrefix(r.URL.Path, base) {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == base {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 4242, "slug": slug, "name": slug})
			return
		}
		page := 1
		fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
		start, end := (page-1)*2, page*2
		if end >= len(api.members) {
			end = len(api.members)
		} else {
			w.Header().Set("Link", fmt.Sprintf(`<%s%s/members?per_page=2&page=%d>; rel="next"`, api.server.URL, base, page+1))
		}
		out := []map[string]string{}
		if start < len(api.members) {
			for _, login := range api.members[start:end] {
				out = append(out, map[string]string{"login": login})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(api.server.Close)
	return api
}

func (api *fakeTeamAPI) set(members ...string) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.members = members
}

func (api *fakeTeamAPI) syncer(t *testing.T) *vcsteams.Syncer {
	client, err := vcs.NewGitHubClient(vcs.Config{Provider: vcs.GitHub, Token: "test", BaseURL: api.server.URL, AllowInsecureTransport: true})
	require.NoError(t, err)
	ps := postgres_store.PostgresStore
	return &vcsteams.Syncer{Store: ps, Principals: principals.NewResolver(ps, 0),
		Clients: func(ctx context.Context, orgID, provider, owner string) (vcs.TeamMemberLister, error) {
			return client, nil
		}}
}

type teamFixture struct {
	org   *models.Organization
	group *models.Group
	link  *models.GroupVCSLink
}

func newTeamFixture(t *testing.T, ctx context.Context, name string) teamFixture {
	ps := postgres_store.PostgresStore
	org := newPrincipalOrg(t, ctx, name)
	require.NoError(t, ps.CreateProject(ctx, &models.Project{Name: name + "-app", RepoURL: "github.com/acme/" + name, OrgID: org.OrgID}))
	group := &models.Group{OrgID: org.OrgID, Name: "platform"}
	require.NoError(t, ps.CreateGroup(ctx, group))
	link, err := vcsteams.CreateLink(ctx, ps, group, "github", "Acme/Platform", "")
	require.NoError(t, err)
	return teamFixture{org: org, group: group, link: link}
}

func memberSources(t *testing.T, ctx context.Context, groupID string) map[string]string {
	details, err := postgres_store.PostgresStore.ListGroupMemberDetails(ctx, groupID)
	require.NoError(t, err)
	out := map[string]string{}
	for _, d := range details {
		key := d.Username
		if key == "" {
			key = d.Name
		}
		out[key] = d.Source
	}
	return out
}

func TestGroupVCSLinkValidation(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		fx := newTeamFixture(t, ctx, "gv-valid")
		assert.Equal(t, "acme", fx.link.Owner)
		assert.Equal(t, "platform", fx.link.TeamSlug)
		_, err := vcsteams.CreateLink(ctx, ps, fx.group, "github", "acme/platform", "")
		assert.ErrorIs(t, err, store.ErrAlreadyExists)
		_, err = vcsteams.CreateLink(ctx, ps, fx.group, "github", "other/platform", "")
		assert.ErrorIs(t, err, vcsteams.ErrNoOwnerProject, "the org has no repository under the owner")
		_, err = vcsteams.CreateLink(ctx, ps, fx.group, "gitlab", "acme/platform", "")
		assert.ErrorIs(t, err, vcsteams.ErrUnsupportedProvider)
		_, err = vcsteams.CreateLink(ctx, ps, fx.group, "github", "acme", "")
		assert.ErrorIs(t, err, vcsteams.ErrInvalidTeam)

		groups, err := ps.ListGroupsLinkedToVCSTeam(ctx, fx.org.OrgID, "vcs_team:Acme/Platform")
		require.NoError(t, err)
		require.Len(t, groups, 1)
		assert.Equal(t, fx.group.GroupID, groups[0].GroupID)
		groups, err = ps.ListGroupsLinkedToVCSTeam(ctx, fx.org.OrgID, "acme/other")
		require.NoError(t, err)
		assert.Empty(t, groups)
		other := newPrincipalOrg(t, ctx, "gv-valid-other")
		groups, err = ps.ListGroupsLinkedToVCSTeam(ctx, other.OrgID, "acme/platform")
		require.NoError(t, err)
		assert.Empty(t, groups, "another organization")
	})
}

func TestGroupVCSSyncWithFakeGitHub(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		fx := newTeamFixture(t, ctx, "gv-sync")
		du := &DataUtils{db: tx}
		manual, err := du.CreateUser(DataSetup{"Username": "gv-manual", "Email": "gvm@example.com"})
		require.NoError(t, err)
		require.NoError(t, ps.AddGroupMember(ctx, fx.group.GroupID, manual.UserID))
		// A user with a linked GitHub login: the synced membership lands on
		// the principal of the user.
		linked, err := du.CreateUser(DataSetup{"Username": "gv-linked", "Email": "gvl@example.com"})
		require.NoError(t, err)
		require.NoError(t, ps.LinkPrincipalIdentity(ctx, &models.PrincipalIdentity{IdentityType: models.IdentityTypeVCS,
			IdentityValue: "github/carol", PrincipalID: linked.UserID, VerifiedBy: "admin"}))

		api := newFakeTeamAPI(t, "acme", "platform", "alice", "bob", "carol", "gv-manual-login")
		syncer := api.syncer(t)
		outcome, err := syncer.SyncLinkID(ctx, fx.link.LinkID)
		require.NoError(t, err)
		require.Empty(t, outcome.Error)
		assert.Equal(t, 4, outcome.Members, "all pages are read")
		assert.Len(t, outcome.Added, 4)

		sources := memberSources(t, ctx, fx.group.GroupID)
		assert.Equal(t, "manual", sources["gv-manual"])
		assert.Equal(t, "vcs_sync", sources["gv-linked"])
		assert.Equal(t, "vcs_sync", sources["github/alice"], "a login seen in a team becomes a principal")
		assert.Equal(t, "vcs_sync", sources["github/bob"])

		link, err := ps.GetGroupVCSLink(ctx, fx.link.LinkID)
		require.NoError(t, err)
		require.NotNil(t, link.LastSyncedAt)
		require.NotNil(t, link.ExternalTeamID)
		assert.EqualValues(t, 4242, *link.ExternalTeamID)
		assert.Equal(t, 4, link.MemberCount)
		assert.Empty(t, link.LastError)

		// Subjects: the principal of a synced login has the group.
		alice, err := ps.ResolvePrincipalIdentity(ctx, models.IdentityTypeVCS, "github/alice")
		require.NoError(t, err)
		groups, err := ps.ListGroupsForPrincipal(ctx, alice.PrincipalID)
		require.NoError(t, err)
		require.Len(t, groups, 1)
		assert.Equal(t, "platform", groups[0].Name, "reactorcide_group:platform")

		// RBAC: an org role on the group applies to the linked user.
		orgID := fx.org.OrgID
		require.NoError(t, ps.CreateRoleAssignment(ctx, &models.RoleAssignment{PrincipalType: models.PrincipalTypeGroup,
			PrincipalID: fx.group.GroupID, ScopeType: models.ScopeTypeOrg, ScopeID: &orgID, Role: models.RoleAdmin}))
		resolver := authz.NewResolver(ps)
		admin, err := resolver.IsOrgAdmin(ctx, authz.Identity{UserID: linked.UserID}, orgID)
		require.NoError(t, err)
		assert.True(t, admin, "a role of a linked group applies to a synced member")

		// Removal from the team removes the synced membership at the next
		// sync. The manual member stays.
		api.set("alice", "gv-manual-login")
		outcome, err = syncer.SyncLinkID(ctx, fx.link.LinkID)
		require.NoError(t, err)
		require.Empty(t, outcome.Error)
		assert.Len(t, outcome.Removed, 2)
		sources = memberSources(t, ctx, fx.group.GroupID)
		assert.NotContains(t, sources, "gv-linked")
		assert.NotContains(t, sources, "github/bob")
		assert.Equal(t, "manual", sources["gv-manual"])
		admin, err = resolver.IsOrgAdmin(ctx, authz.Identity{UserID: linked.UserID}, orgID)
		require.NoError(t, err)
		assert.False(t, admin, "the role goes with the membership")

		// History rows for attribution.
		history, err := ps.ListGroupMembershipHistory(ctx, store.GroupMembershipHistoryFilter{GroupID: fx.group.GroupID, PrincipalID: linked.UserID})
		require.NoError(t, err)
		require.Len(t, history, 2)
		assert.Equal(t, models.GroupMembershipRemoved, history[0].Change)
		assert.Equal(t, models.GroupMembershipAdded, history[1].Change)
		assert.Equal(t, models.GroupMemberSourceVCSSync, history[1].Source)
		assert.Equal(t, "platform", history[1].GroupName)
		at, err := ps.ListGroupsOfPrincipalAt(ctx, linked.UserID, history[1].ChangedAt)
		require.NoError(t, err)
		require.Len(t, at, 1, "the principal was in the group after the first sync")
		now, err := ps.ListGroupsOfPrincipalAt(ctx, linked.UserID, time.Now().Add(time.Hour))
		require.NoError(t, err)
		assert.Empty(t, now)

		// The team is deleted: the next sync gets 404 and removes the synced
		// members at once.
		api.mu.Lock()
		api.gone = true
		api.mu.Unlock()
		outcome, err = syncer.SyncLinkID(ctx, fx.link.LinkID)
		require.NoError(t, err)
		assert.Contains(t, outcome.Error, "not found")
		sources = memberSources(t, ctx, fx.group.GroupID)
		assert.Equal(t, map[string]string{"gv-manual": "manual"}, sources)
		link, err = ps.GetGroupVCSLink(ctx, fx.link.LinkID)
		require.NoError(t, err)
		assert.NotEmpty(t, link.LastError)
		assert.Equal(t, 1, link.FailureCount)
		require.NotNil(t, link.NextSyncAt)
		assert.True(t, link.NextSyncAt.After(time.Now()), "backoff")
	})
}

func TestGroupVCSManualAndSyncedMembership(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		fx := newTeamFixture(t, ctx, "gv-manual")
		du := &DataUtils{db: tx}
		user, err := du.CreateUser(DataSetup{"Username": "gv-both", "Email": "gvb@example.com"})
		require.NoError(t, err)
		team := []models.GroupVCSTeamMember{{PrincipalID: user.UserID, Login: "both"}}
		now := time.Now().UTC()
		_, err = ps.ApplyGroupVCSSync(ctx, fx.link.LinkID, team, nil, now, now.Add(time.Hour))
		require.NoError(t, err)

		// A synced-only member cannot be removed by hand.
		assert.ErrorIs(t, ps.RemoveGroupMember(ctx, fx.group.GroupID, user.UserID), store.ErrConflict)
		// A manual add makes it manual; then a team removal keeps it.
		require.NoError(t, ps.AddGroupMember(ctx, fx.group.GroupID, user.UserID))
		assert.Equal(t, "manual", memberSources(t, ctx, fx.group.GroupID)["gv-both"])
		_, err = ps.ApplyGroupVCSSync(ctx, fx.link.LinkID, nil, nil, now, now.Add(time.Hour))
		require.NoError(t, err)
		assert.Equal(t, "manual", memberSources(t, ctx, fx.group.GroupID)["gv-both"], "a sync never removes a manual member")

		// Manual removal while the team still has the member keeps the
		// synced membership.
		_, err = ps.ApplyGroupVCSSync(ctx, fx.link.LinkID, team, nil, now, now.Add(time.Hour))
		require.NoError(t, err)
		require.NoError(t, ps.RemoveGroupMember(ctx, fx.group.GroupID, user.UserID))
		assert.Equal(t, "vcs_sync", memberSources(t, ctx, fx.group.GroupID)["gv-both"])

		// Two linked teams: removal from one keeps the membership.
		second, err := vcsteams.CreateLink(ctx, ps, fx.group, "github", "acme/infra", "")
		require.NoError(t, err)
		_, err = ps.ApplyGroupVCSSync(ctx, second.LinkID, team, nil, now, now.Add(time.Hour))
		require.NoError(t, err)
		_, err = ps.ApplyGroupVCSSync(ctx, fx.link.LinkID, nil, nil, now, now.Add(time.Hour))
		require.NoError(t, err)
		assert.Equal(t, "vcs_sync", memberSources(t, ctx, fx.group.GroupID)["gv-both"])
		details, err := ps.ListGroupMemberDetails(ctx, fx.group.GroupID)
		require.NoError(t, err)
		require.Len(t, details, 1)
		assert.Equal(t, []string{"vcs_team:acme/infra"}, details[0].Teams)

		// Unlink removes what only that link gave.
		require.NoError(t, ps.DeleteGroupVCSLink(ctx, second.LinkID))
		assert.Empty(t, memberSources(t, ctx, fx.group.GroupID))

		// Stale members: a failure after MaxStale clears them.
		_, err = ps.ApplyGroupVCSSync(ctx, fx.link.LinkID, team, nil, now.Add(-48*time.Hour), now.Add(-time.Minute))
		require.NoError(t, err)
		result, err := ps.RecordGroupVCSSyncFailure(ctx, fx.link.LinkID, "boom", now, now.Add(time.Minute), true)
		require.NoError(t, err)
		assert.Equal(t, []string{user.UserID}, result.Removed)
	})
}

func TestGroupVCSDueAndWebhookHint(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		fx := newTeamFixture(t, ctx, "gv-due")
		now := time.Now().UTC()
		due, err := ps.ListDueGroupVCSLinks(ctx, now, 100)
		require.NoError(t, err)
		assert.True(t, containsLink(due, fx.link.LinkID), "a new link is due")
		id := int64(77)
		_, err = ps.ApplyGroupVCSSync(ctx, fx.link.LinkID, nil, &id, now, now.Add(time.Hour))
		require.NoError(t, err)
		due, err = ps.ListDueGroupVCSLinks(ctx, now, 100)
		require.NoError(t, err)
		assert.False(t, containsLink(due, fx.link.LinkID))

		// A membership event makes the link due.
		count, err := vcsteams.HandleWebhook(ctx, ps, "github", &vcs.TeamEventInfo{Kind: "membership", Action: "removed", Owner: "acme", Slug: "platform", TeamID: 77})
		require.NoError(t, err)
		assert.Equal(t, 1, count)
		due, err = ps.ListDueGroupVCSLinks(ctx, time.Now().UTC().Add(time.Second), 100)
		require.NoError(t, err)
		assert.True(t, containsLink(due, fx.link.LinkID))

		// A rename moves the link by team ID.
		count, err = vcsteams.HandleWebhook(ctx, ps, "github", &vcs.TeamEventInfo{Kind: "team", Action: "edited", Owner: "acme", Slug: "platform-eng", TeamID: 77, Renamed: true})
		require.NoError(t, err)
		assert.Equal(t, 1, count)
		link, err := ps.GetGroupVCSLink(ctx, fx.link.LinkID)
		require.NoError(t, err)
		assert.Equal(t, "platform-eng", link.TeamSlug)
		count, err = vcsteams.HandleWebhook(ctx, ps, "github", &vcs.TeamEventInfo{Kind: "membership", Owner: "other", Slug: "platform-eng"})
		require.NoError(t, err)
		assert.Zero(t, count, "another owner")
	})
}

// A webhook-only VCS principal with a synced membership can still merge into
// a user. The membership moves and stays synced.
func TestGroupVCSMergeKeepsSource(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		fx := newTeamFixture(t, ctx, "gv-merge")
		seen, _, err := ps.EnsureVCSPrincipal(ctx, "github", "dana")
		require.NoError(t, err)
		now := time.Now().UTC()
		_, err = ps.ApplyGroupVCSSync(ctx, fx.link.LinkID, []models.GroupVCSTeamMember{{PrincipalID: seen.PrincipalID, Login: "dana"}}, nil, now, now.Add(time.Hour))
		require.NoError(t, err)
		du := &DataUtils{db: tx}
		user, err := du.CreateUser(DataSetup{"Username": "gv-dana", "Email": "dana@example.com"})
		require.NoError(t, err)
		require.NoError(t, ps.LinkPrincipalIdentity(ctx, &models.PrincipalIdentity{IdentityType: models.IdentityTypeVCS,
			IdentityValue: "github/dana", PrincipalID: user.UserID, VerifiedBy: "admin"}))
		assert.Equal(t, map[string]string{"gv-dana": "vcs_sync"}, memberSources(t, ctx, fx.group.GroupID))
		members, err := ps.ListGroupVCSLinkMembers(ctx, fx.link.LinkID)
		require.NoError(t, err)
		require.Len(t, members, 1)
		assert.Equal(t, user.UserID, members[0].PrincipalID)
		// The team removes the person: the moved membership goes.
		_, err = ps.ApplyGroupVCSSync(ctx, fx.link.LinkID, nil, nil, now, now.Add(time.Hour))
		require.NoError(t, err)
		assert.Empty(t, memberSources(t, ctx, fx.group.GroupID))
	})
}

func containsLink(links []models.GroupVCSLink, id string) bool {
	for _, link := range links {
		if link.LinkID == id {
			return true
		}
	}
	return false
}

// The REST routes with an admin token (no logins).
func TestGroupVCSRestAPI(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		du := &DataUtils{db: tx}
		admin, err := du.CreateUser(DataSetup{"Username": "gv-rest-admin", "Email": "gvr@example.com"})
		require.NoError(t, err)
		auth, err := createTokenAuthHeader(ctx, tx, admin.UserID)
		require.NoError(t, err)
		org := newPrincipalOrg(t, ctx, "gv-rest")
		require.NoError(t, ps.CreateProject(ctx, &models.Project{Name: "gv-rest-app", RepoURL: "github.com/acme/gv-rest", OrgID: org.OrgID}))
		group := &models.Group{OrgID: org.OrgID, Name: "platform"}
		require.NoError(t, ps.CreateGroup(ctx, group))
		base := "/api/v1/organizations/gv-rest/groups/platform"

		rr := principalRequest(t, ctx, http.MethodPost, base+"/teams", auth, map[string]string{"team": "nobody/x"})
		assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, base+"/teams", auth, map[string]string{"team": "acme/platform"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		var link struct {
			LinkID  string `json:"link_id"`
			Subject string `json:"subject"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &link))
		assert.Equal(t, "vcs_team:acme/platform", link.Subject)
		rr = principalRequest(t, ctx, http.MethodPost, base+"/teams", auth, map[string]string{"team": "acme/platform"})
		assert.Equal(t, http.StatusConflict, rr.Code)

		bob, _, err := ps.EnsureVCSPrincipal(ctx, "github", "gv-bob")
		require.NoError(t, err)
		now := time.Now().UTC()
		_, err = ps.ApplyGroupVCSSync(ctx, link.LinkID, []models.GroupVCSTeamMember{{PrincipalID: bob.PrincipalID, Login: "gv-bob"}}, nil, now, now.Add(time.Hour))
		require.NoError(t, err)
		require.NoError(t, ps.AddGroupMember(ctx, group.GroupID, admin.UserID))

		rr = principalRequest(t, ctx, http.MethodGet, base+"/members", auth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var members struct {
			Members []struct {
				Name   string   `json:"name"`
				Source string   `json:"source"`
				Teams  []string `json:"teams"`
			} `json:"members"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &members))
		require.Len(t, members.Members, 2)

		rr = principalRequest(t, ctx, http.MethodGet, base+"/teams", auth, nil)
		require.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Body.String(), `"member_count":1`)

		rr = principalRequest(t, ctx, http.MethodGet, base+"/history", auth, nil)
		require.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Body.String(), bob.PrincipalID)

		// The sync route answers with a result for each link. The test
		// deployment has no GitHub credential, so the result has an error.
		rr = principalRequest(t, ctx, http.MethodPost, base+"/sync", auth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), `"results"`)

		rr = principalRequest(t, ctx, http.MethodDelete, base+"/teams/acme/platform", auth, nil)
		require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())
		assert.Equal(t, map[string]string{"gv-rest-admin": "manual"}, memberSources(t, ctx, group.GroupID))

		var audits int64
		require.NoError(t, tx.Raw(`SELECT count(*) FROM audit_events WHERE org_id = ? AND action IN ('group.team_link', 'group.team_unlink')`, org.OrgID).Scan(&audits).Error)
		assert.EqualValues(t, 2, audits)
	})
}

// WP8: the VCS owner allowlist of an organization. Only a global admin (or
// the admin token) reads or sets it.
func TestVCSOwnerAllowlistREST(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		du := &DataUtils{db: tx}
		admin, err := du.CreateUser(DataSetup{"Username": "gv-allow-admin", "Email": "gva@example.com"})
		require.NoError(t, err)
		adminAuth, err := createTokenAuthHeader(ctx, tx, admin.UserID)
		require.NoError(t, err)
		org := newPrincipalOrg(t, ctx, "gv-allow")
		base := "/api/v1/organizations/gv-allow/vcs-owner-allowlist"

		rr := principalRequest(t, ctx, http.MethodGet, base, adminAuth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.JSONEq(t, `{"owners":[]}`, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPut, base, adminAuth, map[string]any{"owners": []string{"Acme", "acme", " other "}})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.JSONEq(t, `{"owners":["acme","other"]}`, rr.Body.String())
		stored, err := ps.GetOrganizationVCSOwnerAllowlist(ctx, org.OrgID)
		require.NoError(t, err)
		assert.Equal(t, []string{"acme", "other"}, stored)
		rr = principalRequest(t, ctx, http.MethodPut, base, adminAuth, map[string]any{"owners": []string{"bad owner"}})
		assert.Equal(t, http.StatusBadRequest, rr.Code)

		// A whole-struct save of the organization keeps the list.
		loaded, err := ps.GetOrganizationByID(ctx, org.OrgID)
		require.NoError(t, err)
		loaded.DisplayName = "changed"
		require.NoError(t, ps.UpdateOrganization(ctx, loaded))
		stored, err = ps.GetOrganizationVCSOwnerAllowlist(ctx, org.OrgID)
		require.NoError(t, err)
		assert.Equal(t, []string{"acme", "other"}, stored)

		// An org admin token is not a global admin.
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/gv-allow/principals", adminAuth, map[string]string{"name": "gv-bot"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/gv-allow/principals/gv-bot/roles", adminAuth, map[string]string{"role": "admin"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/gv-allow/principals/gv-bot/tokens", adminAuth,
			map[string]any{"name": "ci", "capabilities": []string{"organizations:read", "organizations:manage"}})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		var issued struct {
			Token string `json:"token"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &issued))
		rr = principalRequest(t, ctx, http.MethodPut, base, "Bearer "+issued.Token, map[string]any{"owners": []string{"victim"}})
		assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodGet, base, "Bearer "+issued.Token, nil)
		assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
	})
}
