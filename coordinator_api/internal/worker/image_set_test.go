package worker

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/errdefs"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker/vmrunner"
)

const (
	digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func setImages() []ImageSetImage {
	return []ImageSetImage{
		{Ref: "registry.example/team/a:1@" + digestA, Digest: digestA, Aliases: []string{"a"}},
		{Ref: "oci://registry.example/team/b:2@" + digestB, Digest: digestB},
	}
}

type readyCall struct {
	digest string
	owned  bool
}

type failedCall struct {
	digest    string
	permanent bool
	text      string
}

func collectReady(calls *[]readyCall) ImageSeedHooks {
	return ImageSeedHooks{Ready: func(img ImageSetImage, owned bool) { *calls = append(*calls, readyCall{img.Digest, owned}) }}
}

func collectHooks(ready *[]readyCall, failed *[]failedCall) ImageSeedHooks {
	return ImageSeedHooks{
		Ready: func(img ImageSetImage, owned bool) { *ready = append(*ready, readyCall{img.Digest, owned}) },
		Failed: func(img ImageSetImage, err error) {
			*failed = append(*failed, failedCall{img.Digest, IsPermanentPullError(err), err.Error()})
		},
	}
}

// fakeDockerImages is an in-memory Docker image API.
type fakeDockerImages struct {
	mu      sync.Mutex
	present map[string]bool
	pulls   []string
	removes []string
	pullErr error
	// failRefs fail with this error per reference.
	failRefs map[string]error
	conflict map[string]bool
}

func (f *fakeDockerImages) ImageInspectWithRaw(_ context.Context, ref string) (image.InspectResponse, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.present[ref] {
		return image.InspectResponse{ID: ref}, nil, nil
	}
	return image.InspectResponse{}, nil, errdefs.NotFound(errors.New("no such image"))
}

func (f *fakeDockerImages) ImagePull(_ context.Context, ref string, _ image.PullOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls = append(f.pulls, ref)
	if err := f.failRefs[ref]; err != nil {
		return nil, err
	}
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	f.present[ref] = true
	return io.NopCloser(strings.NewReader(`{"status":"done"}`)), nil
}

func (f *fakeDockerImages) ImageRemove(_ context.Context, ref string, _ image.RemoveOptions) ([]image.DeleteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conflict[ref] {
		return nil, errdefs.Conflict(errors.New("image is used by a container"))
	}
	f.removes = append(f.removes, ref)
	delete(f.present, ref)
	return nil, nil
}

func TestDockerImageSeederPullsMissingImagesOnly(t *testing.T) {
	api := &fakeDockerImages{present: map[string]bool{"registry.example/team/a:1@" + digestA: true}}
	var ready []readyCall
	require.NoError(t, dockerImageSeeder{api: api}.seed(context.Background(), setImages(), collectReady(&ready)))

	// The oci:// scheme is removed for the runtime; the pinned reference is
	// otherwise unchanged.
	require.Equal(t, []string{"registry.example/team/b:2@" + digestB}, api.pulls)
	require.Equal(t, []readyCall{{digestA, false}, {digestB, true}}, ready, "only a pulled image is owned")
}

func TestDockerImageSeederPullFailure(t *testing.T) {
	api := &fakeDockerImages{present: map[string]bool{}, pullErr: errors.New("registry down")}
	var ready []readyCall
	var failed []failedCall
	require.NoError(t, dockerImageSeeder{api: api}.seed(context.Background(), setImages(), collectHooks(&ready, &failed)))
	require.Empty(t, ready)
	require.Len(t, failed, 2, "each image is tried")
	require.False(t, failed[0].permanent, "an unknown error is transient")
	require.Contains(t, failed[0].text, "registry down")
}

// One image that cannot be pulled does not stop the other images.
func TestDockerImageSeederOneBadImage(t *testing.T) {
	refA := "registry.example/team/a:1@" + digestA
	api := &fakeDockerImages{present: map[string]bool{}, failRefs: map[string]error{
		refA: errdefs.NotFound(errors.New("manifest unknown")),
	}}
	var ready []readyCall
	var failed []failedCall
	require.NoError(t, dockerImageSeeder{api: api}.seed(context.Background(), setImages(), collectHooks(&ready, &failed)))
	require.Equal(t, []readyCall{{digestB, true}}, ready)
	require.Len(t, failed, 1)
	require.Equal(t, digestA, failed[0].digest)
	require.True(t, failed[0].permanent)
}

// An error message in the pull stream is a failure with that text.
func TestDockerImageSeederStreamError(t *testing.T) {
	err := dockerPullStreamError(strings.NewReader(`{"status":"Pulling"}` + "\n" + `{"errorDetail":{"message":"unauthorized: authentication required"},"error":"unauthorized"}`))
	require.ErrorContains(t, err, "authentication required")
	require.True(t, IsPermanentPullError(err))
	require.NoError(t, dockerPullStreamError(strings.NewReader(`{"status":"done"}`)))
}

// A skipped image (its context is done: it left the set) gets no callback.
func TestDockerImageSeederSkipsImagesThatLeftTheSet(t *testing.T) {
	api := &fakeDockerImages{present: map[string]bool{}}
	done, cancel := context.WithCancel(context.Background())
	cancel()
	var ready []readyCall
	var failed []failedCall
	hooks := collectHooks(&ready, &failed)
	hooks.ImageContext = func(img ImageSetImage) context.Context {
		if img.Digest == digestA {
			return done
		}
		return context.Background()
	}
	require.NoError(t, dockerImageSeeder{api: api}.seed(context.Background(), setImages(), hooks))
	require.Equal(t, []readyCall{{digestB, true}}, ready)
	require.Empty(t, failed)
}

func TestPullErrorClasses(t *testing.T) {
	permanent := []error{
		errors.New("pull x: manifest unknown: manifest unknown"),
		errors.New("failed to resolve reference: not found"),
		errors.New("unauthorized: authentication required"),
		errors.New("denied: requested access to the resource is denied"),
		errors.New("invalid reference format"),
		errors.New("vmrunner: blob digest mismatch: got sha256:1, expected sha256:2"),
		errdefs.Unauthorized(errors.New("x")),
		errdefs.InvalidParameter(errors.New("x")),
		PermanentPullError(errors.New("x")),
	}
	for _, err := range permanent {
		require.True(t, IsPermanentPullError(err), err.Error())
	}
	transient := []error{
		errors.New("dial tcp 10.0.0.1:443: i/o timeout"),
		errors.New("received unexpected HTTP status: 503 Service Unavailable"),
		errors.New("write /var/lib/x: no space left on device"),
		errors.New("pull registry.example/a@sha256:404404404: connection reset by peer"),
		errors.New("something else"),
		context.DeadlineExceeded,
		TransientPullError(errors.New("not found")),
	}
	for _, err := range transient {
		require.False(t, IsPermanentPullError(err), err.Error())
	}
}

func TestDockerImageSeederRemoveKeepsImagesInUse(t *testing.T) {
	refA := "registry.example/team/a:1@" + digestA
	refB := "registry.example/team/b:2@" + digestB
	api := &fakeDockerImages{present: map[string]bool{refA: true, refB: true}, conflict: map[string]bool{refA: true}}
	kept, err := dockerImageSeeder{api: api}.remove(context.Background(), setImages())
	require.NoError(t, err)
	require.Len(t, kept, 1)
	require.Equal(t, digestA, kept[0].Digest)
	require.Equal(t, []string{refB}, api.removes)
}

func TestContainerdImageSeederPullAndRemove(t *testing.T) {
	refA := "registry.example/team/a:1@" + digestA
	refB := "registry.example/team/b:2@" + digestB
	present := map[string]bool{refA: true}
	var calls []string
	run := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "image":
			if present[args[2]] {
				return []byte("[]"), nil
			}
			return []byte("no such image"), errors.New("exit 1")
		case "pull":
			present[args[1]] = true
			return nil, nil
		case "rmi":
			if args[1] == refA {
				return []byte("image is in use by a container"), errors.New("exit 1")
			}
			delete(present, args[1])
			return nil, nil
		}
		return nil, errors.New("unexpected command")
	}
	seeder := containerdImageSeeder{run: run}
	var ready []readyCall
	require.NoError(t, seeder.seed(context.Background(), setImages(), collectReady(&ready)))
	require.Equal(t, []string{"image inspect " + refA, "image inspect " + refB, "pull " + refB}, calls)
	require.Equal(t, []readyCall{{digestA, false}, {digestB, true}}, ready)

	calls = nil
	kept, err := seeder.remove(context.Background(), setImages())
	require.NoError(t, err)
	require.Len(t, kept, 1)
	require.Equal(t, digestA, kept[0].Digest)
	require.Contains(t, calls, "rmi "+refB)
	require.False(t, present[refB])
}

