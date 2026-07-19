package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/auth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// DataStore is everything the CSIL UI service implementations (Task G) need
// from the store: the base store.Store surface, authz's role resolution
// surface, auth's login/session/admission/credential surfaces, and the
// group/role/rotation/settings/trusted-identity/secret-grant operations Task
// A added on the concrete postgres store. This is this repo's
// consumer-defined-narrow-interface convention (see handlers/router.go's
// makeTokenResolver, handlers/authz_visibility.go's roleStoreResolver)
// applied at package scope: production wiring (router.go) type-asserts
// store.AppStore onto DataStore once at startup; tests build a hand-rolled
// fake satisfying it directly.
//
// A handful of methods here are not exposed by any existing narrow
// interface in this repo (postgres_store didn't need a bare by-ID lookup for
// its own callers) and were added to postgres_store as small, additive
// helpers alongside this task: GetRoleAssignmentByID, GetSecretGrantByID,
// ListSecretGrantsByOrg, GetProjectWebhookSecretByID,
// GetProjectVCSCredentialByID, ListProjectsByOrg, GetAuthIdentityByUserID.
// See each op's implementation file for why it needs a bare-ID lookup: every
// mutating CSIL op that targets a row by ID alone (no org/project in the
// request) must load the row first to discover its owning scope before it
// can run an authorization check against that scope.
type DataStore interface {
	store.Store
	authz.RoleStore
	auth.LoginServiceStore
	auth.CredentialStore

	// --- projects (additive) ---
	ListProjectsByOrg(ctx context.Context, orgID string, limit, offset int) ([]models.Project, error)

	// --- auth_identities (additive) ---
	GetAuthIdentityByUserID(ctx context.Context, userID string) (*models.AuthIdentity, error)

	// --- groups / group_members ---
	CreateGroup(ctx context.Context, group *models.Group) error
	GetGroupByID(ctx context.Context, groupID string) (*models.Group, error)
	ListGroupsByOrg(ctx context.Context, orgID string) ([]models.Group, error)
	UpdateGroup(ctx context.Context, group *models.Group) error
	DeleteGroup(ctx context.Context, groupID string) error
	AddGroupMember(ctx context.Context, groupID, userID string) error
	RemoveGroupMember(ctx context.Context, groupID, userID string) error
	ListGroupMembers(ctx context.Context, groupID string) ([]models.User, error)

	// --- role_assignments ---
	DeleteRoleAssignment(ctx context.Context, assignmentID string) error
	GetRoleAssignmentByID(ctx context.Context, assignmentID string) (*models.RoleAssignment, error)

	// --- project_webhook_secrets / project_vcs_credentials ---
	CreateProjectWebhookSecret(ctx context.Context, secret *models.ProjectWebhookSecret) error
	ListProjectWebhookSecrets(ctx context.Context, projectID string, provider *string) ([]models.ProjectWebhookSecret, error)
	DeactivateProjectWebhookSecret(ctx context.Context, id string) error
	DeleteProjectWebhookSecret(ctx context.Context, id string) error
	GetProjectWebhookSecretByID(ctx context.Context, id string) (*models.ProjectWebhookSecret, error)
	CreateProjectVCSCredential(ctx context.Context, cred *models.ProjectVCSCredential) error
	ListProjectVCSCredentials(ctx context.Context, projectID string, provider *string) ([]models.ProjectVCSCredential, error)
	DeactivateProjectVCSCredential(ctx context.Context, id string) error
	DeleteProjectVCSCredential(ctx context.Context, id string) error
	GetProjectVCSCredentialByID(ctx context.Context, id string) (*models.ProjectVCSCredential, error)

	// --- global_settings ---
	GetGlobalSetting(ctx context.Context, key string) (*models.GlobalSetting, error)
	SetGlobalSetting(ctx context.Context, key string, value models.JSONValue) error
	ListGlobalSettings(ctx context.Context) ([]models.GlobalSetting, error)

	// --- auth_trusted_identities / auth_trusted_domain_patterns (beyond
	// auth.AdmissionStore, which already declares
	// TrustedIdentityExists/ListTrustedDomainPatterns/UpsertTrustedIdentity) ---
	ListTrustedIdentities(ctx context.Context) ([]models.AuthTrustedIdentity, error)
	DeleteTrustedIdentity(ctx context.Context, domain, handle string) error
	CreateTrustedDomainPattern(ctx context.Context, pattern *models.AuthTrustedDomainPattern) error
	DeleteTrustedDomainPattern(ctx context.Context, patternID string) error

	// --- secret_grants ---
	CreateSecretGrant(ctx context.Context, grant *models.SecretGrant) error
	ListSecretGrantsByOrg(ctx context.Context, orgID string) ([]models.SecretGrant, error)
	UpdateSecretGrant(ctx context.Context, grant *models.SecretGrant) error
	DeleteSecretGrant(ctx context.Context, userID string, projectID *string, ref string) error
	GetSecretGrantByID(ctx context.Context, grantID string) (*models.SecretGrant, error)
}
