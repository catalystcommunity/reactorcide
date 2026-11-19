package worker

import "github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"

// ProxyDeliveryGrant returns the secret grant that delivers the secret at
// path through the egress proxy (Delivery "proxy"), or nil when the secret
// goes into the job environment. grants are the grants of the job
// (SecretGrantStore.ListSecretGrantsForJob). A job-scoped secret
// (jobs/{id}, projects/{p}/jobs/{file}) has no grant and is always env.
//
// When several grants match the path, a proxy grant wins over an env grant:
// an administrator who binds a secret to hosts does not want its value in
// any job environment. Among proxy grants, the first one (in store order,
// the order AuthorizeSecretAccess also uses) wins.
//
// This function does not authorize: the caller must call
// AuthorizeSecretAccess first.
func ProxyDeliveryGrant(grants []models.SecretGrant, job *models.Job, path string) *models.SecretGrant {
	if job == nil || isJobScopedSecret(job, path) {
		return nil
	}
	for i := range grants {
		if grants[i].EffectiveDelivery() == models.SecretDeliveryProxy && grantMatchesSecret(grants[i], job, path) {
			return &grants[i]
		}
	}
	return nil
}
