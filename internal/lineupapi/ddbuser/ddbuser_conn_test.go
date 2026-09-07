package ddbuser_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/conntest"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser/ddbusertest"
)

// ddbuser.Store is the ONLY production implementation of ConnectionStore —
// FileUserStore does not implement it — and there is no shared contract package
// for it the way identitytest/pushdevicetest exist. These two tests are
// therefore the only thing standing between a field rename and a field that
// silently stops persisting.

// TestConnection_RoundTripsThroughDynamoDB pins the repo's recorded trap:
// attributevalue marshals by Go FIELD NAME and ignores json tags, so a
// json-tag-only rename of LastConnectRun would keep compiling, keep serving the
// right wire shape, and quietly stop storing anything.
func TestConnection_RoundTripsThroughDynamoDB(t *testing.T) {
	st := ddbuser.NewWithAPI(ddbusertest.New(), "test-table")
	ctx := context.Background()

	in := &lineupapi.FantraxConnection{
		UserID:    "alice",
		TeamID:    "tenant-team",
		Status:    lineupapi.ConnNeedsReconnect,
		LastError: lineupapi.ConnErrNoTeam,
		LastConnectRun: &lineupapi.ConnectRun{
			RunID:     "task-1",
			Verdict:   lineupapi.ConnectVerdictFailed,
			LastError: lineupapi.ConnErrNoTeam,
		},
	}
	if err := st.PutConnection(ctx, in); err != nil {
		t.Fatalf("PutConnection: %v", err)
	}
	got, ok, err := st.GetConnection(ctx, "alice")
	if err != nil || !ok {
		t.Fatalf("GetConnection = (%v, %v, %v)", got, ok, err)
	}
	if got.Status != in.Status || got.LastError != in.LastError || got.TeamID != in.TeamID {
		t.Fatalf("connection = %+v, want the record that was written", *got)
	}
	if got.LastConnectRun == nil {
		t.Fatal("LastConnectRun did not survive the round trip; the Runs tab would silently " +
			"go back to showing the ledger status alone")
	}
	if *got.LastConnectRun != *in.LastConnectRun {
		t.Fatalf("LastConnectRun = %+v, want %+v", *got.LastConnectRun, *in.LastConnectRun)
	}
}

// TestConnection_LegacyItemWithoutTheStampDecodes: every connection record in
// production predates this field. A decode that failed on its absence would
// take down the whole connect flow and AuthorizeRun with it.
func TestConnection_LegacyItemWithoutTheStampDecodes(t *testing.T) {
	api := ddbusertest.New()
	st := ddbuser.NewWithAPI(api, "test-table")
	ctx := context.Background()

	// Written by hand from the PRE-change field set: no LastConnectRun attribute
	// at all, not even a NULL.
	if _, err := api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String("test-table"),
		Item: map[string]types.AttributeValue{
			"pk":        &types.AttributeValueMemberS{Value: "USER#alice"},
			"sk":        &types.AttributeValueMemberS{Value: "FANTRAX"},
			"UserID":    &types.AttributeValueMemberS{Value: "alice"},
			"TeamID":    &types.AttributeValueMemberS{Value: "tenant-team"},
			"Status":    &types.AttributeValueMemberS{Value: string(lineupapi.ConnVerified)},
			"UpdatedAt": &types.AttributeValueMemberS{Value: "2026-08-01T00:00:00Z"},
		},
	}); err != nil {
		t.Fatalf("seed legacy item: %v", err)
	}

	got, ok, err := st.GetConnection(ctx, "alice")
	if err != nil {
		t.Fatalf("GetConnection on a pre-change record: %v", err)
	}
	if !ok {
		t.Fatal("a pre-change connection record read as absent")
	}
	if got.LastConnectRun != nil {
		t.Fatalf("LastConnectRun = %+v on a record that never had one; absence must decode to "+
			"nil, which the read side treats as 'we cannot attribute an outcome'", *got.LastConnectRun)
	}
	if got.Status != lineupapi.ConnVerified {
		t.Fatalf("Status = %q, want verified", got.Status)
	}
}

// TestConnection_NilStampSurvivesAWriteAndReadsBackNil: a nil pointer marshals
// to a DynamoDB NULL rather than being omitted. That is harmless — it decodes
// back to nil — but unlike a zero time.Time (which PutConnection deletes,
// because year 1 reads as a real timestamp) it must NOT be special-cased away.
func TestConnection_NilStampSurvivesAWriteAndReadsBackNil(t *testing.T) {
	st := ddbuser.NewWithAPI(ddbusertest.New(), "test-table")
	ctx := context.Background()

	if err := st.PutConnection(ctx, &lineupapi.FantraxConnection{
		UserID: "alice", Status: lineupapi.ConnPending,
	}); err != nil {
		t.Fatalf("PutConnection: %v", err)
	}
	got, ok, err := st.GetConnection(ctx, "alice")
	if err != nil || !ok {
		t.Fatalf("GetConnection = (%v, %v, %v)", got, ok, err)
	}
	if got.LastConnectRun != nil {
		t.Fatalf("LastConnectRun = %+v, want nil", *got.LastConnectRun)
	}
}

