package artifacts_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/artifacts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/artifacts/artifactstest"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func newService(limits artifacts.Limits) (*artifacts.Service, *artifactstest.MemoryStore, objects.ObjectStore) {
	st := artifactstest.NewMemoryStore()
	obj := objects.NewMemoryObjectStore()
	return &artifacts.Service{Store: st, Objects: obj, Limits: limits}, st, obj
}

func job(id string) *models.Job { return &models.Job{JobID: id} }

func upload(t *testing.T, s *artifacts.Service, j *models.Job, path string, data []byte, chunk int) *models.JobArtifact {
	t.Helper()
	var last *models.JobArtifact
	offset := 0
	for {
		end := offset + chunk
		if end > len(data) {
			end = len(data)
		}
		var err error
		last, err = s.AppendChunk(context.Background(), j, "main-default", artifacts.ChunkRequest{Path: path, Offset: int64(offset), Data: data[offset:end], Final: end == len(data)})
		if err != nil {
			t.Fatalf("chunk at %d: %v", offset, err)
		}
		offset = end
		if end == len(data) {
			return last
		}
	}
}

func TestValidatePath(t *testing.T) {
	for _, good := range []string{"a.txt", "dir/b.bin", "reports/junit-unit.xml", "a b/ü.txt"} {
		if err := artifacts.ValidatePath(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
	for _, bad := range []string{"", "/abs", "../up", "a/../b", "a//b", "./a", "a/", `a\b`, "a\x00b", "a\nb", strings.Repeat("x", 513), strings.Repeat("y", 256)} {
		if err := artifacts.ValidatePath(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestChunkedUploadDigestAndRead(t *testing.T) {
	s, st, _ := newService(artifacts.DefaultLimits)
	data := bytes.Repeat([]byte("0123456789abcdef"), 1000) // 16000 bytes
	artifact := upload(t, s, job("job-1"), "out/data.bin", data, 4096)
	sum := sha256.Sum256(data)
	if artifact.Status != models.ArtifactStatusComplete || artifact.SHA256 != hex.EncodeToString(sum[:]) || artifact.SizeBytes != 16000 {
		t.Fatalf("artifact %+v", artifact)
	}
	stored, _ := st.GetJobArtifact(context.Background(), artifact.ArtifactID)
	if stored.ChunkCount != 4 || !strings.HasPrefix(stored.StoragePrefix, "artifacts/main-default/job-1") {
		t.Fatalf("stored %+v", stored)
	}
	if st.JobKeys["job-1"] != "artifacts/main-default/job-1" {
		t.Fatalf("job object key %v", st.JobKeys)
	}
	reader, err := s.Open(context.Background(), stored)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(reader)
	if !bytes.Equal(got, data) {
		t.Fatal("download differs")
	}
	part, err := s.ReadRange(context.Background(), stored, 15990, 100)
	if err != nil || string(part) != "6789abcdef" {
		t.Fatalf("range %q %v", part, err)
	}
	// An empty file is one final chunk.
	empty := upload(t, s, job("job-1"), "empty.txt", nil, 10)
	if empty.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty digest %s", empty.SHA256)
	}
}

func TestRetryAndOffsetRules(t *testing.T) {
	s, _, _ := newService(artifacts.DefaultLimits)
	ctx := context.Background()
	j := job("job-2")
	first, err := s.AppendChunk(ctx, j, "", artifacts.ChunkRequest{Path: "a.txt", Offset: 0, Data: []byte("hello ")})
	if err != nil {
		t.Fatal(err)
	}
	// A retry of the same chunk is a no-op.
	if again, err := s.AppendChunk(ctx, j, "", artifacts.ChunkRequest{Path: "a.txt", Offset: 0, Data: []byte("hello ")}); err != nil || again.SizeBytes != 6 {
		t.Fatalf("retry: %v %+v", err, again)
	}
	if _, err := s.AppendChunk(ctx, j, "", artifacts.ChunkRequest{Path: "a.txt", Offset: 10, Data: []byte("x")}); artifacts.ErrorCode(err) != "invalid_argument" {
		t.Fatalf("gap must be refused: %v", err)
	}
	if _, err := s.AppendChunk(ctx, j, "", artifacts.ChunkRequest{Path: "b.txt", Offset: 3, Data: []byte("x")}); artifacts.ErrorCode(err) != "invalid_argument" {
		t.Fatalf("a first chunk must start at 0: %v", err)
	}
	done, err := s.AppendChunk(ctx, j, "", artifacts.ChunkRequest{Path: "a.txt", Offset: 6, Data: []byte("world"), Final: true})
	if err != nil || done.Status != models.ArtifactStatusComplete {
		t.Fatalf("final: %v %+v", err, done)
	}
	if again, err := s.AppendChunk(ctx, j, "", artifacts.ChunkRequest{Path: "a.txt", Offset: 6, Data: []byte("world"), Final: true}); err != nil || again.ArtifactID != first.ArtifactID {
		t.Fatalf("a retried final chunk is a no-op: %v", err)
	}
	if _, err := s.AppendChunk(ctx, j, "", artifacts.ChunkRequest{Path: "a.txt", Offset: 11, Data: []byte("!"), Final: true}); artifacts.ErrorCode(err) != "conflict" {
		t.Fatalf("a complete artifact is closed: %v", err)
	}
}

func TestLimits(t *testing.T) {
	ctx := context.Background()
	s, st, _ := newService(artifacts.Limits{Enabled: true, MaxFileBytes: 10, MaxJobBytes: 15, MaxFiles: 2, ChunkBytes: 8})
	j := job("job-3")
	if _, err := s.AppendChunk(ctx, j, "", artifacts.ChunkRequest{Path: "big", Data: bytes.Repeat([]byte("x"), 9)}); artifacts.ErrorCode(err) != "invalid_argument" {
		t.Fatalf("a chunk over ChunkBytes: %v", err)
	}
	a, err := s.AppendChunk(ctx, j, "", artifacts.ChunkRequest{Path: "f1", Data: []byte("12345678")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendChunk(ctx, j, "", artifacts.ChunkRequest{Path: "f1", Offset: 8, Data: []byte("abc"), Final: true}); artifacts.ErrorCode(err) != "too_large" {
		t.Fatalf("file limit: %v", err)
	}
	failed, _ := st.GetJobArtifact(ctx, a.ArtifactID)
	if failed.Status != models.ArtifactStatusFailed || !strings.Contains(failed.StatusReason, "limit") {
		t.Fatalf("a refused upload fails: %+v", failed)
	}
	if _, err := s.AppendChunk(ctx, j, "", artifacts.ChunkRequest{Path: "f2", Data: []byte("1234567"), Final: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendChunk(ctx, j, "", artifacts.ChunkRequest{Path: "f2", Offset: 7, Data: []byte("8"), Final: true}); artifacts.ErrorCode(err) != "too_large" {
		t.Fatalf("job limit (8 + 8 > 15): %v", err)
	}
	if _, err := s.AppendChunk(ctx, j, "", artifacts.ChunkRequest{Path: "f3", Data: []byte("1"), Final: true}); artifacts.ErrorCode(err) != "too_large" {
		t.Fatalf("file count limit: %v", err)
	}
	off, _, _ := newService(artifacts.Limits{Enabled: false})
	if _, err := off.AppendChunk(ctx, j, "", artifacts.ChunkRequest{Path: "x", Final: true}); artifacts.ErrorCode(err) != "unavailable" {
		t.Fatalf("turned off: %v", err)
	}
}

func TestFinalizeReclassifiesAndFailsIncomplete(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(artifacts.DefaultLimits)
	j := job("job-4")
	upload(t, s, j, "results/unit.xml", []byte("<testsuite/>"), 100)
	upload(t, s, j, "reports/junit-api.xml", []byte("<testsuite/>"), 100)
	if _, err := s.AppendChunk(ctx, j, "", artifacts.ChunkRequest{Path: "partial.bin", Data: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	list, err := s.Finalize(ctx, j.JobID, []string{"./results/unit.xml"})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	statuses := map[string]string{}
	for _, a := range list {
		kinds[a.Path], statuses[a.Path] = a.Kind, a.Status
	}
	if kinds["results/unit.xml"] != models.ArtifactKindTestReport || kinds["reports/junit-api.xml"] != models.ArtifactKindTestReport {
		t.Fatalf("kinds %v", kinds)
	}
	if statuses["partial.bin"] != models.ArtifactStatusFailed {
		t.Fatalf("statuses %v", statuses)
	}
}

func TestPutCompleteAndPrune(t *testing.T) {
	ctx := context.Background()
	s, st, obj := newService(artifacts.DefaultLimits)
	j := job("job-5")
	a, err := s.PutComplete(ctx, j, "ns", models.ArtifactKindProvenance, "provenance.json", "application/json", []byte(`{"x":1}`))
	if err != nil || a.Status != models.ArtifactStatusComplete || a.SizeBytes != 7 {
		t.Fatalf("%v %+v", err, a)
	}
	if again, err := s.PutComplete(ctx, j, "ns", models.ArtifactKindProvenance, "provenance.json", "application/json", []byte(`other`)); err != nil || again.ArtifactID != a.ArtifactID {
		t.Fatalf("a second put keeps the first record: %v", err)
	}
	upload(t, s, j, "a.txt", []byte("abc"), 10)
	old := time.Now().Add(-48 * time.Hour)
	getJob := func(_ context.Context, id string) (*models.Job, error) {
		return &models.Job{JobID: id, CompletedAt: &old}, nil
	}
	deleted, err := s.PruneBefore(ctx, time.Now().Add(-24*time.Hour), getJob)
	if err != nil || deleted != 1 {
		t.Fatalf("prune %d %v", deleted, err)
	}
	left, _ := obj.List(ctx, artifacts.ObjectRoot+"/")
	rows, _ := st.ListJobArtifacts(ctx, j.JobID)
	if len(left) != 0 || len(rows) != 0 {
		t.Fatalf("left objects %d rows %d", len(left), len(rows))
	}
}

func TestKindMediaAndNamespace(t *testing.T) {
	if artifacts.KindFor("x/junit.xml", nil) != models.ArtifactKindTestReport || artifacts.KindFor("x/a.xml", nil) != models.ArtifactKindFile {
		t.Fatal("kind convention")
	}
	if artifacts.MediaType("a.json") != "application/json" || artifacts.MediaType("a.unknownext") != "application/octet-stream" {
		t.Fatal("media type")
	}
	if artifacts.ValidateNamespace("") != "standard" || artifacts.ValidateNamespace("Main/../X") != "main-..-x" {
		t.Fatalf("namespace %q", artifacts.ValidateNamespace("Main/../X"))
	}
	if strings.Contains(artifacts.JobPrefix("a/b", "j"), "a/b") {
		t.Fatal("a namespace cannot add a key level")
	}
}
