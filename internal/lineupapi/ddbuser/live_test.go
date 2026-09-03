//go:build live

// Live exercises ddbuser's ConditionExpressions against a real DynamoDB
// table — the gap rosterbot-6my exposed. ddbusertest is a hand-written
// evaluator, and until that bead it compared PutUser's `ver = :v` condition
// by decimal TEXT rather than by AttributeValue TYPE, so a numeric `ver`
// conditioned against a STRING `:v` read as passing in the fake while
// DynamoDB itself would reject it every time — the service compares type AND
// value, so {"N":"1"} never equals {"S":"1"}. Every conditional write in this
// package is only ever evaluated by that one hand-written approximation; this
// file is the check that runs the real evaluator instead.
//
// The table name comes from DDBUSER_LIVE_TABLE, DELIBERATELY NOT
// IDENTITY_TABLE — production wiring (cmd/, lambda/, infra/) reads
// IDENTITY_TABLE, and a local .env pointed at the deployed identity table
// (for `serve`, `connect`, or any of the other IDENTITY_TABLE-reading
// commands) would otherwise let `go test -tags live ./...` write and delete
// scratch rows in the real user directory the moment someone ran it with
// credentials configured. A name nothing else in the tree reads means the
// table has to be named on purpose, for this test alone.
//
//	DDBUSER_LIVE_TABLE=<scratch-table> AWS_REGION=us-west-1 go test -tags live ./internal/lineupapi/ddbuser/ -v
//
// A scratch table matching production's schema (infra/infra.go's
// IdentityTable: string pk HASH + string sk RANGE, on-demand billing, no
// secondary indexes) can be created with:
//
//	aws dynamodb create-table \
//	  --table-name rosterbot-ddbuser-scratch \
//	  --attribute-definitions AttributeName=pk,AttributeType=S AttributeName=sk,AttributeType=S \
//	  --key-schema AttributeName=pk,KeyType=HASH AttributeName=sk,KeyType=RANGE \
//	  --billing-mode PAY_PER_REQUEST \
//	  --region us-west-1
//
// Every item this file writes carries a per-run unique key prefix (uid,
// email, team, enrollment hash) via runPrefix(), so concurrent runs and
// reruns never collide with each other or with anything already in the
// table, and every key written is deleted in t.Cleanup — this is a shared
// scratch table, not a throwaway one.
package ddbuser_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser"
)

func liveTable(t *testing.T) string {
	t.Helper()
	table := os.Getenv("DDBUSER_LIVE_TABLE")
	if table == "" {
		t.Skip("DDBUSER_LIVE_TABLE unset; skipping the live DynamoDB exercise")
	}
	return table
}

// runPrefix gives every item one test run writes a unique, greppable key: two
// concurrent live runs (or a rerun after a failed cleanup) must never collide
// on the same uid/email/team/enrollment-hash, since a collision would make a
// uniqueness-condition test pass or fail for the wrong reason.
func runPrefix() string { return fmt.Sprintf("live-%d", time.Now().UnixNano()) }

func rawClient(t *testing.T) *dynamodb.Client {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		t.Fatalf("LoadDefaultConfig: %v", err)
	}
	return dynamodb.NewFromConfig(cfg)
}

func itemKey(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: pk},
		"sk": &types.AttributeValueMemberS{Value: sk},
	}
}

// trackKeys registers a t.Cleanup that deletes every key ever appended to
// *keys, read at cleanup time rather than at registration time so subtests
// that append after this call still get swept. Deleting a key nothing ever
// wrote (a conditional write this file deliberately makes fail) is a
// documented DynamoDB no-op, not an error, so failed-write keys are safe to
// include defensively.
func trackKeys(t *testing.T, client *dynamodb.Client, table string, keys *[]map[string]types.AttributeValue) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, k := range *keys {
			if _, err := client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
				TableName: aws.String(table), Key: k,
			}); err != nil {
				t.Errorf("cleanup delete %v: %v", k, err)
			}
		}
	})
}

// profileKeys/emailKey/teamKey/enrollKey mirror ddbuser's own (unexported)
// key builders. Duplicating them here is deliberate: this file exercises
// ddbuser only through its exported UserStore/EnrollmentStore methods, the
// same surface a real caller has, and reaches for the raw key layout only to
// prove what landed (or didn't) directly against the table.
func profileKey(uid lineupapi.UserID) map[string]types.AttributeValue {
	return itemKey("USER#"+string(uid), "PROFILE")
}
func emailKey(email string) map[string]types.AttributeValue {
	return itemKey("EMAIL#"+strings.ToLower(email), "USER")
}
func teamKey(teamID string) map[string]types.AttributeValue {
	return itemKey("TEAM#"+teamID, "USER")
}
func enrollKey(hash string) map[string]types.AttributeValue {
	return itemKey("ENROLL#"+hash, "TOKEN")
}

