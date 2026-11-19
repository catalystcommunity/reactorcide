// Package imagealias resolves the image of a job at job creation
// (VM_IMAGES_PLAN.md "Resolution"). Every path that creates a job from a
// job definition calls ResolveJob before the queue find-or-create. A retry
// and a rerun copy the stored values and do not call it.
//
// Rules:
//
//   - A job image that has the form of an alias name, and that names a
//     project alias (of the job project) or an organization alias, becomes
//     the active reference of that alias, in the image field that holds
//     the name (JobImage: container_image when set, else runner_image). The job records the alias name in
//     Job.ImageAlias and gets the characteristic image_digest.
//   - Any other image is a reference. A container job routes as before.
//   - A VM job (characteristic os is macos or windows) must name an alias
//     or a pinned reference whose digest an alias in its scope pins, with
//     the same kind and platform (os, arch). Else the job is refused: no
//     worker can have the image (owner decision I4). The job then stores
//     the reference of that alias (not the reference that the job named),
//     so that the job never chooses the pull location for a pool. The job
//     records no alias name: the display shows the reference alone (I2).
//   - A job that can never run is refused at creation: its os or arch
//     characteristic conflicts with the platform of the alias, an alias
//     characteristic conflicts with a job characteristic, or the alias has
//     a worker_class that is not the worker class of the job.
//   - ResolveRetry is the rule for a retry and a rerun (owner decision,
//     2026-11-18): the job resolves its image again through an alias and
//     gets the current pin of that alias. A job that named an alias uses
//     that alias. A job that named a pinned reference uses the alias that
//     pinned the digest at creation (Job.ImagePinAlias). Reason: after an
//     alias update the old digest leaves the worker image sets, so a job
//     that keeps it cannot run. The retry is refused only when the alias
//     does not exist any more, or does not fit the job.
//   - CheckCarried is the rule for a triggered child job that keeps the
//     resolved image of its parent: the digest must still be in the image
//     sets of the workers (an alias of the same kind and platform pins it,
//     or the alias that the job named still exists with the same kind and
//     platform, so that the drain keeps the digest).
//   - The organization image policy checks the reference of an alias and
//     the pinned reference of a VM job here. A container job reference is
//     checked as before: at REST submit and at the claim.
package imagealias

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagepolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imageref"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// DigestCharacteristic is the job characteristic that routes a job to the
// workers that have its image (VM_IMAGES_PLAN.md "Routing").
const DigestCharacteristic = "image_digest"

// Refusal codes.
const (
	// CodeImageAlias is a refusal by the image alias rules.
	CodeImageAlias = "image_alias"
	// CodeImagePolicy is a refusal by the organization image policy.
	CodeImagePolicy = "image_policy"
)

// RefusedError is a job creation refusal. Callers show Message to the
// user. Any other error from ResolveJob is a store error (for example the
// database is not available), and the caller must keep its work retryable.
type RefusedError struct {
	Code    string
	Message string
}

func (e *RefusedError) Error() string { return e.Message }

// IsRefused reports whether err is a RefusedError, and returns it.
func IsRefused(err error) (*RefusedError, bool) {
	var refused *RefusedError
	if errors.As(err, &refused) {
		return refused, true
	}
	return nil, false
}

