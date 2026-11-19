// Package imageset computes worker image sets and image alias rollouts
// (docs/image-aliases.md, VM_IMAGES_PLAN.md "Worker image sets" and
// "Rollout").
//
// A worker image set is every image that the worker can be asked to run
// (owner decisions I4, I5, I11). An alias goes to a worker when all of
// these are true:
//
//   - Pool boundary: the pool of the worker has a worker class grant in the
//     organization of the alias. An organization pool serves only its own
//     organization (the same rule as ListQueuesForPool). A hosted pool
//     serves each organization that granted it a class.
//   - Kind: a vm alias goes to a worker with runtime vm, a container alias
//     to a worker with runtime docker, containerd, or kubernetes.
//   - Platform: the os and arch of the alias are the os and arch of the
//     worker.
//   - Characteristics: the worker satisfies the alias characteristics with
//     the queue rule (characteristics.Satisfies). The key worker_class is
//     special: the pool must have a grant for that class in the
//     organization of the alias.
//
// For each alias that the worker serves, the set has the active pin, the
// pending pin (during a rollout), and each older digest that a job that is
// not terminal still uses (drain). A job with a direct pinned VM reference
// (no alias) also keeps its digest in the set of each worker that could
// run it, until the job ends. The set has one entry for each digest. An
// entry that is only a pending pin is marked Pending: the worker pulls it,
// but its startup gate does not wait for it.
package imageset

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagealias"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// WorkerClassKey is the alias characteristic that names a worker class. It
// is matched against the class grants of the pool, not against the worker
// characteristics.
const WorkerClassKey = "worker_class"

// ReservedCharacteristicKeys are the keys that an alias characteristic
// cannot use: os and arch are alias fields, and image_digest is the routing
// characteristic (imagealias.DigestCharacteristic).
var ReservedCharacteristicKeys = []string{"os", "arch", imagealias.DigestCharacteristic}

// OnlineWindow is how long after its last heartbeat a worker is online.
// Workers heartbeat every 30 seconds (workerauth.HeartbeatInterval); three
// missed heartbeats make a worker offline.
const OnlineWindow = 90 * time.Second

// generation increases at each alias change on this replica. A cached image
// set of an older generation is computed again (see Invalidate).
var generation atomic.Uint64

// Invalidate tells this replica that an alias changed. The next heartbeat
// of each worker computes its image set again.
func Invalidate() { generation.Add(1) }

// Generation returns the alias change generation of this replica.
func Generation() uint64 { return generation.Load() }

// KindForRuntime returns the alias kind that a worker runtime serves, or ""
// for an unknown runtime.
func KindForRuntime(runtime string) string {
	switch runtime {
	case models.WorkerRuntimeVM:
		return models.ImageAliasKindVM
	case models.WorkerRuntimeDocker, models.WorkerRuntimeContainerd, models.WorkerRuntimeKubernetes:
		return models.ImageAliasKindContainer
	}
	return ""
}

// ImageAware reports whether a worker takes part in image sets: it sent a
// runtime at Register, or it sent an image set report.
func ImageAware(w *models.Worker) bool {
	return w != nil && ((w.ImageRuntime != nil && *w.ImageRuntime != "") || w.ImageState != nil)
}

// WorkerKind returns the alias kind of the images that the worker holds. A
// worker with a runtime uses KindForRuntime. A worker that sent a report but
// no runtime gets vm when its os is a VM guest os (macos, windows), else
// container.
func WorkerKind(w *models.Worker) string {
	if w == nil {
		return ""
	}
	if w.ImageRuntime != nil && *w.ImageRuntime != "" {
		return KindForRuntime(*w.ImageRuntime)
	}
	if imagealias.IsVMOS(w.OS) {
		return models.ImageAliasKindVM
	}
	return models.ImageAliasKindContainer
}

// Online reports whether a worker is online at now: its status is active,
// it has an active session, and its last heartbeat is in OnlineWindow. Only
// ListWorkers sets HasActiveSession; use a worker from ListWorkers.
func Online(w *models.Worker, now time.Time) bool {
	return w != nil && w.Status == models.WorkerStatusActive && w.HasActiveSession &&
		w.LastSeenAt != nil && now.Sub(*w.LastSeenAt) <= OnlineWindow
}

// Grants are the worker classes that a pool serves: organization ID to the
// set of class names.
type Grants map[string]map[string]bool

