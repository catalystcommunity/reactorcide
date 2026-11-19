package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/artifacts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/oidcissuer"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// ArtifactPath is the path of the provenance artifact of a job.
const ArtifactPath = "provenance.intoto.dsse.json"

// MediaType is the media type of the provenance artifact.
const MediaType = "application/vnd.dsse.envelope.v1+json"

// keyStore adapts a store.ProvenanceKeyStore to the oidcissuer key store
// interface, so the provenance keys use the same rotation and encryption
// code as the OIDC keys, on their own table.
type keyStore struct{ store.ProvenanceKeyStore }

func (k keyStore) ListOIDCSigningKeys(ctx context.Context) ([]models.OIDCSigningKey, error) {
	return k.ListProvenanceSigningKeys(ctx)
}
func (k keyStore) CreateOIDCSigningKey(ctx context.Context, key *models.OIDCSigningKey) error {
	return k.CreateProvenanceSigningKey(ctx, key)
}
func (k keyStore) RetireOIDCSigningKeys(ctx context.Context, keep string, retiresAt time.Time) error {
	return k.RetireProvenanceSigningKeys(ctx, keep, retiresAt)
}
func (k keyStore) SetOIDCSigningKeyRetirement(ctx context.Context, kid string, retiresAt time.Time) error {
	return k.SetProvenanceSigningKeyRetirement(ctx, kid, retiresAt)
}
func (k keyStore) WithOIDCKeyLock(ctx context.Context, fn func(ctx context.Context) error) error {
	return k.WithProvenanceKeyLock(ctx, fn)
}

// NewSigner returns the provenance key manager, or nil when provenance is
// off: no builder ID, no master key, or a store without provenance keys.
// builderID is REACTORCIDE_PROVENANCE_BUILDER_ID, or the OIDC issuer URL.
func NewSigner(value any, encryptor oidcissuer.Encryptor, builderID string) *oidcissuer.Issuer {
	keys, ok := value.(store.ProvenanceKeyStore)
	if !ok || encryptor == nil || strings.TrimSpace(builderID) == "" {
		return nil
	}
	issuer := oidcissuer.New(builderID, oidcissuer.AlgorithmES256, keyStore{keys}, encryptor)
	// A provenance signature must verify for a long time after the job:
	// keep a retired key in the JWKS for a year. Rotate yearly.
	issuer.RetireAfter = 365 * 24 * time.Hour
	issuer.RotationPeriod = 365 * 24 * time.Hour
	return issuer
}

// BuilderIDFromEnv returns REACTORCIDE_PROVENANCE_BUILDER_ID, or
// fallback (the OIDC issuer URL). REACTORCIDE_PROVENANCE_ENABLED=false
// turns provenance off.
func BuilderIDFromEnv(fallback string) string {
	if value := strings.TrimSpace(os.Getenv("REACTORCIDE_PROVENANCE_ENABLED")); value == "false" || value == "0" {
		return ""
	}
	if value := strings.TrimSpace(os.Getenv("REACTORCIDE_PROVENANCE_BUILDER_ID")); value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}

// Writer writes the provenance of completed jobs.
type Writer struct {
	Signer    *oidcissuer.Issuer
	Facts     oidcissuer.FactsStore
	Artifacts *artifacts.Service
}

// Enabled reports whether the writer can sign.
func (w *Writer) Enabled() bool {
	return w != nil && w.Signer != nil && w.Signer.Enabled() && w.Facts != nil && w.Artifacts != nil
}

// ErrNoSubjects is returned when a job has nothing to attest.
var ErrNoSubjects = errors.New("provenance: the job has no artifacts and no declared subjects")

