package cmd

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/urfave/cli/v2"
)

func TestLocalCacheEnv(t *testing.T) {
	env := localCacheEnv(localCacheProfile{}, false)
	if env["REACTORCIDE_CACHE_NAMESPACE"] != "standard" || env["REACTORCIDE_CACHE_WRITABLE"] != "false" ||
		env["REACTORCIDE_ARTIFACT_NAMESPACE"] != "standard" {
		t.Fatalf("default env = %#v", env)
	}
	if env := localCacheEnv(localCacheProfile{}, true); env["REACTORCIDE_CACHE_WRITABLE"] != "true" {
		t.Fatalf("writable env = %#v", env)
	}
	cache, artifacts := "team-cache", "team-artifacts"
	local := &localContext{DefaultExecutionProfile: "trusted", CacheNamespace: &cache, ArtifactNamespace: &artifacts}
	env = localCacheEnv(cacheProfileOf(local), false)
	if env["REACTORCIDE_CACHE_NAMESPACE"] != "team-cache" || env["REACTORCIDE_ARTIFACT_NAMESPACE"] != "team-artifacts" {
		t.Fatalf("context env = %#v", env)
	}
	if env := localCacheEnv(localCacheProfile{Profile: "trusted"}, false); env["REACTORCIDE_CACHE_NAMESPACE"] != "trusted" {
		t.Fatalf("profile env = %#v", env)
	}

	// The values are reserved: a job variable cannot replace them.
	config := &worker.JobConfig{Env: map[string]string{"REACTORCIDE_CACHE_WRITABLE": "true"}}
	applyLocalReservedEnv(config, localCacheProfile{}, false)
	if config.Env["REACTORCIDE_CACHE_WRITABLE"] != "false" {
		t.Fatalf("job value replaced the reserved value: %#v", config.Env)
	}

	// A workflow node subprocess gets the profile in hidden flags.
	args := cacheProfileOf(local).args()
	app := &cli.App{Flags: []cli.Flag{&cli.StringFlag{Name: "cache-profile"}, &cli.StringFlag{Name: "cache-namespace"},
		&cli.StringFlag{Name: "artifact-namespace"}}, Action: func(ctx *cli.Context) error {
		got, ok := cacheProfileFromFlags(ctx)
		if !ok || got.Profile != "trusted" || got.CacheNamespace == nil || *got.CacheNamespace != "team-cache" {
			t.Fatalf("flags = %+v %t", got, ok)
		}
		return nil
	}}
	if err := app.Run(append([]string{"test"}, args...)); err != nil {
		t.Fatal(err)
	}
}

// slowRunner is a job that runs until Stop.
type slowRunner struct {
	stopped chan struct{}
}

func (r *slowRunner) SpawnJob(context.Context, *worker.JobConfig) (string, error) { return "c1", nil }
func (r *slowRunner) StreamLogs(context.Context, string) (io.ReadCloser, io.ReadCloser, error) {
	return nil, nil, errors.New("no logs")
}
func (r *slowRunner) WaitForCompletion(ctx context.Context, _ string) (int, error) {
	select {
	case <-r.stopped:
		return 143, nil
	case <-time.After(10 * time.Second):
		return 0, nil
	}
}
func (r *slowRunner) Stop(context.Context, string, time.Duration) error {
	close(r.stopped)
	return nil
}
func (r *slowRunner) Cleanup(context.Context, string) error { return nil }
func (r *slowRunner) SampleResources(context.Context, string, worker.ResourceSampleOptions) (worker.ResourceSnapshot, error) {
	return worker.ResourceSnapshot{}, nil
}

func TestExecuteLocalJobEnforcesTimeout(t *testing.T) {
	runner := &slowRunner{stopped: make(chan struct{})}
	config := &worker.JobConfig{Image: "alpine", TimeoutSeconds: 1, WorkspaceDir: t.TempDir()}
	err := executeLocalJob(context.Background(), runner, config, secrets.NewMasker())
	var exit cli.ExitCoder
	if !errors.As(err, &exit) || exit.ExitCode() != localTimeoutExitCode {
		t.Fatalf("err = %v, want exit code %d", err, localTimeoutExitCode)
	}
}
