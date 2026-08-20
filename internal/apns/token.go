// Package apns sends push notifications through Apple Push Notification
// service using provider-token (JWT) authentication. Stdlib only: ES256 over
// a JWT this small does not earn a dependency.
package apns

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ParseAuthKey reads the PKCS#8 PEM that Apple issues as a .p8 file.
func ParseAuthKey(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("apns: auth key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apns: parse auth key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("apns: auth key is %T, want *ecdsa.PrivateKey", parsed)
	}
	return key, nil
}

// tokenValidity is Apple's hard ceiling on a provider token's lifetime;
// refreshAfter is the matching floor's safe complement — Apple rejects
// refreshes more frequent than once per 20 minutes, and regenerating at 50
// minutes sits comfortably inside both bounds.
const (
	tokenValidity = time.Hour
	refreshAfter  = tokenValidity - 10*time.Minute
)

// TokenSource mints and caches the provider token. One per process: a Fargate
// task is short-lived enough that it will usually mint exactly one.
type TokenSource struct {
	key           *ecdsa.PrivateKey
	keyID, teamID string

	mu     sync.Mutex
	cached string
	issued time.Time
}

func NewTokenSource(key *ecdsa.PrivateKey, keyID, teamID string) *TokenSource {
	return &TokenSource{key: key, keyID: keyID, teamID: teamID}
}

func (ts *TokenSource) Token(now time.Time) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.cached != "" && now.Sub(ts.issued) < refreshAfter {
		return ts.cached, nil
	}
	tok, err := ts.sign(now)
	if err != nil {
		return "", err
	}
	ts.cached, ts.issued = tok, now
	return tok, nil
}

func (ts *TokenSource) sign(now time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "ES256", "kid": ts.keyID, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{"iss": ts.teamID, "iat": now.Unix()})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)

	digest := sha256.Sum256([]byte(signingInput))

	// ecdsa.Sign, NOT ecdsa.SignASN1. JWS ES256 (RFC 7518 §3.4) is the raw
	// concatenation of r and s, each left-padded to the curve's byte length.
	// SignASN1 returns a DER SEQUENCE, which APNs rejects as
	// InvalidProviderToken — a message that points at the key rather than at
	// the encoding, making it very expensive to diagnose from production.
	r, s, err := ecdsa.Sign(rand.Reader, ts.key, digest[:])
	if err != nil {
		return "", fmt.Errorf("apns: sign: %w", err)
	}
	// FillBytes writes the value right-aligned (zero-padded) into the slice,
	// which is exactly the left-padding the spec asks for; a P-256 scalar
	// always fits in 32 bytes.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])

	return signingInput + "." + enc.EncodeToString(sig), nil
}
