package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cachens"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/urfave/cli/v2"
)

// localOrgID is the organization part of a local cache volume key. A local
// run has no organization.
const localOrgID = "local"

// localCacheProfile is the part of a local context that the cache
// decision reads. A workflow node subprocess gets it in hidden flags.
type localCacheProfile struct {
	Profile           string
	CacheNamespace    *string
	ArtifactNamespace *string
}

func cacheProfileOf(local *localContext) localCacheProfile {
	if local == nil {
		return localCacheProfile{}
	}
	return localCacheProfile{Profile: local.DefaultExecutionProfile, CacheNamespace: local.CacheNamespace,
		ArtifactNamespace: local.ArtifactNamespace}
}

// cacheProfileFromFlags reads the hidden flags of a workflow node
// subprocess. It returns false when they are not set.
func cacheProfileFromFlags(ctx *cli.Context) (localCacheProfile, bool) {
	if !ctx.IsSet("cache-profile") {
		return localCacheProfile{}, false
	}
	value := localCacheProfile{Profile: ctx.String("cache-profile")}
	if ctx.IsSet("cache-namespace") {
		namespace := ctx.String("cache-namespace")
		value.CacheNamespace = &namespace
	}
	if ctx.IsSet("artifact-namespace") {
		namespace := ctx.String("artifact-namespace")
		value.ArtifactNamespace = &namespace
	}
	return value, true
}

// args returns the hidden flags that give the profile to a subprocess.
func (p localCacheProfile) args() []string {
	args := []string{"--cache-profile", p.Profile}
	if p.CacheNamespace != nil {
		args = append(args, "--cache-namespace", *p.CacheNamespace)
	}
	if p.ArtifactNamespace != nil {
		args = append(args, "--artifact-namespace", *p.ArtifactNamespace)
	}
	return args
}

// localCacheEnv returns the reserved cache values of a local run
// (docs/builds-and-caches.md#reserved-job-facts). The profile is the
// default execution profile of the local context, or "standard". The
// namespaces of the context apply. A local run has no ref classes. It may
// write to its cache only with --cache-writable: a local run is not a
// trusted build.
func localCacheEnv(source localCacheProfile, writable bool) map[string]string {
	profileName := cachens.DefaultProfile
	if name := strings.TrimSpace(source.Profile); name != "" {
		profileName = name
	}
	profile := &models.ExecutionProfile{TrustedCacheWrites: writable, CacheNamespace: source.CacheNamespace,
		ArtifactNamespace: source.ArtifactNamespace}
	decision := cachens.Compute(localOrgID, profileName, profile, nil, cachens.DefaultProfile)
	return map[string]string{
		cachens.EnvNamespace:         decision.Namespace,
		cachens.EnvWritable:          strconv.FormatBool(decision.Writable),
		cachens.EnvArtifactNamespace: decision.ArtifactNamespace,
	}
}

// applyLocalReservedEnv sets the reserved values after the job variables,
// as the coordinator does, so a job file or an overlay cannot replace them.
func applyLocalReservedEnv(config *worker.JobConfig, source localCacheProfile, writable bool) {
	if config.Env == nil {
		config.Env = map[string]string{}
	}
	for key, value := range localCacheEnv(source, writable) {
		config.Env[key] = value
	}
}

// localTimeoutExitCode is the exit code of a local job that run-local
// stopped at its timeout (the code of timeout(1)).
const localTimeoutExitCode = 124

// localTimeoutGrace is the time between the stop signal and the kill of a
// timed out local job.
const localTimeoutGrace = 10 * time.Second

// localTimeout stops a local job at timeout_seconds, as the workers do. The
// time starts when the container starts.
type localTimeout struct {
	mu       sync.Mutex
	timer    *time.Timer
	timedOut bool
}

// startLocalTimeout starts the timer of a job. A zero or negative timeout
// starts nothing.
func startLocalTimeout(runner worker.JobRunner, containerID string, seconds int) *localTimeout {
	t := &localTimeout{}
	if seconds <= 0 {
		return t
	}
	t.timer = time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		t.mu.Lock()
		t.timedOut = true
		t.mu.Unlock()
		fmt.Fprintf(os.Stderr, "\nJob exceeded its timeout of %d seconds; stopping it\n", seconds)
		if err := runner.Stop(context.Background(), containerID, localTimeoutGrace); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to stop timed out job: %v\n", err)
		}
	})
	return t
}

// stop stops the timer and reports whether the job timed out.
func (t *localTimeout) stop() bool {
	if t.timer != nil {
		t.timer.Stop()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.timedOut
}
