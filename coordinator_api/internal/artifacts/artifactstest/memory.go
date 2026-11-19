// Package artifactstest has an in-memory store.ArtifactStore for tests.
package artifactstest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// MemoryStore is an in-memory store.ArtifactStore.
type MemoryStore struct {
	mu        sync.Mutex
	next      int
	Artifacts map[string]*models.JobArtifact
	JobKeys   map[string]string
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{Artifacts: map[string]*models.JobArtifact{}, JobKeys: map[string]string{}}
}

var _ store.ArtifactStore = (*MemoryStore)(nil)

func (m *MemoryStore) CreateJobArtifact(_ context.Context, a *models.JobArtifact) (*models.JobArtifact, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.Artifacts {
		if existing.JobID == a.JobID && existing.Kind == a.Kind && existing.Path == a.Path {
			copy := *existing
			return &copy, false, nil
		}
	}
	m.next++
	row := *a
	row.ArtifactID = fmt.Sprintf("00000000-0000-0000-0000-%012d", m.next)
	row.CreatedAt = time.Now().UTC()
	m.Artifacts[row.ArtifactID] = &row
	copy := row
	return &copy, true, nil
}

func (m *MemoryStore) GetJobArtifact(_ context.Context, id string) (*models.JobArtifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.Artifacts[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *row
	return &copy, nil
}

func (m *MemoryStore) ListJobArtifacts(_ context.Context, jobID string) ([]models.JobArtifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []models.JobArtifact{}
	for _, row := range m.Artifacts {
		if row.JobID == jobID {
			out = append(out, *row)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Kind != out[b].Kind {
			return out[a].Kind < out[b].Kind
		}
		return out[a].Path < out[b].Path
	})
	return out, nil
}

func (m *MemoryStore) JobArtifactTotals(_ context.Context, jobID string) (int, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count, total := 0, int64(0)
	for _, row := range m.Artifacts {
		if row.JobID == jobID && row.Kind != models.ArtifactKindProvenance {
			count++
			total += row.SizeBytes
		}
	}
	return count, total, nil
}

func (m *MemoryStore) AppendJobArtifactChunk(_ context.Context, id string, expected, size int64, state []byte, maxJobBytes int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.Artifacts[id]
	if !ok || row.Status != models.ArtifactStatusUploading || row.SizeBytes != expected {
		return false, nil
	}
	if maxJobBytes > 0 {
		total := int64(0)
		for _, other := range m.Artifacts {
			if other.JobID == row.JobID && other.Kind != models.ArtifactKindProvenance {
				total += other.SizeBytes
			}
		}
		if total+(size-expected) > maxJobBytes {
			return false, store.ErrArtifactJobLimit
		}
	}
	row.SizeBytes = size
	row.HashState = append([]byte(nil), state...)
	row.ChunkCount++
	return true, nil
}

func (m *MemoryStore) CompleteJobArtifact(_ context.Context, id, digest string, size int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.Artifacts[id]
	if !ok || row.Status != models.ArtifactStatusUploading || row.SizeBytes != size {
		return store.ErrConflict
	}
	now := time.Now().UTC()
	row.Status, row.SHA256, row.HashState, row.CompletedAt = models.ArtifactStatusComplete, digest, nil, &now
	return nil
}

func (m *MemoryStore) FailJobArtifact(_ context.Context, id, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row, ok := m.Artifacts[id]; ok && row.Status == models.ArtifactStatusUploading {
		row.Status, row.StatusReason, row.HashState = models.ArtifactStatusFailed, reason, nil
	}
	return nil
}

func (m *MemoryStore) FailUploadingJobArtifacts(_ context.Context, jobID, reason string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, row := range m.Artifacts {
		if row.JobID == jobID && row.Status == models.ArtifactStatusUploading {
			row.Status, row.StatusReason = models.ArtifactStatusFailed, reason
			n++
		}
	}
	return n, nil
}

func (m *MemoryStore) SetJobArtifactKind(_ context.Context, id, kind, mediaType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row, ok := m.Artifacts[id]; ok {
		row.Kind, row.MediaType = kind, mediaType
	}
	return nil
}

func (m *MemoryStore) SetJobArtifactsObjectKey(_ context.Context, jobID, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.JobKeys[jobID] == "" {
		m.JobKeys[jobID] = key
	}
	return nil
}

func (m *MemoryStore) DeleteJobArtifactRecords(_ context.Context, jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, row := range m.Artifacts {
		if row.JobID == jobID {
			delete(m.Artifacts, id)
		}
	}
	return nil
}
