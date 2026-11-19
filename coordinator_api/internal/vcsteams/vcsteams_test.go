package vcsteams

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/lease"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

type fakeStore struct {
	store.GroupVCSStore
	mu       sync.Mutex
	links    map[string]*models.GroupVCSLink
	applied  map[string][]models.GroupVCSTeamMember
	failures map[string]bool // link -> clearMembers of the last failure
	nextAt   map[string]time.Time
}

func newFakeStore(links ...*models.GroupVCSLink) *fakeStore {
	f := &fakeStore{links: map[string]*models.GroupVCSLink{}, applied: map[string][]models.GroupVCSTeamMember{},
		failures: map[string]bool{}, nextAt: map[string]time.Time{}}
	for _, l := range links {
		f.links[l.LinkID] = l
	}
	return f
}

func (f *fakeStore) GetGroupByID(ctx context.Context, id string) (*models.Group, error) {
	return &models.Group{GroupID: id, OrgID: "org"}, nil
}
func (f *fakeStore) GetGroupVCSLink(ctx context.Context, id string) (*models.GroupVCSLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l, ok := f.links[id]; ok {
		return l, nil
	}
	return nil, store.ErrNotFound
}
func (f *fakeStore) ListDueGroupVCSLinks(ctx context.Context, now time.Time, limit int) ([]models.GroupVCSLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []models.GroupVCSLink{}
	for _, l := range f.links {
		if next, ok := f.nextAt[l.LinkID]; !ok || !next.After(now) {
			out = append(out, *l)
		}
	}
	return out, nil
}
func (f *fakeStore) ApplyGroupVCSSync(ctx context.Context, id string, members []models.GroupVCSTeamMember, teamID *int64, at, next time.Time) (*models.GroupVCSSyncResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied[id] = members
	f.nextAt[id] = next
	return &models.GroupVCSSyncResult{Members: len(members)}, nil
}
func (f *fakeStore) RecordGroupVCSSyncFailure(ctx context.Context, id, msg string, at, next time.Time, clear bool) (*models.GroupVCSSyncResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[id] = clear
	f.nextAt[id] = next
	return &models.GroupVCSSyncResult{}, nil
}

type fakeLister struct {
	logins []string
	err    error
	calls  atomic.Int32
}

func (l *fakeLister) GetTeam(ctx context.Context, owner, slug string) (*vcs.Team, error) {
	l.calls.Add(1)
	if l.err != nil {
		return nil, l.err
	}
	return &vcs.Team{ID: 1, Slug: slug}, nil
}
func (l *fakeLister) ListTeamMembers(ctx context.Context, owner, slug string) ([]string, error) {
	return l.logins, l.err
}

type fakePrincipals struct{ disabled map[string]bool }

func (p fakePrincipals) ResolveOrCreateVCS(ctx context.Context, provider, login string) (*principals.Resolution, error) {
	return &principals.Resolution{Principal: models.Principal{PrincipalID: "p-" + login}, Inactive: p.disabled[login]}, nil
}

func newSyncer(st *fakeStore, lister *fakeLister, now time.Time) *Syncer {
	return &Syncer{Store: st, Principals: fakePrincipals{disabled: map[string]bool{"off": true}}, Now: func() time.Time { return now },
		Clients: func(ctx context.Context, orgID, provider, owner string) (vcs.TeamMemberLister, error) {
			return lister, nil
		}}
}

func TestSyncLinkSuccess(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	link := &models.GroupVCSLink{LinkID: "l1", GroupID: "g", Provider: "github", Owner: "acme", TeamSlug: "platform"}
	st := newFakeStore(link)
	outcome := newSyncer(st, &fakeLister{logins: []string{"alice", "off", "bob"}}, now).SyncLink(context.Background(), link)
	if outcome.Error != "" || len(st.applied["l1"]) != 2 {
		t.Fatalf("outcome %+v applied %+v", outcome, st.applied["l1"])
	}
	if !st.nextAt["l1"].Equal(now.Add(DefaultInterval)) {
		t.Fatalf("next = %v", st.nextAt["l1"])
	}
}

