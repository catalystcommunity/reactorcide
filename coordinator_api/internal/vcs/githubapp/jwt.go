// Package githubapp holds the GitHub App pool of the coordinator (plan item
// B6, decision D2, docs/github-app.md): App JWTs, installation tokens, rate
// limits, App selection, installation webhooks, self-service project
// registration, and the check run client. Other packages use it only through
// the interfaces in internal/vcs (app_source.go).
package githubapp

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// GitHub accepts an App JWT with an expiry of at most 10 minutes. The issue
// time is 60 seconds in the past, for clock drift (GitHub documentation).
const (
	jwtClockSkew = 60 * time.Second
	jwtLifetime  = 9 * time.Minute
)

// ParsePrivateKey reads the PEM private key of an App (PKCS#1, as GitHub
// gives it, or PKCS#8). The error never contains key material.
func ParsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("the private key is not PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("the private key is not an RSA key in PKCS#1 or PKCS#8 form")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the private key is not an RSA key")
	}
	return key, nil
}

// AppJWT makes the RS256 JWT that authenticates as the App: iss is the App
// ID, iat is now-60s, exp is now+9m.
func AppJWT(appID int64, key *rsa.PrivateKey, now time.Time) (string, error) {
	if appID <= 0 || key == nil {
		return "", errors.New("an App ID and a private key are required")
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return "", fmt.Errorf("creating JWT signer: %w", err)
	}
	claims := jwt.Claims{
		Issuer:   strconv.FormatInt(appID, 10),
		IssuedAt: jwt.NewNumericDate(now.Add(-jwtClockSkew)),
		Expiry:   jwt.NewNumericDate(now.Add(jwtLifetime)),
	}
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		return "", fmt.Errorf("signing App JWT: %w", err)
	}
	return token, nil
}
