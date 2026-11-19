package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// actorContext returns ctx with the caller as the explicit actor, so work
// the operation starts (a retry) records the principal of the session user
// or of the API token.
func (d *Deps) actorContext(ctx context.Context) context.Context {
	_, user, credential := d.resolveIdentityDetails(ctx)
	return principals.WithActor(ctx, principals.FromCredential(credential, user))
}

// actorToWire is the wire form of a row's actor, or nil when the row has
// none. describeActors fills the kind and the display name.
func actorToWire(principalID *string, identity string) *csilapi.ActorSummary {
	if principalID == nil || *principalID == "" {
		return nil
	}
	actor := &csilapi.ActorSummary{PrincipalId: *principalID}
	if identity != "" {
		value := identity
		actor.Identity = &value
	}
	return actor
}

// describeActors fills the kind and display name of each actor with one
// store read. A store without principals leaves them empty.
func (s *UiService) describeActors(ctx context.Context, actors ...*csilapi.ActorSummary) {
	ids := make([]string, 0, len(actors))
	for _, actor := range actors {
		if actor != nil {
			ids = append(ids, actor.PrincipalId)
		}
	}
	described := principals.Describe(ctx, s.deps.Store, ids)
	for _, actor := range actors {
		if actor == nil {
			continue
		}
		if found, ok := described[actor.PrincipalId]; ok {
			actor.Kind = found.Kind
			actor.DisplayName = found.DisplayName
		}
	}
}

func jobActors(jobs []csilapi.JobSummary) []*csilapi.ActorSummary {
	actors := make([]*csilapi.ActorSummary, 0, len(jobs))
	for i := range jobs {
		actors = append(actors, jobs[i].Actor)
	}
	return actors
}
