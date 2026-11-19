package uiapi

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagealias"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagepolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imageref"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imageset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// Image alias management (docs/image-aliases.md). An organization alias
// needs organization admin; a project alias needs project owner (the
// project admin role). Each change is an audit record with the old and the
// new reference.

// Outcomes of set-image-alias.
const (
	aliasOutcomeCreated   = "created"
	aliasOutcomePending   = "pending"
	aliasOutcomePromoted  = "promoted"
	aliasOutcomeUpdated   = "updated"
	aliasOutcomeUnchanged = "unchanged"
)

// imageAliasUIStore is what the alias operations need from the store.
type imageAliasUIStore interface {
	store.ImageAliasStore
	imageset.Store
	imageset.FleetStore
	store.ImageAliasLocker
}

func (s *UiService) imageAliasStore() (imageAliasUIStore, error) {
	value, ok := s.deps.Store.(imageAliasUIStore)
	if !ok {
		return nil, NewServiceError("internal", "image aliases are not available")
	}
	return value, nil
}

// aliasScope is the resolved scope of an alias request.
type aliasScope struct {
	org     *models.Organization
	project *models.Project
}

func (a aliasScope) projectID() *string {
	if a.project == nil {
		return nil
	}
	id := a.project.ProjectID
	return &id
}

// requireAliasScope resolves organization and project_id and checks the
// permission of the caller: organization admin for the organization scope,
// project owner for a project scope. A token also needs one of
// capabilities for the organization: aliasReadCapabilities for a read,
// aliasWriteCapabilities for a change (there is no policies:read).
func (s *UiService) requireAliasScope(ctx context.Context, id authz.Identity, organization string, projectID *string, capabilities ...string) (aliasScope, error) {
	if err := requireNonEmpty("organization", organization, 256); err != nil {
		return aliasScope{}, err
	}
	orgs, ok := s.deps.Store.(interface {
		GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error)
	})
	if !ok {
		return aliasScope{}, NewServiceError("internal", "organizations are not available")
	}
	org, err := orgs.GetOrganizationByName(ctx, organization)
	if err != nil {
		return aliasScope{}, mapStoreErr(err, "organization not found")
	}
	if err := requireTokenGrant(id, &org.OrgID, capabilities...); err != nil {
		return aliasScope{}, err
	}
	if projectID == nil || *projectID == "" {
		if err := s.deps.Resolver.RequireOrgAdmin(ctx, id, org.OrgID); err != nil {
			return aliasScope{}, mapPermissionErr(err)
		}
		return aliasScope{org: org}, nil
	}
	project, err := s.deps.Store.GetProjectByID(ctx, *projectID)
	if err != nil || project.OwnershipOrgID() != org.OrgID {
		return aliasScope{}, NewServiceError("not_found", "project not found")
	}
	if err := s.deps.Resolver.RequireProjectOwner(ctx, id, project.ProjectID); err != nil {
		return aliasScope{}, mapPermissionErr(err)
	}
	return aliasScope{org: org, project: project}, nil
}

// The token capabilities of the image alias operations. An alias is CI
// policy of the organization.
var (
	aliasReadCapabilities  = []string{tokencaps.OrganizationsRead, tokencaps.PoliciesManage}
	aliasWriteCapabilities = []string{tokencaps.PoliciesManage}
)

func imageAliasToCsil(alias *models.ImageAlias) csilapi.ImageAliasSummary {
	out := csilapi.ImageAliasSummary{
		AliasId:         alias.AliasID,
		OrgId:           alias.OrgID,
		Scope:           "organization",
		Name:            alias.Name,
		Kind:            alias.Kind,
		Os:              alias.OS,
		Arch:            alias.Arch,
		Characteristics: queueCharacteristicsToCsil(alias.Characteristics),
		ActiveRef:       alias.ActiveRef,
		ActiveDigest:    alias.ActiveDigest,
		PendingSince:    formatTimePtr(alias.PendingSince),
		CreatedAt:       formatTime(alias.CreatedAt),
		UpdatedAt:       formatTime(alias.UpdatedAt),
		UpdatedBy:       alias.UpdatedBy,
	}
	if alias.IsProjectAlias() {
		projectID := *alias.ProjectID
		out.ProjectId = &projectID
		out.Scope = "project"
	}
	if alias.PendingRef != nil {
		ref := *alias.PendingRef
		out.PendingRef = &ref
	}
	if alias.PendingDigest != nil {
		digest := *alias.PendingDigest
		out.PendingDigest = &digest
	}
	return out
}

