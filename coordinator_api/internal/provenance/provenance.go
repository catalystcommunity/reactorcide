// Package provenance writes and verifies the signed provenance of jobs
// (WP15, plan item G2, docs/provenance.md).
//
// The platform writes the provenance, not the job. A job only reports what
// it made: the artifacts that the worker uploaded (the coordinator computes
// their digests) and, optionally, external digests in its workflow output
// (`provenance_subjects`, for example the digest of a pushed image). When the
// job completes, the coordinator writes an in-toto Statement v1 with a SLSA
// Provenance v1 predicate. Every other fact comes from coordinator records:
// the source and trusted CI commits, the workflow, the node, the ref, the
// event, the ref classes, the actor principal, the CI origin, the execution
// profile, the policy revision and rule, the approval, and the worker class.
//
// The coordinator signs the statement as a DSSE envelope with its own
// provenance key (ES256 by default). The key management is the same as for
// the OIDC issuer (internal/oidcissuer), with a separate key table, so a
// provenance key never signs an OIDC token. The public keys are a JWKS at
// GET /api/v1/provenance/keys.
package provenance

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Statement and predicate types.
const (
	StatementType     = "https://in-toto.io/Statement/v1"
	PredicateType     = "https://slsa.dev/provenance/v1"
	PayloadType       = "application/vnd.in-toto+json"
	BuildTypeJob      = "https://github.com/catalystcommunity/reactorcide/blob/main/docs/provenance.md#build-type-job-v1"
	MaxSubjects       = 200
	MaxSubjectNameLen = 512
)

// Statement is an in-toto Statement v1.
type Statement struct {
	Type          string    `json:"_type"`
	Subject       []Subject `json:"subject"`
	PredicateType string    `json:"predicateType"`
	Predicate     Predicate `json:"predicate"`
}

// Subject is one artifact with its digest.
type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
	// Annotations hold SubjectSourceAnnotation: where the digest came from.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// SubjectSourceAnnotation is the subject annotation that says where the
// digest came from.
const SubjectSourceAnnotation = "reactorcide.subject_source"

// Subject sources.
const (
	// SubjectCoordinatorHashed: an artifact that the worker uploaded. The
	// coordinator computed the digest from the bytes it stored.
	SubjectCoordinatorHashed = "coordinator_hashed"
	// SubjectDeclared: an entry of provenance_subjects in the workflow
	// output. The job stated the digest; the coordinator did not see the
	// bytes. A job can declare any digest.
	SubjectDeclared = "declared"
)

// Source returns the subject source annotation, or "" for a statement from
// before the annotation.
func (s Subject) Source() string {
	return s.Annotations[SubjectSourceAnnotation]
}

// Predicate is a SLSA Provenance v1 predicate.
type Predicate struct {
	BuildDefinition BuildDefinition `json:"buildDefinition"`
	RunDetails      RunDetails      `json:"runDetails"`
}

// BuildDefinition is the SLSA build definition.
type BuildDefinition struct {
	BuildType            string               `json:"buildType"`
	ExternalParameters   ExternalParameters   `json:"externalParameters"`
	InternalParameters   InternalParameters   `json:"internalParameters"`
	ResolvedDependencies []ResourceDescriptor `json:"resolvedDependencies"`
}

// ExternalParameters are the inputs that the event and the trusted CI
// definition chose.
type ExternalParameters struct {
	Workflow WorkflowParameters `json:"workflow"`
	Node     string             `json:"node"`
	Job      string             `json:"job"`
	Ref      string             `json:"ref"`
	// SubmittedRef is the ref that an API submitter stated. Only the
	// submitter states it, so it is never in Ref.
	SubmittedRef string          `json:"submitted_ref,omitempty"`
	Event        EventParameters `json:"event"`
}

// WorkflowParameters identify the workflow.
type WorkflowParameters struct {
	Name       string `json:"name"`
	SecurityID string `json:"security_id"`
	RunID      string `json:"run_id,omitempty"`
}

// EventParameters identify the event.
type EventParameters struct {
	Type     string `json:"type"`
	ID       string `json:"id,omitempty"`
	PRNumber int    `json:"pr_number,omitempty"`
	HeadRef  string `json:"head_ref,omitempty"`
	BaseRef  string `json:"base_ref,omitempty"`
}