func TestSyncLinkFailurePlan(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Hour)
	old := now.Add(-48 * time.Hour)
	reset := now.Add(30 * time.Minute)
	cases := []struct {
		name      string
		err       error
		synced    *time.Time
		wantClear bool
		wantNext  time.Time
	}{
		{"team gone clears at once", vcs.ErrTeamNotFound, &recent, true, now.Add(time.Minute)},
		{"transient keeps members", errors.New("boom"), &recent, false, now.Add(time.Minute)},
		{"stale members go", errors.New("boom"), &old, true, now.Add(time.Minute)},
		{"rate limit waits for the reset", &vcs.RateLimitError{Status: 403, Reset: reset}, &recent, false, reset.Add(5 * time.Second)},
	}
	for _, c := range cases {
		link := &models.GroupVCSLink{LinkID: "l", GroupID: "g", Provider: "github", Owner: "acme", TeamSlug: "t", LastSyncedAt: c.synced}
		st := newFakeStore(link)
		outcome := newSyncer(st, &fakeLister{err: c.err}, now).SyncLink(context.Background(), link)
		if outcome.Error == "" || st.failures["l"] != c.wantClear || !st.nextAt["l"].Equal(c.wantNext) {
			t.Errorf("%s: clear=%v next=%v outcome=%+v", c.name, st.failures["l"], st.nextAt["l"], outcome)
		}
	}
}

func TestBackoff(t *testing.T) {
	if Backoff(1, 15*time.Minute) != time.Minute || Backoff(3, 15*time.Minute) != 4*time.Minute {
		t.Fatal("doubling")
	}
	if Backoff(20, 15*time.Minute) != time.Hour {
		t.Fatal("capped at 4 * interval")
	}
	if Backoff(30, 24*time.Hour) != MaxBackoff {
		t.Fatal("capped at MaxBackoff")
	}
}

func TestNormalizeTeam(t *testing.T) {
	for in, want := range map[string]string{"Acme/Platform": "acme/platform", "vcs_team:acme/a.b_c": "acme/a.b_c"} {
		o, s, ok := NormalizeTeam(in)
		if !ok || o+"/"+s != want {
			t.Errorf("%s -> %s/%s %v", in, o, s, ok)
		}
	}
	for _, bad := range []string{"acme", "a/b/c", "a/", "a b/c", "../x", "acme/..", "-a/b"} {
		if _, _, ok := NormalizeTeam(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if RepoOwner("github.com/Acme/app") != "acme" || RepoOwner("x") != "" {
		t.Fatal("RepoOwner")
	}
}

// fakeLeases is one lease table for two replicas.
type fakeLeases struct {
	mu     sync.Mutex
	holder string
}

func (l *fakeLeases) TryAcquireLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder == "" || l.holder == holder {
		l.holder = holder
		return true, nil
	}
	return false, nil
}
func (l *fakeLeases) ReleaseLease(ctx context.Context, name, holder string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder == holder {
		l.holder = ""
	}
	return nil
}

// Two replicas run the loop under one lease: only the holder syncs.
func TestSyncLoopRunsOnlyUnderTheLease(t *testing.T) {
	leases := &fakeLeases{}
	link := &models.GroupVCSLink{LinkID: "l1", GroupID: "g", Provider: "github", Owner: "acme", TeamSlug: "platform"}
	listers := []*fakeLister{{logins: []string{"a"}}, {logins: []string{"a"}}}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i, name := range []string{"replica-a", "replica-b"} {
		st := newFakeStore(link)
		syncer := newSyncer(st, listers[i], time.Now())
		syncer.Now = nil
		syncer.Poll = 5 * time.Millisecond
		syncer.Interval = time.Millisecond // due again almost at once
		manager := &lease.Manager{Store: leases, Holder: name, TTL: time.Second, RenewEvery: 10 * time.Millisecond}
		wg.Add(1)
		go func() { defer wg.Done(); manager.Run(ctx, LeaseName, syncer.Run) }()
	}
	time.Sleep(150 * time.Millisecond)
	cancel()
	wg.Wait()
	a, b := listers[0].calls.Load(), listers[1].calls.Load()
	if (a == 0) == (b == 0) {
		t.Fatalf("exactly one replica must sync: a=%d b=%d", a, b)
	}
}