// TestLivePutUser_VersionCondition is the literal rosterbot-6my regression.
// A PutUser carrying the CURRENT version must succeed — under the pre-fix
// code this is exactly the case that failed, because `:v` was bound as a
// STRING against a `ver` attribute written as a NUMBER, so DynamoDB's
// type-and-value comparison made the condition permanently false and EVERY
// update failed with ErrUserConflict, not only concurrent ones. A PutUser
// carrying a STALE version must still be rejected — the condition doing its
// actual job, which a type-mismatched condition also (coincidentally, for
// the wrong reason) rejects, which is exactly how the bug hid.
func TestLivePutUser_VersionCondition(t *testing.T) {
	table := liveTable(t)
	ctx := context.Background()
	client := rawClient(t)

	prefix := runPrefix()
	uid := lineupapi.UserID(prefix + "-put")
	email := prefix + "-put@example.test"

	keys := []map[string]types.AttributeValue{profileKey(uid), emailKey(email)}
	trackKeys(t, client, table, &keys)

	store, err := ddbuser.New(ctx, table)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	u := &lineupapi.User{ID: uid, Email: email, Role: lineupapi.RoleMember, Status: lineupapi.UserActive}
	if err := store.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.Version != "1" {
		t.Fatalf("Version after CreateUser = %q, want \"1\"", u.Version)
	}

	// The regression: updating with the version we were just handed must
	// succeed against the real service.
	u.DisplayName = "updated"
	if err := store.PutUser(ctx, u); err != nil {
		t.Fatalf("PutUser with the current version = %v, want success — this is the rosterbot-6my "+
			"regression: a type-mismatched `ver = :v` condition rejects every update, not only stale "+
			"ones, and reads identically to routine optimistic-concurrency contention", err)
	}
	if u.Version != "2" {
		t.Fatalf("Version after a successful PutUser = %q, want \"2\"", u.Version)
	}
	got, err := client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(table), Key: profileKey(uid)})
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if verAttr, ok := got.Item["ver"].(*types.AttributeValueMemberN); !ok || verAttr.Value != "2" {
		t.Fatalf("stored `ver` attribute = %#v, want N \"2\"", got.Item["ver"])
	}

	// The condition doing its real job: a write carrying the now-superseded
	// version "1" must be rejected.
	stale := &lineupapi.User{ID: uid, Email: email, Role: lineupapi.RoleMember, Version: "1"}
	if err := store.PutUser(ctx, stale); !errors.Is(err, lineupapi.ErrUserConflict) {
		t.Fatalf("PutUser with a stale version = %v, want ErrUserConflict", err)
	}
}

// TestLiveCreateUser_TransactionalConditions covers each of CreateUser's
// three attribute_not_exists(pk) conditions and, critically, that
// TransactWriteItems is all-or-nothing: a cancelled transaction must leave
// NONE of its three items behind, including ones whose own individual
// condition would have passed.
func TestLiveCreateUser_TransactionalConditions(t *testing.T) {
	table := liveTable(t)
	ctx := context.Background()
	client := rawClient(t)

	prefix := runPrefix()
	email1 := prefix + "-e1@example.test"
	team1 := prefix + "-team1"

	keys := []map[string]types.AttributeValue{
		profileKey(lineupapi.UserID(prefix + "-base")), emailKey(email1), teamKey(team1),
	}
	trackKeys(t, client, table, &keys)

	store, err := ddbuser.New(ctx, table)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	base := &lineupapi.User{
		ID: lineupapi.UserID(prefix + "-base"), Email: email1, TeamID: team1,
		Role: lineupapi.RoleMember, Status: lineupapi.UserActive,
	}
	if err := store.CreateUser(ctx, base); err != nil {
		t.Fatalf("CreateUser (base): %v", err)
	}

	t.Run("id already exists -> ErrUserConflict, nothing new lands", func(t *testing.T) {
		otherEmail := prefix + "-idconflict@example.test"
		keys = append(keys, emailKey(otherEmail))
		dup := &lineupapi.User{ID: base.ID, Email: otherEmail, Role: lineupapi.RoleMember}

		if err := store.CreateUser(ctx, dup); !errors.Is(err, lineupapi.ErrUserConflict) {
			t.Fatalf("CreateUser with a taken id = %v, want ErrUserConflict", err)
		}
		got, err := client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(table), Key: emailKey(otherEmail)})
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if len(got.Item) != 0 {
			t.Fatal("the email-claim item exists after a cancelled transaction; its own condition " +
				"(a fresh email) would have passed, but TransactWriteItems must still reject it as a " +
				"whole when the profile's condition fails")
		}
	})

	t.Run("email already claimed -> ErrEmailTaken, nothing new lands", func(t *testing.T) {
		newID := lineupapi.UserID(prefix + "-emaildup")
		keys = append(keys, profileKey(newID))
		dup := &lineupapi.User{ID: newID, Email: email1, Role: lineupapi.RoleMember}

		if err := store.CreateUser(ctx, dup); !errors.Is(err, lineupapi.ErrEmailTaken) {
			t.Fatalf("CreateUser with a taken email = %v, want ErrEmailTaken", err)
		}
		got, err := client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(table), Key: profileKey(newID)})
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if len(got.Item) != 0 {
			t.Fatal("the profile item exists after a cancelled transaction; the id condition passed " +
				"(a fresh id) but the email condition's failure must still cancel the whole write")
		}
	})

	t.Run("team already claimed -> ErrTeamTaken, nothing new lands", func(t *testing.T) {
		newID := lineupapi.UserID(prefix + "-teamdup")
		newEmail := prefix + "-teamdup@example.test"
		keys = append(keys, profileKey(newID), emailKey(newEmail))
		dup := &lineupapi.User{ID: newID, Email: newEmail, TeamID: team1, Role: lineupapi.RoleMember}

		if err := store.CreateUser(ctx, dup); !errors.Is(err, lineupapi.ErrTeamTaken) {
			t.Fatalf("CreateUser with a taken team = %v, want ErrTeamTaken", err)
		}
		got, err := client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(table), Key: emailKey(newEmail)})
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if len(got.Item) != 0 {
			t.Fatal("the email-claim item exists after a cancelled transaction; the email condition " +
				"passed (a fresh email) but the team condition's failure must still cancel the whole write")
		}
	})
}