// TestStore_ConnectionConformance runs the shared ConnectionStore contract —
// the version precondition on PutConnection (rosterbot-wm9g) — over the same
// in-memory double the UserStore and EnrollmentStore suites use. The
// conditions it exercises are ALSO evaluated by the real service in
// live_test.go; this is the hermetic half.
func TestStore_ConnectionConformance(t *testing.T) {
	conntest.Run(t, func(t *testing.T) lineupapi.ConnectionStore {
		return ddbuser.NewWithAPI(ddbusertest.New(), "test-table")
	})
}

// TestConnection_LegacyItemMigratesOnItsFirstVersionedWrite is the migration
// path for every connection record in production, none of which carries
// `ver` (rosterbot-wm9g). The read must report ConnUnversioned — not "", which
// would turn the next write into a create-that-must-fail — and exactly ONE
// write at that version must land, versioning the row; a second writer still
// holding ConnUnversioned is the pre-migration race and must lose.
func TestConnection_LegacyItemMigratesOnItsFirstVersionedWrite(t *testing.T) {
	api := ddbusertest.New()
	st := ddbuser.NewWithAPI(api, "test-table")
	ctx := context.Background()

	if _, err := api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String("test-table"),
		Item: map[string]types.AttributeValue{
			"pk":     &types.AttributeValueMemberS{Value: "USER#alice"},
			"sk":     &types.AttributeValueMemberS{Value: "FANTRAX"},
			"UserID": &types.AttributeValueMemberS{Value: "alice"},
			"Status": &types.AttributeValueMemberS{Value: string(lineupapi.ConnVerified)},
		},
	}); err != nil {
		t.Fatalf("seed legacy item: %v", err)
	}

	first, ok, err := st.GetConnection(ctx, "alice")
	if err != nil || !ok {
		t.Fatalf("GetConnection = (%v, %v, %v)", first, ok, err)
	}
	if first.Version != lineupapi.ConnUnversioned {
		t.Fatalf("Version on a pre-wm9g record = %q, want ConnUnversioned (%q)", first.Version, lineupapi.ConnUnversioned)
	}
	second := *first // a concurrent reader of the same legacy row

	first.LastError = ""
	if err := st.PutConnection(ctx, first); err != nil {
		t.Fatalf("PutConnection at ConnUnversioned = %v, want success — this is the migration write", err)
	}
	if first.Version == lineupapi.ConnUnversioned || first.Version == "" {
		t.Fatalf("Version after the migration write = %q; the row must now be versioned", first.Version)
	}

	if err := st.PutConnection(ctx, &second); !errors.Is(err, lineupapi.ErrConnectionConflict) {
		t.Fatalf("second PutConnection at ConnUnversioned = %v, want ErrConnectionConflict — the row "+
			"is versioned now, and attribute_not_exists(ver) is what refuses the stale reader", err)
	}
}

// TestConnection_WriteAtAVersionAfterTheRecordIsGoneIsRefused: a tenant
// deletion (DeleteUser removes the FANTRAX item) landing between a connect
// task's read and its write must not resurrect the record — `ver = :v` is
// false against a missing item, and so is the migration branch's
// attribute_exists(pk).
func TestConnection_WriteAtAVersionAfterTheRecordIsGoneIsRefused(t *testing.T) {
	api := ddbusertest.New()
	st := ddbuser.NewWithAPI(api, "test-table")
	ctx := context.Background()

	c := &lineupapi.FantraxConnection{UserID: "alice", Status: lineupapi.ConnPending}
	if err := st.PutConnection(ctx, c); err != nil {
		t.Fatalf("PutConnection (create): %v", err)
	}
	if _, err := api.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String("test-table"),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: "USER#alice"},
			"sk": &types.AttributeValueMemberS{Value: "FANTRAX"},
		},
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	c.Status = lineupapi.ConnVerified
	if err := st.PutConnection(ctx, c); !errors.Is(err, lineupapi.ErrConnectionConflict) {
		t.Fatalf("PutConnection at a version after deletion = %v, want ErrConnectionConflict", err)
	}
	legacy := &lineupapi.FantraxConnection{UserID: "alice", Version: lineupapi.ConnUnversioned}
	if err := st.PutConnection(ctx, legacy); !errors.Is(err, lineupapi.ErrConnectionConflict) {
		t.Fatalf("PutConnection at ConnUnversioned after deletion = %v, want ErrConnectionConflict — "+
			"attribute_exists(pk) is the half of the migration condition that stops a resurrection", err)
	}
	if _, ok, _ := st.GetConnection(ctx, "alice"); ok {
		t.Fatal("a refused write resurrected the deleted record")
	}
}
