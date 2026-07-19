package handlers

import (
	"log"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
)

// roleStoreResolver type-asserts s onto authz.RoleStore and, if it
// qualifies, returns a ready-to-use *authz.Resolver; otherwise nil. This is
// the repo's consumer-defined-narrow-interface convention (see
// project_handler.go's projectSecretGrantStore) applied to Task D's authz
// package: *postgres_store.PostgresDbStore satisfies authz.RoleStore in
// production, while the hand-rolled store.Store mocks used by this
// package's existing tests don't implement the extra
// ListGroupsForUser/ListRoleAssignmentsForPrincipal methods, so they get a
// nil resolver and every additive visibility check below becomes a no-op —
// existing tests keep exercising exactly their original owner-or-admin
// behavior.
//
// component names the calling handler (e.g. "JobHandler") purely for the
// warning message below — see roleStoreResolverNamed's doc comment for why
// the nil case now logs instead of degrading silently.
func roleStoreResolver(s store.Store, component string) *authz.Resolver {
	rs, ok := s.(authz.RoleStore)
	if !ok {
		// Previously this fallback was silent: a handler wired with a store
		// that doesn't satisfy authz.RoleStore would quietly revert to
		// legacy owner-or-admin-only access checks with zero diagnostics —
		// in production that means public/private visibility (and, for
		// JobHandler.KillJob, the org-admin/global-admin kill gate) is
		// simply off, with nothing in the logs to explain why. Match
		// router.go's buildUIAPIDeps warning style so this is loud instead.
		log.Printf("WARNING: %s: configured store does not implement authz.RoleStore "+
			"(missing ListGroupsForUser/ListRoleAssignmentsForPrincipal); public/private "+
			"visibility filtering is disabled and this handler falls back to legacy "+
			"owner-or-admin-only access checks", component)
		return nil
	}
	return authz.NewResolver(rs)
}
