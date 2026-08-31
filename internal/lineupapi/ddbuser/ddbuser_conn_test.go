package ddbuser_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
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
