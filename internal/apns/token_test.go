package apns

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func testKeyPEM(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), key
}

func TestTokenIsVerifiableES256WithRawSignature(t *testing.T) {
	pemBytes, want := testKeyPEM(t)
	key, err := ParseAuthKey(pemBytes)
	if err != nil {
		t.Fatalf("ParseAuthKey: %v", err)
	}
	if !key.Equal(want) {
		t.Fatal("parsed key differs from the generated one")
	}

	ts := NewTokenSource(key, "KEYID123", "8KBU54NP6U")
	tok, err := ts.Token(time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatalf("Token: %v", err)
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 JWT segments, got %d", len(parts))
	}

	var hdr struct{ Alg, Kid, Typ string }
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if err := json.Unmarshal(raw, &hdr); err != nil {
		t.Fatalf("header: %v", err)
	}
	if hdr.Alg != "ES256" || hdr.Kid != "KEYID123" || hdr.Typ != "JWT" {
		t.Errorf("header = %+v", hdr)
	}

	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
	}
	raw, _ = base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	if claims.Iss != "8KBU54NP6U" || claims.Iat != 1_800_000_000 {
		t.Errorf("claims = %+v", claims)
	}

	// THE important assertion. JWT ES256 requires a raw 64-byte r||s
	// signature. ecdsa.SignASN1 returns DER instead, which APNs rejects as
	// InvalidProviderToken — a message that reads like a wrong key rather
	// than a wrong encoding, so this is very expensive to diagnose in prod.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("signature not base64url: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("signature must be 64 raw bytes (r||s), got %d — ASN.1 DER was almost certainly used", len(sig))
	}

	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, digest[:], r, s) {
		t.Fatal("signature does not verify against the signing key")
	}
}

func TestTokenIsCachedThenRefreshedAfterTheWindow(t *testing.T) {
	pemBytes, _ := testKeyPEM(t)
	key, err := ParseAuthKey(pemBytes)
	if err != nil {
		t.Fatalf("ParseAuthKey: %v", err)
	}
	ts := NewTokenSource(key, "K", "T")

	base := time.Unix(1_800_000_000, 0)
	first, err := ts.Token(base)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}

	// Apple rejects provider tokens refreshed more often than once per 20
	// minutes, so within the window the SAME token must come back.
	same, err := ts.Token(base.Add(19 * time.Minute))
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if same != first {
		t.Error("token must be reused inside the refresh window")
	}

	// And it must not be used past its 1-hour validity.
	later, err := ts.Token(base.Add(61 * time.Minute))
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if later == first {
		t.Error("token must be regenerated once the validity window has passed")
	}
}

func TestParseAuthKeyRejectsNonECKeys(t *testing.T) {
	if _, err := ParseAuthKey([]byte("not a pem block")); err == nil {
		t.Error("want an error for malformed PEM")
	}
}