// aliasCharacteristics validates the alias characteristics of a request.
// Values are scalar. os, arch, and image_digest are refused: os and arch are
// alias fields, and image_digest is the routing characteristic.
func aliasCharacteristics(entries []csilapi.CharacteristicEntry) (characteristics.Characteristics, error) {
	out := characteristics.Characteristics{}
	for _, entry := range entries {
		key := strings.TrimSpace(entry.Key)
		if key == "" {
			return nil, NewServiceError("invalid_argument", "a characteristic key must not be empty")
		}
		for _, reserved := range imageset.ReservedCharacteristicKeys {
			if key == reserved {
				return nil, NewServiceError("invalid_argument", fmt.Sprintf("characteristic %q is reserved: use the os and arch fields of the alias", key))
			}
		}
		if _, dup := out[key]; dup {
			return nil, NewServiceError("invalid_argument", fmt.Sprintf("characteristic %q is given two times", key))
		}
		parsed, err := characteristics.ParseJobCharacteristics(map[string]any{key: entry.Value})
		if err != nil {
			return nil, NewServiceError("invalid_argument", err.Error())
		}
		out[key] = parsed[key]
	}
	if class, ok := out[imageset.WorkerClassKey]; ok {
		if _, isText := class.(characteristics.StringValue); !isText {
			return nil, NewServiceError("invalid_argument", "characteristic worker_class must be a text value")
		}
	}
	return out, nil
}

func updatedByOf(id authz.Identity, user *models.User) string {
	if user != nil && user.UserID != "" {
		return user.UserID
	}
	if id.Token != nil {
		return "token"
	}
	return ""
}

func aliasAuditDetails(alias *models.ImageAlias, oldRef, oldDigest string) models.JSONB {
	details := models.JSONB{
		"name":       alias.Name,
		"kind":       alias.Kind,
		"os":         alias.OS,
		"arch":       alias.Arch,
		"old_ref":    oldRef,
		"old_digest": oldDigest,
		"active_ref": alias.ActiveRef,
		"scope":      "organization",
	}
	if alias.PendingRef != nil {
		details["pending_ref"] = *alias.PendingRef
	}
	if alias.IsProjectAlias() {
		details["scope"] = "project"
		details["project_id"] = *alias.ProjectID
	}
	return details
}

// ListImageAliases lists the aliases of one scope. A project scope also
// gives the organization aliases that the project uses.
func (s *UiService) ListImageAliases(ctx context.Context, req csilapi.ListImageAliasesRequest) (csilapi.ListImageAliasesResponse, error) {
	ctx, id, _, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return csilapi.ListImageAliasesResponse{}, err
	}
	scope, err := s.requireAliasScope(ctx, id, req.Organization, req.ProjectId, aliasReadCapabilities...)
	if err != nil {
		return csilapi.ListImageAliasesResponse{}, err
	}
	aliases, err := s.imageAliasStore()
	if err != nil {
		return csilapi.ListImageAliasesResponse{}, err
	}
	rows, err := aliases.ListImageAliases(ctx, scope.org.OrgID, scope.projectID())
	if err != nil {
		return csilapi.ListImageAliasesResponse{}, NewServiceError("internal", "could not list the image aliases")
	}
	out := make([]csilapi.ImageAliasSummary, 0, len(rows))
	names := map[string]bool{}
	for i := range rows {
		names[rows[i].Name] = true
		out = append(out, imageAliasToCsil(&rows[i]))
	}
	if scope.project != nil {
		orgRows, err := aliases.ListImageAliases(ctx, scope.org.OrgID, nil)
		if err != nil {
			return csilapi.ListImageAliasesResponse{}, NewServiceError("internal", "could not list the image aliases")
		}
		for i := range orgRows {
			if !names[orgRows[i].Name] {
				out = append(out, imageAliasToCsil(&orgRows[i]))
			}
		}
	}
	return csilapi.ListImageAliasesResponse{Aliases: out}, nil
}

