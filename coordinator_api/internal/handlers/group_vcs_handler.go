package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/lease"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcsteams"
)

// Groups linked to VCS teams (plan item B3): the REST routes, the VCS
// credential of the sync, the webhook hint, and the lease-guarded loop.

// vcsOwnerAllowlistStore reads the VCS owner allowlist of an organization
// (migration 000054). A global admin sets it.
type vcsOwnerAllowlistStore interface {
	GetOrganizationVCSOwnerAllowlist(ctx context.Context, orgID string) ([]string, error)
}

// teamClientResolver returns the VCS client that reads the teams of an owner
// for an organization: the credential of a project of the organization
// whose repository is under the owner (a GitHub App installation, a project
// credential, or the organization credential; the same credential that
// resolves vcs_team subjects live for that project).
//
// The global client of the provider is used only when a global admin put
// the owner on the VCS owner allowlist of the organization. Without that, a
// tenant that adds a project under another tenant's VCS owner could read
// that owner's teams with the shared credential.
func (h *WebhookHandler) teamClientResolver(st store.GroupVCSStore) vcsteams.ClientResolver {
	return func(ctx context.Context, orgID, provider, owner string) (vcs.TeamMemberLister, error) {
		projects, err := st.ListOrgProjectsForTeamSync(ctx, orgID)
		if err != nil {
			return nil, err
		}
		candidates := vcsteams.ProjectsForOwner(projects, owner)
		if len(candidates) == 0 {
			return nil, vcsteams.ErrNoOwnerProject
		}
		// First choice: a GitHub App installation on the owner account that
		// the organization is entitled to (it holds a repository of a
		// project of the organization, or the owner is on the allowlist).
		// The App pool prefers an installation that granted "members".
		if tokens := vcs.AppTokenProvider(); tokens != nil {
			client, ok, err := tokens.ClientForAccount(ctx, vcs.Provider(provider), owner, orgID)
			if err != nil {
				h.logger.WithError(err).WithField("owner", owner).Warn("Could not get a GitHub App client for team sync; using the project credentials")
			} else if lister, isLister := client.(vcs.TeamMemberLister); ok && isLister && lister != nil {
				return lister, nil
			}
		}
		for i := range candidates {
			client := h.scopedVCSClient(ctx, &candidates[i], vcs.Provider(provider))
			if lister, ok := client.(vcs.TeamMemberLister); ok && lister != nil {
				return lister, nil
			}
		}
		global, _ := h.vcsClients[vcs.Provider(provider)].(vcs.TeamMemberLister)
		if global == nil {
			return nil, vcsteams.ErrNoCredential
		}
		allowed, err := h.ownerAllowsGlobalCredential(ctx, orgID, owner)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, vcsteams.ErrNoCredential
		}
		return global, nil
	}
}