// PoolGrants returns the grants of one pool. For an organization pool, only
// grants in its own organization count.
func PoolGrants(pool *models.WorkerPool, grants []models.PoolClassGrant) Grants {
	out := Grants{}
	for _, grant := range grants {
		if grant.PoolID != pool.PoolID {
			continue
		}
		if pool.OrgID != nil && *pool.OrgID != "" && grant.OrgID != *pool.OrgID {
			continue
		}
		if out[grant.OrgID] == nil {
			out[grant.OrgID] = map[string]bool{}
		}
		out[grant.OrgID][grant.ClassName] = true
	}
	return out
}

// OrgIDs returns the organizations of the grants, sorted.
func (g Grants) OrgIDs() []string {
	ids := make([]string, 0, len(g))
	for id := range g {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Target is what decides which aliases a worker serves.
type Target struct {
	Kind            string
	Characteristics characteristics.Characteristics
	Grants          Grants
}

// TargetFor returns the target of a worker. The worker characteristics get
// the os and arch of the worker row.
func TargetFor(w *models.Worker, pool *models.WorkerPool, grants []models.PoolClassGrant) Target {
	chars := make(characteristics.Characteristics, len(w.Characteristics)+2)
	for key, value := range w.Characteristics {
		chars[key] = value
	}
	if w.OS != "" {
		chars["os"] = characteristics.StringValue(w.OS)
	}
	if w.Arch != "" {
		chars["arch"] = characteristics.StringValue(w.Arch)
	}
	return Target{Kind: WorkerKind(w), Characteristics: chars, Grants: PoolGrants(pool, grants)}
}

// Serves reports whether the target serves the alias.
func (t Target) Serves(alias *models.ImageAlias) bool {
	if alias == nil || t.Kind == "" || alias.Kind != t.Kind {
		return false
	}
	classes := t.Grants[alias.OrgID]
	if len(classes) == 0 {
		return false
	}
	want := characteristics.Characteristics{
		"os":   characteristics.StringValue(alias.OS),
		"arch": characteristics.StringValue(alias.Arch),
	}
	for key, value := range alias.Characteristics {
		if key == WorkerClassKey {
			class, ok := value.(characteristics.StringValue)
			if !ok || !classes[string(class)] {
				return false
			}
			continue
		}
		if key == imagealias.DigestCharacteristic {
			continue
		}
		want[key] = value
	}
	return characteristics.Satisfies(t.Characteristics, want)
}

// Entry is one image of a set.
type Entry struct {
	Ref     string
	Digest  string
	Kind    string
	Aliases []string
	// Pending is true when the digest is only the pending pin of a
	// rollout: no served alias has it as its active pin, and no job that
	// is not terminal uses it. Promotion waits until every serving worker
	// has it, so the worker can serve jobs before it has it.
	Pending bool
}

// Set is a computed image set. Hash identifies its content.
type Set struct {
	Entries []Entry
	Hash    string
}

// Digests returns the digests of the set.
func (s Set) Digests() []string {
	out := make([]string, len(s.Entries))
	for i, entry := range s.Entries {
		out[i] = entry.Digest
	}
	return out
}

type scopeName struct {
	orgID     string
	projectID string
	name      string
}

func aliasScope(alias *models.ImageAlias) scopeName {
	key := scopeName{orgID: alias.OrgID, name: alias.Name}
	if alias.ProjectID != nil {
		key.projectID = *alias.ProjectID
	}
	return key
}

// drainAlias returns the alias that a draining job resolved: the project
// alias of the job project with that name, else the organization alias,
// and only when its kind and platform are the kind and platform of the job
// (imagealias.PlatformOf). It returns nil when there is no such alias (the
// alias was deleted, or a new alias with that name is for another
// platform). The job is then not drained; imagealias.ResolveRetry refuses a
// retry of it.
func drainAlias(byScope map[scopeName]*models.ImageAlias, image models.DrainingImage) *models.ImageAlias {
	alias := byScope[scopeName{orgID: image.OrgID, name: image.AliasName}]
	if image.ProjectID != nil && *image.ProjectID != "" {
		if project := byScope[scopeName{orgID: image.OrgID, projectID: *image.ProjectID, name: image.AliasName}]; project != nil {
			alias = project
		}
	}
	if alias == nil {
		return nil
	}
	// A row without characteristics (an older store) is not checked.
	if image.Characteristics != nil && !imagealias.PlatformOf(image.Characteristics).Matches(alias) {
		return nil
	}
	return alias
}

// ServesDirect reports whether the target could run a job that is not
// terminal and that names a direct pinned VM reference (no alias): the
// target holds vm images, the pool serves the worker class of the job in
// its organization, and the worker satisfies the job characteristics
// (except image_digest, which the image set itself gives).
func (t Target) ServesDirect(image models.DrainingImage) bool {
	if t.Kind != models.ImageAliasKindVM || image.AliasName != "" {
		return false
	}
	class := image.WorkerClass
	if class == "" {
		class = "default"
	}
	if !t.Grants[image.OrgID][class] {
		return false
	}
	want := make(characteristics.Characteristics, len(image.Characteristics))
	for key, value := range image.Characteristics {
		if key == imagealias.DigestCharacteristic {
			continue
		}
		want[key] = value
	}
	return characteristics.Satisfies(t.Characteristics, want)
}

// Compute returns the image set of a target. aliases are the aliases of the
// organizations that the target serves; drains are the images of the jobs
// that are not terminal in those organizations. The result does not depend
// on the order of the inputs.
func Compute(t Target, aliases []models.ImageAlias, drains []models.DrainingImage) Set {
	byScope := make(map[scopeName]*models.ImageAlias, len(aliases))
	for i := range aliases {
		byScope[aliasScope(&aliases[i])] = &aliases[i]
	}
	type acc struct {
		// aliasRefs are the references of alias pins (active or pending);
		// jobRefs are the references that drained jobs store. The entry
		// uses an alias reference when there is one, so that the pull
		// location of a digest does not depend on the jobs.
		aliasRefs map[string]bool
		jobRefs   map[string]bool
		kind      string
		aliases   map[string]bool
		required  bool
	}
	byDigest := map[string]*acc{}
	add := func(kind, aliasName, ref, digest string, required, fromAlias bool) {
		if digest == "" || ref == "" {
			return
		}
		entry := byDigest[digest]
		if entry == nil {
			entry = &acc{aliasRefs: map[string]bool{}, jobRefs: map[string]bool{}, kind: kind, aliases: map[string]bool{}}
			byDigest[digest] = entry
		}
		if fromAlias {
			entry.aliasRefs[imagealias.JobReference(ref)] = true
		} else {
			entry.jobRefs[imagealias.JobReference(ref)] = true
		}
		if aliasName != "" {
			entry.aliases[aliasName] = true
		}
		entry.required = entry.required || required
	}
	for i := range aliases {
		alias := &aliases[i]
		if !t.Serves(alias) {
			continue
		}
		add(alias.Kind, alias.Name, alias.ActiveRef, alias.ActiveDigest, true, true)
		if alias.PendingRef != nil && alias.PendingDigest != nil {
			add(alias.Kind, alias.Name, *alias.PendingRef, *alias.PendingDigest, false, true)
		}
	}
	for _, image := range drains {
		if image.AliasName == "" {
			// A direct pinned VM reference: no alias, so match the job.
			// The job stores the reference of an alias that pinned the
			// digest at creation (imagealias.ResolveJob).
			if t.ServesDirect(image) {
				add(models.ImageAliasKindVM, "", image.Ref, image.Digest, true, false)
			}
			continue
		}
		alias := drainAlias(byScope, image)
		if alias == nil || !t.Serves(alias) {
			continue
		}
		add(alias.Kind, alias.Name, image.Ref, image.Digest, true, false)
	}

	digests := make([]string, 0, len(byDigest))
	for digest := range byDigest {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	set := Set{Entries: make([]Entry, 0, len(digests))}
	for _, digest := range digests {
		entry := byDigest[digest]
		// Two aliases can pin one digest at two locations (for example an
		// OCI registry and S3). The worker needs one: the first alias
		// reference in sort order (a job reference only when no alias pins
		// the digest), so that the set does not change between
		// computations or when jobs start and end.
		refs := sortedKeys(entry.aliasRefs)
		if len(refs) == 0 {
			refs = sortedKeys(entry.jobRefs)
		}
		set.Entries = append(set.Entries, Entry{Ref: refs[0], Digest: digest, Kind: entry.kind, Aliases: sortedKeys(entry.aliases), Pending: !entry.required})
	}
	set.Hash = Hash(set.Entries)
	return set
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// Hash returns the hash of the canonical form of entries: entries in digest
// order, each as digest, ref, kind, the sorted alias names, and a pending
// marker only for a pending entry (so the hash of a set without pending
// entries is the same as before the marker existed).
func Hash(entries []Entry) string {
	sorted := append([]Entry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Digest < sorted[j].Digest })
	h := sha256.New()
	for _, entry := range sorted {
		names := append([]string(nil), entry.Aliases...)
		sort.Strings(names)
		line := entry.Digest + "\x00" + entry.Ref + "\x00" + entry.Kind + "\x00" + strings.Join(names, ",")
		if entry.Pending {
			line += "\x00pending"
		}
		h.Write([]byte(line + "\n"))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
