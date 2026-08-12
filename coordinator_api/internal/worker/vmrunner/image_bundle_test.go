package vmrunner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMacBundleArchiveRoundTrip(t *testing.T) {
	src := writeTestMacBundle(t)
	var archive bytes.Buffer
	require.NoError(t, WriteMacBundleArchive(context.Background(), src, &archive))

	dst := filepath.Join(t.TempDir(), "bundle")
	require.NoError(t, ExtractMacBundleArchive(context.Background(), bytes.NewReader(archive.Bytes()), dst))
	for _, name := range macBundleFiles {
		want, err := os.ReadFile(filepath.Join(src, name))
		require.NoError(t, err)
		got, err := os.ReadFile(filepath.Join(dst, name))
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}

func TestExtractMacBundleArchiveRejectsUnexpectedEntry(t *testing.T) {
	src := writeTestMacBundle(t)
	require.NoError(t, os.WriteFile(filepath.Join(src, "unexpected"), []byte("x"), 0o600))

	// WriteMacBundleArchive intentionally ignores files outside the fixed
	// bundle shape, which prevents accidental credential inclusion.
	var archive bytes.Buffer
	require.NoError(t, WriteMacBundleArchive(context.Background(), src, &archive))
	dst := filepath.Join(t.TempDir(), "bundle")
	require.NoError(t, ExtractMacBundleArchive(context.Background(), bytes.NewReader(archive.Bytes()), dst))
	_, err := os.Stat(filepath.Join(dst, "unexpected"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestWindowsBundleArchiveRoundTrip(t *testing.T) {
	src := writeTestWindowsBundle(t)
	var archive bytes.Buffer
	require.NoError(t, WriteWindowsBundleArchive(context.Background(), src, &archive))

	dst := filepath.Join(t.TempDir(), "bundle")
	require.NoError(t, ExtractWindowsBundleArchive(context.Background(), bytes.NewReader(archive.Bytes()), dst))
	for _, name := range windowsBundleFiles {
		want, err := os.ReadFile(filepath.Join(src, name))
		require.NoError(t, err)
		got, err := os.ReadFile(filepath.Join(dst, name))
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}

func TestWindowsBundleArchiveExcludesUnknownFiles(t *testing.T) {
	src := writeTestWindowsBundle(t)
	require.NoError(t, os.WriteFile(filepath.Join(src, "guest-private-key"), []byte("must-not-publish"), 0o600))
	var archive bytes.Buffer
	require.NoError(t, WriteWindowsBundleArchive(context.Background(), src, &archive))

	dst := filepath.Join(t.TempDir(), "bundle")
	require.NoError(t, ExtractWindowsBundleArchive(context.Background(), bytes.NewReader(archive.Bytes()), dst))
	_, err := os.Stat(filepath.Join(dst, "guest-private-key"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func writeTestMacBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range macBundleFiles {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("contents-"+name), 0o600))
	}
	return dir
}

func writeTestWindowsBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range windowsBundleFiles {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("contents-"+name), 0o600))
	}
	return dir
}
