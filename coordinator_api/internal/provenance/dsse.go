package provenance

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/go-jose/go-jose/v4"
)

// Envelope is a DSSE envelope (https://github.com/secure-systems-lab/dsse).
type Envelope struct {
	PayloadType string      `json:"payloadType"`
	Payload     string      `json:"payload"`
	Signatures  []Signature `json:"signatures"`
}

// Signature is one DSSE signature. Sig is base64 of the signature bytes:
// ASN.1 DER for ECDSA, PKCS #1 v1.5 for RSA.
type Signature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

// PAE is the DSSE pre-authentication encoding of a payload.
func PAE(payloadType string, payload []byte) []byte {
	out := []byte("DSSEv1 ")
	out = append(out, strconv.Itoa(len(payloadType))...)
	out = append(out, ' ')
	out = append(out, payloadType...)
	out = append(out, ' ')
	out = append(out, strconv.Itoa(len(payload))...)
	out = append(out, ' ')
	out = append(out, payload...)
	return out
}

// Sign makes a DSSE envelope of payload with signer.
func Sign(payloadType string, payload []byte, keyID string, signer crypto.Signer) (*Envelope, error) {
	digest := sha256.Sum256(PAE(payloadType, payload))
	var sig []byte
	var err error
	switch signer.Public().(type) {
	case *ecdsa.PublicKey:
		sig, err = signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	case *rsa.PublicKey:
		sig, err = signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	default:
		return nil, errors.New("provenance: the signing key type is not supported")
	}
	if err != nil {
		return nil, fmt.Errorf("provenance: sign: %w", err)
	}
	return &Envelope{
		PayloadType: payloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  []Signature{{KeyID: keyID, Sig: base64.StdEncoding.EncodeToString(sig)}},
	}, nil
}

// ErrNoValidSignature is returned when no signature of an envelope verifies.
var ErrNoValidSignature = errors.New("provenance: no signature verifies with the given keys")

// Verify checks the envelope against the keys and returns the payload and
// the ID of the key that verified it. A signature with a key ID that is
// not in keys does not count.
func Verify(envelope *Envelope, keys *jose.JSONWebKeySet) ([]byte, string, error) {
	if envelope == nil || keys == nil {
		return nil, "", ErrNoValidSignature
	}
	payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return nil, "", fmt.Errorf("provenance: the payload is not base64: %w", err)
	}
	digest := sha256.Sum256(PAE(envelope.PayloadType, payload))
	for _, signature := range envelope.Signatures {
		raw, err := base64.StdEncoding.DecodeString(signature.Sig)
		if err != nil {
			continue
		}
		for _, key := range keys.Key(signature.KeyID) {
			if verifyWith(key.Key, digest[:], raw) {
				return payload, signature.KeyID, nil
			}
		}
	}
	return nil, "", ErrNoValidSignature
}

func verifyWith(key any, digest, sig []byte) bool {
	switch public := key.(type) {
	case *ecdsa.PublicKey:
		return ecdsa.VerifyASN1(public, digest, sig)
	case *rsa.PublicKey:
		return rsa.VerifyPKCS1v15(public, crypto.SHA256, digest, sig) == nil
	}
	return false
}

// ParseEnvelope reads a DSSE envelope document.
func ParseEnvelope(data []byte) (*Envelope, error) {
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("provenance: the envelope is not valid JSON: %w", err)
	}
	if envelope.PayloadType == "" || envelope.Payload == "" || len(envelope.Signatures) == 0 {
		return nil, errors.New("provenance: the document is not a DSSE envelope")
	}
	return &envelope, nil
}

// ParseStatement reads the statement of a verified payload.
func ParseStatement(payloadType string, payload []byte) (*Statement, error) {
	if payloadType != PayloadType {
		return nil, fmt.Errorf("provenance: the payload type %q is not %q", payloadType, PayloadType)
	}
	var statement Statement
	if err := json.Unmarshal(payload, &statement); err != nil {
		return nil, fmt.Errorf("provenance: the statement is not valid JSON: %w", err)
	}
	if statement.Type != StatementType || statement.PredicateType != PredicateType {
		return nil, errors.New("provenance: the statement is not an in-toto v1 statement with a SLSA v1 predicate")
	}
	return &statement, nil
}