// TestLiveClaimTeam_Condition covers ClaimTeam's
// `attribute_not_exists(pk) OR uid = :uid`: a fresh team can be claimed, the
// SAME uid re-claiming it is idempotent (a reconnect must not lock the owner
// out of their own team), and a DIFFERENT uid claiming an already-held team
// is rejected.
func TestLiveClaimTeam_Condition(t *testing.T) {
	table := liveTable(t)
	ctx := context.Background()
	client := rawClient(t)

	prefix := runPrefix()
	team := prefix + "-team"
	emailA := prefix + "-a@example.test"
	emailB := prefix + "-b@example.test"
	uidA := lineupapi.UserID(prefix + "-a")
	uidB := lineupapi.UserID(prefix + "-b")

	keys := []map[string]types.AttributeValue{
		profileKey(uidA), emailKey(emailA), profileKey(uidB), emailKey(emailB), teamKey(team),
	}
	trackKeys(t, client, table, &keys)

	store, err := ddbuser.New(ctx, table)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	userA := &lineupapi.User{ID: uidA, Email: emailA, Role: lineupapi.RoleMember, Status: lineupapi.UserActive}
	if err := store.CreateUser(ctx, userA); err != nil {
		t.Fatalf("CreateUser(A): %v", err)
	}
	userB := &lineupapi.User{ID: uidB, Email: emailB, Role: lineupapi.RoleMember, Status: lineupapi.UserActive}
	if err := store.CreateUser(ctx, userB); err != nil {
		t.Fatalf("CreateUser(B): %v", err)
	}

	if err := store.ClaimTeam(ctx, uidA, team); err != nil {
		t.Fatalf("ClaimTeam(A, fresh team): %v", err)
	}
	got, ok, err := store.GetUser(ctx, uidA)
	if err != nil || !ok {
		t.Fatalf("GetUser(A): ok=%v err=%v", ok, err)
	}
	if got.TeamID != team {
		t.Fatalf("A's TeamID = %q after claiming, want %q", got.TeamID, team)
	}

	// Same uid re-claiming: the `uid = :uid` branch, not attribute_not_exists.
	if err := store.ClaimTeam(ctx, uidA, team); err != nil {
		t.Fatalf("ClaimTeam(A, re-claim) = %v, want success — a reconnect must not lock the owner out", err)
	}

	// A different uid claiming an already-held team: rejected.
	if err := store.ClaimTeam(ctx, uidB, team); !errors.Is(err, lineupapi.ErrTeamTaken) {
		t.Fatalf("ClaimTeam(B, held team) = %v, want ErrTeamTaken", err)
	}
	got, ok, err = store.GetUser(ctx, uidB)
	if err != nil || !ok {
		t.Fatalf("GetUser(B): ok=%v err=%v", ok, err)
	}
	if got.TeamID == team {
		t.Fatal("B's TeamID was set to the contested team despite ClaimTeam reporting ErrTeamTaken")
	}
}

