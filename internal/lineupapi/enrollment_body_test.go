package lineupapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEnrollmentToken_LeavesTheBodyReadable is the bug that would have made a
// browser enrollment fail in a way nobody could diagnose.
//
// enrollmentToken accepts the token from ?token= OR from the JSON body. But
// handleAuthRegisterFinish calls it FIRST and then hands the same request to
// FinishRegistration, which parses the WebAuthn attestation out of that body.
// Reading the body to find the token consumed it, so a body-carried token left
// the attestation parse looking at an empty reader — the ceremony failing with
// a credential error while the real cause was two lines earlier.
//
// The query-string path hid it: enrollmentToken returns early on ?token= and
// never touches the body, so whichever transport a client happened to pick
// decided whether registration worked.
func TestEnrollmentToken_LeavesTheBodyReadable(t *testing.T) {
	body := `{"token":"enroll-abc","id":"credential-id","type":"public-key"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/register/finish", strings.NewReader(body))

	if got := enrollmentToken(r); got != "enroll-abc" {
		t.Fatalf("enrollmentToken = %q, want the body token", got)
	}

	rest, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("re-reading the body: %v", err)
	}
	if len(rest) == 0 {
		t.Fatal("the body was consumed; FinishRegistration would see nothing and the " +
			"ceremony would fail for a reason unrelated to the credential")
	}
	var parsed map[string]any
	if err := json.Unmarshal(rest, &parsed); err != nil {
		t.Fatalf("the restored body is not the original JSON: %v", err)
	}
	if parsed["id"] != "credential-id" {
		t.Errorf("restored body lost fields: %v", parsed)
	}
}

// TestEnrollmentToken_QueryStillWins keeps the documented precedence, and keeps
// the body untouched on that path too.
func TestEnrollmentToken_QueryStillWins(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost,
		"/v1/auth/register/finish?token=from-query", strings.NewReader(`{"token":"from-body","id":"x"}`))

	if got := enrollmentToken(r); got != "from-query" {
		t.Errorf("enrollmentToken = %q, want the query token to win", got)
	}
	if rest, _ := io.ReadAll(r.Body); len(rest) == 0 {
		t.Error("the body was consumed on the query path")
	}
}

// TestEnrollmentToken_NoTokenIsNotAnError: an ordinary session-authorized
// registration carries no token at all, and must still reach the ceremony with
// its body intact.
func TestEnrollmentToken_NoTokenIsNotAnError(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/register/finish", strings.NewReader(`{"id":"x"}`))

	if got := enrollmentToken(r); got != "" {
		t.Errorf("enrollmentToken = %q, want empty", got)
	}
	if rest, _ := io.ReadAll(r.Body); len(rest) == 0 {
		t.Error("the body was consumed when there was no token to find")
	}
}
