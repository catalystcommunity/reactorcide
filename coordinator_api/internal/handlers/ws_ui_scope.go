package handlers

import (
	"context"
	"errors"
	"sync"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/pubsub"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// scopeStore is the narrow store surface uiStreamScope resolves ownership
// through. store.Store satisfies it; tests hand in a small fake.
type scopeStore interface {
	GetProjectByID(ctx context.Context, projectID string) (*models.Project, error)
	GetJobByID(ctx context.Context, jobID string) (*models.Job, error)
	GetUserByID(ctx context.Context, userID string) (*models.User, error)
}

// scopeOrganizationLookup is the OPTIONAL surface for reading an
// organization's is_private flag. store.Store does not carry it, so it is
// type-asserted from the scope's store (*postgres_store.PostgresDbStore has
// it); a store without it falls back to the legacy users row.
type scopeOrganizationLookup interface {
	GetOrganizationByID(ctx context.Context, orgID string) (*models.Organization, error)
}

// uiStreamScope answers "may this caller see this event" for the life of one
// WebSocket connection.
//
// The point of this type is that the answer costs no I/O in the common case.
// An event carries its own ProjectID and OwnerUserID, so deciding whether a
// caller may see it is a lookup in two small caches built from data this
// connection has already resolved. Compare the code this replaces:
// WSHandler.StreamAllJobs runs GetJobByID inside its subscription filter, so a
// busy bus multiplied by connected browsers is a query storm.
//
// The caches are per-connection and bounded by the number of DISTINCT projects
// and orgs whose events the caller actually receives, which for a real session
// is small. A project's cached answer is dropped when a project_update event
// for it arrives, so a project turned private stops feeding an open socket
// (see canSee).
type uiStreamScope struct {
	resolver *authz.Resolver
	store    scopeStore
	identity authz.Identity

	// globalAdmin short-circuits everything, resolved once at connect.
	globalAdmin bool

	mu sync.Mutex
	// projectVisible caches the per-project answer. Keyed by project id.
	projectVisible map[string]bool
	// orgVisible caches the answer for project-less resources (loose jobs,
	// project-less workflows), keyed by owning org id.
	orgVisible map[string]bool
	// jobVisible caches the answer for events that carry only a job id. See
	// canSee's fallback branch.
	jobVisible map[string]bool
}

func (h *UIStreamHandler) newScope(ctx context.Context, identity authz.Identity) (*uiStreamScope, error) {
	scope := &uiStreamScope{
		resolver:       h.resolver,
		identity:       identity,
		projectVisible: make(map[string]bool),
		orgVisible:     make(map[string]bool),
		jobVisible:     make(map[string]bool),
	}
	if !identity.Anonymous {
		isGlobalAdmin, err := h.resolver.IsGlobalAdmin(ctx, identity)
		if err != nil {
			return nil, err
		}
		scope.globalAdmin = isGlobalAdmin
	}
	// A nil store.Store must stay a nil scopeStore, or the `s.store == nil`
	// guards below would see a non-nil interface wrapping nothing.
	if h.store != nil {
		scope.store = h.store
	}
	return scope, nil
}

// canSee is the authorization gate every frame passes before any topic filter
// is consulted.
//
// An event that carries neither a project nor an owner is DROPPED. That is the
// safe direction: such an event cannot be authorized from its own fields, and
// falling back to "show it" would reintroduce exactly the leak this endpoint
// exists to close. If a publisher forgets to populate the ownership fields, its
// events go dark rather than going to everyone — a visible bug rather than a
// silent disclosure.
func (s *uiStreamScope) canSee(evt pubsub.Event) bool {
	// A project whose settings changed may have just become private. Drop its
	// cached answer before deciding anything else, so a project flipped to
	// private stops feeding an already-open socket. This is why
	// EventProjectUpdate exists as an event type at all: without it, a
	// connection opened while a project was public would keep receiving its
	// events until the browser reconnected.
	if evt.Type == pubsub.EventProjectUpdate && evt.ProjectID != "" {
		s.mu.Lock()
		delete(s.projectVisible, evt.ProjectID)
		s.mu.Unlock()
	}
	if s.globalAdmin {
		return true
	}
	if evt.ProjectID == "" && evt.OwnerUserID == "" {
		// Log- and metrics-availability events are published from the worker
		// telemetry path, which holds a lease and a job id but not the job's
		// ownership. Rather than make every telemetry write do an ownership
		// lookup, resolve the job ONCE per connection and cache the answer:
		// telemetry events for a single job are numerous, so this amortizes to
		// nothing while keeping the publish path cheap.
		if evt.JobID != "" {
			return s.jobAllows(evt.JobID)
		}
		// Neither ownership nor a job to resolve it from. Drop it. Failing
		// closed turns a publisher that forgot its ownership fields into a
		// visible bug rather than a silent disclosure.
		return false
	}
	// The owner of a resource can always see it. OwnerUserID carries the
	// resource's OwnershipOrgID(); for legacy users-as-orgs that is the user's
	// own id, so this also covers an org's own resources.
	if evt.OwnerUserID != "" && !s.identity.Anonymous && evt.OwnerUserID == s.identity.UserID {
		return true
	}
	if evt.ProjectID != "" {
		return s.projectAllows(evt.ProjectID)
	}
	return s.orgAllows(evt.OwnerUserID)
}

func (s *uiStreamScope) projectAllows(projectID string) bool {
	s.mu.Lock()
	cached, ok := s.projectVisible[projectID]
	s.mu.Unlock()
	if ok {
		return cached
	}

	// Resolve against the real rule, then cache. context.Background is correct
	// here rather than the request context: this runs on the bus's publish
	// path, and a cancelled request context would turn into "invisible" for
	// every subsequent event on a connection that is still open.
	// A nil store must not panic here. This runs on the bus's publish
	// goroutine, where a panic takes the process down rather than failing one
	// request, so an unusable store degrades to "nothing is visible".
	visible := false
	if s.store == nil {
		return false
	}
	if project, err := s.store.GetProjectByID(context.Background(), projectID); err == nil && project != nil {
		if ok, err := s.resolver.CanViewProject(context.Background(), s.identity, project); err == nil {
			visible = ok
		}
	}

	s.mu.Lock()
	s.projectVisible[projectID] = visible
	s.mu.Unlock()
	return visible
}

// jobAllows resolves visibility for an event that identifies only a job.
func (s *uiStreamScope) jobAllows(jobID string) bool {
	s.mu.Lock()
	cached, ok := s.jobVisible[jobID]
	s.mu.Unlock()
	if ok {
		return cached
	}

	visible := false
	if s.store == nil {
		return false
	}
	if job, err := s.store.GetJobByID(context.Background(), jobID); err == nil && job != nil {
		if ok, err := s.resolver.CanViewJob(context.Background(), s.identity, job); err == nil {
			visible = ok
		}
	}

	s.mu.Lock()
	s.jobVisible[jobID] = visible
	s.mu.Unlock()
	return visible
}

func (s *uiStreamScope) orgAllows(orgID string) bool {
	s.mu.Lock()
	cached, ok := s.orgVisible[orgID]
	s.mu.Unlock()
	if ok {
		return cached
	}

	// A project-less resource is visible when its owning org is not private,
	// or when the caller is an admin of that org. An org whose privacy cannot
	// be resolved at all (no organizations row and no legacy users row) stays
	// invisible: fail closed, as canSee does for unauthorizable events.
	visible := false
	if s.store == nil {
		return false
	}
	if isPrivate, known := s.orgIsPrivate(orgID); known {
		if !isPrivate {
			visible = true
		} else if !s.identity.Anonymous {
			if isAdmin, err := s.resolver.IsOrgAdmin(context.Background(), s.identity, orgID); err == nil {
				visible = isAdmin
			}
		}
	}

	s.mu.Lock()
	s.orgVisible[orgID] = visible
	s.mu.Unlock()
	return visible
}

// orgIsPrivate resolves an owning org's privacy the way
// authz.visibilityBatch.orgIsPrivate does: organizations.is_private when the
// store can look organizations up by id and the row exists, else the legacy
// users.is_private for the same id (backfilled orgs share the user's UUID).
// known is false when neither row exists.
func (s *uiStreamScope) orgIsPrivate(orgID string) (isPrivate, known bool) {
	if orgID == "" || s.store == nil {
		return false, false
	}
	ctx := context.Background()
	if lookup, ok := s.store.(scopeOrganizationLookup); ok {
		org, err := lookup.GetOrganizationByID(ctx, orgID)
		if err == nil && org != nil {
			return org.IsPrivate, true
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return false, false
		}
	}
	owner, err := s.store.GetUserByID(ctx, orgID)
	if err != nil || owner == nil {
		return false, false
	}
	return owner.IsPrivate, true
}
