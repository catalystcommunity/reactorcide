package worker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func tarOf(t *testing.T, entries []tar.Header, bodies []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for i, h := range entries {
		h.Size = int64(len(bodies[i]))
		if h.Typeflag != tar.TypeReg {
			h.Size = 0
		}
		if err := w.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = w.Write([]byte(bodies[i]))
		}
	}
	_ = w.Close()
	return buf.Bytes()
}

func TestExtractArtifactTarKeepsOnlyRegularFilesInside(t *testing.T) {
	dest := t.TempDir()
	data := tarOf(t, []tar.Header{
		{Name: "./a.txt", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "./dir/b.txt", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "./link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "../escape.txt", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "/abs.txt", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "./dev", Typeflag: tar.TypeChar},
	}, []string{"A", "B", "", "X", "Y", ""})
	if err := ExtractArtifactTar(bytes.NewReader(data), dest, 100); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "a.txt")); string(got) != "A" {
		t.Fatalf("a.txt %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "dir", "b.txt")); string(got) != "B" {
		t.Fatalf("b.txt %q", got)
	}
	for _, name := range []string{"link", "dev"} {
		if _, err := os.Lstat(filepath.Join(dest, name)); err == nil {
			t.Fatalf("%s must be skipped", name)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escape.txt")); err == nil {
		t.Fatal("a path outside the destination must be skipped")
	}
	// "/abs.txt" is cleaned to a path inside the destination or skipped;
	// it never reaches the root.
	if _, err := os.Stat("/abs.txt"); err == nil {
		t.Fatal("an absolute path must not be written")
	}
}

func TestExtractArtifactTarLimit(t *testing.T) {
	data := tarOf(t, []tar.Header{{Name: "a", Typeflag: tar.TypeReg}, {Name: "b", Typeflag: tar.TypeReg}}, []string{"12345", "67890"})
	dest := t.TempDir()
	if err := ExtractArtifactTar(bytes.NewReader(data), dest, 7); !errors.Is(err, ErrArtifactsTooLarge) {
		t.Fatalf("limit: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "a")); string(got) != "12345" {
		t.Fatal("the files before the limit stay")
	}
}

func TestAddKubernetesJobFiles(t *testing.T) {
	container := corev1.Container{Command: []string{"sh", "-c", "set -eu; chmod -R 0777 /job"}}
	skipped := addKubernetesJobFiles(&container, map[string][]byte{
		"quarantined-tests.json": []byte(`{"tests":[]}`),
		"../evil":                []byte("x"),
		".hidden":                []byte("x"),
		"big.json":               bytes.Repeat([]byte("x"), maxKubernetesJobFilesBytes+1),
	})
	if len(skipped) != 3 {
		t.Fatalf("skipped %v", skipped)
	}
	if len(container.Env) != 1 || container.Env[0].Value != `{"tests":[]}` {
		t.Fatalf("env %+v", container.Env)
	}
	if !strings.Contains(container.Command[2], `> /job/quarantined-tests.json`) || strings.Contains(container.Command[2], "evil") {
		t.Fatalf("script %q", container.Command[2])
	}
	if !strings.Contains(container.Command[2], `"$`+container.Env[0].Name+`"`) {
		t.Fatal("the content goes through an environment value, never into the script text")
	}
}

type summaryStore struct {
	store.Store
	summary *models.JobTestSummary
}

func (s summaryStore) GetJobTestSummary(context.Context, string) (*models.JobTestSummary, error) {
	if s.summary == nil {
		return nil, store.ErrNotFound
	}
	return s.summary, nil
}

func TestQuarantineOverrideReadsTheSummary(t *testing.T) {
	failed := &models.Job{JobID: "j", Status: "failed"}
	tp := &TriggerProcessor{store: summaryStore{summary: &models.JobTestSummary{QuarantineOverride: true}}}
	if !tp.quarantineOverride(context.Background(), failed) {
		t.Fatal("override")
	}
	if tp.quarantineOverride(context.Background(), &models.Job{JobID: "j", Status: "completed"}) {
		t.Fatal("only a failed job")
	}
	tp = &TriggerProcessor{store: summaryStore{}}
	if tp.quarantineOverride(context.Background(), failed) {
		t.Fatal("no summary: no override")
	}
}
