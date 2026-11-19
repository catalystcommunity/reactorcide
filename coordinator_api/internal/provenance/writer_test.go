package provenance

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/artifacts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/artifacts/artifactstest"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type memKeys struct {
	mu   sync.Mutex
	keys []models.OIDCSigningKey
}

func (m *memKeys) ListProvenanceSigningKeys(context.Context) ([]models.OIDCSigningKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]models.OIDCSigningKey(nil), m.keys...), nil
}
func (m *memKeys) CreateProvenanceSigningKey(_ context.Context, key *models.OIDCSigningKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys = append(m.keys, *key)
	return nil
}
func (m *memKeys) RetireProvenanceSigningKeys(_ context.Context, keep string, at time.Time) error {
	return nil
}
func (m *memKeys) SetProvenanceSigningKeyRetirement(context.Context, string, time.Time) error {
	return nil
}
func (m *memKeys) WithProvenanceKeyLock(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (m *memKeys) MasterKeyIDByName(context.Context, string) (string, error) { return "mk-1", nil }
func (m *memKeys) MasterKeyNameByID(context.Context, string) (string, error) { return "primary", nil }

var _ store.ProvenanceKeyStore = (*memKeys)(nil)

type plainCrypto struct{}

func (plainCrypto) EncryptWithPrimary(p []byte) (string, []byte, error) { return "primary", p, nil }
func (plainCrypto) DecryptWithKey(_ string, c []byte) ([]byte, error)   { return c, nil }

type factsStore struct{}

func (factsStore) GetOrganizationByID(context.Context, string) (*models.Organization, error) {
	return &models.Organization{OrgID: "org-1", Name: "acme"}, nil
}
func (factsStore) GetProjectByID(context.Context, string) (*models.Project, error) {
	return &models.Project{ProjectID: "p-1", Name: "app"}, nil
}
func (factsStore) GetWorkflowInstance(context.Context, string) (*models.WorkflowInstance, error) {
	return nil, store.ErrNotFound
}
func (factsStore) GetCIEvent(context.Context, string) (*models.CIEvent, error) {
	return nil, store.ErrNotFound
}
func (factsStore) GetPrincipal(context.Context, string) (*models.Principal, error) {
	return nil, store.ErrNotFound
}

func TestWriterSignsAndStoresVerifiableProvenance(t *testing.T) {
	ctx := context.Background()
	keys := &memKeys{}
	signer := NewSigner(keys, plainCrypto{}, "https://ci.example/api/v1/oidc")
	if signer == nil {
		t.Fatal("signer must be on")
	}
	service := &artifacts.Service{Store: artifactstest.NewMemoryStore(), Objects: objects.NewMemoryObjectStore(), Limits: artifacts.DefaultLimits}
	writer := &Writer{Signer: signer, Facts: factsStore{}, Artifacts: service}
	project := "p-1"
	job := &models.Job{JobID: "job-9", OrgID: "org-1", ProjectID: &project, Name: "build", Status: "completed", WorkerClass: "default"}
	if _, err := service.AppendChunk(ctx, job, "", artifacts.ChunkRequest{Path: "app.bin", Data: []byte("binary"), Final: true}); err != nil {
		t.Fatal(err)
	}
	list, _ := service.Store.ListJobArtifacts(ctx, job.JobID)
	record, err := writer.Write(ctx, job, list, []byte(`{"provenance_subjects":[{"name":"img","digest":{"sha256":"`+strings.Repeat("e", 64)+`"}}]}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if record.Kind != models.ArtifactKindProvenance || record.Path != ArtifactPath || record.SHA256 == "" {
		t.Fatalf("record %+v", record)
	}
	data, err := service.ReadRange(ctx, record, 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := ParseEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := signer.JWKS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	payload, kid, err := Verify(envelope, jwks)
	if err != nil || kid == "" {
		t.Fatalf("verify %v", err)
	}
	statement, err := ParseStatement(envelope.PayloadType, payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(statement.Subject) != 2 || statement.Predicate.BuildDefinition.InternalParameters.Organization != "acme" ||
		statement.Predicate.BuildDefinition.ExternalParameters.Event.Type != "api" {
		t.Fatalf("statement %+v", statement)
	}
	// Only completed jobs get provenance.
	failed := *job
	failed.Status = "failed"
	if _, err := writer.Write(ctx, &failed, list, nil, ""); err == nil {
		t.Fatal("a failed job must get no provenance")
	}
	// No builder ID: provenance is off.
	if NewSigner(keys, plainCrypto{}, " ") != nil {
		t.Fatal("no builder ID must turn provenance off")
	}
}
