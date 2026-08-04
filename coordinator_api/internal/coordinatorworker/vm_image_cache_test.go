package coordinatorworker

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/stretchr/testify/require"
)

type fakeCacheRunner struct {
	calls []string
	refs  []string
}

func (f *fakeCacheRunner) SpawnJob(context.Context, *worker.JobConfig) (string, error) {
	return "", nil
}
func (f *fakeCacheRunner) StreamLogs(context.Context, string) (io.ReadCloser, io.ReadCloser, error) {
	return nil, nil, nil
}
func (f *fakeCacheRunner) WaitForCompletion(context.Context, string) (int, error) { return 0, nil }
func (f *fakeCacheRunner) Stop(context.Context, string, time.Duration) error      { return nil }
func (f *fakeCacheRunner) Cleanup(context.Context, string) error                  { return nil }
func (f *fakeCacheRunner) PruneImages(context.Context, time.Duration, time.Time) (int, error) {
	f.calls = append(f.calls, "prune")
	return 2, nil
}
func (f *fakeCacheRunner) PrefetchImages(_ context.Context, refs []string) error {
	f.calls = append(f.calls, "prefetch")
	f.refs = append([]string(nil), refs...)
	return nil
}

func TestInitializeVMImageCachePrunesBeforePrefetch(t *testing.T) {
	runner := &fakeCacheRunner{}
	cache, ok, err := initializeVMImageCache(context.Background(), runner, Config{
		VMImagePrefetch:  []string{"registry.example/a@sha256:one", "registry.example/b@sha256:two"},
		VMImageMaxUnused: 30 * 24 * time.Hour,
	}, time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	require.Same(t, runner, cache)
	require.Equal(t, []string{"prune", "prefetch"}, runner.calls)
	require.Equal(t, []string{"registry.example/a@sha256:one", "registry.example/b@sha256:two"}, runner.refs)
}
