package workerapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/catalystcommunity/app-utils-go/logging"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cachens"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/envdefaults"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagepolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/joblinks"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// WP14 (plan items C1-C4): the coordinator-side build settings of a lease.
// The coordinator decides all of them from its own records; the worker
// cannot change them.

type executionProfileReader interface {
	GetExecutionProfile(ctx context.Context, orgID, name string) (*models.ExecutionProfile, error)
}

// jobImage is the image that the lease runs (the same rule as buildLease).
func jobImage(job *models.Job) string {
	if job.ContainerImage != nil && *job.ContainerImage != "" {
		return *job.ContainerImage
	}
	if job.RunnerImage != "" {
		return job.RunnerImage
	}
	return worker.DefaultRunnerImage
}

// prepareLeaseBuildSettings applies the build settings to env and returns
// the cache decision of the lease. It returns a step name and an error when
// the claim must be refused:
//
//  1. The image policy of the organization (docs/builds-and-caches.md).
//  2. Environment defaults: job > project > worker class > organization
//     (docs/builds-and-caches.md). Only keys that env does not have.
//  3. The cache decision and its reserved environment values
//     (docs/builds-and-caches.md). Set last, so nothing can replace them.
func (d *Deps) prepareLeaseBuildSettings(ctx context.Context, job *models.Job, env map[string]string) (*csilapi.CacheLease, string, error) {
	orgID := job.OwnershipOrgID()
	if policies, ok := d.Store.(store.ImagePolicyStore); ok && orgID != "" {
		policy, err := policies.GetImagePolicy(ctx, orgID)
		if err != nil {
			return nil, "image policy", err
		}
		if err := imagepolicy.Check(policy, jobImage(job)); err != nil {
			return nil, "image policy", err
		}
	}

	if defaults, ok := d.Store.(store.EnvDefaultsStore); ok && orgID != "" {
		org, class, project, err := defaults.EnvDefaultLayers(ctx, orgID, job.WorkerClass, job.ProjectID)
		if err != nil {
			return nil, "environment defaults", err
		}
		effective := envdefaults.Layers{Org: org, WorkerClass: class, Project: project}.Effective()
		if added := envdefaults.Apply(env, effective); len(added) > 0 {
			logging.Log.WithField("job_id", job.JobID).WithField("keys", added).Debug("Applied environment defaults to lease")
		}
	}

	decision, err := d.cacheDecision(ctx, job)
	if err != nil {
		return nil, "cache namespace", err
	}
	for key, value := range decision.Env() {
		env[key] = value
	}
	return &csilapi.CacheLease{
		Namespace:         decision.Namespace,
		VolumeKey:         decision.VolumeKey,
		Writable:          decision.Writable,
		ArtifactNamespace: decision.ArtifactNamespace,
	}, "", nil
}

// cacheDecision computes the cache decision of a job. A store that cannot
// read profiles (some in-memory test stores) gives the decision of a
// missing profile.
func (d *Deps) cacheDecision(ctx context.Context, job *models.Job) (cachens.Decision, error) {
	name := job.ExecutionProfile
	if name == "" {
		name = cachens.DefaultProfile
	}
	var profile *models.ExecutionProfile
	if profiles, ok := d.Store.(executionProfileReader); ok && job.OwnershipOrgID() != "" {
		found, err := profiles.GetExecutionProfile(ctx, job.OwnershipOrgID(), name)
		switch {
		case err == nil:
			profile = found
		case errors.Is(err, store.ErrNotFound):
		default:
			return cachens.Decision{}, fmt.Errorf("read execution profile %q: %w", name, err)
		}
	}
	return cachens.Compute(job.OwnershipOrgID(), name, profile, refclass.ForJob(job), cachens.SharedNamespaceFromEnv()), nil
}

// recordJobLinks stores the named links of the workflow output of a job
// (docs/builds-and-caches.md). An invalid link is dropped and logged; it does not
// fail the job. A store error is logged too: links are a convenience, not
// a result.
func (d *Deps) recordJobLinks(ctx context.Context, job *models.Job, workflowOutput *string) {
	if workflowOutput == nil || *workflowOutput == "" {
		return
	}
	linkStore, ok := d.Store.(store.JobLinkStore)
	if !ok {
		return
	}
	links, dropped, err := joblinks.Parse(job.JobID, []byte(*workflowOutput))
	logger := logging.Log.WithField("job_id", job.JobID)
	if err != nil {
		// The workflow merge reports a malformed document.
		return
	}
	if len(dropped) > 0 {
		logger.WithField("dropped", dropped).Warn("Dropped invalid job links from the workflow output")
	}
	if links == nil && len(dropped) == 0 {
		return
	}
	if err := linkStore.ReplaceJobLinks(ctx, job.JobID, links); err != nil {
		logger.WithError(err).Warn("Failed to store job links")
	}
}
