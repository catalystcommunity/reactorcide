package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/opencontainers/go-digest"
)

// vmImageUse records the VM image digests that running jobs use and the
// digests that the worker image set keeps. A nil *vmImageUse records
// nothing (adapters built in tests).
type vmImageUse struct {
	mu    sync.Mutex
	inUse map[string]int
	jobs  map[string]func()
	kept  map[string]bool
}

func newVMImageUse() *vmImageUse {
	return &vmImageUse{inUse: map[string]int{}, jobs: map[string]func(){}, kept: map[string]bool{}}
}

// acquire marks digest as in use and returns the function that releases
// it once.
func (u *vmImageUse) acquire(dgst string) func() {
	if u == nil || dgst == "" {
		return func() {}
	}
	u.mu.Lock()
	u.inUse[dgst]++
	u.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			u.mu.Lock()
			defer u.mu.Unlock()
			if u.inUse[dgst]--; u.inUse[dgst] <= 0 {
				delete(u.inUse, dgst)
			}
		})
	}
}

func (u *vmImageUse) bind(jobID string, release func()) {
	if u == nil {
		return
	}
	u.mu.Lock()
	u.jobs[jobID] = release
	u.mu.Unlock()
}

func (u *vmImageUse) releaseJob(jobID string) {
	if u == nil {
		return
	}
	u.mu.Lock()
	release := u.jobs[jobID]
	delete(u.jobs, jobID)
	u.mu.Unlock()
	if release != nil {
		release()
	}
}

func (u *vmImageUse) used(dgst string) bool {
	if u == nil {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.inUse[dgst] > 0
}

func (u *vmImageUse) setKept(digests []string) {
	if u == nil {
		return
	}
	kept := make(map[string]bool, len(digests))
	for _, d := range digests {
		if d != "" {
			kept[d] = true
		}
	}
	u.mu.Lock()
	u.kept = kept
	u.mu.Unlock()
}

// keepSet returns the digests that retention must keep: the kept set and
// every digest that a running job uses.
func (u *vmImageUse) keepSet() map[digest.Digest]bool {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make(map[digest.Digest]bool, len(u.kept)+len(u.inUse))
	for d := range u.kept {
		out[digest.Digest(d)] = true
	}
	for d := range u.inUse {
		out[digest.Digest(d)] = true
	}
	return out
}

// errVMImageSetNeedsOCI is the failure of an image set on a VM worker with
// the local image source: it cannot pull.
var errVMImageSetNeedsOCI = errors.New("VM image sets need REACTORCIDE_VM_IMAGE_SOURCE=oci or s3")

// ImageSetKind implements ImageSetSeeder.
func (a *vmRunnerAdapter) ImageSetKind() string { return ImageKindVM }

// ImageSpace implements ImageSetSeeder: the free space of the VM image
// cache and the estimated size of the images that it does not have.
func (a *vmRunnerAdapter) ImageSpace(ctx context.Context, images []ImageSetImage) (ImageSpace, error) {
	if a.ociImages == nil {
		if len(images) == 0 {
			return ImageSpace{}, nil
		}
		return ImageSpace{}, errVMImageSetNeedsOCI
	}
	var space ImageSpace
	free, err := a.ociImages.FreeBytes()
	if err == nil {
		space.FreeBytes, space.FreeKnown = free, true
	} else {
		logging.Log.WithError(err).Warn("could not read the free space of the VM image cache")
	}
	space.RequiredKnown = true
	for _, img := range images {
		size, err := a.ociImages.PullSize(ctx, img.Ref)
		if err != nil {
			if ctx.Err() != nil {
				return space, ctx.Err()
			}
			// The pull of this image reports the failure for this image
			// only. One bad image must not stop the others.
			logging.Log.WithError(err).WithField("image", img.Label()).Warn("could not read the size of an image set VM image")
			continue
		}
		space.RequiredBytes += size
	}
	return space, nil
}

// SeedImages implements ImageSetSeeder. The pull checks every blob digest.
// The VM image cache belongs to the worker, so every image is owned. Each
// image is pulled independently: a failure of one image does not stop the
// others.
func (a *vmRunnerAdapter) SeedImages(ctx context.Context, images []ImageSetImage, hooks ImageSeedHooks) error {
	if a.ociImages == nil {
		if len(images) == 0 {
			return nil
		}
		return errVMImageSetNeedsOCI
	}
	for _, img := range images {
		if err := ctx.Err(); err != nil {
			return err
		}
		imgCtx := hooks.imageContext(ctx, img)
		if imgCtx.Err() != nil {
			continue // the image left the set
		}
		// Pull is cheap for a cached image, and it makes sure that the
		// bundle is extracted.
		cached := img.Digest != "" && a.ociImages.HasImage(imgCtx, img.Digest)
		if !cached {
			logging.Log.WithField("image", img.Label()).Info("pulling image set VM image")
		}
		if _, err := a.ociImages.Pull(imgCtx, img.Ref); err != nil {
			if imgCtx.Err() != nil {
				continue // stopped: the image left the set, or the attempt stopped
			}
			pullErr := fmt.Errorf("pull %s: %w", img.Label(), err)
			logging.Log.WithError(pullErr).WithField("image", img.Label()).Warn("could not pull image set VM image")
			hooks.failed(img, pullErr)
			continue
		}
		if !cached {
			logging.Log.WithField("image", img.Label()).Info("pulled image set VM image")
		}
		hooks.ready(img, true)
	}
	return ctx.Err()
}

// KeepImages implements ImageSetSeeder: retention (PruneImages) never
// removes these digests.
func (a *vmRunnerAdapter) KeepImages(digests []string) { a.imageUse.setKept(digests) }

// RemoveImages implements ImageSetSeeder. An image that a running job
// uses stays (returned in kept).
func (a *vmRunnerAdapter) RemoveImages(ctx context.Context, images []ImageSetImage) ([]ImageSetImage, error) {
	if a.ociImages == nil {
		return nil, nil
	}
	var kept []ImageSetImage
	for _, img := range images {
		if img.Digest == "" {
			continue
		}
		if a.imageUse.used(img.Digest) {
			kept = append(kept, img)
			continue
		}
		if err := a.ociImages.RemoveImage(ctx, img.Digest); err != nil {
			return kept, fmt.Errorf("remove %s: %w", img.Label(), err)
		}
		logging.Log.WithField("image", img.Label()).Info("removed VM image that left the image set")
	}
	return kept, nil
}

var _ ImageSetSeeder = (*vmRunnerAdapter)(nil)