// GetImageAlias returns one alias of exactly the scope of the request.
func (s *UiService) GetImageAlias(ctx context.Context, req csilapi.GetImageAliasRequest) (csilapi.GetImageAliasResponse, error) {
	ctx, id, _, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return csilapi.GetImageAliasResponse{}, err
	}
	scope, err := s.requireAliasScope(ctx, id, req.Organization, req.ProjectId, aliasReadCapabilities...)
	if err != nil {
		return csilapi.GetImageAliasResponse{}, err
	}
	aliases, err := s.imageAliasStore()
	if err != nil {
		return csilapi.GetImageAliasResponse{}, err
	}
	alias, err := aliases.GetImageAlias(ctx, scope.org.OrgID, scope.projectID(), req.Name)
	if err != nil {
		return csilapi.GetImageAliasResponse{}, mapStoreErr(err, "image alias not found")
	}
	return csilapi.GetImageAliasResponse{Alias: imageAliasToCsil(alias)}, nil
}

// SetImageAlias creates an alias or changes it (see the CSIL comment of
// SetImageAliasRequest for the rollout rule).
func (s *UiService) SetImageAlias(ctx context.Context, req csilapi.SetImageAliasRequest) (csilapi.SetImageAliasResponse, error) {
	ctx, id, user, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return csilapi.SetImageAliasResponse{}, err
	}
	scope, err := s.requireAliasScope(ctx, id, req.Organization, req.ProjectId, aliasWriteCapabilities...)
	if err != nil {
		return csilapi.SetImageAliasResponse{}, err
	}
	if err := imageref.ValidateAliasName(req.Name); err != nil {
		return csilapi.SetImageAliasResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	ref, err := imageref.ParsePinned(strings.TrimSpace(req.Ref))
	if err != nil {
		return csilapi.SetImageAliasResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	refText := ref.String()
	if err := s.checkAliasImagePolicy(ctx, scope.org.OrgID, refText); err != nil {
		return csilapi.SetImageAliasResponse{}, err
	}
	var chars characteristics.Characteristics
	if req.Characteristics != nil {
		if chars, err = aliasCharacteristics(req.Characteristics); err != nil {
			return csilapi.SetImageAliasResponse{}, err
		}
	}
	aliases, err := s.imageAliasStore()
	if err != nil {
		return csilapi.SetImageAliasResponse{}, err
	}
	updatedBy := updatedByOf(id, user)
	force := req.Force != nil && *req.Force

	existing, err := aliases.GetImageAlias(ctx, scope.org.OrgID, scope.projectID(), req.Name)
	if errors.Is(err, store.ErrNotFound) {
		return s.createImageAlias(ctx, aliases, scope, req, refText, ref.Digest, chars, updatedBy)
	}
	if err != nil {
		return csilapi.SetImageAliasResponse{}, NewServiceError("internal", "could not read the image alias")
	}
	// kind, os, arch, and characteristics select the workers and the jobs
	// of the alias. A change would skip the rollout (I10): the workers that
	// serve the new platform do not have the image yet. So they cannot
	// change; a different platform or constraint is a new alias name.
	if err := aliasMetadataChange(existing, req, chars); err != nil {
		return csilapi.SetImageAliasResponse{}, err
	}

	// The change runs on the locked alias row: a promotion at the same
	// time (the promotion loop) waits, and then sees the new pending pin, so
	// neither write undoes the other.
	var (
		before, alias models.ImageAlias
		outcome       string
		changed       bool
	)
	err = inAliasTransaction(ctx, aliases, func(txCtx context.Context) error {
		locked, err := aliases.LockImageAlias(txCtx, existing.AliasID)
		if err != nil {
			return err
		}
		before = *locked
		// The metadata of the locked row is the same: it cannot change.
		alias, outcome, changed = applyAliasSet(*locked, refText, ref.Digest, force, time.Now().UTC())
		if !changed {
			return nil
		}
		alias.UpdatedBy = updatedBy
		return aliases.UpdateImageAlias(txCtx, &alias)
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalidInput) {
			return csilapi.SetImageAliasResponse{}, NewServiceError("invalid_argument", err.Error())
		}
		return csilapi.SetImageAliasResponse{}, mapStoreErr(err, "image alias not found")
	}
	if !changed {
		return csilapi.SetImageAliasResponse{Alias: imageAliasToCsil(&before), Outcome: aliasOutcomeUnchanged}, nil
	}
	imageset.Invalidate()
	s.recordAudit(ctx, scope.org.OrgID, "image_alias.set", "image_alias", alias.AliasID,
		mergeDetails(aliasAuditDetails(&alias, before.ActiveRef, before.ActiveDigest), models.JSONB{"outcome": outcome, "forced": force}))
	if outcome == aliasOutcomePromoted {
		s.recordAudit(ctx, scope.org.OrgID, "image_alias.promote", "image_alias", alias.AliasID,
			imageset.PromotionDetails(&alias, before.ActiveRef, before.ActiveDigest, true, 0))
	}
	return csilapi.SetImageAliasResponse{Alias: imageAliasToCsil(&alias), Outcome: outcome}, nil
}

// inAliasTransaction runs fn in one transaction when the store has
// InTransaction (PostgresDbStore). Other stores run fn directly.
func inAliasTransaction(ctx context.Context, st any, fn func(context.Context) error) error {
	if tx, ok := st.(interface {
		InTransaction(context.Context, func(context.Context) error) error
	}); ok {
		return tx.InTransaction(ctx, fn)
	}
	return fn(ctx)
}

// aliasMetadataChange refuses a set-image-alias request that changes the
// kind, os, arch, or characteristics of an alias that exists. A value that
// is absent, or the same, is not a change.
func aliasMetadataChange(alias *models.ImageAlias, req csilapi.SetImageAliasRequest, chars characteristics.Characteristics) error {
	refuse := func(field, have, want string) error {
		return NewServiceError("failed_precondition", fmt.Sprintf(
			"image alias %q has %s %q; the %s of an alias cannot change after it is created. Create an alias with a new name for %s %q",
			alias.Name, field, have, field, field, want))
	}
	if req.Kind != nil && strings.TrimSpace(*req.Kind) != "" && strings.TrimSpace(*req.Kind) != alias.Kind {
		return refuse("kind", alias.Kind, strings.TrimSpace(*req.Kind))
	}
	if req.Os != nil && strings.TrimSpace(*req.Os) != "" && strings.TrimSpace(*req.Os) != alias.OS {
		return refuse("os", alias.OS, strings.TrimSpace(*req.Os))
	}
	if req.Arch != nil && strings.TrimSpace(*req.Arch) != "" && strings.TrimSpace(*req.Arch) != alias.Arch {
		return refuse("arch", alias.Arch, strings.TrimSpace(*req.Arch))
	}
	if chars != nil && characteristics.Hash(chars) != characteristics.Hash(alias.Characteristics) {
		return NewServiceError("failed_precondition", fmt.Sprintf(
			"image alias %q has other characteristics; the characteristics of an alias cannot change after it is created. Create an alias with a new name for other characteristics",
			alias.Name))
	}
	return nil
}

// applyAliasSet applies the pin of a set-image-alias request to an alias
// that exists. It returns the new alias, the outcome, and whether anything
// changed.
//
//   - The digest of the active pin: the active reference can change, and a
//     pending pin is cancelled.
//   - force: the reference becomes the active pin at once.
//   - The pending pin again: no pin change.
//   - Any other digest: the reference becomes the pending pin.
func applyAliasSet(alias models.ImageAlias, ref, digest string, force bool, now time.Time) (models.ImageAlias, string, bool) {
	changed := false
	outcome := aliasOutcomeUpdated
	switch {
	case digest == alias.ActiveDigest:
		if alias.ActiveRef != ref {
			alias.ActiveRef, changed = ref, true
		}
		if alias.PendingDigest != nil {
			alias.PendingRef, alias.PendingDigest, alias.PendingSince = nil, nil, nil
			changed = true
		}
	case force:
		alias.ActiveRef, alias.ActiveDigest = ref, digest
		alias.PendingRef, alias.PendingDigest, alias.PendingSince = nil, nil, nil
		changed, outcome = true, aliasOutcomePromoted
	case alias.PendingDigest != nil && *alias.PendingDigest == digest && alias.PendingRef != nil && *alias.PendingRef == ref:
	default:
		pendingRef, pendingDigest, since := ref, digest, now
		alias.PendingRef, alias.PendingDigest, alias.PendingSince = &pendingRef, &pendingDigest, &since
		changed, outcome = true, aliasOutcomePending
	}
	if !changed {
		return alias, aliasOutcomeUnchanged, false
	}
	return alias, outcome, true
}

func mergeDetails(base, more models.JSONB) models.JSONB {
	for key, value := range more {
		base[key] = value
	}
	return base
}

func (s *UiService) createImageAlias(ctx context.Context, aliases imageAliasUIStore, scope aliasScope, req csilapi.SetImageAliasRequest,
	ref, digest string, chars characteristics.Characteristics, updatedBy string) (csilapi.SetImageAliasResponse, error) {
	if req.Os == nil || strings.TrimSpace(*req.Os) == "" || req.Arch == nil || strings.TrimSpace(*req.Arch) == "" {
		return csilapi.SetImageAliasResponse{}, NewServiceError("invalid_argument", "a new image alias needs os and arch")
	}
	kind := models.ImageAliasKindContainer
	if imagealias.IsVMOS(*req.Os) {
		kind = models.ImageAliasKindVM
	}
	if req.Kind != nil && *req.Kind != "" {
		kind = *req.Kind
	}
	if chars == nil {
		chars = characteristics.Characteristics{}
	}
	alias := &models.ImageAlias{
		OrgID: scope.org.OrgID, ProjectID: scope.projectID(), Name: req.Name, Kind: kind,
		OS: strings.TrimSpace(*req.Os), Arch: strings.TrimSpace(*req.Arch), Characteristics: chars,
		ActiveRef: ref, ActiveDigest: digest, UpdatedBy: updatedBy,
	}
	if err := aliases.CreateImageAlias(ctx, alias); err != nil {
		switch {
		case errors.Is(err, store.ErrInvalidInput):
			return csilapi.SetImageAliasResponse{}, NewServiceError("invalid_argument", err.Error())
		case errors.Is(err, store.ErrAlreadyExists):
			return csilapi.SetImageAliasResponse{}, NewServiceError("conflict", "the image alias was created at the same time; try again")
		}
		return csilapi.SetImageAliasResponse{}, NewServiceError("internal", "could not create the image alias")
	}
	imageset.Invalidate()
	s.recordAudit(ctx, scope.org.OrgID, "image_alias.create", "image_alias", alias.AliasID, aliasAuditDetails(alias, "", ""))
	return csilapi.SetImageAliasResponse{Alias: imageAliasToCsil(alias), Outcome: aliasOutcomeCreated}, nil
}

// checkAliasImagePolicy refuses a reference that the organization image
// policy refuses. Job creation checks the policy again (imagealias).
func (s *UiService) checkAliasImagePolicy(ctx context.Context, orgID, ref string) error {
	policies, ok := s.deps.Store.(store.ImagePolicyStore)
	if !ok {
		return nil
	}
	policy, err := policies.GetImagePolicy(ctx, orgID)
	if err != nil {
		return NewServiceError("internal", "could not read the image policy")
	}
	if err := imagepolicy.Check(policy, ref); err != nil {
		return NewServiceError("invalid_argument", err.Error())
	}
	return nil
}

// DeleteImageAlias deletes one alias. It refuses while a job that is not
// terminal uses the alias (by name, or by a direct pinned reference to its
// active or pending digest): such a job could then wait with no worker that
// has its image.
func (s *UiService) DeleteImageAlias(ctx context.Context, req csilapi.DeleteImageAliasRequest) (csilapi.DeleteImageAliasResponse, error) {
	ctx, id, _, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return csilapi.DeleteImageAliasResponse{}, err
	}
	scope, err := s.requireAliasScope(ctx, id, req.Organization, req.ProjectId, aliasWriteCapabilities...)
	if err != nil {
		return csilapi.DeleteImageAliasResponse{}, err
	}
	aliases, err := s.imageAliasStore()
	if err != nil {
		return csilapi.DeleteImageAliasResponse{}, err
	}
	existing, err := aliases.GetImageAlias(ctx, scope.org.OrgID, scope.projectID(), req.Name)
	if err != nil {
		return csilapi.DeleteImageAliasResponse{}, mapStoreErr(err, "image alias not found")
	}
	if usage, ok := s.deps.Store.(store.ImageAliasUsage); ok {
		count, err := usage.CountActiveImageAliasJobs(ctx, existing)
		if err != nil {
			return csilapi.DeleteImageAliasResponse{}, NewServiceError("internal", "could not count the jobs that use the image alias")
		}
		if count > 0 {
			return csilapi.DeleteImageAliasResponse{}, NewServiceError("failed_precondition", fmt.Sprintf(
				"image alias %q is in use by %d job(s) that are not terminal; wait until they end, or cancel them, and then delete the alias",
				existing.Name, count))
		}
	}
	if err := aliases.DeleteImageAlias(ctx, scope.org.OrgID, scope.projectID(), req.Name); err != nil {
		return csilapi.DeleteImageAliasResponse{}, mapStoreErr(err, "image alias not found")
	}
	imageset.Invalidate()
	s.recordAudit(ctx, scope.org.OrgID, "image_alias.delete", "image_alias", existing.AliasID,
		aliasAuditDetails(existing, existing.ActiveRef, existing.ActiveDigest))
	return csilapi.DeleteImageAliasResponse{Deleted: true}, nil
}

