package observability

import (
	"sort"
	"time"

	"github.com/lib/pq"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Granularity is the bucket size of a rollup.
type Granularity string

const (
	Hourly Granularity = "hour"
	Daily  Granularity = "day"
)

// Step returns the bucket length.
func (g Granularity) Step() time.Duration {
	if g == Daily {
		return 24 * time.Hour
	}
	return time.Hour
}

// BucketStart returns the start of the UTC bucket that holds t.
func BucketStart(t time.Time, g Granularity) time.Time {
	t = t.UTC()
	if g == Daily {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	return t.Truncate(time.Hour)
}

// RefClassBucket returns the rollup bucket of a set of ref classes: the first
// of default, tag, pr, schedule, manual that the set has, else other. A fork
// pull request is in pr.
func RefClassBucket(classes []string) string {
	has := make(map[string]bool, len(classes))
	for _, c := range classes {
		has[c] = true
	}
	for _, bucket := range models.RefClassBuckets {
		if bucket != models.RefClassBucketOther && has[bucket] {
			return bucket
		}
	}
	return models.RefClassBucketOther
}

// ActorKindOf returns the actor kind label of a job: human, service, or
// system (also for a job with no actor).
func ActorKindOf(kind string) string {
	switch kind {
	case "human", "service":
		return kind
	}
	return "system"
}

// QueueWait returns the seconds from the creation of a job to the time a
// worker took it (scheduling start, else run start). ok is false for a job
// that no worker took.
func QueueWait(f *models.RollupJobFact) (float64, bool) {
	start := f.SchedulingStartedAt
	if start == nil {
		start = f.StartedAt
	}
	if start == nil {
		return 0, false
	}
	wait := start.Sub(f.CreatedAt).Seconds()
	if wait < 0 {
		wait = 0
	}
	return wait, true
}

// Duration returns the run time of a job in seconds. ok is false for a job
// that did not start.
func Duration(f *models.RollupJobFact) (float64, bool) {
	if f.StartedAt == nil {
		return 0, false
	}
	d := f.CompletedAt.Sub(*f.StartedAt).Seconds()
	if d < 0 {
		d = 0
	}
	return d, true
}

// OutcomeClass returns the failure class that counts for a finished job:
// "" for a success, the class of a failure, "timeout" for a timeout with no
// class, "cancelled" for a cancel, and "unknown" for a failure with no class.
func OutcomeClass(status, class string) string {
	switch status {
	case "completed":
		return ""
	case "cancelled":
		return models.FailureClassCancelled
	case "timeout":
		if class == "" {
			return models.FailureClassTimeout
		}
	}
	if class == models.FailureClassCancelled {
		return models.FailureClassCancelled
	}
	if class == "" || !models.IsValidFailureClass(class) {
		return "unknown"
	}
	return class
}

// IsPipelineRun reports whether a finished job counts in pipeline pass rate
// and duration: a success, or a failure of the class job, timeout, or oom.
func IsPipelineRun(status, class string) bool {
	outcome := OutcomeClass(status, class)
	return outcome == "" || models.FailureOwner(outcome) == models.FailureOwnerPipeline
}

type jobKey struct {
	bucket   time.Time
	org      string
	project  string
	workflow string
	owner    string
	lane     string
	refClass string
	actor    string
}

type jobAccumulator struct {
	row       models.JobRollup
	durations []float64
	waits     []float64
	dHist     Histogram
	wHist     Histogram
}

func usage(f *models.RollupJobFact, seconds float64) (cpu, memory, cpuReserved, memoryReserved float64, measured bool) {
	hours := seconds / 3600
	if f.SampleCount != nil && *f.SampleCount > 0 {
		measured = true
		if f.CPUAvgMillicores != nil {
			cpu = float64(*f.CPUAvgMillicores) / 1000 * hours
		}
		if f.MemoryAvgBytes != nil {
			memory = float64(*f.MemoryAvgBytes) / (1 << 30) * hours
		}
	}
	if f.CPURequestMillicores != nil {
		cpuReserved = float64(*f.CPURequestMillicores) / 1000 * hours
	}
	if f.MemoryLimitBytes != nil {
		memoryReserved = float64(*f.MemoryLimitBytes) / (1 << 30) * hours
	}
	return
}

func addOutcome(row *models.JobRollup, outcome string) {
	switch outcome {
	case "":
		row.Successes++
	case models.FailureClassJob:
		row.FailedJob++
	case models.FailureClassTimeout:
		row.FailedTimeout++
	case models.FailureClassOOM:
		row.FailedOOM++
	case models.FailureClassImagePull:
		row.FailedImagePull++
	case models.FailureClassScheduling:
		row.FailedScheduling++
	case models.FailureClassWorkerLost:
		row.FailedWorkerLost++
	case models.FailureClassPlatform:
		row.FailedPlatform++
	case models.FailureClassCancelled:
		row.Cancelled++
	default:
		row.FailedUnknown++
	}
}

func optional(v float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return &v
}

// ComputeJobRollups groups finished jobs by bucket (of completed_at) and
// dimensions, and computes each row. Percentiles are exact (the same as
// percentile_cont); histograms use Bounds. Rows are sorted by key.
func ComputeJobRollups(facts []models.RollupJobFact, g Granularity) []models.JobRollup {
	groups := map[jobKey]*jobAccumulator{}
	for i := range facts {
		f := &facts[i]
		project := ""
		if f.ProjectID != nil {
			project = *f.ProjectID
		}
		key := jobKey{
			bucket: BucketStart(f.CompletedAt, g), org: f.OrgID, project: project,
			workflow: f.WorkflowSecurityID, owner: f.OwnerGroup, lane: f.Lane,
			refClass: RefClassBucket(f.RefClasses), actor: ActorKindOf(f.ActorKind),
		}
		acc := groups[key]
		if acc == nil {
			acc = &jobAccumulator{dHist: NewHistogram(), wHist: NewHistogram()}
			acc.row = models.JobRollup{
				BucketStart: key.bucket, OrgID: key.org, ProjectID: f.ProjectID, WorkflowSecurityID: key.workflow,
				OwnerGroup: key.owner, Lane: key.lane, RefClassBucket: key.refClass, ActorKind: key.actor,
			}
			groups[key] = acc
		}
		row := &acc.row
		row.Runs++
		outcome := OutcomeClass(f.Status, f.FailureClass)
		addOutcome(row, outcome)
		if wait, ok := QueueWait(f); ok {
			acc.waits = append(acc.waits, wait)
			acc.wHist.Observe(wait)
			row.QueueWaitSumSeconds += wait
		}
		if seconds, ok := Duration(f); ok {
			if IsPipelineRun(f.Status, f.FailureClass) {
				acc.durations = append(acc.durations, seconds)
				acc.dHist.Observe(seconds)
				row.DurationSumSeconds += seconds
			}
			cpu, memory, cpuReserved, memoryReserved, measured := usage(f, seconds)
			if measured {
				row.MeasuredRuns++
			}
			row.CPUCoreHours += cpu
			row.MemoryGiBHours += memory
			row.CPUReservedCoreHours += cpuReserved
			row.MemoryReservedGiBHours += memoryReserved
		}
	}
	out := make([]models.JobRollup, 0, len(groups))
	for _, acc := range groups {
		row := acc.row
		row.DurationP50Seconds = optional(ExactQuantile(acc.durations, 0.5))
		row.DurationP95Seconds = optional(ExactQuantile(acc.durations, 0.95))
		row.QueueWaitP50Seconds = optional(ExactQuantile(acc.waits, 0.5))
		row.QueueWaitP95Seconds = optional(ExactQuantile(acc.waits, 0.95))
		row.DurationHist = pq.Int64Array(acc.dHist)
		row.QueueWaitHist = pq.Int64Array(acc.wHist)
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return jobRollupLess(&out[i], &out[j]) })
	return out
}

func strOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func jobRollupLess(a, b *models.JobRollup) bool {
	ka := []string{a.BucketStart.Format(time.RFC3339), a.OrgID, strOr(a.ProjectID), a.WorkflowSecurityID, a.OwnerGroup, a.Lane, a.RefClassBucket, a.ActorKind}
	kb := []string{b.BucketStart.Format(time.RFC3339), b.OrgID, strOr(b.ProjectID), b.WorkflowSecurityID, b.OwnerGroup, b.Lane, b.RefClassBucket, b.ActorKind}
	for i := range ka {
		if ka[i] != kb[i] {
			return ka[i] < kb[i]
		}
	}
	return false
}

// MembershipIndex answers "which groups was a principal in at time t" from
// group membership history.
type MembershipIndex struct {
	byPrincipal map[string][]models.GroupMembershipChange
}

// NewMembershipIndex sorts the changes of each principal by time.
func NewMembershipIndex(changes []models.GroupMembershipChange) *MembershipIndex {
	idx := &MembershipIndex{byPrincipal: map[string][]models.GroupMembershipChange{}}
	for _, c := range changes {
		idx.byPrincipal[c.PrincipalID] = append(idx.byPrincipal[c.PrincipalID], c)
	}
	for _, list := range idx.byPrincipal {
		sort.SliceStable(list, func(i, j int) bool { return list[i].ChangedAt.Before(list[j].ChangedAt) })
	}
	return idx
}

// GroupRef is a group with its name at the time of a change.
type GroupRef struct {
	ID   string
	Name string
}

// GroupsAt returns the groups of the organization that the principal was a
// member of at time t, sorted by ID.
func (idx *MembershipIndex) GroupsAt(principalID, orgID string, t time.Time) []GroupRef {
	if idx == nil {
		return nil
	}
	members := map[string]string{}
	for _, c := range idx.byPrincipal[principalID] {
		if c.ChangedAt.After(t) {
			break
		}
		if c.OrgID != nil && *c.OrgID != orgID {
			continue
		}
		if c.Change == "added" {
			members[c.GroupID] = c.GroupName
		} else {
			delete(members, c.GroupID)
		}
	}
	out := make([]GroupRef, 0, len(members))
	for id, name := range members {
		out = append(out, GroupRef{ID: id, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ComputeActorGroupRollups attributes each finished job to the groups of its
// actor at the creation time of the job, by day of completion. A job of an
// actor in two groups counts in both. A job with no actor, or an actor in no
// group, is not in the result.
func ComputeActorGroupRollups(facts []models.RollupJobFact, idx *MembershipIndex) []models.ActorGroupRollup {
	type key struct {
		bucket time.Time
		org    string
		group  string
	}
	groups := map[key]*models.ActorGroupRollup{}
	for i := range facts {
		f := &facts[i]
		if f.ActorPrincipalID == nil {
			continue
		}
		refs := idx.GroupsAt(*f.ActorPrincipalID, f.OrgID, f.CreatedAt)
		if len(refs) == 0 {
			continue
		}
		outcome := OutcomeClass(f.Status, f.FailureClass)
		seconds, started := Duration(f)
		var cpu, memory, cpuReserved float64
		if started {
			cpu, memory, cpuReserved, _, _ = usage(f, seconds)
		}
		for _, ref := range refs {
			k := key{bucket: BucketStart(f.CompletedAt, Daily), org: f.OrgID, group: ref.ID}
			row := groups[k]
			if row == nil {
				row = &models.ActorGroupRollup{BucketStart: k.bucket, OrgID: f.OrgID, GroupID: ref.ID, GroupName: ref.Name}
				groups[k] = row
			}
			row.Runs++
			switch {
			case outcome == "":
				row.Successes++
			case models.FailureOwner(outcome) == models.FailureOwnerPipeline:
				row.PipelineFailures++
			case models.FailureOwner(outcome) == models.FailureOwnerInfrastructure:
				row.InfraFailures++
			}
			if started {
				row.DurationSumSeconds += seconds
			}
			row.CPUCoreHours += cpu
			row.MemoryGiBHours += memory
			row.CPUReservedCoreHours += cpuReserved
		}
	}
	out := make([]models.ActorGroupRollup, 0, len(groups))
	for _, row := range groups {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].BucketStart.Equal(out[j].BucketStart) {
			return out[i].BucketStart.Before(out[j].BucketStart)
		}
		if out[i].OrgID != out[j].OrgID {
			return out[i].OrgID < out[j].OrgID
		}
		return out[i].GroupID < out[j].GroupID
	})
	return out
}

// EvalLatency returns the seconds from the receipt of an event to its first
// status.
func EvalLatency(f *models.RollupEventFact) float64 {
	v := f.FirstStatusAt.Sub(f.ReceivedAt).Seconds()
	if v < 0 {
		return 0
	}
	return v
}

// ComputeEvalRollups groups CI events by bucket (of the first status),
// organization, project, and source.
func ComputeEvalRollups(facts []models.RollupEventFact, g Granularity) []models.EvalRollup {
	type key struct {
		bucket  time.Time
		org     string
		project string
		source  string
	}
	type acc struct {
		row    models.EvalRollup
		values []float64
		hist   Histogram
	}
	groups := map[key]*acc{}
	for i := range facts {
		f := &facts[i]
		k := key{bucket: BucketStart(f.FirstStatusAt, g), org: f.OrgID, project: strOr(f.ProjectID), source: f.Source}
		a := groups[k]
		if a == nil {
			a = &acc{row: models.EvalRollup{BucketStart: k.bucket, OrgID: f.OrgID, ProjectID: f.ProjectID, Source: f.Source}, hist: NewHistogram()}
			groups[k] = a
		}
		v := EvalLatency(f)
		a.row.Events++
		a.row.LatencySumSeconds += v
		a.values = append(a.values, v)
		a.hist.Observe(v)
	}
	out := make([]models.EvalRollup, 0, len(groups))
	for _, a := range groups {
		row := a.row
		row.LatencyP50Seconds = optional(ExactQuantile(a.values, 0.5))
		row.LatencyP95Seconds = optional(ExactQuantile(a.values, 0.95))
		row.LatencyHist = pq.Int64Array(a.hist)
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.BucketStart.Equal(b.BucketStart) {
			return a.BucketStart.Before(b.BucketStart)
		}
		if a.OrgID != b.OrgID {
			return a.OrgID < b.OrgID
		}
		if strOr(a.ProjectID) != strOr(b.ProjectID) {
			return strOr(a.ProjectID) < strOr(b.ProjectID)
		}
		return a.Source < b.Source
	})
	return out
}
