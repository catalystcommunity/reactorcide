package vmrunner

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry"
)

func TestOCIImageSourceImageSetRetention(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	pushFakeVMImageArtifact(t, ctx, store, "a", []byte("image-a-bytes"))
	pushFakeVMImageArtifact(t, ctx, store, "b", []byte("image-b-bytes-longer"))
	descA, err := store.Resolve(ctx, "a")
	require.NoError(t, err)
	descB, err := store.Resolve(ctx, "b")
	require.NoError(t, err)

	src, err := NewOCIImageSource(t.TempDir())
	require.NoError(t, err)
	src.sourceFactory = func(registry.Reference) (oras.ReadOnlyTarget, error) { return store, nil }

	// The memory store resolves tags only; a registry resolves the digest.
	refA := "example.invalid/vm/a:a"
	refB := "example.invalid/vm/b:b"

	size, err := src.PullSize(ctx, refA)
	require.NoError(t, err)
	require.Greater(t, size, int64(len("image-a-bytes")), "the estimate counts the manifest and the layer")

	_, err = src.Pull(ctx, refA)
	require.NoError(t, err)
	_, err = src.Pull(ctx, refB)
	require.NoError(t, err)
	require.True(t, src.HasImage(ctx, descA.Digest.String()))
	require.True(t, src.HasImage(ctx, descB.Digest.String()))

	size, err = src.PullSize(ctx, refA)
	require.NoError(t, err)
	require.Zero(t, size, "a cached image needs no space")

	free, err := src.FreeBytes()
	require.NoError(t, err)
	require.Positive(t, free)

	// Both images are old. Retention keeps the image of the set.
	old := time.Now().Add(-60 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(src.accessPath(descA), old, old))
	require.NoError(t, os.Chtimes(src.accessPath(descB), old, old))
	removed, err := src.PruneKeeping(ctx, 30*24*time.Hour, time.Now(), map[digest.Digest]bool{descA.Digest: true})
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	require.True(t, src.HasImage(ctx, descA.Digest.String()), "an image in the set is never pruned")
	require.False(t, src.HasImage(ctx, descB.Digest.String()))

	// An image that left the set is removed directly.
	require.NoError(t, src.RemoveImage(ctx, descA.Digest.String()))
	require.False(t, src.HasImage(ctx, descA.Digest.String()))
	require.NoError(t, src.RemoveImage(ctx, descA.Digest.String()), "removing a missing image is not an error")
}
