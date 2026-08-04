package vmrunner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalImageSourceRestrictsReferencesToBaseDir(t *testing.T) {
	baseDir := t.TempDir()
	imageDir := filepath.Join(baseDir, "macos-base")
	if err := os.Mkdir(imageDir, 0o755); err != nil {
		t.Fatal(err)
	}

	source := NewLocalImageSource(baseDir)
	path, err := source.Resolve(context.Background(), "macos-base")
	if err != nil {
		t.Fatalf("Resolve valid image: %v", err)
	}
	wantPath, err := filepath.EvalSymlinks(imageDir)
	if err != nil {
		t.Fatal(err)
	}
	if path != wantPath {
		t.Fatalf("Resolve valid image = %q, want %q", path, wantPath)
	}

	for _, ref := range []string{"../outside", imageDir} {
		if _, err := source.Resolve(context.Background(), ref); err == nil {
			t.Fatalf("Resolve(%q) succeeded, want path restriction error", ref)
		}
	}
}

func TestLocalImageSourceAllowsAbsoluteReferenceWithoutBaseDir(t *testing.T) {
	imageDir := t.TempDir()
	path, err := NewLocalImageSource("").Resolve(context.Background(), imageDir)
	if err != nil {
		t.Fatalf("Resolve absolute smoke-test image: %v", err)
	}
	if path != imageDir {
		t.Fatalf("Resolve absolute image = %q, want %q", path, imageDir)
	}
}
