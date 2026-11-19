package outboundevents

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr(s string) *string { return &s }

func baseEvent() *models.OutboxEventFacts {
	return &models.OutboxEventFacts{EventOutbox: models.EventOutbox{
		OutboxID: 7, EventID: "0193a6f0-0000-7000-8000-000000000001", EventType: models.OutboundEventNodePostComplete,
		OccurredAt: time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC), OrgID: "org-1", ProjectID: ptr("proj-1"),
		WorkflowID: ptr("wf-1"), NodeID: ptr("node-1"), JobID: ptr("job-1"), Status: "failed", Result: "failed",
		FailureClass: "job", RefClasses: pq.StringArray{"default_branch", "protected"}, OrderKey: "workflow:wf-1",
		CommitSHA: "abc123", Details: models.JSONB{"attempt": float64(2), "exit_code": float64(1), "job_name": "build"},
	}, WorkflowSecID: "sec-build", ActorKind: "human", OrgName: "acme", ProjectName: "app", WorkflowName: "ci",
		NodeName: "build", JobName: "build", EventRef: "refs/heads/main"}
}

func baseSub() *models.EventSubscription {
	return &models.EventSubscription{SubscriptionID: "sub-1", OrgID: "org-1", Scope: models.SubscriptionScopeOrg,
		Enabled: true, OwnerActive: true}
}

func TestMatchesScopes(t *testing.T) {
	ev := baseEvent()
	sub := baseSub()
	assert.True(t, Matches(sub, ev))

	other := baseSub()
	other.OrgID = "org-2"
	assert.False(t, Matches(other, ev), "another org")

	disabled := baseSub()
	disabled.Enabled = false
	assert.False(t, Matches(disabled, ev))
	ownerOff := baseSub()
	ownerOff.OwnerActive = false
	assert.False(t, Matches(ownerOff, ev), "a disabled owner receives nothing")

	project := baseSub()
	project.Scope, project.ProjectID = models.SubscriptionScopeProject, ptr("proj-1")
	assert.True(t, Matches(project, ev))
	project.ProjectID = ptr("proj-2")
	assert.False(t, Matches(project, ev))

	wf := baseSub()
	wf.Scope, wf.ProjectID, wf.WorkflowSecurityID = models.SubscriptionScopeWorkflow, ptr("proj-1"), "sec-build"
	assert.True(t, Matches(wf, ev))
	wf.WorkflowSecurityID = "sec-other"
	assert.False(t, Matches(wf, ev))

	group := baseSub()
	group.Scope, group.OwnerGroup = models.SubscriptionScopeOwnerGroup, "platform"
	assert.False(t, Matches(group, ev), "no owner group on the event until WP7")
	withGroup := baseEvent()
	withGroup.OwnerGroup = "platform"
	assert.True(t, Matches(group, withGroup))
}

func TestMatchesFilters(t *testing.T) {
	ev := baseEvent()
	sub := baseSub()
	sub.EventTypes = pq.StringArray{models.OutboundEventNodePreStart}
	assert.False(t, Matches(sub, ev))
	sub.EventTypes = pq.StringArray{models.OutboundEventNodePreStart, models.OutboundEventNodePostComplete}
	assert.True(t, Matches(sub, ev))

	sub.RefClassesAny = pq.StringArray{"release"}
	assert.False(t, Matches(sub, ev))
	sub.RefClassesAny = pq.StringArray{"release", "protected"}
	assert.True(t, Matches(sub, ev))

	sub.Results = pq.StringArray{"completed"}
	assert.False(t, Matches(sub, ev))
	sub.Results = pq.StringArray{"failed", "timeout"}
	assert.True(t, Matches(sub, ev))
	started := baseEvent()
	started.EventType, started.Result = models.OutboundEventNodeStarted, ""
	sub.EventTypes = nil
	assert.False(t, Matches(sub, started), "a results filter does not match an event with no result")

	sub.ActorKinds = pq.StringArray{"service"}
	assert.False(t, Matches(sub, ev))
	sub.ActorKinds = pq.StringArray{"service", "human"}
	assert.True(t, Matches(sub, ev))
}

