package provenance

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/oidcissuer"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func sampleFacts() oidcissuer.JobFacts {
	return oidcissuer.JobFacts{Org: "acme", OrgID: "org-1", Project: "app", ProjectID: "p-1", Repository: "acme/app",
		WorkflowSecurityID: "deploy", WorkflowName: "Deploy", WorkflowID: "wf-1", Node: "release", JobID: "job-1",
		Ref: "refs/heads/main", SHA: "abc123", EventType: "push", EventID: "ev-1", RefClasses: []string{"default", "production"},
		ActorPrincipalID: "pr-1", ActorKind: "human", CIOrigin: "base", ExecutionProfile: "trusted", PolicyRevision: "rev-7"}
}

func sampleJob() *models.Job {
	src, ci := "https://git.example/acme/app.git", "https://git.example/acme/app.git"
	started, finished := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 16, 10, 5, 0, 0, time.UTC)
	approval := "ap-1"
	return &models.Job{JobID: "job-1", Name: "release", Status: "completed", SourceURL: &src, CISourceURL: &ci, CISHA: "def456",
		PolicyRuleID: "rule-main", ApprovalID: &approval, WorkerClass: "trusted-pool", StartedAt: &started, CompletedAt: &finished}
}

func TestBuildStatementShape(t *testing.T) {
	list := []models.JobArtifact{
		{Path: "dist/app.tar.gz", Kind: models.ArtifactKindFile, Status: models.ArtifactStatusComplete, SHA256: strings.Repeat("a", 64)},
		{Path: "junit.xml", Kind: models.ArtifactKindTestReport, Status: models.ArtifactStatusComplete, SHA256: strings.Repeat("b", 64)},
		{Path: "partial", Kind: models.ArtifactKindFile, Status: models.ArtifactStatusFailed},
	}
	output := []byte(`{"provenance_subjects": [
		{"name": "registry.example/acme/app", "digest": {"sha256": "sha256:` + strings.Repeat("C", 64) + `"}},
		{"name": "bad", "digest": {"sha256": "nothex"}},
		{"name": "", "digest": {"sha256": "` + strings.Repeat("d", 64) + `"}}
	]}`)
	statement, dropped, err := Build(sampleFacts(), sampleJob(), "https://ci.example/api/v1/oidc", list, output)
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 2 {
		t.Fatalf("dropped %v", dropped)
	}
	if statement.Type != StatementType || statement.PredicateType != PredicateType {
		t.Fatalf("types %s %s", statement.Type, statement.PredicateType)
	}
	if len(statement.Subject) != 2 || statement.Subject[0].Name != "dist/app.tar.gz" || statement.Subject[1].Digest["sha256"] != strings.Repeat("c", 64) {
		t.Fatalf("subjects %+v", statement.Subject)
	}
	def := statement.Predicate.BuildDefinition
	if def.BuildType != BuildTypeJob || def.ExternalParameters.Workflow.SecurityID != "deploy" || def.ExternalParameters.Node != "release" ||
		def.ExternalParameters.Ref != "refs/heads/main" || def.ExternalParameters.Event.Type != "push" {
		t.Fatalf("external %+v", def.ExternalParameters)
	}
	in := def.InternalParameters
	if in.CIOrigin != "base" || in.ExecutionProfile != "trusted" || in.PolicyRevision != "rev-7" || in.PolicyRule != "rule-main" ||
		in.ApprovalID != "ap-1" || in.WorkerClass != "trusted-pool" || in.ActorPrincipalID != "pr-1" || strings.Join(in.RefClasses, ",") != "default,production" {
		t.Fatalf("internal %+v", in)
	}
	if len(def.ResolvedDependencies) != 2 || def.ResolvedDependencies[0].Digest["gitCommit"] != "abc123" ||
		def.ResolvedDependencies[1].Digest["gitCommit"] != "def456" || !strings.HasPrefix(def.ResolvedDependencies[1].URI, "git+https://") {
		t.Fatalf("deps %+v", def.ResolvedDependencies)
	}
	run := statement.Predicate.RunDetails
	if run.Builder.ID != "https://ci.example/api/v1/oidc" || run.Metadata.InvocationID != "job-1" || run.Metadata.StartedOn != "2026-11-16T10:00:00Z" {
		t.Fatalf("run %+v", run)
	}
	raw, _ := json.Marshal(statement)
	for _, key := range []string{`"_type"`, `"predicateType"`, `"buildDefinition"`, `"externalParameters"`, `"internalParameters"`, `"resolvedDependencies"`, `"runDetails"`, `"invocationId"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("JSON lacks %s", key)
		}
	}
	if _, _, err := Build(sampleFacts(), sampleJob(), "b", nil, nil); !errors.Is(err, ErrNoSubjects) {
		t.Fatalf("no subjects: %v", err)
	}
}

func keyPair(t *testing.T, kid string) (*ecdsa.PrivateKey, *jose.JSONWebKeySet) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key, &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: kid, Algorithm: "ES256", Use: "sig"}}}
}

func TestDSSESignVerifyRoundTrip(t *testing.T) {
	key, keys := keyPair(t, "k1")
	statement, _, err := Build(sampleFacts(), sampleJob(), "https://ci.example", []models.JobArtifact{
		{Path: "a", Kind: models.ArtifactKindFile, Status: models.ArtifactStatusComplete, SHA256: strings.Repeat("a", 64)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(statement)
	envelope, err := Sign(PayloadType, payload, "k1", key)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(envelope)
	parsed, err := ParseEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	got, kid, err := Verify(parsed, keys)
	if err != nil || kid != "k1" {
		t.Fatalf("verify: %v %s", err, kid)
	}
	back, err := ParseStatement(parsed.PayloadType, got)
	if err != nil || back.Subject[0].Name != "a" {
		t.Fatalf("statement: %v", err)
	}

	// A changed payload does not verify.
	tampered := *parsed
	tampered.Payload = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(got), `"production"`, `"pr"`, 1)))
	if _, _, err := Verify(&tampered, keys); !errors.Is(err, ErrNoSubjects) && !errors.Is(err, ErrNoValidSignature) {
		t.Fatalf("tampered: %v", err)
	}
	// A changed payload type does not verify (PAE binds it).
	retyped := *parsed
	retyped.PayloadType = "application/json"
	if _, _, err := Verify(&retyped, keys); !errors.Is(err, ErrNoValidSignature) {
		t.Fatalf("retyped: %v", err)
	}
	// Another key does not verify.
	_, other := keyPair(t, "k1")
	if _, _, err := Verify(parsed, other); !errors.Is(err, ErrNoValidSignature) {
		t.Fatalf("other key: %v", err)
	}
	// An unknown key ID does not count.
	_, unknown := keyPair(t, "k2")
	unknown.Keys[0].Key = key.Public()
	if _, _, err := Verify(parsed, unknown); !errors.Is(err, ErrNoValidSignature) {
		t.Fatalf("unknown kid: %v", err)
	}
}

func TestPAE(t *testing.T) {
	if got := string(PAE("http://example.com/HelloWorld", []byte("hello world"))); got != "DSSEv1 29 http://example.com/HelloWorld 11 hello world" {
		t.Fatalf("PAE %q", got)
	}
}

func TestCheckRequirements(t *testing.T) {
	declared := []byte(`{"provenance_subjects": [{"name": "registry.example/acme/app", "digest": {"sha256": "` + strings.Repeat("c", 64) + `"}}]}`)
	statement, _, _ := Build(sampleFacts(), sampleJob(), "https://ci.example", []models.JobArtifact{
		{Path: "a", Kind: models.ArtifactKindFile, Status: models.ArtifactStatusComplete, SHA256: strings.Repeat("a", 64)}}, declared)
	hashed, declaredDigest := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("c", 64)
	if err := Check(statement, Requirements{RefClasses: []string{"production"}, CIOrigin: "base", BuilderID: "https://ci.example",
		Repository: "https://github.com/Acme/App.git", SubjectSHA256: hashed}); err == nil {
		t.Fatal("the repository acme/app is not github.com/acme/app")
	}
	good := []Requirements{
		{RefClasses: []string{"production"}, CIOrigin: "base", BuilderID: "https://ci.example", Repository: "Acme/App", SubjectSHA256: hashed},
		{Project: "p-1", SubjectSHA256: hashed, CoordinatorHashed: true},
		{Project: "app", Organization: "acme", WorkflowSecurityID: "deploy", SubjectSHA256: declaredDigest},
		{SubjectSHA256: hashed, AllowAnyRepository: true},
		{Repository: "acme/app", SubjectSHA256: declaredDigest},
	}
	for _, req := range good {
		if err := Check(statement, req); err != nil {
			t.Fatalf("%+v: %v", req, err)
		}
	}
	for name, req := range map[string]Requirements{
		"ref class":              {RefClasses: []string{"release"}},
		"ci origin":              {CIOrigin: "head"},
		"builder":                {BuilderID: "x"},
		"unknown subject":        {SubjectSHA256: strings.Repeat("f", 64), AllowAnyRepository: true},
		"subject with no scope":  {SubjectSHA256: hashed},
		"other repository":       {Repository: "evil/app", SubjectSHA256: hashed},
		"other project":          {Project: "p-2", SubjectSHA256: hashed},
		"project name, no org":   {Project: "app", SubjectSHA256: hashed},
		"other org":              {Project: "app", Organization: "evil", SubjectSHA256: hashed},
		"other workflow":         {Project: "p-1", WorkflowSecurityID: "build", SubjectSHA256: hashed},
		"declared, need hashed":  {Project: "p-1", SubjectSHA256: declaredDigest, CoordinatorHashed: true},
		"all hashed, one is not": {Project: "p-1", CoordinatorHashed: true},
	} {
		if err := Check(statement, req); err == nil {
			t.Fatalf("%s: %+v must fail", name, req)
		}
	}
	if err := Check(statement, Requirements{SubjectSHA256: hashed}); !errors.Is(err, ErrSubjectNeedsScope) {
		t.Fatalf("no scope: %v", err)
	}
	for _, subject := range statement.Subject {
		want := SubjectCoordinatorHashed
		if subject.Name == "registry.example/acme/app" {
			want = SubjectDeclared
		}
		if subject.Source() != want {
			t.Fatalf("subject %s source %q, want %q", subject.Name, subject.Source(), want)
		}
	}
	if len(Facts(statement)) < 20 {
		t.Fatal("facts")
	}
}

// A declared subject with the name and digest of an uploaded artifact does
// not replace the coordinator-hashed one.
func TestSortSubjectsKeepsCoordinatorHashed(t *testing.T) {
	digest := strings.Repeat("a", 64)
	declared := []byte(`{"provenance_subjects": [{"name": "a", "digest": {"sha256": "` + digest + `"}}]}`)
	statement, _, err := Build(sampleFacts(), sampleJob(), "b", []models.JobArtifact{
		{Path: "a", Kind: models.ArtifactKindFile, Status: models.ArtifactStatusComplete, SHA256: digest}}, declared)
	if err != nil {
		t.Fatal(err)
	}
	if len(statement.Subject) != 1 || statement.Subject[0].Source() != SubjectCoordinatorHashed {
		t.Fatalf("subjects %+v", statement.Subject)
	}
}