// InternalParameters are the coordinator decisions.
type InternalParameters struct {
	Organization     string   `json:"organization"`
	Project          string   `json:"project,omitempty"`
	ProjectID        string   `json:"project_id,omitempty"`
	Repository       string   `json:"repository,omitempty"`
	CIOrigin         string   `json:"ci_origin"`
	ExecutionProfile string   `json:"execution_profile"`
	PolicyRevision   string   `json:"policy_revision"`
	PolicyRule       string   `json:"policy_rule"`
	ApprovalID       string   `json:"approval_id"`
	WorkerClass      string   `json:"worker_class"`
	RefClasses       []string `json:"ref_classes"`
	ActorPrincipalID string   `json:"actor_principal_id"`
	ActorKind        string   `json:"actor_kind"`
	OwnerGroup       string   `json:"owner_group,omitempty"`
}

// ResourceDescriptor is a SLSA resource descriptor.
type ResourceDescriptor struct {
	Name   string            `json:"name"`
	URI    string            `json:"uri,omitempty"`
	Digest map[string]string `json:"digest,omitempty"`
}

// RunDetails are the facts of the run.
type RunDetails struct {
	Builder  Builder  `json:"builder"`
	Metadata Metadata `json:"metadata"`
}

// Builder identifies the coordinator.
type Builder struct {
	ID string `json:"id"`
}

// Metadata is the run metadata.
type Metadata struct {
	InvocationID string `json:"invocationId"`
	StartedOn    string `json:"startedOn,omitempty"`
	FinishedOn   string `json:"finishedOn,omitempty"`
}

// DeclaredSubject is one entry of `provenance_subjects` in the workflow
// output.
type DeclaredSubject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

type outputDocument struct {
	ProvenanceSubjects json.RawMessage `json:"provenance_subjects"`
}

// ParseDeclaredSubjects reads `provenance_subjects` of a workflow output
// document. Only sha256 digests are accepted. It returns the valid subjects
// and a reason for each dropped entry.
func ParseDeclaredSubjects(data []byte) ([]Subject, []string) {
	if len(data) == 0 {
		return nil, nil
	}
	var doc outputDocument
	if err := json.Unmarshal(data, &doc); err != nil || len(doc.ProvenanceSubjects) == 0 || string(doc.ProvenanceSubjects) == "null" {
		return nil, nil
	}
	var raw []DeclaredSubject
	if err := json.Unmarshal(doc.ProvenanceSubjects, &raw); err != nil {
		return nil, []string{"provenance_subjects must be a list of {name, digest: {sha256}} objects"}
	}
	out := []Subject{}
	dropped := []string{}
	for _, item := range raw {
		name := strings.TrimSpace(item.Name)
		digest := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(item.Digest["sha256"]), "sha256:"))
		switch {
		case name == "" || utf8.RuneCountInString(name) > MaxSubjectNameLen || hasControl(name):
			dropped = append(dropped, fmt.Sprintf("subject %q: the name must have 1 to %d characters", name, MaxSubjectNameLen))
		case !validSHA256(digest):
			dropped = append(dropped, fmt.Sprintf("subject %q: digest.sha256 must have 64 hex characters", name))
		case len(out) >= MaxSubjects:
			dropped = append(dropped, fmt.Sprintf("subject %q: at most %d subjects", name, MaxSubjects))
		default:
			out = append(out, Subject{Name: name, Digest: map[string]string{"sha256": digest},
				Annotations: map[string]string{SubjectSourceAnnotation: SubjectDeclared}})
		}
	}
	return out, dropped
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// SortSubjects orders subjects by name and removes exact duplicates (the
// same name and digest). The first one stays, so a coordinator-hashed
// subject (Build adds them first) stays over a declared one.
func SortSubjects(subjects []Subject) []Subject {
	sort.SliceStable(subjects, func(a, b int) bool { return subjects[a].Name < subjects[b].Name })
	out := subjects[:0]
	for i, subject := range subjects {
		if i > 0 && subject.Name == out[len(out)-1].Name && subject.Digest["sha256"] == out[len(out)-1].Digest["sha256"] {
			continue
		}
		out = append(out, subject)
	}
	return out
}

func timeText(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