// GetImageRolloutStatus returns the serving workers and their image state
// for each alias of a scope, or for one alias.
func (s *UiService) GetImageRolloutStatus(ctx context.Context, req csilapi.GetImageRolloutStatusRequest) (csilapi.GetImageRolloutStatusResponse, error) {
	ctx, id, _, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return csilapi.GetImageRolloutStatusResponse{}, err
	}
	scope, err := s.requireAliasScope(ctx, id, req.Organization, req.ProjectId, aliasReadCapabilities...)
	if err != nil {
		return csilapi.GetImageRolloutStatusResponse{}, err
	}
	aliases, err := s.imageAliasStore()
	if err != nil {
		return csilapi.GetImageRolloutStatusResponse{}, err
	}
	var rows []models.ImageAlias
	if req.Name != nil && *req.Name != "" {
		alias, err := aliases.GetImageAlias(ctx, scope.org.OrgID, scope.projectID(), *req.Name)
		if err != nil {
			return csilapi.GetImageRolloutStatusResponse{}, mapStoreErr(err, "image alias not found")
		}
		rows = []models.ImageAlias{*alias}
	} else if rows, err = aliases.ListImageAliases(ctx, scope.org.OrgID, scope.projectID()); err != nil {
		return csilapi.GetImageRolloutStatusResponse{}, NewServiceError("internal", "could not list the image aliases")
	}
	fleet, err := imageset.LoadFleet(ctx, aliases)
	if err != nil {
		return csilapi.GetImageRolloutStatusResponse{}, NewServiceError("internal", "could not read the workers")
	}
	now := time.Now().UTC()
	// Full worker rows only for the pools that the caller can manage (the
	// list-workers rule). For other pools (for example a hosted pool that
	// serves more organizations), counts and the pool name only.
	canManage := map[string]bool{}
	manages := func(poolID string) bool {
		if v, ok := canManage[poolID]; ok {
			return v
		}
		pool := fleet.Pools[poolID]
		v := pool != nil && s.requireViewWorkers(ctx, id, pool.OrgID) == nil
		canManage[poolID] = v
		return v
	}
	out := make([]csilapi.ImageAliasRollout, 0, len(rows))
	for i := range rows {
		rollout := imageset.EvaluateRollout(&rows[i], fleet, now)
		wire := csilapi.ImageAliasRollout{
			Alias:         imageAliasToCsil(&rows[i]),
			Workers:       make([]csilapi.ImageRolloutWorker, 0, len(rollout.Workers)),
			OnlineWorkers: int64(rollout.Online),
			ReadyWorkers:  int64(rollout.Ready),
			Promotable:    rollout.Promotable,
			Problems:      rollout.Problems(),
		}
		if rollout.Blocked != "" {
			blocked := rollout.Blocked
			wire.Blocked = &blocked
		}
		others := map[string]*csilapi.ImageRolloutPoolSummary{}
		for _, entry := range rollout.Workers {
			if manages(entry.Worker.PoolID) {
				wire.Workers = append(wire.Workers, rolloutWorkerToCsil(entry))
				continue
			}
			name := entry.Worker.PoolID
			if pool := fleet.Pools[entry.Worker.PoolID]; pool != nil && pool.Name != "" {
				name = pool.Name
			}
			sum := others[name]
			if sum == nil {
				sum = &csilapi.ImageRolloutPoolSummary{PoolName: name}
				others[name] = sum
			}
			sum.Workers++
			if entry.Online {
				sum.OnlineWorkers++
			}
			if entry.Ready {
				sum.ReadyWorkers++
			}
			if entry.ActiveFailure != nil || entry.PendingFailure != nil {
				sum.FailedWorkers++
			}
		}
		names := make([]string, 0, len(others))
		for name := range others {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			wire.OtherPools = append(wire.OtherPools, *others[name])
		}
		out = append(out, wire)
	}
	return csilapi.GetImageRolloutStatusResponse{Rollouts: out}, nil
}