func TestBuildPayload(t *testing.T) {
	p := BuildPayload(*baseEvent(), 5, "https://ci.example.com/")
	assert.Equal(t, int64(5), p.Sequence)
	assert.Equal(t, SchemaVersion, p.SchemaVersion)
	assert.Equal(t, "2026-11-16T10:00:00Z", p.OccurredAt)
	assert.Equal(t, "refs/heads/main", p.Ref, "the event ref fills a missing row ref")
	assert.Equal(t, "abc123", p.SHA)
	require.NotNil(t, p.Workflow)
	assert.Equal(t, "sec-build", p.Workflow.SecurityID)
	require.NotNil(t, p.Job)
	assert.Equal(t, 2, p.Job.Attempt)
	assert.Equal(t, map[string]any{"exit_code": float64(1)}, p.Details, "names and attempt move out of details")
	assert.Equal(t, "https://ci.example.com/app/workflows/wf-1", p.Links.UI)
	assert.Equal(t, []string{"default_branch", "protected"}, p.RefClasses)

	stored := p.ToJSONB()
	assert.Equal(t, "node.post_complete", stored["type"])
	assert.Equal(t, "", stored["owner_group"], "the owner group field is present while empty")

	loose := baseEvent()
	loose.WorkflowID, loose.NodeID, loose.WorkflowSecID, loose.NodeName = nil, nil, "", ""
	assert.Equal(t, "https://ci.example.com/app/jobs/job-1", BuildPayload(*loose, 1, "https://ci.example.com").Links.UI)
	assert.Equal(t, "", BuildPayload(*loose, 1, "").Links.UI)
}

func TestSignatureRoundTrip(t *testing.T) {
	secret := []byte("rcwh_test-secret-value")
	body := []byte(`{"id":"x"}`)
	sig := Sign(secret, "1700000000", body)
	assert.True(t, strings.HasPrefix(sig, "sha256="))
	assert.True(t, Verify(secret, "1700000000", body, sig))
	assert.False(t, Verify(secret, "1700000001", body, sig), "the timestamp is signed")
	assert.False(t, Verify(secret, "1700000000", []byte(`{"id":"y"}`), sig))
	assert.False(t, Verify([]byte("another"), "1700000000", body, sig))
	// A fixed vector, so a receiver in another language can check its code:
	// HMAC-SHA256("secret", "1700000000.{}").
	assert.Equal(t, "sha256=b8569b78799ff9e3cbff0fc2d63a33a2b57f3282abd07c37ae5e8e7d79a5f163", Sign([]byte("secret"), "1700000000", []byte("{}")))
}

func TestDestinationPolicy(t *testing.T) {
	strict := DestinationPolicy{}
	for _, u := range []string{
		"http://example.com/hook",         // not HTTPS
		"https://127.0.0.1/hook",          // loopback
		"https://[::1]/hook",              // loopback v6
		"https://10.1.2.3/hook",           // private
		"https://192.168.1.1/hook",        // private
		"https://172.16.0.1/hook",         // private
		"https://169.254.169.254/latest",  // link-local (cloud metadata)
		"https://[fe80::1]/hook",          // link-local v6
		"https://100.64.0.1/hook",         // CGNAT
		"https://0.0.0.0/hook",            // unspecified
		"https://[::ffff:127.0.0.1]/hook", // mapped loopback
		"https://localhost/hook",          // loopback name
		"https://user:pass@example.com/h", // credentials in the URL
		"ftp://example.com/hook",          // scheme
		"",                                // empty
	} {
		assert.Error(t, strict.ValidateURL(u), u)
	}
	assert.NoError(t, strict.ValidateURL("https://hooks.example.com/reactorcide?team=1"))
	assert.NoError(t, strict.ValidateURL("https://93.184.216.34/hook"))

	dev := DestinationPolicy{AllowInsecure: true, AllowedNetworks: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	assert.NoError(t, dev.ValidateURL("http://127.0.0.1:8080/hook"))
	assert.Error(t, dev.ValidateURL("http://10.0.0.1/hook"), "only the allowed network")

	nets, err := ParseAllowedNetworks("10.0.0.0/8, 192.168.1.5 ,fd00::/8")
	require.NoError(t, err)
	assert.Len(t, nets, 3)
	_, err = ParseAllowedNetworks("not-a-network")
	assert.Error(t, err)
}

func TestWebhookSinkRefusesPrivateAddressAtConnect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the request must not reach a loopback server")
	}))
	defer server.Close()
	// The host name passes ValidateURL (it is not an IP literal), but every
	// address of it is loopback: the dialer refuses at connect time. This is
	// the DNS rebinding case.
	url := strings.Replace(server.URL, "127.0.0.1", "loopback.reactorcide.test", 1)
	sink := &WebhookSink{Policy: DestinationPolicy{AllowInsecure: true}}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			_, port, _ := strings.Cut(address, ":")
			return sink.Policy.dialContext(ctx, network, "127.0.0.1:"+port)
		},
	}
	sink.Transport = transport
	out := sink.Send(context.Background(), Message{EventID: "e", EventType: "t", Body: []byte("{}"), Secret: []byte("s"),
		Config: models.JSONB{"url": url}})
	require.Error(t, out.Err)
	assert.True(t, out.Refused, "a refused destination is final: %v", out.Err)
	assert.True(t, errors.Is(out.Err, ErrDestinationRefused))
}