// TestLiveListActive_StatusAlias covers ListActive's reserved-word alias:
// Status is a DynamoDB reserved word, so the FilterExpression must reference
// it via the #st placeholder rather than by name. An unaliased filter is
// rejected by the service outright (a syntax error the fake cannot produce,
// since it never parses the expression at all) — this is the one live case
// where the failure mode is "the whole call errors", not "the condition
// silently reads as false".
func TestLiveListActive_StatusAlias(t *testing.T) {
	table := liveTable(t)
	ctx := context.Background()
	client := rawClient(t)

	prefix := runPrefix()
	activeEmail := prefix + "-active@example.test"
	parkedEmail := prefix + "-parked@example.test"
	activeID := lineupapi.UserID(prefix + "-active")
	parkedID := lineupapi.UserID(prefix + "-parked")

	keys := []map[string]types.AttributeValue{
		profileKey(activeID), emailKey(activeEmail), profileKey(parkedID), emailKey(parkedEmail),
	}
	trackKeys(t, client, table, &keys)

	store, err := ddbuser.New(ctx, table)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	active := &lineupapi.User{ID: activeID, Email: activeEmail, Role: lineupapi.RoleMember, Status: lineupapi.UserActive}
	if err := store.CreateUser(ctx, active); err != nil {
		t.Fatalf("CreateUser(active): %v", err)
	}
	parked := &lineupapi.User{ID: parkedID, Email: parkedEmail, Role: lineupapi.RoleMember, Status: lineupapi.UserParked}
	if err := store.CreateUser(ctx, parked); err != nil {
		t.Fatalf("CreateUser(parked): %v", err)
	}

	got, err := store.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	var sawActive, sawParked bool
	for _, u := range got {
		switch u.ID {
		case activeID:
			sawActive = true
		case parkedID:
			sawParked = true
		}
	}
	if !sawActive {
		t.Error("ListActive did not return the active user; if #st fails to alias the reserved word " +
			"Status, the scan's FilterExpression matches nothing and the fan-out silently skips every tenant")
	}
	if sawParked {
		t.Error("ListActive returned a parked user; the status filter is not narrowing at all")
	}

	// ListUsers is the unfiltered sibling: it must keep showing the parked
	// tenant, which is the whole reason the admin directory does not reuse
	// ListActive's scan.
	all, err := store.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	var listUsersSawParked bool
	for _, u := range all {
		if u.ID == parkedID {
			listUsersSawParked = true
		}
	}
	if !listUsersSawParked {
		t.Error("ListUsers did not return the parked user; the admin directory must keep showing " +
			"parked tenants, since its row is the only reactivate control there is")
	}
}

// TestLiveEnrollment_SingleUseCondition covers CreateEnrollment's
// attribute_not_exists(pk) (a spent or in-flight token hash can never be
// overwritten back to fresh) and RedeemEnrollment's
// attribute_not_exists(UsedAt) (the single-use guarantee under concurrency:
// two simultaneous redemptions of one link must not both succeed).
func TestLiveEnrollment_SingleUseCondition(t *testing.T) {
	table := liveTable(t)
	ctx := context.Background()
	client := rawClient(t)

	_, hash, err := lineupapi.MintEnrollmentToken()
	if err != nil {
		t.Fatalf("MintEnrollmentToken: %v", err)
	}
	keys := []map[string]types.AttributeValue{enrollKey(hash)}
	trackKeys(t, client, table, &keys)

	store, err := ddbuser.New(ctx, table)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e := lineupapi.Enrollment{UserID: lineupapi.UserID(runPrefix() + "-enroll"), ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.CreateEnrollment(ctx, hash, e); err != nil {
		t.Fatalf("CreateEnrollment: %v", err)
	}

	// Overwriting an existing (unused) token hash must be refused — the
	// condition exists so a spent link can never be reset to unused by a
	// second CreateEnrollment call reusing (astronomically unlikely, but not
	// the point) the same hash.
	if err := store.CreateEnrollment(ctx, hash, e); !errors.Is(err, lineupapi.ErrUserConflict) {
		t.Fatalf("CreateEnrollment over an existing hash = %v, want ErrUserConflict", err)
	}

	if _, err := store.RedeemEnrollment(ctx, hash, time.Now()); err != nil {
		t.Fatalf("RedeemEnrollment (first, valid): %v", err)
	}

	// The single-use guarantee: a second redemption of the now-used link must
	// be rejected by attribute_not_exists(UsedAt), not silently re-accepted.
	if _, err := store.RedeemEnrollment(ctx, hash, time.Now()); !errors.Is(err, lineupapi.ErrEnrollmentInvalid) {
		t.Fatalf("RedeemEnrollment (second, already used) = %v, want ErrEnrollmentInvalid", err)
	}
}
