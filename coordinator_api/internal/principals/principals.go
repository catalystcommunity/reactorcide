// Package principals resolves identities to principals and records actors.
//
// A principal is anything that can act in Reactorcide: a human, a service,
// or the system principal "Reactorcide". A principal has identities: a
// Reactorcide user, VCS logins, LinkKeys identities, API tokens, a VCSStar
// identity, federated subjects. The resolver maps one identity to the
// principal and all of its identities, so policy, attribution, and approvals
// agree for one person whatever identity they used.
package principals

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/subjects"
)

// SystemPrincipalID is the fixed UUID of the system principal.
const SystemPrincipalID = models.SystemPrincipalID

// DefaultCacheTTL is the cache lifetime of a resolution. It is short, so a
// link or an unlink made on another replica applies soon.
const DefaultCacheTTL = 30 * time.Second

// Store is the store surface the resolver reads.
type Store interface {
	GetPrincipal(ctx context.Context, principalID string) (*models.Principal, error)
	ResolvePrincipalIdentity(ctx context.Context, identityType, value string) (*models.Principal, error)
	ListPrincipalIdentities(ctx context.Context, principalID string) ([]models.PrincipalIdentity, error)
}

// VCSStore can create the principal of a VCS login seen in a signed webhook.
type VCSStore interface {
	Store
	EnsureVCSPrincipal(ctx context.Context, provider, login string) (*models.Principal, bool, error)
}

// GroupStore lists the groups of a principal.
type GroupStore interface {
	ListGroupsForPrincipal(ctx context.Context, principalID string) ([]models.Group, error)
}

// Resolution is one principal with all of its identities.
type Resolution struct {
	Principal  models.Principal
	Identities []models.PrincipalIdentity
	// Identity is the identity the lookup used ("TYPE:VALUE"), or "" for a
	// lookup by principal ID.
	Identity string
	// Inactive is true when the resolved principal (after merges) is
	// disabled. An inactive principal grants nothing: Subjects returns no
	// subjects and UserID returns "". The identities stay for display and
	// audit. A caller can still record the raw identity that it used.
	Inactive bool
}

// HasIdentity reports whether the principal has the identity.
func (r *Resolution) HasIdentity(identityType, value string) bool {
	if r == nil {
		return false
	}
	value = models.NormalizeIdentityValue(identityType, value)
	for _, identity := range r.Identities {
		if identity.IdentityType == identityType && identity.IdentityValue == value {
			return true
		}
	}
	return false
}

// IdentitiesOf returns the values of the identities of one type.
func (r *Resolution) IdentitiesOf(identityType string) []string {
	if r == nil {
		return nil
	}
	values := []string{}
	for _, identity := range r.Identities {
		if identity.IdentityType == identityType {
			values = append(values, identity.IdentityValue)
		}
	}
	return values
}

// UserID returns the Reactorcide user of the principal, or "". It is ""
// for an inactive principal, so its user groups and roles do not apply.
func (r *Resolution) UserID() string {
	if r == nil || r.Inactive {
		return ""
	}
	if users := r.IdentitiesOf(models.IdentityTypeUser); len(users) > 0 {
		return users[0]
	}
	return ""
}

type cacheEntry struct {
	resolution *Resolution
	expires    time.Time
}

// Resolver resolves identities to principals. It is safe for concurrent use.
type Resolver struct {
	store Store
	ttl   time.Duration
	now   func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// NewResolver returns a resolver. A ttl of zero or less turns the cache off.
func NewResolver(s Store, ttl time.Duration) *Resolver {
	return &Resolver{store: s, ttl: ttl, now: time.Now, cache: map[string]cacheEntry{}}
}

// Invalidate drops every cached resolution. Call it after a link, an unlink,
// or a status change.
func (r *Resolver) Invalidate() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.cache = map[string]cacheEntry{}
	r.mu.Unlock()
}

func (r *Resolver) cached(key string) *Resolution {
	if r.ttl <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[key]
	if !ok || r.now().After(entry.expires) {
		delete(r.cache, key)
		return nil
	}
	copied := *entry.resolution
	return &copied
}

func (r *Resolver) remember(key string, resolution *Resolution) {
	if r.ttl <= 0 || resolution == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cache) > 10000 {
		r.cache = map[string]cacheEntry{}
	}
	copied := *resolution
	r.cache[key] = cacheEntry{resolution: &copied, expires: r.now().Add(r.ttl)}
}

// Resolve returns the principal of one identity and all of its identities.
// It returns store.ErrNotFound when no principal has the identity.
func (r *Resolver) Resolve(ctx context.Context, identityType, value string) (*Resolution, error) {
	if r == nil || r.store == nil {
		return nil, store.ErrNotFound
	}
	value = models.NormalizeIdentityValue(identityType, value)
	if !models.ValidIdentityType(identityType) || value == "" {
		return nil, store.ErrInvalidInput
	}
	key := "identity:" + identityType + ":" + value
	if hit := r.cached(key); hit != nil {
		return hit, nil
	}
	principal, err := r.store.ResolvePrincipalIdentity(ctx, identityType, value)
	if err != nil {
		return nil, err
	}
	resolution, err := r.complete(ctx, principal)
	if err != nil {
		return nil, err
	}
	resolution.Identity = models.ActorIdentity(identityType, value)
	r.remember(key, resolution)
	return resolution, nil
}

