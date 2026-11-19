package cmd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-jose/go-jose/v4"
	"github.com/urfave/cli/v2"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/provenance"
)

func signedEnvelope(t *testing.T, classes []string, origin string) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	statement := provenance.Statement{Type: provenance.StatementType, PredicateType: provenance.PredicateType,
		Subject: []provenance.Subject{
			{Name: "app.bin", Digest: map[string]string{"sha256": strings.Repeat("a", 64)},
				Annotations: map[string]string{provenance.SubjectSourceAnnotation: provenance.SubjectCoordinatorHashed}},
			{Name: "image", Digest: map[string]string{"sha256": strings.Repeat("c", 64)},
				Annotations: map[string]string{provenance.SubjectSourceAnnotation: provenance.SubjectDeclared}},
		}}
	statement.Predicate.BuildDefinition.InternalParameters.Organization = "acme"
	statement.Predicate.BuildDefinition.InternalParameters.Project = "app"
	statement.Predicate.BuildDefinition.InternalParameters.ProjectID = "p-1"
	statement.Predicate.BuildDefinition.InternalParameters.Repository = "acme/app"
	statement.Predicate.BuildDefinition.ExternalParameters.Workflow.SecurityID = "release"
	statement.Predicate.BuildDefinition.InternalParameters.RefClasses = classes
	statement.Predicate.BuildDefinition.InternalParameters.CIOrigin = origin
	statement.Predicate.RunDetails.Builder.ID = "https://ci.example"
	payload, _ := json.Marshal(statement)
	envelope, err := provenance.Sign(provenance.PayloadType, payload, "kid-1", key)
	if err != nil {
		t.Fatal(err)
	}
	envelopeJSON, _ := json.Marshal(envelope)
	jwks, _ := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: "kid-1", Algorithm: "ES256", Use: "sig"}}})
	return envelopeJSON, jwks
}

func runProvenanceCLI(args ...string) error {
	app := cli.NewApp()
	app.Commands = []*cli.Command{ProvenanceCommand}
	return app.Run(NormalizeArgs(app, append([]string{"reactorcide", "provenance"}, args...)))
}

func TestProvenanceVerifyCLI(t *testing.T) {
	dir := t.TempDir()
	envelope, jwks := signedEnvelope(t, []string{"default", "production"}, "base")
	envPath, keysPath := filepath.Join(dir, "p.json"), filepath.Join(dir, "keys.json")
	_ = os.WriteFile(envPath, envelope, 0o644)
	_ = os.WriteFile(keysPath, jwks, 0o644)

	if err := runProvenanceCLI("verify", envPath, "--keys", keysPath, "--require-ref-class", "production", "--require-ci-origin", "base",
		"--require-repository", "acme/app", "--subject", "sha256:"+strings.Repeat("a", 64), "--format", "json"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// The safe admission recipe: project, workflow, ref class, and a
	// coordinator-hashed subject.
	if err := runProvenanceCLI("verify", envPath, "--keys", keysPath, "--require-project", "p-1", "--require-workflow", "release",
		"--require-ref-class", "production", "--require-coordinator-hashed", "--subject", "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatalf("verify recipe: %v", err)
	}
	// --subject alone is refused: any project can declare a digest.
	if err := runProvenanceCLI("verify", envPath, "--keys", keysPath, "--subject", "sha256:"+strings.Repeat("a", 64)); err == nil ||
		!strings.Contains(err.Error(), "--require-repository") {
		t.Fatalf("a subject with no scope must fail: %v", err)
	}
	if err := runProvenanceCLI("verify", envPath, "--keys", keysPath, "--allow-any-repository", "--subject", "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatalf("--allow-any-repository: %v", err)
	}
	for name, args := range map[string][]string{
		"other repository": {"--require-repository", "evil/app", "--subject", "sha256:" + strings.Repeat("a", 64)},
		"other org":        {"--require-org", "evil"},
		"other workflow":   {"--require-workflow", "build"},
		"declared digest":  {"--require-project", "p-1", "--require-coordinator-hashed", "--subject", "sha256:" + strings.Repeat("c", 64)},
	} {
		if err := runProvenanceCLI(append([]string{"verify", envPath, "--keys", keysPath}, args...)...); err == nil {
			t.Fatalf("%s must fail", name)
		}
	}
	if err := runProvenanceCLI("verify", envPath, "--keys", keysPath, "--require-ref-class", "release"); err == nil || !strings.Contains(err.Error(), "requirement") {
		t.Fatalf("a missing ref class must fail: %v", err)
	}
	if err := runProvenanceCLI("verify", envPath, "--keys", keysPath, "--require-ci-origin", "head"); err == nil {
		t.Fatal("a wrong CI origin must fail")
	}
	_, otherKeys := signedEnvelope(t, nil, "")
	otherPath := filepath.Join(dir, "other.json")
	_ = os.WriteFile(otherPath, otherKeys, 0o644)
	if err := runProvenanceCLI("verify", envPath, "--keys", otherPath); err == nil {
		t.Fatal("another key must fail")
	}
}

func TestProvenanceVerifyCLIFromCoordinator(t *testing.T) {
	envelope, jwks := signedEnvelope(t, []string{"production"}, "base")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/provenance/keys":
			if r.Header.Get("Authorization") != "" {
				t.Error("the keys route needs no token")
			}
			_, _ = w.Write(jwks)
		case "/api/v1/jobs/job-1/provenance":
			if r.Header.Get("Authorization") != "Bearer fake-test-token-not-real" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write(envelope)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	if err := runProvenanceCLI("verify", "--job", "job-1", "--require-ref-class", "production",
		"--api-url", server.URL, "--token", "fake-test-token-not-real", "--allow-insecure-transport"); err != nil {
		t.Fatalf("verify --job: %v", err)
	}
}

func TestTestsQuarantineCLIRequests(t *testing.T) {
	var rec requiredSetRecorded
	server := httptest.NewServer(requiredSetRecorder(&rec, http.StatusCreated, map[string]any{"quarantine_id": "q1", "test_id": "t1"}))
	defer server.Close()
	app := cli.NewApp()
	app.Commands = []*cli.Command{TestsCommand}
	args := []string{"reactorcide", "tests", "quarantine", "add", "--project", "p1", "--suite", "unit", "--class", "pkg", "--name", "t",
		"--reason", "flaky on arm", "--for", "7d", "--owner", "owner-1",
		"--api-url", server.URL, "--token", "fake-test-token-not-real", "--allow-insecure-transport"}
	if err := app.Run(NormalizeArgs(app, args)); err != nil {
		t.Fatal(err)
	}
	if rec.path != "POST /api/v1/projects/p1/test-quarantines" || rec.body["duration"] != "7d" || rec.body["owner_principal_id"] != "owner-1" || rec.body["classname"] != "pkg" {
		t.Fatalf("request %s %v", rec.path, rec.body)
	}
}