func TestKubernetesJobSchedulingConfig(t *testing.T) {
	env := map[string]string{
		"REACTORCIDE_K8S_JOB_NODE_SELECTOR": "pool=ci, kubernetes.io/arch=amd64",
		"REACTORCIDE_K8S_JOB_TOLERATIONS":   `[{"key":"dedicated","operator":"Equal","value":"ci","effect":"NoSchedule"}]`,
		"REACTORCIDE_K8S_JOB_AFFINITY":      `{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"zone","operator":"In","values":["z1"]}]}]}}}`,
	}
	got, err := loadKubernetesJobScheduling(func(k string) string { return env[k] })
	require.NoError(t, err)
	require.Equal(t, map[string]string{"pool": "ci", "kubernetes.io/arch": "amd64"}, got.nodeSelector)
	require.Len(t, got.tolerations, 1)
	require.Equal(t, "dedicated", got.tolerations[0].Key)
	require.NotNil(t, got.affinity.NodeAffinity)

	_, err = loadKubernetesJobScheduling(func(k string) string {
		if k == "REACTORCIDE_K8S_JOB_NODE_SELECTOR" {
			return "novalue"
		}
		return ""
	})
	require.Error(t, err)

	require.Equal(t, corev1.PullIfNotPresent, jobImagePullPolicy("registry.example/a:1@"+digestA))
	require.Equal(t, corev1.PullAlways, jobImagePullPolicy("registry.example/a:1"))
}

