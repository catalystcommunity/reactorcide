package uiapi

import (
	"encoding/json"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// The CI policy travels as a typed CSIL value (csilapi.CiPolicyDocument),
// not as an opaque JSON string. Storage is unchanged: models.CIPolicy.Document
// has always been JSONB, and PutCiPolicy has always stored
// cipolicy.CanonicalDocument's output, so an authored document has always been
// canonicalized on save.
//
// Both conversions below go through JSON rather than copying field by field.
// That is deliberate. cipolicy.Policy and csilapi.CiPolicyDocument carry
// identical `json:` tags for every field (version, defaults, head_ci, id,
// actors, workflows, paths, events, base_branches, head_repository, approval,
// use, ci_source, profile, workers, base_nodes, nodes, any) because the CSIL
// type was written from the Go struct. A hand-written field copy would be a
// third place to update when a policy field is added, and the one most likely
// to be forgotten — a silently dropped field in a security policy is exactly
// the failure this design must not have. A JSON round trip cannot drop a
// field that both sides name the same way, and a field added to only one side
// fails the round-trip test in ci_policy_convert_test.go.
//
// Validation stays where it already was: policyDocumentToPolicy hands the
// bytes to cipolicy.ParseDocument, so every rule in validatePolicy
// (securityIDPattern, node name grammar, path containment) still runs against
// a document that arrived over the typed wire.

// policyDocumentToPolicy converts a wire document to a parsed, validated
// policy. The returned error is already a caller-facing ServiceErr.
func policyDocumentToPolicy(document csilapi.CiPolicyDocument) (*cipolicy.Policy, error) {
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, NewServiceError("internal", "failed to encode CI policy")
	}
	parsed, err := cipolicy.ParseDocument(encoded)
	if err != nil {
		return nil, NewServiceError("invalid_argument", err.Error())
	}
	return parsed, nil
}

// storedDocumentToPolicyDocument converts a stored JSONB document to the wire
// type. A stored document that will not decode is a corrupted row, not a
// caller error.
func storedDocumentToPolicyDocument(stored models.JSONB) (csilapi.CiPolicyDocument, error) {
	encoded, err := json.Marshal(stored)
	if err != nil {
		return csilapi.CiPolicyDocument{}, NewServiceError("internal", "failed to encode CI policy")
	}
	var document csilapi.CiPolicyDocument
	if err := json.Unmarshal(encoded, &document); err != nil {
		return csilapi.CiPolicyDocument{}, NewServiceError("internal", "stored CI policy is not readable")
	}
	// head_ci is `[* CiPolicyRule]`, not optional, so a policy with no rules
	// must still encode as an empty array rather than a null.
	if document.HeadCi == nil {
		document.HeadCi = []csilapi.CiPolicyRule{}
	}
	return document, nil
}