func TestWebhookSinkSendsSignedRequest(t *testing.T) {
	var got *http.Request
	var body []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	sink := &WebhookSink{
		Policy:    DestinationPolicy{AllowedNetworks: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}},
		Now:       func() time.Time { return time.Unix(1700000000, 0) },
		Transport: server.Client().Transport,
	}
	out := sink.Send(context.Background(), Message{EventID: "evt-1", EventType: "node.started", DeliveryID: "del-1",
		SchemaVersion: 1, Body: []byte(`{"id":"evt-1"}`), Secret: []byte("secret-123"), Config: models.JSONB{"url": server.URL + "/hook"}})
	require.NoError(t, out.Err)
	assert.True(t, out.Delivered())
	assert.Equal(t, http.StatusAccepted, out.StatusCode)
	require.NotNil(t, got)
	assert.Equal(t, "evt-1", got.Header.Get(HeaderEventID))
	assert.Equal(t, "node.started", got.Header.Get(HeaderEventType))
	assert.Equal(t, "1", got.Header.Get(HeaderSchemaVersion))
	assert.Equal(t, "1700000000", got.Header.Get(HeaderTimestamp))
	assert.Equal(t, "del-1", got.Header.Get(HeaderDeliveryID))
	assert.True(t, Verify([]byte("secret-123"), "1700000000", body, got.Header.Get(HeaderSignature)))

	// A redirect is not followed and is a failed delivery.
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/", http.StatusFound)
	}))
	defer redirect.Close()
	sink.Transport = redirect.Client().Transport
	out = sink.Send(context.Background(), Message{Body: []byte("{}"), Secret: []byte("s"), Config: models.JSONB{"url": redirect.URL}})
	assert.False(t, out.Delivered())
	assert.Equal(t, http.StatusFound, out.StatusCode)
	assert.False(t, out.Refused)
}

func TestWebhookValidateConfig(t *testing.T) {
	sink := &WebhookSink{}
	assert.NoError(t, sink.Validate(models.JSONB{"url": "https://hooks.example.com/x"}))
	assert.Error(t, sink.Validate(models.JSONB{"url": "https://hooks.example.com/x", "headers": "x"}))
	assert.Error(t, sink.Validate(models.JSONB{}))
}

func TestBackoffDelay(t *testing.T) {
	b := Backoff{Base: 10 * time.Second, Max: time.Minute, Jitter: 0.2, Rand: func() float64 { return 0.5 }}
	assert.Equal(t, 10*time.Second, b.Delay(1))
	assert.Equal(t, 20*time.Second, b.Delay(2))
	assert.Equal(t, 40*time.Second, b.Delay(3))
	assert.Equal(t, time.Minute, b.Delay(10), "the delay stops at Max")
	low := Backoff{Base: 10 * time.Second, Max: time.Minute, Jitter: 0.2, Rand: func() float64 { return 0 }}
	assert.Equal(t, 8*time.Second, low.Delay(1), "jitter is +-20%")
	high := Backoff{Base: 10 * time.Second, Max: time.Minute, Jitter: 0.2, Rand: func() float64 { return 0.999999 }}
	assert.InDelta(t, float64(12*time.Second), float64(high.Delay(1)), float64(time.Millisecond))
}

// The sink builds its HTTP client and transport once, so connections are
// reused across deliveries.
func TestWebhookSinkBuildsClientOnce(t *testing.T) {
	sink := &WebhookSink{Policy: DestinationPolicy{AllowInsecure: true}}
	first := sink.client()
	if first == nil || first.Transport == nil {
		t.Fatal("no client")
	}
	if second := sink.client(); second != first || second.Transport != first.Transport {
		t.Fatal("the client and transport must be built once per sink")
	}
}
