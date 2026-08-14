package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

type stubConns struct {
	conn *lineupapi.FantraxConnection
	err  error
}

func (s stubConns) GetConnection(context.Context, lineupapi.UserID) (*lineupapi.FantraxConnection, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	if s.conn == nil {
		return nil, false, nil
	}
	return s.conn, true, nil
}

func (s stubConns) PutConnection(context.Context, *lineupapi.FantraxConnection) error { return nil }

// stubOpener reverses recordingSealer's marking, so a test can prove the
// credentials that reach the config came through the Opener rather than from
// the environment.
type stubOpener struct{ err error }

func (o stubOpener) Open(_ context.Context, _ lineupapi.UserID, ct []byte) ([]byte, error) {
	if o.err != nil {
		return nil, o.err
	}
	return []byte(strings.TrimPrefix(string(ct), "sealed:")), nil
}

func sealedCreds(t *testing.T, user, pass string) []byte {
	t.Helper()
	b, err := json.Marshal(lineupapi.FantraxCreds{Username: user, Password: pass})
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	return append([]byte("sealed:"), b...)
}

func connFor(t *testing.T, uid lineupapi.UserID, status lineupapi.ConnStatus) *lineupapi.FantraxConnection {
	t.Helper()
	return &lineupapi.FantraxConnection{
		UserID:          uid,
		TeamID:          "tenant-team",
		Status:          status,
		CredsCiphertext: sealedCreds(t, "tenant@example.test", "tenant-pass"),
		FXRMCiphertext:  []byte("sealed:tenant-fxrm"),
	}
}

// envConfig is what config.Load produces on Fargate: the DEPLOYMENT's secrets,
// present in every task's environment regardless of which tenant the run is
// for. Every refusal below must leave a job unable to use these.
func envConfig() *config.Config {
	return &config.Config{
		Username: "operator@example.test",
		Password: "operator-pass",
		LeagueID: "league-1",
		TeamID:   "operator-team",
	}
}

// TestApplyTenantCredentials_ReplacesTheOperatorsCredentials is the whole point
// of the ticket. A verified tenant's run must act as THAT tenant — different
// login, different team — never as the operator whose secrets happen to be in
// the environment.
func TestApplyTenantCredentials_ReplacesTheOperatorsCredentials(t *testing.T) {
	cfg := envConfig()
	conns := stubConns{conn: connFor(t, "alice", lineupapi.ConnVerified)}

	if err := applyTenantCredentials(context.Background(), cfg, "alice", conns, stubOpener{}); err != nil {
		t.Fatalf("applyTenantCredentials: %v", err)
	}

	if cfg.Username != "tenant@example.test" || cfg.Password != "tenant-pass" {
		t.Errorf("credentials = %q/%q, want the tenant's", cfg.Username, cfg.Password)
	}
	if cfg.TeamID != "tenant-team" {
		t.Errorf("TeamID = %q, want the team from the connection record", cfg.TeamID)
	}
	if cfg.LeagueID != "league-1" {
		t.Errorf("LeagueID = %q; the league is deployment-wide and must be left alone", cfg.LeagueID)
	}
}

// TestApplyTenantCredentials_RefusalsLeaveNoUsableCredentials is the security
// property, and it asserts the CONFIG rather than the error.
//
// A caller that checks the error will stop anyway. This pins the other half:
// even if one did not, the operator's credentials must not still be sitting in
// the config for the run to proceed with. Fail safe, not fail open.
func TestApplyTenantCredentials_RefusalsLeaveNoUsableCredentials(t *testing.T) {
	for _, tc := range []struct {
		name   string
		conns  stubConns
		opener stubOpener
	}{
		{"never connected", stubConns{}, stubOpener{}},
		{"still pending", stubConns{conn: connFor(t, "alice", lineupapi.ConnPending)}, stubOpener{}},
		{"needs reconnect", stubConns{conn: connFor(t, "alice", lineupapi.ConnNeedsReconnect)}, stubOpener{}},
		{"record for another tenant", stubConns{conn: connFor(t, "bob", lineupapi.ConnVerified)}, stubOpener{}},
		{"store unavailable", stubConns{err: errors.New("dynamo down")}, stubOpener{}},
		{"decrypt refused", stubConns{conn: connFor(t, "alice", lineupapi.ConnVerified)}, stubOpener{err: errors.New("kms denied")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := envConfig()
			err := applyTenantCredentials(context.Background(), cfg, "alice", tc.conns, tc.opener)
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if cfg.Username == "operator@example.test" || cfg.Password == "operator-pass" {
				t.Error("the operator's credentials survived a refused tenant load; a caller " +
					"ignoring the error would run this job as the operator")
			}
			if cfg.TeamID == "operator-team" {
				t.Error("the deployment's FANTRAX_TEAM_ID survived a refused tenant load")
			}
		})
	}
}

