package vcsreport

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type reportTestStore struct {
	advisory sync.Mutex
	mu       sync.Mutex
	target   models.VCSReportTarget
	entries  map[string]models.VCSReportEntry
	lastErr  error
}

func (s *reportTestStore) ListDirtyVCSReportTargets(context.Context, int) ([]models.VCSReportTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.target.Dirty {
		return nil, nil
	}
	return []models.VCSReportTarget{s.target}, nil
}

func (s *reportTestStore) WithVCSReportTargetLock(ctx context.Context, _ string, fn func(context.Context, *models.VCSReportTarget, []models.VCSReportEntry) error) error {
	s.advisory.Lock()
	defer s.advisory.Unlock()
	s.mu.Lock()
	target := s.target
	rows := make([]models.VCSReportEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		rows = append(rows, entry)
	}
	s.mu.Unlock()
	return fn(ctx, &target, rows)
}

func (s *reportTestStore) SetVCSReportRendered(_ context.Context, _ string, commentID string, revision int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.target.DesiredRevision <= revision {
		s.target.ProviderCommentID = &commentID
		s.target.RenderedRevision = revision
		s.target.Dirty = false
		s.lastErr = nil
	}
	return nil
}

func (s *reportTestStore) RecordVCSReportError(_ context.Context, _ string, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = err
	s.target.Dirty = true
	return nil
}

func (s *reportTestStore) update(entry models.VCSReportEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[entry.EntryKey] = entry
	s.target.DesiredRevision++
	s.target.Dirty = true
}

type reportTestClient struct {
	mu        sync.Mutex
	body      string
	writes    int
	failWrite int
}

func (c *reportTestClient) ReadComment(context.Context, *models.VCSReportTarget) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return "42", c.body, nil
}

func (c *reportTestClient) WriteComment(_ context.Context, _ *models.VCSReportTarget, _ string, body string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failWrite > 0 {
		c.failWrite--
		return "", errors.New("provider unavailable")
	}
	c.body = body
	c.writes++
	return "42", nil
}

type reportTestResolver struct{ client *reportTestClient }

func (r reportTestResolver) ResolveReportClient(context.Context, *models.VCSReportTarget) (CommentClient, error) {
	return r.client, nil
}

func newReportTest() (*reportTestStore, *reportTestClient, *Reconciler) {
	store := &reportTestStore{
		target: models.VCSReportTarget{ReportTargetID: "target", OrgID: "org", CurrentGeneration: 1, GenerationComplete: true, DesiredRevision: 1, Dirty: true},
		entries: map[string]models.VCSReportEntry{
			"a": {ReportTargetID: "target", EntryKey: "a", Generation: 1, Status: "running", StructuredState: models.JSONB{"title": "A", "body": "first"}},
			"b": {ReportTargetID: "target", EntryKey: "b", Generation: 1, Status: "success", StructuredState: models.JSONB{"title": "B", "body": "second"}},
		},
	}
	client := &reportTestClient{}
	return store, client, &Reconciler{Store: store, Clients: reportTestResolver{client: client}}
}

func TestReconcilerRetainsConcurrentSectionsAndIsIdempotent(t *testing.T) {
	store, client, reconciler := newReportTest()
	ctx := context.Background()
	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(client.body, "workflow:a:begin") || !strings.Contains(client.body, "workflow:b:begin") {
		t.Fatalf("the shared report lost a section: %s", client.body)
	}
	store.update(models.VCSReportEntry{ReportTargetID: "target", EntryKey: "a", Generation: 1, Status: "failed", StructuredState: models.JSONB{"title": "A", "body": "changed"}})
	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(client.body, "changed") || !strings.Contains(client.body, "second") {
		t.Fatalf("updating A changed or removed B: %s", client.body)
	}
	writes := client.writes
	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if client.writes != writes {
		t.Fatal("an idempotent pass wrote the provider comment")
	}
}

func TestReconcilerRemovesStaleEntryOnlyAfterGenerationCompletes(t *testing.T) {
	store, client, reconciler := newReportTest()
	store.mu.Lock()
	store.target.CurrentGeneration = 2
	store.target.GenerationComplete = false
	store.entries["new"] = models.VCSReportEntry{ReportTargetID: "target", EntryKey: "new", Generation: 2, Status: "running", StructuredState: models.JSONB{"title": "New"}}
	store.mu.Unlock()
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(client.body, "workflow:a:begin") || !strings.Contains(client.body, "workflow:new:begin") {
		t.Fatal("an incomplete generation removed a stored section")
	}
	store.mu.Lock()
	store.target.GenerationComplete = true
	store.target.DesiredRevision++
	store.target.Dirty = true
	store.mu.Unlock()
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(client.body, "workflow:a:begin") || !strings.Contains(client.body, "workflow:new:begin") {
		t.Fatal("a complete generation did not remove stale sections")
	}
}

func TestReconcilerBacksOffAfterProviderFailure(t *testing.T) {
	store, client, reconciler := newReportTest()
	now := time.Unix(100, 0)
	reconciler.now = func() time.Time { return now }
	client.failWrite = 1
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.lastErr == nil || !store.target.Dirty {
		t.Fatal("a provider failure did not keep the target dirty")
	}
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.writes != 0 {
		t.Fatal("the reconciler ignored its retry delay")
	}
	now = now.Add(baseRetryDelay)
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.writes != 1 || store.target.Dirty {
		t.Fatal("the target did not recover after its retry delay")
	}
}

func TestTwoReconcilersSerializeOneProviderWrite(t *testing.T) {
	_, client, first := newReportTest()
	second := &Reconciler{Store: first.Store, Clients: first.Clients}
	var wg sync.WaitGroup
	wg.Add(2)
	for _, reconciler := range []*Reconciler{first, second} {
		go func(r *Reconciler) {
			defer wg.Done()
			_ = r.ReconcileOnce(context.Background())
		}(reconciler)
	}
	wg.Wait()
	if client.writes != 1 {
		t.Fatalf("two replicas made %d provider writes", client.writes)
	}
}
