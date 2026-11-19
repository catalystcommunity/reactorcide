package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/errdefs"
)

// dockerImageAPI is the part of the Docker client that the image set
// pre-seed uses. Tests replace it with a fake.
type dockerImageAPI interface {
	ImageInspectWithRaw(ctx context.Context, imageID string) (image.InspectResponse, []byte, error)
	ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error)
	ImageRemove(ctx context.Context, imageID string, options image.RemoveOptions) ([]image.DeleteResponse, error)
}

// dockerImageSeeder pulls container images of the worker image set with
// the Docker pull API. It uses the same pull call as a job (ensureImage),
// so the registry access is the same as for a job.
type dockerImageSeeder struct {
	api dockerImageAPI
}

// seed pulls each image independently. A failure of one image is given to
// hooks.Failed, and the next image is pulled.
func (s dockerImageSeeder) seed(ctx context.Context, images []ImageSetImage, hooks ImageSeedHooks) error {
	for _, img := range images {
		if err := ctx.Err(); err != nil {
			return err
		}
		imgCtx := hooks.imageContext(ctx, img)
		if imgCtx.Err() != nil {
			continue // the image left the set
		}
		owned, err := s.pull(imgCtx, img)
		switch {
		case err == nil:
			hooks.ready(img, owned)
		case imgCtx.Err() != nil:
			// Stopped: the image left the set, or the attempt stopped.
		default:
			logging.Log.WithError(err).WithField("image", img.Label()).Warn("could not pull image set container image")
			hooks.failed(img, err)
		}
	}
	return ctx.Err()
}

// pull makes one image present. owned is true when this call pulled it.
func (s dockerImageSeeder) pull(ctx context.Context, img ImageSetImage) (bool, error) {
	ref := containerImageRef(img.Ref)
	if _, _, err := s.api.ImageInspectWithRaw(ctx, ref); err == nil {
		return false, nil
	}
	logging.Log.WithField("image", img.Label()).Info("pulling image set container image")
	resp, err := s.api.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return false, fmt.Errorf("pull %s: %w", img.Label(), err)
	}
	// The pull stream reports a failure in the body (a JSON message with
	// an "error" field). Read it, so that the failure text can be
	// classified.
	streamErr := dockerPullStreamError(resp)
	closeErr := resp.Close()
	if streamErr != nil {
		return false, fmt.Errorf("pull %s: %w", img.Label(), streamErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("pull %s: %w", img.Label(), closeErr)
	}
	// Inspect to be sure that the image is present.
	if _, _, err := s.api.ImageInspectWithRaw(ctx, ref); err != nil {
		return false, TransientPullError(fmt.Errorf("pull %s: image is not present after the pull: %w", img.Label(), err))
	}
	return true, nil
}

// dockerPullStreamError reads a Docker pull stream and returns the error
// message of the stream, or a read error.
func dockerPullStreamError(r io.Reader) error {
	dec := json.NewDecoder(r)
	for {
		var msg struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			// Not JSON: read the rest, so that the pull completes.
			if _, copyErr := io.Copy(io.Discard, r); copyErr != nil {
				return copyErr
			}
			return nil
		}
		if msg.ErrorDetail.Message != "" {
			return errors.New(msg.ErrorDetail.Message)
		}
		if msg.Error != "" {
			return errors.New(msg.Error)
		}
	}
}

func (s dockerImageSeeder) remove(ctx context.Context, images []ImageSetImage) ([]ImageSetImage, error) {
	var kept []ImageSetImage
	for _, img := range images {
		ref := containerImageRef(img.Ref)
		// No Force: Docker refuses to remove an image that a container
		// uses. Then the image stays, and the worker tries again later.
		if _, err := s.api.ImageRemove(ctx, ref, image.RemoveOptions{PruneChildren: true}); err != nil {
			switch {
			case errdefs.IsNotFound(err):
				continue
			case errdefs.IsConflict(err):
				kept = append(kept, img)
				continue
			}
			return kept, fmt.Errorf("remove %s: %w", img.Label(), err)
		}
		logging.Log.WithField("image", img.Label()).Info("removed container image that left the image set")
	}
	return kept, nil
}

// ImageSetKind implements ImageSetSeeder.
func (dr *DockerRunner) ImageSetKind() string { return ImageKindContainer }

// ImageSpace implements ImageSetSeeder. The Docker API does not give the
// free space of the image store, and the worker can run in a container
// that does not see that file system.
func (dr *DockerRunner) ImageSpace(context.Context, []ImageSetImage) (ImageSpace, error) {
	return ImageSpace{}, nil
}

// SeedImages implements ImageSetSeeder.
func (dr *DockerRunner) SeedImages(ctx context.Context, images []ImageSetImage, hooks ImageSeedHooks) error {
	return dockerImageSeeder{api: dr.client}.seed(ctx, images, hooks)
}

// KeepImages implements ImageSetSeeder. Docker has no worker retention.
func (dr *DockerRunner) KeepImages([]string) {}

// RemoveImages implements ImageSetSeeder.
func (dr *DockerRunner) RemoveImages(ctx context.Context, images []ImageSetImage) ([]ImageSetImage, error) {
	return dockerImageSeeder{api: dr.client}.remove(ctx, images)
}

var _ ImageSetSeeder = (*DockerRunner)(nil)