// TestApplyTenantCredentials_RefusalNamesTheReason: these strings reach an
// operator alert, and "connect failed" is not actionable. needs_reconnect means
// a human must reconnect; not_verified means they never finished.
func TestApplyTenantCredentials_RefusalNamesTheReason(t *testing.T) {
	cfg := envConfig()
	conns := stubConns{conn: connFor(t, "alice", lineupapi.ConnNeedsReconnect)}

	err := applyTenantCredentials(context.Background(), cfg, "alice", conns, stubOpener{})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), lineupapi.RunDeniedNeedsReconnect) {
		t.Errorf("error %q does not carry the refusal class", err.Error())
	}
}

// TestApplyTenantCredentials_StoreOutageIsNotReportedAsNeverConnected pins a
// distinction the refusal tests above cannot see.
//
// Swallowing the store error still refuses — GetConnection returned nothing, so
// AuthorizeRun reports no_connection and the config is still cleared. Every
// assertion stays green. But the two causes need OPPOSITE responses: a DynamoDB
// outage is ours to fix and will clear on its own, while no_connection sends an
// operator to tell a user to go connect an account that is already connected
// fine. Reported as the latter, a transient outage becomes a support request.
//
// Proven blind by mutation: replacing the store-error return with `err = nil`
// left the whole suite passing.
func TestApplyTenantCredentials_StoreOutageIsNotReportedAsNeverConnected(t *testing.T) {
	cfg := envConfig()
	conns := stubConns{err: errors.New("dynamo down")}

	err := applyTenantCredentials(context.Background(), cfg, "alice", conns, stubOpener{})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), lineupapi.RunDeniedNoConnection) {
		t.Errorf("a store outage was reported as %q: %v", lineupapi.RunDeniedNoConnection, err)
	}
	if !strings.Contains(err.Error(), "dynamo down") {
		t.Errorf("error %q does not carry the underlying store failure", err.Error())
	}
}

// TestApplyTenantCredentials_RejectsAMalformedBlob: a credential blob that does
// not unmarshal must refuse rather than proceed with empty strings, which
// auth_client would report as "credentials must be set" three layers away.
func TestApplyTenantCredentials_RejectsAMalformedBlob(t *testing.T) {
	cfg := envConfig()
	conn := connFor(t, "alice", lineupapi.ConnVerified)
	conn.CredsCiphertext = []byte("sealed:not-json")

	if err := applyTenantCredentials(context.Background(), cfg, "alice",
		stubConns{conn: conn}, stubOpener{}); err == nil {
		t.Fatal("a malformed credential blob was accepted")
	}
	if cfg.Username == "operator@example.test" {
		t.Error("the operator's credentials survived a malformed-blob refusal")
	}
}

// TestApplyTenantCredentials_RejectsEmptyDecryptedCredentials closes the gap
// the blob test leaves: valid JSON carrying empty strings. Proceeding would
// mean a run with no credentials at all, which fails much later and much less
// legibly.
func TestApplyTenantCredentials_RejectsEmptyDecryptedCredentials(t *testing.T) {
	cfg := envConfig()
	conn := connFor(t, "alice", lineupapi.ConnVerified)
	conn.CredsCiphertext = sealedCreds(t, "", "")

	if err := applyTenantCredentials(context.Background(), cfg, "alice",
		stubConns{conn: conn}, stubOpener{}); err == nil {
		t.Fatal("an empty credential pair was accepted")
	}
}