// ResolveID returns a principal by ID (following merges) with its identities.
func (r *Resolver) ResolveID(ctx context.Context, principalID string) (*Resolution, error) {
	if r == nil || r.store == nil {
		return nil, store.ErrNotFound
	}
	key := "principal:" + strings.ToLower(principalID)
	if hit := r.cached(key); hit != nil {
		return hit, nil
	}
	principal, err := r.store.GetPrincipal(ctx, principalID)
	if err != nil {
		return nil, err
	}
	resolution, err := r.complete(ctx, principal)
	if err != nil {
		return nil, err
	}
	r.remember(key, resolution)
	return resolution, nil
}

// ResolveOrCreateVCS returns the principal of a VCS login. Call it only for
// a login from a signed webhook: when Reactorcide has not seen the login
// before, it creates a human principal for it.
func (r *Resolver) ResolveOrCreateVCS(ctx context.Context, provider, login string) (*Resolution, error) {
	value := models.VCSIdentityValue(provider, login)
	if value == "" {
		return nil, store.ErrInvalidInput
	}
	resolution, err := r.Resolve(ctx, models.IdentityTypeVCS, value)
	if err == nil || !errors.Is(err, store.ErrNotFound) {
		return resolution, err
	}
	creator, ok := r.store.(VCSStore)
	if !ok {
		return nil, store.ErrNotFound
	}
	principal, _, err := creator.EnsureVCSPrincipal(ctx, provider, login)
	if err != nil {
		return nil, err
	}
	resolution, err = r.complete(ctx, principal)
	if err != nil {
		return nil, err
	}
	resolution.Identity = models.ActorIdentity(models.IdentityTypeVCS, value)
	return resolution, nil
}

func (r *Resolver) complete(ctx context.Context, principal *models.Principal) (*Resolution, error) {
	identities, err := r.store.ListPrincipalIdentities(ctx, principal.PrincipalID)
	if err != nil {
		return nil, err
	}
	sort.Slice(identities, func(i, j int) bool {
		if identities[i].IdentityType != identities[j].IdentityType {
			return identities[i].IdentityType < identities[j].IdentityType
		}
		return identities[i].IdentityValue < identities[j].IdentityValue
	})
	return &Resolution{Principal: *principal, Identities: identities, Inactive: !principal.IsActive()}, nil
}

// SubjectContext is the organization a subject list is evaluated in.
type SubjectContext struct {
	OrgID   string
	OrgName string
}

// Subjects returns the subjects that a principal has by its own identity:
// principal:<id>, principal_kind:<kind>, the subject of every linked
// identity, and for a service principal of the organization service:<name>
// and service:<org>/<name>. Roles (groups, teams, repository write, project
// owner) are not here; the caller adds them.
//
// An inactive (disabled) principal has no subjects.
func Subjects(resolution *Resolution, scope SubjectContext) []string {
	if resolution == nil || resolution.Inactive {
		return nil
	}
	principal := resolution.Principal
	result := []string{subjects.Principal(principal.PrincipalID), subjects.PrincipalKind(principal.Kind)}
	for _, identity := range resolution.Identities {
		switch identity.IdentityType {
		case models.IdentityTypeVCS, models.IdentityTypeLinkKeys, models.IdentityTypeVCSStar, models.IdentityTypeFederated:
			result = append(result, subjects.Identity(identity.IdentityType, identity.IdentityValue))
		}
	}
	if principal.Kind == models.PrincipalKindService && principal.OrgID != nil && *principal.OrgID != "" &&
		scope.OrgID != "" && *principal.OrgID == scope.OrgID {
		result = append(result, subjects.LocalService(principal.Name))
		if scope.OrgName != "" {
			result = append(result, subjects.Service(scope.OrgName, principal.Name))
		}
	}
	return result
}

// Actor is the principal that did something, and the identity it used.
type Actor struct {
	PrincipalID string
	Identity    string
}

// System returns the system principal actor.
func System() Actor {
	return Actor{PrincipalID: SystemPrincipalID, Identity: models.SystemActorIdentity}
}

// IsZero reports whether no actor is known.
func (a Actor) IsZero() bool { return a.PrincipalID == "" }

// Ptr returns the principal ID as a pointer for a model field, or nil.
func (a Actor) Ptr() *string {
	if a.PrincipalID == "" {
		return nil
	}
	id := a.PrincipalID
	return &id
}

// FromCredential returns the actor of an API credential or a UI session
// user. A job token has no actor of its own (it acts for its job), so the
// result is zero and the database inherits the actor of the job.
func FromCredential(credential *checkauth.Principal, user *models.User) Actor {
	if credential != nil {
		if credential.PrincipalID != "" {
			return Actor{PrincipalID: credential.PrincipalID,
				Identity: models.ActorIdentity(models.IdentityTypeToken, credential.CredentialID)}
		}
		if credential.CredentialType == "user_token" && credential.UserID != "" {
			return Actor{PrincipalID: credential.UserID,
				Identity: models.ActorIdentity(models.IdentityTypeToken, credential.CredentialID)}
		}
		return Actor{}
	}
	if user != nil && user.UserID != "" {
		return Actor{PrincipalID: user.UserID, Identity: models.ActorIdentity(models.IdentityTypeUser, user.UserID)}
	}
	return Actor{}
}

