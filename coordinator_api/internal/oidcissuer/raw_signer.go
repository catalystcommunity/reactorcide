package oidcissuer

import (
	"context"
	"crypto"
	"errors"
)

// ActiveSigner returns the key that signs now and its signer, for a caller
// that signs other documents than JWTs with the same key management (WP15
// provenance, docs/provenance.md). The caller uses its own Issuer with its
// own KeyStore, so a provenance key never signs an OIDC token.
func (i *Issuer) ActiveSigner(ctx context.Context) (string, crypto.Signer, error) {
	if !i.Enabled() {
		return "", nil, ErrDisabled
	}
	keys, err := i.ensureKeys(ctx)
	if err != nil {
		return "", nil, err
	}
	key := active(keys, i.now())
	if key == nil {
		return "", nil, errors.New("no signing key is active")
	}
	signer, err := i.signerFor(ctx, key)
	if err != nil {
		return "", nil, err
	}
	return key.KID, signer, nil
}
