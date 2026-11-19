package worker

import (
	"context"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imageref"
)

// Image set kinds (ImageSetEntry.kind in reactorcide-worker.csil). A
// backend pulls only the entries of its own kind.
const (
	ImageKindVM        = "vm"
	ImageKindContainer = "container"
)

// ImageSetImage is one image of a worker image set (VM_IMAGES_PLAN.md
// "Worker image sets"). Ref is the pinned reference that the coordinator
// resolved. Digest is its content digest ("sha256:..."), the value of the
// image_digest characteristic.
type ImageSetImage struct {
	Ref     string
	Digest  string
	Aliases []string
}

// Label returns the text for logs: the alias names and the reference.
func (i ImageSetImage) Label() string {
	if len(i.Aliases) == 0 {
		return i.Ref
	}
	return strings.Join(i.Aliases, ",") + " (" + i.Ref + ")"
}

// ImageSpace is the disk space check before an image set pull. A zero
// FreeBytes or RequiredBytes with the Known flag false means that the
// backend cannot measure it.
type ImageSpace struct {
	FreeBytes     int64
	FreeKnown     bool
	RequiredBytes int64
	RequiredKnown bool
}

// ImageSetSeeder is an optional JobRunner capability: the backend pulls the
// images of the worker image set before the worker claims jobs for them
// (owner decisions I4, I8, I12, I13). The coordinator-mediated worker
// (internal/coordinatorworker/imageset.go) calls it. Only one call runs at
// a time.
type ImageSetSeeder interface {
	// ImageSetKind is the ImageSetEntry kind that this backend pulls.
	ImageSetKind() string

	// ImageSpace measures the free space of the image store and the space
	// that the images that are not present yet need.
	ImageSpace(ctx context.Context, images []ImageSetImage) (ImageSpace, error)

	// SeedImages makes every image ready on this worker. It pulls each
	// image independently: a failure of one image does not stop the
	// others. It calls hooks.Ready for each image that is ready, and
	// hooks.Failed for each image that it could not make ready. owned is
	// true when this worker pulled the image for a set, so that the worker
	// can remove it when it leaves the set. An image that was present
	// before (pulled by an operator or another tool) is not owned.
	//
	// hooks.ImageContext gives the context of the pull of one image. When
	// that context is done (the image left the set), SeedImages skips the
	// image without a callback. SeedImages returns an error only when it
	// could not try the images at all (the context of the call is done, or
	// the backend cannot pull image sets); the images that were ready
	// before the error stay ready.
	SeedImages(ctx context.Context, images []ImageSetImage, hooks ImageSeedHooks) error

	// KeepImages tells the backend retention which digests it must keep:
	// the current set, the set that is in progress, the operator list, and
	// the images that the worker owns and did not remove yet.
	KeepImages(digests []string)

	// RemoveImages removes images that left the set. The caller removes
	// only owned images that no running job uses. An image that a running
	// job of the backend uses is skipped without an error (returned in
	// kept), so that the caller tries again later.
	RemoveImages(ctx context.Context, images []ImageSetImage) (kept []ImageSetImage, err error)
}

// ImageSetToucher is an optional ImageSetSeeder capability: the manager
// calls TouchImageSet from time to time while the worker runs, so that the
// backend can mark its pre-seed objects as in use (the Kubernetes
// DaemonSet) and remove old objects that no worker uses.
type ImageSetToucher interface {
	TouchImageSet(ctx context.Context)
}

// ImageSetTouchInterval is the time between two TouchImageSet calls.
const ImageSetTouchInterval = 4 * time.Minute

// ImageSeedHooks are the callbacks of ImageSetSeeder.SeedImages. A nil
// function is not called.
type ImageSeedHooks struct {
	// ImageContext returns the context for the pull of one image. Nil
	// uses the context of the SeedImages call.
	ImageContext func(ImageSetImage) context.Context
	// Ready is called when the image is ready.
	Ready func(image ImageSetImage, owned bool)
	// Failed is called when the image could not be made ready. Use
	// IsPermanentPullError on err to know whether a retry can help.
	Failed func(image ImageSetImage, err error)
}

func (h ImageSeedHooks) imageContext(ctx context.Context, img ImageSetImage) context.Context {
	if h.ImageContext == nil {
		return ctx
	}
	if c := h.ImageContext(img); c != nil {
		return c
	}
	return ctx
}

func (h ImageSeedHooks) ready(img ImageSetImage, owned bool) {
	if h.Ready != nil {
		h.Ready(img, owned)
	}
}

func (h ImageSeedHooks) failed(img ImageSetImage, err error) {
	if h.Failed != nil {
		h.Failed(img, err)
	}
}

// ImageDigest returns the digest of a pinned reference, or "".
func ImageDigest(ref string) string {
	parsed, err := imageref.Parse(strings.TrimSpace(ref))
	if err != nil {
		return ""
	}
	return parsed.Digest
}

// containerImageRef returns a reference that container runtimes accept:
// a job stores an oci:// reference in plain form, but an image set entry
// can carry the scheme.
func containerImageRef(ref string) string {
	return strings.TrimPrefix(strings.TrimSpace(ref), "oci://")
}

// RuntimeName returns the name of the runtime of a JobRunner backend: vm,
// docker, containerd, or kubernetes. It returns "" for an unknown runner
// (a test fake).
func RuntimeName(runner JobRunner) string {
	switch runner.(type) {
	case *vmRunnerAdapter:
		return string(BackendVM)
	case *DockerRunner:
		return string(BackendDocker)
	case *ContainerdRunner:
		return string(BackendContainerd)
	case *KubernetesRunner:
		return string(BackendKubernetes)
	default:
		return ""
	}
}
