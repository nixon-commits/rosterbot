// Package ddbusertest is an in-memory double for the narrow DynamoDB API that
// ddbuser uses.
//
// IT IS NOT A DYNAMODB EMULATOR, and the difference is the point. It recognises
// exactly the condition and filter expressions ddbuser emits, evaluates those
// honestly, and returns an error on anything else. A double that accepted every
// condition would report success for precisely the interleaving the condition
// exists to reject — CLAUDE.md records that lesson about s3blobtest, where a
// fake that rubber-stamped conditional writes would have hidden the bug the
// preconditions were added to close.
//
// The "unknown expression is an error" rule is what keeps it honest over time:
// adding a new condition to ddbuser without teaching this double fails loudly
// instead of silently passing.
package ddbusertest

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type item map[string]types.AttributeValue

// API is the in-memory table. The zero value is not usable; call New.
type API struct {
	mu    sync.Mutex
	items map[string]item // "pk\x00sk" -> item

	// ScanPageSize, when > 0, makes Scan return at most this many items per
	// call with a LastEvaluatedKey, so pagination bugs surface. An unpaginated
	// scan silently truncates in production at 1 MB; a double that always
	// returns everything in one page can never catch that.
	ScanPageSize int

	// AfterGetItem, when set, runs after GetItem has released the table lock
	// and returned its result — never while holding it, or a hook that calls
	// back into this API (BumpVersion included) would deadlock on a mutex
	// that is not reentrant.
	//
	// It exists to simulate the ONE race ddbuser cannot avoid by construction:
	// a concurrent writer landing between a caller's own GetItem (read) and
	// PutItem (conditional write). Nothing else in this double can express
	// that interleaving, because every method here runs a single request to
	// completion before the next one starts.
	AfterGetItem func(pk, sk string)
}

func New() *API { return &API{items: map[string]item{}} }

func key(pk, sk string) string { return pk + "\x00" + sk }

func str(v types.AttributeValue) string {
	if s, ok := v.(*types.AttributeValueMemberS); ok {
		return s.Value
	}
	return ""
}

func keyOf(m map[string]types.AttributeValue) string { return key(str(m["pk"]), str(m["sk"])) }

func clone(i item) item {
	out := item{}
	for k, v := range i {
		out[k] = v
	}
	return out
}

func (a *API) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	a.mu.Lock()
	it, ok := a.items[keyOf(in.Key)]
	var out item
	if ok {
		out = clone(it)
	}
	a.mu.Unlock()

	if a.AfterGetItem != nil {
		a.AfterGetItem(str(in.Key["pk"]), str(in.Key["sk"]))
	}
	if !ok {
		return &dynamodb.GetItemOutput{}, nil
	}
	return &dynamodb.GetItemOutput{Item: out}, nil
}

// BumpVersion increments the profile item's numeric `ver` attribute in place,
// as if an independent writer's PutUser had landed. Test-only: it simulates
// the interleaving AfterGetItem exists to make possible, and is a no-op if the
// item or its `ver` attribute is absent.
func (a *API) BumpVersion(pk, sk string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	it, ok := a.items[key(pk, sk)]
	if !ok {
		return
	}
	cur, ok := it["ver"].(*types.AttributeValueMemberN)
	if !ok {
		return
	}
	n, err := strconv.ParseInt(cur.Value, 10, 64)
	if err != nil {
		return
	}
	it["ver"] = &types.AttributeValueMemberN{Value: strconv.FormatInt(n+1, 10)}
}

// evalCondition understands only the expressions ddbuser emits. Anything else
// is a test failure, not a pass.
func (a *API) evalCondition(expr *string, existing item, exists bool, vals map[string]types.AttributeValue) (bool, error) {
	if expr == nil {
		return true, nil
	}
	switch strings.TrimSpace(*expr) {
	case "attribute_not_exists(pk)":
		return !exists, nil

	case "ver = :v":
		if !exists {
			return false, nil
		}
		want, ok := vals[":v"]
		if !ok {
			return false, fmt.Errorf("ddbusertest: condition %q with no :v value", *expr)
		}
		cur, _ := existing["ver"].(*types.AttributeValueMemberN)
		if cur == nil {
			return false, nil
		}
		// DynamoDB compares TYPE AND VALUE: {"N":"1"} does not equal {"S":"1"},
		// and a mismatch makes this condition permanently false rather than
		// erroring. Comparing only the decimal text made this fake accept a
		// condition the service rejects every time, which is how PutUser shipped
		// sending :v as a string against a numeric `ver` and could never update
		// an existing record — ClaimTeam and token-version revocation both
		// returned ErrUserConflict forever, with the whole conformance suite
		// green (rosterbot-crq.18 follow-up).
		//
		// A type mismatch is therefore a TEST FAILURE here, not a false: a
		// silent false would reproduce the outage faithfully but report it as
		// ordinary optimistic-concurrency contention, which is exactly how it
		// hid the first time.
		wantN, ok := want.(*types.AttributeValueMemberN)
		if !ok {
			return false, fmt.Errorf("ddbusertest: condition %q compares numeric `ver` against a "+
				"%T; DynamoDB matches on type as well as value, so this condition would never be "+
				"true against the real service", *expr, want)
		}
		return cur.Value == wantN.Value, nil

	case "attribute_not_exists(UsedAt)":
		// Conditions on a non-key attribute: the item may exist, but the named
		// attribute must be absent. An implementation that writes a zero
		// time.Time here leaves the attribute present (attributevalue encodes
		// it as a string rather than omitting it), so this correctly never
		// passes for such an item.
		if !exists {
			return true, nil
		}
		_, present := existing["UsedAt"]
		return !present, nil

	case "attribute_not_exists(pk) OR uid = :uid":
		if !exists {
			return true, nil
		}
		return str(existing["uid"]) == str(vals[":uid"]), nil

	default:
		return false, fmt.Errorf("ddbusertest: unrecognised condition expression %q. "+
			"This double evaluates only the expressions ddbuser emits; teach it the "+
			"new one rather than letting it pass unchecked", *expr)
	}
}

