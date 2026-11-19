// Package vcsteams syncs groups that are linked to VCS teams (plan item B3).
//
// A group can bind to one or more VCS teams (group_vcs_links). The sync
// reads the members of each team from the VCS, resolves each login to a
// principal (and makes a principal for a login it has not seen), and stores
// the members. The effective members of the group are its manual members and
// the synced members. A sync never removes a manual member.
//
// The VCS is the authority. A `membership` or `team` webhook only makes the
// links of the team due now; the loop then reads the team from the VCS.
//
// One replica runs the loop, under the coordinator lease LeaseName.
package vcsteams

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// LeaseName is the coordinator lease of the sync loop.
const LeaseName = "group-vcs-sync"

// Defaults.
const (
	DefaultInterval = 15 * time.Minute
	// DefaultMaxStale removes the synced members of a link when no sync
	// succeeded for this long. A person removed from the team must not keep
	// the group while the VCS cannot be read.
	DefaultMaxStale = 24 * time.Hour
	// DefaultPoll is how often the loop looks for due links. A webhook
	// makes a link due now, so this is the delay of a webhook update.
	DefaultPoll = 10 * time.Second
	// MaxBackoff limits the backoff after failures.
	MaxBackoff  = 6 * time.Hour
	syncTimeout = 2 * time.Minute
	batchSize   = 20
)

// ClientResolver returns a client that can read the teams of a VCS owner
// for an organization of Reactorcide.
type ClientResolver func(ctx context.Context, orgID, provider, owner string) (vcs.TeamMemberLister, error)

// ErrNoCredential is returned by a ClientResolver that has no credential.
var ErrNoCredential = errors.New("no VCS credential of the organization can read this team (add a project or organization VCS credential, or ask a global admin to allow the global credential for this owner: reactorcide orgs vcs-owners set)")

// PrincipalResolver maps a VCS login to its principal, and makes a principal
// for a login that Reactorcide has not seen.
type PrincipalResolver interface {
	ResolveOrCreateVCS(ctx context.Context, provider, login string) (*principals.Resolution, error)
}

// Store is what the syncer needs.
type Store interface {
	store.GroupVCSStore
	GetGroupByID(ctx context.Context, groupID string) (*models.Group, error)
}

// Syncer syncs links.
type Syncer struct {
	Store      Store
	Clients    ClientResolver
	Principals PrincipalResolver
	// Interval between successful syncs of one link. Zero is DefaultInterval.
	Interval time.Duration
	// MaxStale, see DefaultMaxStale. Zero is DefaultMaxStale.
	MaxStale time.Duration
	// Poll, see DefaultPoll. Zero is DefaultPoll.
	Poll time.Duration
	Now  func() time.Time
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Syncer) interval() time.Duration {
	if s.Interval > 0 {
		return s.Interval
	}
	return DefaultInterval
}

func (s *Syncer) maxStale() time.Duration {
	if s.MaxStale > 0 {
		return s.MaxStale
	}
	return DefaultMaxStale
}

func (s *Syncer) poll() time.Duration {
	if s.Poll > 0 {
		return s.Poll
	}
	return DefaultPoll
}

// Outcome is the result of one link sync.
type Outcome struct {
	LinkID  string   `json:"link_id"`
	Team    string   `json:"team"`
	Members int      `json:"members"`
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Error   string   `json:"error,omitempty"`
}

// Run syncs due links until ctx ends. Give it to lease.Manager.Run.
func (s *Syncer) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.SyncDue(ctx)
		timer.Reset(s.poll())
	}
}

// SyncDue syncs every link that is due now, in batches.
func (s *Syncer) SyncDue(ctx context.Context) int {
	count := 0
	for ctx.Err() == nil {
		links, err := s.Store.ListDueGroupVCSLinks(ctx, s.now(), batchSize)
		if err != nil {
			if ctx.Err() == nil {
				logging.Log.WithError(err).Warn("group VCS sync: could not list due links")
			}
			return count
		}
		if len(links) == 0 {
			return count
		}
		for i := range links {
			if ctx.Err() != nil {
				return count
			}
			s.SyncLink(ctx, &links[i])
			count++
		}
		if len(links) < batchSize {
			return count
		}
	}
	return count
}

// SyncLinkID loads and syncs one link now.
func (s *Syncer) SyncLinkID(ctx context.Context, linkID string) (*Outcome, error) {
	link, err := s.Store.GetGroupVCSLink(ctx, linkID)
	if err != nil {
		return nil, err
	}
	return s.SyncLink(ctx, link), nil
}

