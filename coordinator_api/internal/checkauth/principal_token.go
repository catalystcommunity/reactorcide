package checkauth

import (
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// NewPrincipalToken builds the token row of a token issued for a service
// principal of orgID (REST and UI issue-principal-token).
//
// Normally the token is a service token of orgID. Its capabilities
// (requested, or all with allCapabilities) intersect with the roles of the
// principal.
//
// With noLoginAdmin (a no-login instance and no capability list, see
// config.NoLoginMode) it is an instance token of the principal with all
// capabilities and all organizations, and roles do not narrow it: a
// no-login instance has no logins that give roles. NOT for production.
func NewPrincipalToken(value, name string, expiresAt *time.Time, orgID, principalID string, requested tokencaps.Set,
	allCapabilities, noLoginAdmin bool) *models.APIToken {
	token := &models.APIToken{TokenHash: HashAPIToken(value), Name: name, ExpiresAt: expiresAt,
		IsActive: true, SubjectType: "service_token", OwnerOrgID: &orgID, AllCapabilities: allCapabilities,
		OrganizationIDs: []string{orgID}, Capabilities: requested.Slice(),
		PrincipalID: &principalID, IntersectRoles: true}
	if noLoginAdmin {
		token.SubjectType = "instance_token"
		token.AllCapabilities = true
		token.AllOrganizations = true
		token.OrganizationIDs = []string{}
		token.Capabilities = []string{}
		token.IntersectRoles = false
	}
	return token
}