// workerImageFailures returns the per-image failures of the newest report
// of a worker.
func (s *UiService) workerImageFailures(w *models.Worker) []csilapi.ImageFailureSummary {
	var out []csilapi.ImageFailureSummary
	for i := range w.ImageFailed {
		out = append(out, imageFailureToCsil(&w.ImageFailed[i]))
	}
	return out
}

func imageFailureToCsil(f *models.ImageFailure) csilapi.ImageFailureSummary {
	return csilapi.ImageFailureSummary{Digest: f.Digest, Permanent: f.Permanent, Detail: f.Detail}
}

func rolloutWorkerToCsil(entry imageset.RolloutWorker) csilapi.ImageRolloutWorker {
	w := entry.Worker
	status, detail, _ := imageset.Readiness(w)
	out := csilapi.ImageRolloutWorker{
		WorkerId:             w.WorkerID,
		PoolId:               w.PoolID,
		Online:               entry.Online,
		ImageStatus:          status,
		ImageSetVersion:      w.ImageSetVersion,
		ImageReportedVersion: w.ImageStateVersion,
		HasActive:            entry.HasActive,
		HasPending:           entry.HasPending,
		Ready:                entry.Ready,
	}
	if w.Hostname != "" {
		hostname := w.Hostname
		out.Hostname = &hostname
	}
	if detail != "" {
		out.ImageStatusDetail = &detail
	}
	for _, f := range []*models.ImageFailure{entry.ActiveFailure, entry.PendingFailure} {
		if f != nil {
			out.Failed = append(out.Failed, imageFailureToCsil(f))
		}
	}
	return out
}