// SyncLink syncs one link and stores the result. A failure is stored on the
// link (last_error) and returned in the outcome.
func (s *Syncer) SyncLink(parent context.Context, link *models.GroupVCSLink) *Outcome {
	ctx, cancel := context.WithTimeout(parent, syncTimeout)
	defer cancel()
	outcome := &Outcome{LinkID: link.LinkID, Team: link.Subject(), Added: []string{}, Removed: []string{}}
	log := logging.Log.WithField("link_id", link.LinkID).WithField("team", link.Subject())
	members, teamID, err := s.fetch(ctx, link)
	now := s.now()
	if err != nil {
		outcome.Error = err.Error()
		next, clear := s.failurePlan(link, err, now)
		result, storeErr := s.Store.RecordGroupVCSSyncFailure(parent, link.LinkID, err.Error(), now, next, clear)
		if storeErr != nil {
			log.WithError(storeErr).Warn("group VCS sync: could not record the failure")
		} else {
			outcome.Members = result.Members
			outcome.Removed = result.Removed
		}
		log.WithError(err).Warn("group VCS sync failed")
		return outcome
	}
	result, err := s.Store.ApplyGroupVCSSync(parent, link.LinkID, members, teamID, now, now.Add(s.interval()))
	if err != nil {
		outcome.Error = err.Error()
		log.WithError(err).Warn("group VCS sync: could not store the members")
		return outcome
	}
	outcome.Members, outcome.Added, outcome.Removed = result.Members, result.Added, result.Removed
	if len(result.Added)+len(result.Removed) > 0 {
		log.WithField("added", len(result.Added)).WithField("removed", len(result.Removed)).Info("group VCS sync changed members")
	}
	return outcome
}

func (s *Syncer) fetch(ctx context.Context, link *models.GroupVCSLink) ([]models.GroupVCSTeamMember, *int64, error) {
	if s.Clients == nil || s.Principals == nil {
		return nil, nil, fmt.Errorf("team sync is not configured")
	}
	group, err := s.Store.GetGroupByID(ctx, link.GroupID)
	if err != nil {
		return nil, nil, fmt.Errorf("loading group: %w", err)
	}
	client, err := s.Clients(ctx, group.OrgID, link.Provider, link.Owner)
	if err != nil {
		return nil, nil, err
	}
	if client == nil {
		return nil, nil, ErrNoCredential
	}
	team, err := client.GetTeam(ctx, link.Owner, link.TeamSlug)
	if err != nil {
		return nil, nil, err
	}
	logins, err := client.ListTeamMembers(ctx, link.Owner, link.TeamSlug)
	if err != nil {
		return nil, nil, err
	}
	members := make([]models.GroupVCSTeamMember, 0, len(logins))
	for _, login := range logins {
		resolution, err := s.Principals.ResolveOrCreateVCS(ctx, link.Provider, login)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving the principal of %s: %w", login, err)
		}
		if resolution == nil || resolution.Inactive {
			// A disabled principal gets nothing.
			continue
		}
		members = append(members, models.GroupVCSTeamMember{PrincipalID: resolution.Principal.PrincipalID, Login: login})
	}
	var teamID *int64
	if team != nil && team.ID != 0 {
		id := team.ID
		teamID = &id
	}
	return members, teamID, nil
}

// failurePlan returns the next attempt time and whether the synced members
// must go. A team that the VCS does not show loses its members at once
// (the safe direction). Members older than MaxStale also go.
func (s *Syncer) failurePlan(link *models.GroupVCSLink, err error, now time.Time) (time.Time, bool) {
	clear := errors.Is(err, vcs.ErrTeamNotFound)
	if link.LastSyncedAt == nil || now.Sub(*link.LastSyncedAt) > s.maxStale() {
		clear = true
	}
	var limit *vcs.RateLimitError
	if errors.As(err, &limit) && !limit.Reset.IsZero() && limit.Reset.After(now) {
		return limit.Reset.Add(5 * time.Second), clear
	}
	return now.Add(Backoff(link.FailureCount+1, s.interval())), clear
}

// Backoff returns the delay after a number of failures in a row: one
// minute, then double for each failure, at most 4 * interval and at most
// MaxBackoff.
func Backoff(failures int, interval time.Duration) time.Duration {
	limit := MaxBackoff
	if interval > 0 && interval*4 < limit {
		limit = interval * 4
	}
	delay := time.Minute
	for i := 1; i < failures && delay < limit; i++ {
		delay *= 2
	}
	if delay > limit {
		delay = limit
	}
	return delay
}

// HandleWebhook makes the links of the team in a GitHub `team` or
// `membership` event due now. A rename moves the links to the new slug. It
// returns the number of links.
func HandleWebhook(ctx context.Context, st store.GroupVCSStore, provider string, event *vcs.TeamEventInfo) (int, error) {
	if st == nil || event == nil {
		return 0, nil
	}
	links, err := st.FindGroupVCSLinksByTeam(ctx, provider, event.Owner, event.Slug, event.TeamID)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(links))
	for _, link := range links {
		if event.Renamed && event.Slug != "" && link.TeamSlug != event.Slug &&
			link.ExternalTeamID != nil && *link.ExternalTeamID == event.TeamID {
			if err := st.RenameGroupVCSLinkTeam(ctx, link.LinkID, event.Slug); err != nil && !errors.Is(err, store.ErrAlreadyExists) {
				return 0, err
			}
		}
		ids = append(ids, link.LinkID)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return len(ids), st.RequestGroupVCSSync(ctx, ids)
}

// NormalizeTeam parses "OWNER/SLUG" or "vcs_team:OWNER/SLUG".
func NormalizeTeam(value string) (owner, slug string, ok bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "vcs_team:")
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	owner, slug = strings.ToLower(strings.TrimSpace(parts[0])), strings.ToLower(strings.TrimSpace(parts[1]))
	if !validPart(owner) || !validPart(slug) {
		return "", "", false
	}
	return owner, slug, true
}

func validPart(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for i, c := range value {
		alnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		// The first character is a letter or a digit, so ".." cannot walk
		// the API path.
		if !alnum && (i == 0 || (c != '-' && c != '_' && c != '.')) {
			return false
		}
	}
	return true
}
