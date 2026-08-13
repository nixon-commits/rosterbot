package ddbuser_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser/ddbusertest"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/usertest"
)

func cred(id string) webauthn.Credential {
	return webauthn.Credential{ID: []byte(id), PublicKey: []byte("pk-" + id)}
}

func queryWithCondition(expr string) *dynamodb.QueryInput {
	return &dynamodb.QueryInput{
		TableName:              aws.String("test-table"),
		KeyConditionExpression: aws.String(expr),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: "USER#a"},
		},
	}
}

func TestStore_Conformance(t *testing.T) {
	usertest.Run(t, func(t *testing.T) lineupapi.UserStore {
		return ddbuser.NewWithAPI(ddbusertest.New(), "test-table")
	})
}

// TestListActive_PaginatesTheScan pins the one behaviour a single-page double
// can never show. DynamoDB applies a scan's page limit BEFORE the filter, so a
// page can contain no matching profiles and still carry a continuation key. A
// reader that stops on an empty page drops every tenant past that boundary —
// and the fan-out would then simply not run their jobs, with nothing failing
// anywhere to say so.
func TestListActive_PaginatesTheScan(t *testing.T) {
	ctx := context.Background()
	api := ddbusertest.New()
	api.ScanPageSize = 2 // small enough to force several pages
	store := ddbuser.NewWithAPI(api, "test-table")

	const want = 7
	for _, name := range []lineupapi.UserID{"a", "b", "c", "d", "e", "f", "g"} {
		u := &lineupapi.User{
			ID: name, Email: string(name) + "@example.test",
			Role: lineupapi.RoleMember, Status: lineupapi.UserActive,
		}
		if err := store.CreateUser(ctx, u); err != nil {
			t.Fatalf("CreateUser(%s): %v", name, err)
		}
		// Each user also writes an EMAIL# pointer item, so the raw item count
		// is well above the profile count and pages land mid-stream.
		if err := store.PutCredential(ctx, name, cred(string(name))); err != nil {
			t.Fatalf("PutCredential(%s): %v", name, err)
		}
	}

	got, err := store.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(got) != want {
		t.Fatalf("ListActive returned %d users, want %d — the scan stopped early, "+
			"so the job fan-out would silently skip the missing tenants", len(got), want)
	}
}

// TestUnknownConditionIsRejected guards the double itself. Its value depends
// entirely on refusing expressions it does not understand: the moment it starts
// waving one through, every conditional write in ddbuser is untested and the
// suite goes green regardless.
func TestUnknownConditionIsRejected(t *testing.T) {
	api := ddbusertest.New()
	_, err := api.Query(context.Background(), queryWithCondition("pk = :pk"))
	if err == nil {
		t.Fatal("double accepted an unrecognised key condition; it must fail loudly " +
			"so a new expression cannot pass unchecked")
	}
}

// TestScanFilterMatchesTheMarshalledAttribute pins the two facts that made
// ListActive silently return nothing on the first attempt.
//
// attributevalue keys on the Go FIELD name, honouring `dynamodbav` tags and
// ignoring `json` ones — so User.Status marshals to "Status", and a filter
// written against "status" matches no item at all. ListActive would then return
// zero users forever, the fan-out would run for nobody, and nothing anywhere
// would fail. A rename of the Go field reintroduces exactly that, so the name
// is asserted rather than trusted.
//
// The same tag blindness means User.Version — `json:"-"` — WOULD be persisted,
// leaving a stale copy of the optimistic-concurrency token inside the body it
// describes, shadowing the real `ver` attribute.
func TestScanFilterMatchesTheMarshalledAttribute(t *testing.T) {
	ctx := context.Background()
	api := ddbusertest.New()
	store := ddbuser.NewWithAPI(api, "test-table")

	u := &lineupapi.User{ID: "alice", Email: "a@example.test", Status: lineupapi.UserActive}
	if err := store.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	item, err := api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String("test-table"),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: "USER#alice"},
			"sk": &types.AttributeValueMemberS{Value: "PROFILE"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := item.Item["Status"]; !ok {
		keys := make([]string, 0, len(item.Item))
		for k := range item.Item {
			keys = append(keys, k)
		}
		t.Fatalf("stored item has no \"Status\" attribute (has %v); ListActive's "+
			"filter names it, so a mismatch makes the scan match nothing and the "+
			"fan-out run for no one", keys)
	}
	if _, ok := item.Item["Version"]; ok {
		t.Error("stored item carries a \"Version\" attribute; the opaque " +
			"concurrency token must never be persisted into the body it describes, " +
			"where it would shadow the real `ver` attribute")
	}
	if _, ok := item.Item["ver"]; !ok {
		t.Error("stored item has no `ver` attribute; conditional writes have nothing to assert on")
	}
}