func refuse(code, format string, args ...any) error {
	return &RefusedError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// IsVMJob reports whether a job runs in a VM: its characteristic os is
// macos or windows.
func IsVMJob(job *models.Job) bool {
	if job == nil {
		return false
	}
	return IsVMOS(osOf(job.Characteristics))
}

// IsVMOS reports whether jobs with this os characteristic run in a VM.
func IsVMOS(osName string) bool { return osName == "macos" || osName == "windows" }

func osOf(chars characteristics.Characteristics) string {
	if value, ok := chars["os"].(characteristics.StringValue); ok {
		return string(value)
	}
	return ""
}

func archOf(chars characteristics.Characteristics) string {
	if value, ok := chars["arch"].(characteristics.StringValue); ok {
		return string(value)
	}
	return ""
}

// workerClassKey is the alias characteristic that names a worker class
// (imageset.WorkerClassKey; imageset imports this package).
const workerClassKey = "worker_class"

// JobPlatform returns the kind and the platform of the image of a job:
// kind vm for a VM job, else container; the os characteristic (the
// default os when the job has none); the arch characteristic, or "" (any
// arch) when the job has none.
func JobPlatform(job *models.Job) models.ImagePlatform {
	return PlatformOf(job.Characteristics)
}

// PlatformOf is JobPlatform for the characteristics of a job.
func PlatformOf(chars characteristics.Characteristics) models.ImagePlatform {
	p := models.ImagePlatform{Kind: models.ImageAliasKindContainer, OS: osOf(chars), Arch: archOf(chars)}
	if IsVMOS(p.OS) {
		p.Kind = models.ImageAliasKindVM
	}
	if p.OS == "" {
		p.OS = characteristics.DefaultOS
	}
	return p
}

// checkFits refuses a job that can never run with the image of alias: the
// workers that get the image (alias platform and characteristics) are not
// workers that can claim the job (job characteristics and worker class).
func checkFits(alias *models.ImageAlias, job *models.Job) error {
	platform := JobPlatform(job)
	if alias.OS != platform.OS {
		return refuse(CodeImageAlias, "image alias %q is for os %q, but the job needs os %q: no worker can run the job. Set the job characteristic os to %q, or use an image alias for os %q",
			alias.Name, alias.OS, platform.OS, alias.OS, platform.OS)
	}
	if platform.Arch != "" && alias.Arch != platform.Arch {
		return refuse(CodeImageAlias, "image alias %q is for arch %q, but the job needs arch %q: no worker can run the job. Set the job characteristic arch to %q, remove it, or use an image alias for arch %q",
			alias.Name, alias.Arch, platform.Arch, alias.Arch, platform.Arch)
	}
	jobClass := job.WorkerClass
	if jobClass == "" {
		jobClass = "default"
	}
	keys := make([]string, 0, len(alias.Characteristics))
	for key := range alias.Characteristics {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := alias.Characteristics[key]
		switch key {
		case "os", "arch", DigestCharacteristic:
			continue
		case workerClassKey:
			class, ok := value.(characteristics.StringValue)
			if ok && string(class) != jobClass {
				return refuse(CodeImageAlias, "image alias %q is only for worker class %q, but the job uses worker class %q: no worker can run the job. Set the job worker_class to %q, or use another image alias",
					alias.Name, string(class), jobClass, string(class))
			}
			continue
		}
		jobValue, ok := job.Characteristics[key]
		if !ok {
			continue
		}
		if !value.Matches(jobValue) {
			return refuse(CodeImageAlias, "image alias %q needs the worker characteristic %s=%s, but the job needs %s=%s: no worker can run the job. Set the job characteristic %s to %s, remove it, or use another image alias",
				alias.Name, key, characteristicText(value), key, characteristicText(jobValue), key, characteristicText(value))
		}
	}
	return nil
}

// characteristicText is the value part of Value.String ("<kind>:<value>").
func characteristicText(v characteristics.Value) string {
	text := v.String()
	if _, after, ok := strings.Cut(text, ":"); ok {
		return after
	}
	return text
}

// Result tells what ResolveJob did.
type Result struct {
	// Alias is the alias that the job named, or nil.
	Alias *models.ImageAlias
	// Digest is the digest that ResolveJob added as the image_digest
	// characteristic, or "".
	Digest string
}

// ResolveJob resolves the image of job in place. It reads aliases and the
// image policy from st with type assertions (store.ImageAliasStore,
// store.ImagePolicyStore). A store without ImageAliasStore has no aliases:
// ResolveJob then changes nothing (some in-memory test stores).
//
// The job must have its OrgID, ProjectID, and Characteristics set. Call it
// before the queue find-or-create, because it can add a characteristic.
func ResolveJob(ctx context.Context, st any, job *models.Job) (Result, error) {
	var result Result
	// Only ResolveJob and ApplyCarried set the alias of a job and the
	// image_digest characteristic. A job can get the characteristics map of
	// its parent: copy it before a change, and remove an inherited digest.
	job.ImageAlias = nil
	job.ImagePinAlias = nil
	job.ImageRetryOf = nil
	if _, ok := job.Characteristics[DigestCharacteristic]; ok {
		job.Characteristics = cloneCharacteristics(job.Characteristics)
		delete(job.Characteristics, DigestCharacteristic)
	}
	image := strings.TrimSpace(JobImage(job))
	vm := IsVMJob(job)
	orgID := job.OwnershipOrgID()
	aliases, hasAliases := st.(store.ImageAliasStore)

	if image == "" {
		if vm && hasAliases {
			return result, refuse(CodeImageAlias, "a %s VM job needs an image: set the job image to an image alias name", osOf(job.Characteristics))
		}
		// The default runner image. The REST path and the claim check
		// the image policy on it.
		return result, nil
	}

	if hasAliases && orgID != "" && imageref.IsAliasName(image) {
		alias, err := aliases.ResolveImageAlias(ctx, orgID, job.ProjectID, image)
		switch {
		case err == nil:
			wantKind := models.ImageAliasKindContainer
			if vm {
				wantKind = models.ImageAliasKindVM
			}
			if alias.Kind != wantKind {
				return result, refuse(CodeImageAlias, "image alias %q is a %s image, but the job is a %s job (job os %q): no worker can run the job. Use a %s image alias, or change the job characteristic os",
					alias.Name, alias.Kind, wantKind, JobPlatform(job).OS, wantKind)
			}
			if err := checkFits(alias, job); err != nil {
				return result, err
			}
			if err := checkPolicy(ctx, st, orgID, alias.ActiveRef); err != nil {
				return result, err
			}
			name := alias.Name
			setJobImage(job, JobReference(alias.ActiveRef))
			job.ImageAlias = &name
			setDigest(job, alias.ActiveDigest)
			result.Alias = alias
			result.Digest = alias.ActiveDigest
			return result, nil
		case errors.Is(err, store.ErrNotFound):
			// Not an alias: the value is a reference.
		default:
			return result, fmt.Errorf("resolve image alias %q: %w", image, err)
		}
	}

	if vm && hasAliases {
		ref, err := imageref.ParsePinned(image)
		if err != nil {
			return result, refuse(CodeImageAlias,
				"VM job image %q is not an image alias of this project or organization, and is not a pinned reference (%v). Add an image alias for the image and set the job image to the alias name", image, err)
		}
		platform := JobPlatform(job)
		pinning, err := aliases.ListImageAliasesWithDigest(ctx, orgID, job.ProjectID, ref.Digest, platform)
		if err != nil {
			return result, fmt.Errorf("find image aliases for digest %s: %w", ref.Digest, err)
		}
		if len(pinning) == 0 {
			return result, refuse(CodeImageAlias,
				"no %s image alias of this project or organization for %s pins %s, so no worker has the image of VM job image %q. Add an image alias that pins the reference",
				platform.Kind, platformText(platform), ref.Digest, image)
		}
		// The first alias that fits the job. The job stores the reference
		// of that alias: image sets use alias references, so a job never
		// chooses the pull location of a pool.
		var chosen *models.ImageAlias
		var fitErr error
		for i := range pinning {
			if err := checkFits(&pinning[i], job); err != nil {
				if fitErr == nil {
					fitErr = err
				}
				continue
			}
			chosen = &pinning[i]
			break
		}
		if chosen == nil {
			return result, fitErr
		}
		aliasRef := chosen.ActiveRef
		if chosen.ActiveDigest != ref.Digest && chosen.PendingRef != nil {
			aliasRef = *chosen.PendingRef
		}
		if err := checkPolicy(ctx, st, orgID, aliasRef); err != nil {
			return result, err
		}
		setJobImage(job, JobReference(aliasRef))
		setDigest(job, ref.Digest)
		// A retry of the job resolves through this alias (ResolveRetry).
		pinName := chosen.Name
		job.ImagePinAlias = &pinName
		result.Digest = ref.Digest
		return result, nil
	}

	// A container job with a reference routes as before. The REST submit
	// path and the claim check the image policy on it, as before.
	return result, nil
}

func platformText(p models.ImagePlatform) string {
	if p.Arch == "" {
		return p.OS
	}
	return p.OS + "/" + p.Arch
}

// CheckCarried refuses a triggered child job that inherits the resolved
// image of its parent when no worker can get that image any more. (A
// retry and a rerun use ResolveRetry.) A job with no image_digest is not checked. The digest is still in
// the image sets when one of these is true:
//
//   - An alias of the job scope with the kind and the platform of the job
//     pins the digest (active or pending).
//   - The job named an alias, and that name resolves in the job scope
//     (project alias, else organization alias) to an alias with the kind
//     and the platform of the job: the drain of that alias keeps the
//     digests of its jobs that are not terminal, this job included.
//
// Call it after ApplyCarried, before the queue find-or-create.
func CheckCarried(ctx context.Context, st any, job *models.Job) error {
	carried := CarriedFromJob(job)
	if carried.Digest == "" {
		return nil
	}
	aliases, ok := st.(store.ImageAliasStore)
	orgID := job.OwnershipOrgID()
	if !ok || orgID == "" {
		return nil
	}
	platform := JobPlatform(job)
	pinning, err := aliases.ListImageAliasesWithDigest(ctx, orgID, job.ProjectID, carried.Digest, platform)
	if err != nil {
		return fmt.Errorf("find image aliases for digest %s: %w", carried.Digest, err)
	}
	if len(pinning) > 0 {
		return nil
	}
	if carried.Alias != nil {
		alias, err := aliases.ResolveImageAlias(ctx, orgID, job.ProjectID, *carried.Alias)
		switch {
		case err == nil && platform.Matches(alias):
			return nil
		case err == nil:
			return refuse(CodeImageAlias,
				"image alias %q is now a %s image for %s/%s, and no %s image alias of this project or organization for %s pins %s (the image that the job keeps from an earlier run or from its parent job: %s). No worker can get the image. Run the job again from its definition to use the current alias",
				*carried.Alias, alias.Kind, alias.OS, alias.Arch, platform.Kind, platformText(platform), carried.Digest, Display(carried.Ref, carried.Alias))
		case !errors.Is(err, store.ErrNotFound):
			return fmt.Errorf("resolve image alias %q: %w", *carried.Alias, err)
		}
		return refuse(CodeImageAlias,
			"image alias %q does not exist any more, and no %s image alias of this project or organization for %s pins %s (the image that the job keeps from an earlier run or from its parent job: %s). No worker can get the image. Run the job again from its definition, or add an alias that pins the image",
			*carried.Alias, platform.Kind, platformText(platform), carried.Digest, Display(carried.Ref, carried.Alias))
	}
	return refuse(CodeImageAlias,
		"no %s image alias of this project or organization for %s pins %s (the image that the job keeps from an earlier run or from its parent job: %s) any more. No worker can get the image. Run the job again from its definition, or add an alias that pins the image",
		platform.Kind, platformText(platform), carried.Digest, carried.Ref)
}

// JobImage returns the image of a job as the lease uses it: ContainerImage
// when it is set, else RunnerImage. It returns "" when neither is set (the
// worker then uses its default runner image).
func JobImage(job *models.Job) string {
	if job == nil {
		return ""
	}
	if job.ContainerImage != nil && *job.ContainerImage != "" {
		return *job.ContainerImage
	}
	return job.RunnerImage
}

// JobReference returns the form of ref that a job stores and a worker
// pulls: an oci:// reference loses its scheme (registries, container
// runtimes, and the worker OCI image source use the plain form). Other
// references do not change.
func JobReference(ref string) string {
	parsed, err := imageref.Parse(ref)
	if err != nil || parsed.Scheme != imageref.SchemeOCI {
		return ref
	}
	return parsed.WithoutScheme()
}

// setJobImage writes ref to the image field that JobImage reads, so that
// the lease, the job record, and the logs show the resolved reference.
// ResolveJob passes the JobReference form; ApplyCarried passes the stored
// value unchanged.
func setJobImage(job *models.Job, ref string) {
	if job.ContainerImage != nil && *job.ContainerImage != "" {
		job.ContainerImage = &ref
		return
	}
	job.RunnerImage = ref
}

// setDigest sets the image_digest characteristic on a copy of the
// characteristics map, so that a map shared with another job does not
// change.
func setDigest(job *models.Job, digest string) {
	chars := cloneCharacteristics(job.Characteristics)
	if _, ok := chars["os"]; !ok {
		chars["os"] = characteristics.StringValue(characteristics.DefaultOS)
	}
	chars[DigestCharacteristic] = characteristics.StringValue(digest)
	job.Characteristics = chars
}

func cloneCharacteristics(in characteristics.Characteristics) characteristics.Characteristics {
	out := make(characteristics.Characteristics, len(in)+1)
	for key, value := range in {
		out[key] = value
	}
	return out
}

// checkPolicy runs the organization image policy on ref. A policy read
// error does not refuse the job: the claim checks the policy again and
// fails closed (the same rule as the REST submit check).
func checkPolicy(ctx context.Context, st any, orgID, ref string) error {
	policies, ok := st.(store.ImagePolicyStore)
	if !ok || orgID == "" {
		return nil
	}
	policy, err := policies.GetImagePolicy(ctx, orgID)
	if err != nil {
		return nil
	}
	if err := imagepolicy.Check(policy, ref); err != nil {
		return &RefusedError{Code: CodeImagePolicy, Message: err.Error()}
	}
	return nil
}

// Carried is the resolved image of an earlier job: the image that a
// triggered child inherits from its parent, or the image of the earlier
// run that a retry or a re-run resolves again (ResolveRetry).
type Carried struct {
	// Ref is the image of the job (JobImage).
	Ref string
	// Alias is the alias name, or nil.
	Alias *string
	// PinAlias is the alias that pinned the digest of a direct pinned
	// reference (Job.ImagePinAlias), or nil.
	PinAlias *string
	// Digest is the value of the image_digest characteristic, or "".
	Digest string
}

// CarriedFromJob returns the resolved image of job.
func CarriedFromJob(job *models.Job) Carried {
	carried := Carried{Ref: JobImage(job)}
	if job.ImageAlias != nil && *job.ImageAlias != "" {
		name := *job.ImageAlias
		carried.Alias = &name
	}
	if job.ImagePinAlias != nil && *job.ImagePinAlias != "" {
		name := *job.ImagePinAlias
		carried.PinAlias = &name
	}
	if value, ok := job.Characteristics[DigestCharacteristic].(characteristics.StringValue); ok {
		carried.Digest = string(value)
	}
	return carried
}

// ApplyCarried gives job the resolved image of an earlier run. Use it in
// place of ResolveJob, before the queue find-or-create.
func ApplyCarried(job *models.Job, carried Carried) {
	if carried.Ref != "" {
		setJobImage(job, carried.Ref)
	}
	job.ImageAlias = nil
	if carried.Alias != nil && *carried.Alias != "" {
		name := *carried.Alias
		job.ImageAlias = &name
	}
	job.ImagePinAlias = nil
	if carried.PinAlias != nil && *carried.PinAlias != "" {
		name := *carried.PinAlias
		job.ImagePinAlias = &name
	}
	if carried.Digest != "" {
		setDigest(job, carried.Digest)
	} else if _, ok := job.Characteristics[DigestCharacteristic]; ok {
		job.Characteristics = cloneCharacteristics(job.Characteristics)
		delete(job.Characteristics, DigestCharacteristic)
	}
}

// Display returns the image text for logs, events, and the UI (owner
// decision I2): "name (ref)" when the job used an alias, else ref.
func Display(ref string, alias *string) string {
	if alias != nil && *alias != "" {
		return *alias + " (" + ref + ")"
	}
	return ref
}

// DisplayJob returns Display for the image of a job (JobImage). It returns
// "" when the job has no image.
func DisplayJob(job *models.Job) string {
	image := JobImage(job)
	if image == "" {
		return ""
	}
	return Display(image, job.ImageAlias)
}

// RetryResult tells what ResolveRetry did.
type RetryResult struct {
	// Alias is the alias that the retry resolved through, or nil when the
	// earlier run has no image_digest (nothing to resolve).
	Alias *models.ImageAlias
	// Changed is true when the retry got another digest than the earlier
	// run (the alias has a new pin).
	Changed bool
	// Previous is the image of the earlier run in display form.
	Previous string
}

// ResolveRetry gives a retry or a rerun job (job, a copy of the earlier
// run) its image. earlier is the resolved image of the earlier run
// (CarriedFromJob of the earlier job, or the carried values of a workflow
// node). Owner decision 2026-11-18: a retry after an alias update is
// allowed and uses the current pin, because the old digest leaves the
// worker image sets after the drain.
//
//   - No image_digest (a container job with a plain reference): the job
//     keeps the image of the earlier run.
//   - The earlier run named an alias: the alias name resolves again in
//     the job scope (project alias, else organization alias), and the job
//     gets its active pin.
//   - The earlier run named a pinned reference: the job resolves through
//     the alias that pinned the digest at creation (Job.ImagePinAlias; for
//     a job from before migration 000072, an alias that pins the digest
//     now). While that alias still pins the digest (active or pending),
//     the job keeps it. Else the job gets the active pin, and records the
//     alias name (Job.ImageAlias), so that the display is "alias (ref)".
//
// When the digest changes, Job.ImageRetryOf records the image of the
// earlier run. The retry is refused (a RefusedError) when the alias does
// not exist any more, is now for another kind or platform, does not fit
// the job (checkFits), or the image policy refuses its reference.
func ResolveRetry(ctx context.Context, st any, job *models.Job, earlier Carried) (RetryResult, error) {
	ApplyCarried(job, earlier)
	job.ImageRetryOf = nil
	result := RetryResult{Previous: Display(earlier.Ref, earlier.Alias)}
	if earlier.Digest == "" {
		return result, nil
	}
	aliases, ok := st.(store.ImageAliasStore)
	orgID := job.OwnershipOrgID()
	if !ok || orgID == "" {
		return result, nil
	}
	platform := JobPlatform(job)
	named := earlier.Alias != nil && *earlier.Alias != ""
	var name string
	switch {
	case named:
		name = *earlier.Alias
	case earlier.PinAlias != nil && *earlier.PinAlias != "":
		name = *earlier.PinAlias
	default:
		// A job from before migration 000072 does not record the alias
		// that pinned its digest. Use an alias that pins it now.
		pinning, err := aliases.ListImageAliasesWithDigest(ctx, orgID, job.ProjectID, earlier.Digest, platform)
		if err != nil {
			return result, fmt.Errorf("find image aliases for digest %s: %w", earlier.Digest, err)
		}
		for i := range pinning {
			if checkFits(&pinning[i], job) == nil {
				name = pinning[i].Name
				break
			}
		}
		if name == "" {
			return result, refuse(CodeImageAlias,
				"no %s image alias of this project or organization for %s pins %s (the image of the earlier run: %s), and the job does not record the alias that pinned it. The retry cannot get an image. Run the job again from its definition, or add an image alias that pins the image",
				platform.Kind, platformText(platform), earlier.Digest, result.Previous)
		}
	}

	alias, err := aliases.ResolveImageAlias(ctx, orgID, job.ProjectID, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return result, refuse(CodeImageAlias,
			"image alias %q does not exist any more, so the retry cannot get an image (the image of the earlier run: %s). Add the image alias %q again, or run the job from a definition that names another image alias",
			name, result.Previous, name)
	case err != nil:
		return result, fmt.Errorf("resolve image alias %q: %w", name, err)
	}
	if !platform.Matches(alias) {
		return result, refuse(CodeImageAlias,
			"image alias %q is now a %s image for %s/%s, but the job is a %s job for %s (the image of the earlier run: %s). The retry cannot get an image. Run the job from a definition that names an image alias for %s",
			alias.Name, alias.Kind, alias.OS, alias.Arch, platform.Kind, platformText(platform), result.Previous, platformText(platform))
	}
	if err := checkFits(alias, job); err != nil {
		return result, err
	}

	ref, digest := alias.ActiveRef, alias.ActiveDigest
	if !named && alias.HasDigest(earlier.Digest) {
		// The alias still pins the digest that the job named: keep it.
		digest = earlier.Digest
		if alias.ActiveDigest != digest && alias.PendingRef != nil {
			ref = *alias.PendingRef
		}
	}
	if err := checkPolicy(ctx, st, orgID, ref); err != nil {
		return result, err
	}
	setJobImage(job, JobReference(ref))
	setDigest(job, digest)
	result.Alias = alias
	aliasName := alias.Name
	if named {
		job.ImageAlias = &aliasName
		job.ImagePinAlias = nil
	} else {
		job.ImagePinAlias = &aliasName
	}
	if digest != earlier.Digest {
		result.Changed = true
		previous := result.Previous
		job.ImageRetryOf = &previous
		// The image now comes from the alias, not from the reference that
		// the job named: the display is "alias (ref)" (owner decision I2).
		job.ImageAlias = &aliasName
		job.ImagePinAlias = nil
	}
	return result, nil
}