// GetWorkerImageState returns the image state of one worker and the set
// that the coordinator computes for it now. It needs the worker view
// permission of the pool organization (the list-workers rule: a token needs
// workers:read or workers:manage).
func (s *UiService) GetWorkerImageState(ctx context.Context, req csilapi.GetWorkerImageStateRequest) (csilapi.GetWorkerImageStateResponse, error) {
	ctx, id, _, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return csilapi.GetWorkerImageStateResponse{}, err
	}
	if err := requireNonEmpty("worker_id", req.WorkerId, 64); err != nil {
		return csilapi.GetWorkerImageStateResponse{}, err
	}
	worker, err := s.deps.Store.GetWorkerByID(ctx, req.WorkerId)
	if err != nil {
		return csilapi.GetWorkerImageStateResponse{}, mapStoreErr(err, "worker not found")
	}
	orgID, err := s.resolveWorkerOrgScope(ctx, worker)
	if err != nil {
		return csilapi.GetWorkerImageStateResponse{}, mapStoreErr(err, "worker's pool not found")
	}
	if err := s.requireViewWorkers(ctx, id, orgID); err != nil {
		return csilapi.GetWorkerImageStateResponse{}, err
	}
	out := csilapi.GetWorkerImageStateResponse{
		WorkerId:             worker.WorkerID,
		ImageAware:           imageset.ImageAware(worker),
		ImageRuntime:         worker.ImageRuntime,
		ImageSetVersion:      worker.ImageSetVersion,
		ImageReportedVersion: worker.ImageStateVersion,
		State:                worker.ImageState,
		ReadyDigests:         append([]string{}, worker.ImageReadyDigests...),
		Images:               []csilapi.WorkerImageSetEntrySummary{},
	}
	out.Failed = s.workerImageFailures(worker)
	if status, detail, ok := imageset.Readiness(worker); ok {
		out.ImageStatus = &status
		if detail != "" {
			out.ImageStatusDetail = &detail
		}
	}
	if !out.ImageAware {
		return out, nil
	}
	sets, ok := s.deps.Store.(imageset.Store)
	if !ok {
		return out, nil
	}
	set, err := imageset.ComputeFor(ctx, sets, worker)
	if err != nil {
		return csilapi.GetWorkerImageStateResponse{}, NewServiceError("internal", "could not compute the image set")
	}
	for _, entry := range set.Entries {
		summary := csilapi.WorkerImageSetEntrySummary{
			Ref: entry.Ref, Digest: entry.Digest, Kind: entry.Kind, Aliases: append([]string{}, entry.Aliases...),
			Ready: imageset.HasDigest(worker, entry.Digest),
		}
		if entry.Pending {
			pending := true
			summary.Pending = &pending
		}
		if f := models.FailureFor(worker.ImageFailed, entry.Digest); f != nil && !summary.Ready {
			failure := imageFailureToCsil(f)
			summary.Failure = &failure
		}
		out.Images = append(out.Images, summary)
	}
	return out, nil
}