// ownerAllowsGlobalCredential reports whether a global admin put the VCS
// owner on the allowlist of the organization.
func (h *WebhookHandler) ownerAllowsGlobalCredential(ctx context.Context, orgID, owner string) (bool, error) {
	lists, ok := h.store.(vcsOwnerAllowlistStore)
	if !ok {
		return false, nil
	}
	allowlist, err := lists.GetOrganizationVCSOwnerAllowlist(ctx, orgID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range allowlist {
		if strings.EqualFold(entry, owner) {
			return true, nil
		}
	}
	return false, nil
}

// handleTeamEvent makes the links of the team in a signed `team` or
// `membership` delivery due now. The sync then reads the team from the VCS;
// the payload is not trusted for membership.
func (h *WebhookHandler) handleTeamEvent(ctx context.Context, event *vcs.WebhookEvent) {
	st, ok := h.store.(store.GroupVCSStore)
	if !ok || event.Team == nil {
		return
	}
	count, err := vcsteams.HandleWebhook(ctx, st, string(event.Provider), event.Team)
	if err != nil {
		h.logger.WithError(err).Warn("Could not request a group VCS sync for a team event")
		return
	}
	if count > 0 {
		h.logger.WithField("team", event.Team.Owner+"/"+event.Team.Slug).WithField("links", count).Info("Requested group VCS sync for team event")
	}
}

// newGroupVCSSyncer returns the syncer of the coordinator, or nil when the
// store has no group VCS links.
func newGroupVCSSyncer(appStore store.Store, webhooks *WebhookHandler) *vcsteams.Syncer {
	st, ok := appStore.(vcsteams.Store)
	if !ok {
		return nil
	}
	resolver := principalResolverFor(appStore)
	if resolver == nil {
		return nil
	}
	return &vcsteams.Syncer{Store: st, Clients: webhooks.teamClientResolver(st), Principals: resolver,
		Interval: config.GroupVCSSyncInterval, MaxStale: config.GroupVCSSyncMaxStale}
}

var groupVCSSyncOnce sync.Once

// startGroupVCSSyncOnce runs the sync loop on the one replica that holds the
// group-vcs-sync lease. An interval of 0 stops the loop.
func startGroupVCSSyncOnce(appStore store.Store, syncer *vcsteams.Syncer) {
	leases, ok := appStore.(store.LeaseStore)
	if !ok || syncer == nil || config.GroupVCSSyncInterval <= 0 {
		return
	}
	groupVCSSyncOnce.Do(func() {
		go lease.NewManager(leases).Run(context.Background(), vcsteams.LeaseName, syncer.Run)
	})
}

type groupTeamLinkRequest struct {
	Provider string `json:"provider"`
	Team     string `json:"team"`
}

type groupTeamLinkResponse struct {
	LinkID         string     `json:"link_id"`
	Provider       string     `json:"provider"`
	Team           string     `json:"team"`
	Subject        string     `json:"subject"`
	ExternalTeamID *int64     `json:"external_team_id,omitempty"`
	LastSyncedAt   *time.Time `json:"last_synced_at,omitempty"`
	LastAttemptAt  *time.Time `json:"last_attempt_at,omitempty"`
	NextSyncAt     *time.Time `json:"next_sync_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	FailureCount   int        `json:"failure_count"`
	MemberCount    int        `json:"member_count"`
	CreatedAt      time.Time  `json:"created_at"`
}

func teamLinkResponse(link *models.GroupVCSLink) groupTeamLinkResponse {
	return groupTeamLinkResponse{LinkID: link.LinkID, Provider: link.Provider, Team: link.Owner + "/" + link.TeamSlug,
		Subject: link.Subject(), ExternalTeamID: link.ExternalTeamID, LastSyncedAt: link.LastSyncedAt,
		LastAttemptAt: link.LastAttemptAt, NextSyncAt: link.NextSyncAt, LastError: link.LastError,
		FailureCount: link.FailureCount, MemberCount: link.MemberCount, CreatedAt: link.CreatedAt}
}

func (h *GroupHandler) vcsStore() store.GroupVCSStore {
	st, _ := h.appStore.(store.GroupVCSStore)
	return st
}

// SetTeamSync gives the REST sync route the syncer of the coordinator.
func (h *GroupHandler) SetTeamSync(syncer *vcsteams.Syncer) { h.teamSync = syncer }

// serveTeams routes /groups/{group}/teams[/{link-id} | /{owner}/{slug}],
// /groups/{group}/sync and /groups/{group}/history. It reports whether it
// served the request.
func (h *GroupHandler) serveTeams(w http.ResponseWriter, r *http.Request, org *models.Organization, parts []string) bool {
	if len(parts) < 2 || (parts[1] != "teams" && parts[1] != "sync" && parts[1] != "history") {
		return false
	}
	st := h.vcsStore()
	if st == nil {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("group VCS links are not available"))
		return true
	}
	h.withGroup(w, r, org, parts[0], func(w http.ResponseWriter, r *http.Request, org *models.Organization, group *models.Group) {
		switch {
		case parts[1] == "teams" && len(parts) == 2 && r.Method == http.MethodGet:
			links, err := st.ListGroupVCSLinks(r.Context(), group.GroupID)
			if err != nil {
				h.respondWithError(w, http.StatusInternalServerError, err)
				return
			}
			out := make([]groupTeamLinkResponse, 0, len(links))
			for i := range links {
				out = append(out, teamLinkResponse(&links[i]))
			}
			h.respondWithJSON(w, http.StatusOK, map[string]any{"teams": out})
		case parts[1] == "teams" && len(parts) == 2 && r.Method == http.MethodPost:
			h.linkTeam(w, r, st, org, group)
		case parts[1] == "teams" && (len(parts) == 3 || len(parts) == 4) && r.Method == http.MethodDelete:
			ref := parts[2]
			if len(parts) == 4 {
				ref = parts[2] + "/" + parts[3]
			}
			h.unlinkTeam(w, r, st, org, group, ref)
		case parts[1] == "sync" && len(parts) == 2 && r.Method == http.MethodPost:
			h.syncGroup(w, r, st, org, group)
		case parts[1] == "history" && len(parts) == 2 && r.Method == http.MethodGet:
			h.groupHistory(w, r, st, group)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	return true
}

func (h *GroupHandler) linkTeam(w http.ResponseWriter, r *http.Request, st store.GroupVCSStore, org *models.Organization, group *models.Group) {
	var req groupTeamLinkRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	link, err := vcsteams.CreateLink(r.Context(), st, group, req.Provider, req.Team, principals.ActorOf(r.Context()).PrincipalID)
	switch {
	case vcsteams.IsValidationError(err):
		h.respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, store.ErrAlreadyExists):
		h.respondWithError(w, http.StatusConflict, err)
		return
	case err != nil:
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "group.team_link", "group", group.GroupID,
		models.JSONB{"link_id": link.LinkID, "provider": link.Provider, "team": link.Owner + "/" + link.TeamSlug})
	h.respondWithJSON(w, http.StatusCreated, teamLinkResponse(link))
}

func (h *GroupHandler) unlinkTeam(w http.ResponseWriter, r *http.Request, st store.GroupVCSStore, org *models.Organization, group *models.Group, ref string) {
	link, err := vcsteams.FindLink(r.Context(), st, group.GroupID, ref)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if err := st.DeleteGroupVCSLink(r.Context(), link.LinkID); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "group.team_unlink", "group", group.GroupID,
		models.JSONB{"link_id": link.LinkID, "provider": link.Provider, "team": link.Owner + "/" + link.TeamSlug})
	w.WriteHeader(http.StatusNoContent)
}

// syncGroup syncs every link of the group now when the syncer is wired, and
// otherwise makes the links due for the loop ("requested" in the answer).
func (h *GroupHandler) syncGroup(w http.ResponseWriter, r *http.Request, st store.GroupVCSStore, org *models.Organization, group *models.Group) {
	links, err := st.ListGroupVCSLinks(r.Context(), group.GroupID)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	if h.teamSync == nil {
		ids := make([]string, 0, len(links))
		for _, link := range links {
			ids = append(ids, link.LinkID)
		}
		if err := st.RequestGroupVCSSync(r.Context(), ids); err != nil {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, map[string]any{"results": []any{}, "requested": len(ids)})
		return
	}
	outcomes := make([]*vcsteams.Outcome, 0, len(links))
	for i := range links {
		outcomes = append(outcomes, h.teamSync.SyncLink(r.Context(), &links[i]))
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "group.team_sync", "group", group.GroupID, models.JSONB{"links": len(links)})
	h.respondWithJSON(w, http.StatusOK, map[string]any{"results": outcomes})
}

func (h *GroupHandler) groupHistory(w http.ResponseWriter, r *http.Request, st store.GroupVCSStore, group *models.Group) {
	filter := store.GroupMembershipHistoryFilter{GroupID: group.GroupID, PrincipalID: r.URL.Query().Get("principal")}
	if value := r.URL.Query().Get("since"); value != "" {
		since, err := time.Parse(time.RFC3339, value)
		if err != nil {
			h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		filter.Since = &since
	}
	if value := r.URL.Query().Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil {
			h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		filter.Limit = limit
	}
	changes, err := st.ListGroupMembershipHistory(r.Context(), filter)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, map[string]any{"history": changes})
}