// FromContext returns the actor of the credential in the request context.
func FromContext(ctx context.Context) Actor {
	return FromCredential(checkauth.GetPrincipalFromContext(ctx), checkauth.GetUserFromContext(ctx))
}

// FromResolution returns the actor of a resolution.
func FromResolution(resolution *Resolution) Actor {
	if resolution == nil {
		return Actor{}
	}
	return Actor{PrincipalID: resolution.Principal.PrincipalID, Identity: resolution.Identity}
}

// ApplyToJob sets the actor of a job that has none.
func ApplyToJob(job *models.Job, actor Actor) {
	if job == nil || actor.IsZero() || (job.ActorPrincipalID != nil && *job.ActorPrincipalID != "") {
		return
	}
	job.ActorPrincipalID = actor.Ptr()
	job.ActorIdentity = actor.Identity
}

// ApplyToEvent sets the actor of a CI event that has none.
func ApplyToEvent(event *models.CIEvent, actor Actor) {
	if event == nil || actor.IsZero() || (event.ActorPrincipalID != nil && *event.ActorPrincipalID != "") {
		return
	}
	event.ActorPrincipalID = actor.Ptr()
	event.ActorIdentity = actor.Identity
}

// ApplyToWorkflow sets the actor of a workflow that has none.
func ApplyToWorkflow(workflow *models.WorkflowInstance, actor Actor) {
	if workflow == nil || actor.IsZero() || (workflow.ActorPrincipalID != nil && *workflow.ActorPrincipalID != "") {
		return
	}
	workflow.ActorPrincipalID = actor.Ptr()
	workflow.ActorIdentity = actor.Identity
}

// Summary is the display form of an actor in API responses.
type Summary = models.ActorSummary

// BatchStore reads many principals at once.
type BatchStore interface {
	GetPrincipalsByIDs(ctx context.Context, principalIDs []string) (map[string]models.Principal, error)
}

// Describe returns the display form of principals, keyed by ID. A store that
// cannot read principals gives an empty map; callers then omit the actor.
func Describe(ctx context.Context, value any, principalIDs []string) map[string]Summary {
	result := map[string]Summary{}
	batch, ok := value.(BatchStore)
	if !ok {
		return result
	}
	unique := map[string]bool{}
	ids := []string{}
	for _, id := range principalIDs {
		if id != "" && !unique[id] {
			unique[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return result
	}
	rows, err := batch.GetPrincipalsByIDs(ctx, ids)
	if err != nil {
		return result
	}
	for id, row := range rows {
		row := row
		result[id] = Summary{PrincipalID: row.PrincipalID, Kind: row.Kind, DisplayName: row.Label()}
	}
	return result
}

// SummaryFor returns the actor summary of one row, or nil when the row has
// no actor or the principal is unknown.
func SummaryFor(described map[string]Summary, principalID *string, identity string) *Summary {
	if principalID == nil || *principalID == "" {
		return nil
	}
	summary, ok := described[*principalID]
	if !ok {
		summary = Summary{PrincipalID: *principalID}
	}
	summary.Identity = identity
	return &summary
}

type actorContextKey struct{}

// WithActor returns a context that carries an explicit actor. Code paths
// with no API credential in the context (a UI session, a PR comment) use it
// so that the work they start records the right actor.
func WithActor(ctx context.Context, actor Actor) context.Context {
	if actor.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, actorContextKey{}, actor)
}

// ActorOf returns the explicit actor of the context, else the actor of the
// credential in the context, else zero.
func ActorOf(ctx context.Context) Actor {
	if actor, ok := ctx.Value(actorContextKey{}).(Actor); ok && !actor.IsZero() {
		return actor
	}
	return FromContext(ctx)
}

// OrgLister lists the principals of an organization.
type OrgLister interface {
	ListPrincipals(ctx context.Context, orgID, kind string) ([]models.Principal, error)
}

// InOrg reports whether a principal belongs to an organization for
// administration: a service principal of the organization, or a human with
// a group membership or a role in it. The system principal belongs to none.
func InOrg(ctx context.Context, lister OrgLister, orgID string, principal *models.Principal) bool {
	if principal == nil || principal.PrincipalID == SystemPrincipalID || orgID == "" {
		return false
	}
	if principal.OrgID != nil {
		return *principal.OrgID == orgID
	}
	if principal.Kind == models.PrincipalKindService {
		// An instance service principal belongs to no organization.
		return false
	}
	rows, err := lister.ListPrincipals(ctx, orgID, principal.Kind)
	if err != nil {
		return false
	}
	for _, row := range rows {
		if row.PrincipalID == principal.PrincipalID {
			return true
		}
	}
	return false
}
