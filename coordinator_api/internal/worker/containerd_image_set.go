package worker

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/catalystcommunity/app-utils-go/logging"
)

// nerdctlCommand runs one nerdctl command in the reactorcide namespace and
// returns its combined output. Tests replace it with a fake.
type nerdctlCommand func(ctx context.Context, args ...string) ([]byte, error)

func runNerdctl(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"--namespace", containerdNamespace}, args...)
	return exec.CommandContext(ctx, nerdctlBinary, full...).CombinedOutput()
}

// containerdImageSeeder pulls container images of the worker image set
// with `nerdctl pull`, the same command (and the same registry
// credentials) that a job uses.
type containerdImageSeeder struct {
	run nerdctlCommand
}

// seed pulls each image independently. A failure of one image is given to
// hooks.Failed, and the next image is pulled.
func (s containerdImageSeeder) seed(ctx context.Context, images []ImageSetImage, hooks ImageSeedHooks) error {
	for _, img := range images {
		if err := ctx.Err(); err != nil {
			return err
		}
		imgCtx := hooks.imageContext(ctx, img)
		if imgCtx.Err() != nil {
			continue // the image left the set
		}
		ref := containerImageRef(img.Ref)
		if _, err := s.run(imgCtx, "image", "inspect", ref); err == nil {
			hooks.ready(img, false)
			continue
		}
		logging.Log.WithField("image", img.Label()).Info("pulling image set container image")
		out, err := s.run(imgCtx, "pull", ref)
		switch {
		case err == nil:
			hooks.ready(img, true)
		case imgCtx.Err() != nil:
			// Stopped: the image left the set, or the attempt stopped.
		default:
			pullErr := fmt.Errorf("pull %s: %w (%s)", img.Label(), err, strings.TrimSpace(lastLines(string(out), 3)))
			logging.Log.WithError(pullErr).WithField("image", img.Label()).Warn("could not pull image set container image")
			hooks.failed(img, pullErr)
		}
	}
	return ctx.Err()
}

func (s containerdImageSeeder) remove(ctx context.Context, images []ImageSetImage) ([]ImageSetImage, error) {
	var kept []ImageSetImage
	for _, img := range images {
		ref := containerImageRef(img.Ref)
		if _, err := s.run(ctx, "image", "inspect", ref); err != nil {
			continue // already removed
		}
		// No --force: nerdctl refuses to remove an image that a container
		// uses. Then the image stays, and the worker tries again later.
		if out, err := s.run(ctx, "rmi", ref); err != nil {
			text := strings.ToLower(string(out))
			if strings.Contains(text, "in use") || strings.Contains(text, "is used by") {
				kept = append(kept, img)
				continue
			}
			return kept, fmt.Errorf("remove %s: %w (%s)", img.Label(), err, strings.TrimSpace(lastLines(string(out), 3)))
		}
		logging.Log.WithField("image", img.Label()).Info("removed container image that left the image set")
	}
	return kept, nil
}

func (cr *ContainerdRunner) nerdctl() nerdctlCommand {
	if cr.runNerdctl != nil {
		return cr.runNerdctl
	}
	return runNerdctl
}

// ImageSetKind implements ImageSetSeeder.
func (cr *ContainerdRunner) ImageSetKind() string { return ImageKindContainer }

// ImageSpace implements ImageSetSeeder. nerdctl does not give the free
// space of the containerd content store, and the worker can run in a
// container that does not see that file system.
func (cr *ContainerdRunner) ImageSpace(context.Context, []ImageSetImage) (ImageSpace, error) {
	return ImageSpace{}, nil
}

// SeedImages implements ImageSetSeeder.
func (cr *ContainerdRunner) SeedImages(ctx context.Context, images []ImageSetImage, hooks ImageSeedHooks) error {
	return containerdImageSeeder{run: cr.nerdctl()}.seed(ctx, images, hooks)
}

// KeepImages implements ImageSetSeeder. containerd has no worker retention.
func (cr *ContainerdRunner) KeepImages([]string) {}

// RemoveImages implements ImageSetSeeder.
func (cr *ContainerdRunner) RemoveImages(ctx context.Context, images []ImageSetImage) ([]ImageSetImage, error) {
	return containerdImageSeeder{run: cr.nerdctl()}.remove(ctx, images)
}

var _ ImageSetSeeder = (*ContainerdRunner)(nil)
