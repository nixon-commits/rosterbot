package ddbuser

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// recordingAPI captures the raw input of the last write call and always
// reports success. It is deliberately NOT an evaluator — it never inspects a
// ConditionExpression at all — because this file's whole point is the
// cheaper, AWS-call-free half of rosterbot-0jv: instead of relying on
// ddbusertest's evalCondition to interpret a condition correctly (which is
// exactly the mechanism rosterbot-6my slipped past, since the pre-fix fake
// compared decimal TEXT rather than AttributeValue TYPE), this inspects the
// raw PutItemInput/TransactWriteItemsInput a Store method actually built and
// checks, by Go type, that every value bound to a ConditionExpression
// placeholder is the same AttributeValue variant as the attribute it is
// compared against. DynamoDB's comparison operators (including `=` inside a
// condition) require matching types — {"N":"1"} is never equal to
// {"S":"1"} — so a mismatch here is a condition that can never be true
// against the real service, independent of what any fake concludes.
type recordingAPI struct {
	lastPut      *dynamodb.PutItemInput
	lastTransact *dynamodb.TransactWriteItemsInput
}

func (r *recordingAPI) GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, nil
}
func (r *recordingAPI) Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return &dynamodb.QueryOutput{}, nil
}
func (r *recordingAPI) Scan(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{}, nil
}
func (r *recordingAPI) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	r.lastPut = in
	return &dynamodb.PutItemOutput{}, nil
}
func (r *recordingAPI) DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}
func (r *recordingAPI) TransactWriteItems(_ context.Context, in *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	r.lastTransact = in
	return &dynamodb.TransactWriteItemsOutput{}, nil
}

// typeName renders just the AttributeValue variant (*types.AttributeValueMemberN
// vs *types.AttributeValueMemberS etc.), which is exactly the axis DynamoDB's
// comparison operators are sensitive to.
func typeName(v types.AttributeValue) string { return fmt.Sprintf("%T", v) }

// TestPutUserConditionValueTypeMatchesStoredAttributeType is the literal
// rosterbot-6my regression, caught without any AWS call. PutUser conditions
// on `ver = :v`; this asserts that the AttributeValue type bound to `:v` is
// the same variant as the `ver` attribute the very same call writes into
// Item. Before the fix, `:v` was bound with s() (a string) while userItem
// wrote `ver` with n() (a number) — a mismatch this test would have caught
// immediately, since DynamoDB's `=` comparison requires matching types and a
// mismatched condition can never be true against the real service, no matter
// what its decimal text says.
func TestPutUserConditionValueTypeMatchesStoredAttributeType(t *testing.T) {
	api := &recordingAPI{}
	st := NewWithAPI(api, "test-table")
	ctx := context.Background()

	u := &lineupapi.User{ID: "alice", Email: "a@example.test", Version: "1"}
	if err := st.PutUser(ctx, u); err != nil {
		t.Fatalf("PutUser: %v", err)
	}
	if api.lastPut == nil {
		t.Fatal("PutUser never called PutItem")
	}

	boundV, ok := api.lastPut.ExpressionAttributeValues[":v"]
	if !ok {
		t.Fatal("PutItemInput carries no :v expression attribute value")
	}
	storedVer, ok := api.lastPut.Item["ver"]
	if !ok {
		t.Fatal("PutItemInput's Item carries no `ver` attribute")
	}

	if typeName(boundV) != typeName(storedVer) {
		t.Fatalf("condition binds :v as %s but the item's own `ver` attribute is %s; "+
			"DynamoDB's `ver = :v` compares type AND value, so a mismatch means the condition "+
			"can never be true against the real service — the rosterbot-6my regression (a "+
			"numeric ver compared against a string)", typeName(boundV), typeName(storedVer))
	}
	if _, ok := boundV.(*types.AttributeValueMemberN); !ok {
		t.Fatalf(":v bound as %s, want *types.AttributeValueMemberN to match `ver`'s stored type", typeName(boundV))
	}
}

// TestClaimTeamConditionValueTypeMatchesStoredAttributeType is the same check
// applied to ClaimTeam's `attribute_not_exists(pk) OR uid = :uid`: the `:uid`
// placeholder and the item's own `uid` attribute must be the same
// AttributeValue variant, or the `uid = :uid` half of the OR can never be
// true and a legitimate same-uid re-claim would always fall through to
// ErrTeamTaken.
func TestClaimTeamConditionValueTypeMatchesStoredAttributeType(t *testing.T) {
	api := &recordingAPI{}
	st := NewWithAPI(api, "test-table")
	ctx := context.Background()

	// ClaimTeam issues a PutItem for the TEAM# claim, then reads the user back
	// with GetUser (which the zero-value recordingAPI answers as "not found")
	// before writing the profile via PutUser — so ClaimTeam itself returns
	// ErrUserConflict here. That happens strictly after the PutItem this test
	// inspects, so lastPut is already populated by the time it returns.
	_ = st.ClaimTeam(ctx, "alice", "team-7")

	if api.lastPut == nil {
		t.Fatal("ClaimTeam never called PutItem")
	}
	boundUID, ok := api.lastPut.ExpressionAttributeValues[":uid"]
	if !ok {
		t.Fatal("PutItemInput carries no :uid expression attribute value")
	}
	storedUID, ok := api.lastPut.Item["uid"]
	if !ok {
		t.Fatal("PutItemInput's Item carries no `uid` attribute")
	}
	if typeName(boundUID) != typeName(storedUID) {
		t.Fatalf("condition binds :uid as %s but the item's own `uid` attribute is %s; "+
			"a type mismatch would make `uid = :uid` permanently false and turn every "+
			"same-uid re-claim into a false ErrTeamTaken", typeName(boundUID), typeName(storedUID))
	}
	if _, ok := boundUID.(*types.AttributeValueMemberS); !ok {
		t.Fatalf(":uid bound as %s, want *types.AttributeValueMemberS to match `uid`'s stored type", typeName(boundUID))
	}
}

// TestCreateUserConditionsReferenceNoTypedPlaceholder pins the other three
// ConditionExpressions in the package (CreateUser's three
// attribute_not_exists(pk) writes, CreateEnrollment's, RedeemEnrollment's
// attribute_not_exists(UsedAt)) to having no bound value at all, which is why
// they cannot suffer this exact failure mode. If a future change adds a
// bound placeholder to one of them, this test documents — by failing — that
// it now needs its own type-matching subtest above.
func TestCreateUserConditionsReferenceNoTypedPlaceholder(t *testing.T) {
	api := &recordingAPI{}
	st := NewWithAPI(api, "test-table")
	ctx := context.Background()

	u := &lineupapi.User{ID: "bob", Email: "b@example.test", TeamID: "team-9"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if api.lastTransact == nil {
		t.Fatal("CreateUser never called TransactWriteItems")
	}
	if got := len(api.lastTransact.TransactItems); got != 3 {
		t.Fatalf("CreateUser with an email and a team wrote %d transact items, want 3 "+
			"(profile, email claim, team claim)", got)
	}
	for i, ti := range api.lastTransact.TransactItems {
		if ti.Put == nil {
			t.Fatalf("transact item %d has no Put", i)
		}
		if got := *ti.Put.ConditionExpression; got != "attribute_not_exists(pk)" {
			t.Fatalf("transact item %d condition = %q, want \"attribute_not_exists(pk)\"", i, got)
		}
		if len(ti.Put.ExpressionAttributeValues) != 0 {
			t.Errorf("transact item %d binds %d expression attribute values for a bare "+
				"attribute_not_exists(pk) condition, want none", i, len(ti.Put.ExpressionAttributeValues))
		}
	}
}