func (a *API) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	k := keyOf(in.Item)
	existing, exists := a.items[k]
	ok, err := a.evalCondition(in.ConditionExpression, existing, exists, in.ExpressionAttributeValues)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &types.ConditionalCheckFailedException{}
	}
	a.items[k] = clone(in.Item)
	return &dynamodb.PutItemOutput{}, nil
}

func (a *API) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.items, keyOf(in.Key))
	return &dynamodb.DeleteItemOutput{}, nil
}

// TransactWriteItems is all-or-nothing and reports which item failed, because
// ddbuser maps the cancellation reason back to a specific error (email taken vs
// team taken). A double that reported only "cancelled" would let that mapping
// rot untested.
func (a *API) TransactWriteItems(_ context.Context, in *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	reasons := make([]types.CancellationReason, len(in.TransactItems))
	failed := false
	for i, ti := range in.TransactItems {
		if ti.Put == nil {
			return nil, fmt.Errorf("ddbusertest: only Put is supported in transactions")
		}
		k := keyOf(ti.Put.Item)
		existing, exists := a.items[k]
		ok, err := a.evalCondition(ti.Put.ConditionExpression, existing, exists, ti.Put.ExpressionAttributeValues)
		if err != nil {
			return nil, err
		}
		code := "None"
		if !ok {
			code = "ConditionalCheckFailed"
			failed = true
		}
		reasons[i] = types.CancellationReason{Code: &code}
	}
	if failed {
		return nil, &types.TransactionCanceledException{CancellationReasons: reasons}
	}
	for _, ti := range in.TransactItems {
		a.items[keyOf(ti.Put.Item)] = clone(ti.Put.Item)
	}
	return &dynamodb.TransactWriteItemsOutput{}, nil
}

func (a *API) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// The two conditions the store actually sends: the sk-prefixed credential
	// listing and DeleteUser's whole-item-collection sweep.
	expr := strings.TrimSpace(*in.KeyConditionExpression)
	prefix := ""
	switch expr {
	case "pk = :pk AND begins_with(sk, :sk)":
		prefix = str(in.ExpressionAttributeValues[":sk"])
	case "pk = :pk":
		// every sk under the partition
	default:
		return nil, fmt.Errorf("ddbusertest: unrecognised key condition %q", expr)
	}
	pk := str(in.ExpressionAttributeValues[":pk"])

	var out []map[string]types.AttributeValue
	for _, it := range a.items {
		if str(it["pk"]) == pk && strings.HasPrefix(str(it["sk"]), prefix) {
			out = append(out, clone(it))
		}
	}
	sort.Slice(out, func(i, j int) bool { return str(out[i]["sk"]) < str(out[j]["sk"]) })
	return &dynamodb.QueryOutput{Items: out}, nil
}

func (a *API) Scan(_ context.Context, in *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// The two filters the store actually sends: the fan-out's status-scoped
	// scan and the admin directory's unscoped one. Anything else is a typo in
	// the store, and failing loudly here is this fake's whole job.
	expr := strings.TrimSpace(*in.FilterExpression)
	wantSK := str(in.ExpressionAttributeValues[":sk"])
	match := func(it map[string]types.AttributeValue) bool { return str(it["sk"]) == wantSK }
	switch expr {
	case "sk = :sk AND #st = :active":
		wantStatus := str(in.ExpressionAttributeValues[":active"])
		statusAttr := in.ExpressionAttributeNames["#st"]
		if statusAttr == "" {
			return nil, fmt.Errorf("ddbusertest: filter uses #st with no name mapping; " +
				"status is a DynamoDB reserved word and must be aliased")
		}
		skOnly := match
		match = func(it map[string]types.AttributeValue) bool {
			return skOnly(it) && str(it[statusAttr]) == wantStatus
		}
	case "sk = :sk":
		// no further predicate
	default:
		return nil, fmt.Errorf("ddbusertest: unrecognised filter %q", expr)
	}

	keys := make([]string, 0, len(a.items))
	for k := range a.items {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	start := 0
	if in.ExclusiveStartKey != nil {
		want := keyOf(in.ExclusiveStartKey)
		for i, k := range keys {
			if k == want {
				start = i + 1
				break
			}
		}
	}

	// The page limit applies to items SCANNED, before the filter — matching
	// DynamoDB. That is the detail worth emulating: a page can come back with
	// zero matching items and still carry a continuation key, so a reader that
	// stops when a page looks empty silently loses every tenant beyond it.
	out := []map[string]types.AttributeValue{}
	var last map[string]types.AttributeValue
	scanned := 0
	for i := start; i < len(keys); i++ {
		if a.ScanPageSize > 0 && scanned == a.ScanPageSize {
			it := a.items[keys[i-1]]
			last = map[string]types.AttributeValue{"pk": it["pk"], "sk": it["sk"]}
			break
		}
		it := a.items[keys[i]]
		scanned++
		if match(it) {
			out = append(out, clone(it))
		}
	}
	return &dynamodb.ScanOutput{Items: out, LastEvaluatedKey: last}, nil
}