// Build makes the statement of job. list is the artifacts of the job;
// workflowOutput is its workflow output document (for
// provenance_subjects).
func Build(facts oidcissuer.JobFacts, job *models.Job, builderID string, list []models.JobArtifact, workflowOutput []byte) (*Statement, []string, error) {
	subjects := []Subject{}
	for _, artifact := range list {
		if artifact.Kind == models.ArtifactKindFile && artifact.Status == models.ArtifactStatusComplete && artifact.SHA256 != "" {
			subjects = append(subjects, Subject{Name: artifact.Path, Digest: map[string]string{"sha256": artifact.SHA256},
				Annotations: map[string]string{SubjectSourceAnnotation: SubjectCoordinatorHashed}})
		}
	}
	declared, dropped := ParseDeclaredSubjects(workflowOutput)
	subjects = append(subjects, declared...)
	if len(subjects) > MaxSubjects {
		dropped = append(dropped, fmt.Sprintf("only the first %d subjects are in the provenance", MaxSubjects))
		subjects = subjects[:MaxSubjects]
	}
	if len(subjects) == 0 {
		return nil, dropped, ErrNoSubjects
	}
	refClasses := append([]string{}, facts.RefClasses...)
	if refClasses == nil {
		refClasses = []string{}
	}
	approval := ""
	if job.ApprovalID != nil {
		approval = *job.ApprovalID
	}
	deps := []ResourceDescriptor{}
	if facts.SHA != "" || (job.SourceURL != nil && *job.SourceURL != "") {
		source := ResourceDescriptor{Name: "source", Digest: map[string]string{}}
		if job.SourceURL != nil {
			source.URI = gitURI(*job.SourceURL)
		}
		if facts.SHA != "" {
			source.Digest["gitCommit"] = facts.SHA
		}
		deps = append(deps, source)
	}
	if job.CISHA != "" || job.CIRepository != "" || (job.CISourceURL != nil && *job.CISourceURL != "") {
		ci := ResourceDescriptor{Name: "trusted_ci", Digest: map[string]string{}}
		switch {
		case job.CIRepository != "":
			ci.URI = gitURI(job.CIRepository)
		case job.CISourceURL != nil:
			ci.URI = gitURI(*job.CISourceURL)
		}
		if job.CISHA != "" {
			ci.Digest["gitCommit"] = job.CISHA
		}
		deps = append(deps, ci)
	}
	statement := &Statement{
		Type:          StatementType,
		Subject:       SortSubjects(subjects),
		PredicateType: PredicateType,
		Predicate: Predicate{
			BuildDefinition: BuildDefinition{
				BuildType: BuildTypeJob,
				ExternalParameters: ExternalParameters{
					Workflow:     WorkflowParameters{Name: facts.WorkflowName, SecurityID: facts.WorkflowSecurityID, RunID: facts.WorkflowID},
					Node:         facts.Node,
					Job:          job.Name,
					Ref:          facts.Ref,
					SubmittedRef: facts.SubmittedRef,
					Event:        EventParameters{Type: facts.EventType, ID: facts.EventID, PRNumber: facts.PRNumber, HeadRef: facts.HeadRef, BaseRef: facts.BaseRef},
				},
				InternalParameters: InternalParameters{
					Organization: facts.Org, Project: facts.Project, ProjectID: facts.ProjectID, Repository: facts.Repository,
					CIOrigin: facts.CIOrigin, ExecutionProfile: facts.ExecutionProfile, PolicyRevision: facts.PolicyRevision,
					PolicyRule: job.PolicyRuleID, ApprovalID: approval, WorkerClass: job.WorkerClass, RefClasses: refClasses,
					ActorPrincipalID: facts.ActorPrincipalID, ActorKind: facts.ActorKind, OwnerGroup: facts.OwnerGroup,
				},
				ResolvedDependencies: deps,
			},
			RunDetails: RunDetails{
				Builder:  Builder{ID: builderID},
				Metadata: Metadata{InvocationID: job.JobID, StartedOn: timeText(job.StartedAt), FinishedOn: timeText(job.CompletedAt)},
			},
		},
	}
	return statement, dropped, nil
}

func gitURI(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "git+") {
		return value
	}
	return "git+" + value
}

// Write builds, signs, and stores the provenance of a completed job. It
// returns ErrNoSubjects when the job made nothing to attest.
func (w *Writer) Write(ctx context.Context, job *models.Job, list []models.JobArtifact, workflowOutput []byte, namespace string) (*models.JobArtifact, error) {
	if !w.Enabled() {
		return nil, oidcissuer.ErrDisabled
	}
	if job.Status != "completed" {
		return nil, fmt.Errorf("provenance: the job is %s, not completed", job.Status)
	}
	facts, err := oidcissuer.LoadJobFacts(ctx, w.Facts, job)
	if err != nil {
		return nil, err
	}
	statement, dropped, err := Build(facts, job, w.Signer.IssuerValue(), list, workflowOutput)
	if len(dropped) > 0 {
		log.Printf("provenance: job %s: dropped subjects: %s", job.JobID, strings.Join(dropped, "; "))
	}
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(statement)
	if err != nil {
		return nil, err
	}
	kid, signer, err := w.Signer.ActiveSigner(ctx)
	if err != nil {
		return nil, fmt.Errorf("provenance: signing key: %w", err)
	}
	envelope, err := Sign(PayloadType, payload, kid, signer)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	return w.Artifacts.PutComplete(ctx, job, namespace, models.ArtifactKindProvenance, ArtifactPath, MediaType, data)
}