func TestVMAdapterImageSetKeepsImagesInUse(t *testing.T) {
	src, err := vmrunner.NewOCIImageSource(t.TempDir())
	require.NoError(t, err)
	a := &vmRunnerAdapter{ociImages: src, imageUse: newVMImageUse()}
	a.KeepImages([]string{digestB})

	release := a.imageUse.acquire(digestA)
	a.imageUse.bind("job-1", release)
	keep := a.imageUse.keepSet()
	require.True(t, keep[digestA], "an image of a running job is kept")
	require.True(t, keep[digestB], "an image of the set is kept")

	kept, err := a.RemoveImages(context.Background(), setImages())
	require.NoError(t, err)
	require.Len(t, kept, 1)
	require.Equal(t, digestA, kept[0].Digest)

	a.imageUse.releaseJob("job-1")
	require.False(t, a.imageUse.used(digestA))
	kept, err = a.RemoveImages(context.Background(), setImages()[:1])
	require.NoError(t, err)
	require.Empty(t, kept)
}

func TestVMAdapterImageSetNeedsOCISource(t *testing.T) {
	a := &vmRunnerAdapter{imageUse: newVMImageUse()}
	require.ErrorIs(t, a.SeedImages(context.Background(), setImages(), ImageSeedHooks{}), errVMImageSetNeedsOCI)
	require.NoError(t, a.SeedImages(context.Background(), nil, ImageSeedHooks{}))
}
