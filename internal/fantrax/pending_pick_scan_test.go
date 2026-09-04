package fantrax

import (
	"reflect"
	"testing"
)

// TestScanForDraftPickKeys_FindsCandidateFields pins the presence case: a
// payload carrying fields that look like the TRADE-view's
// draftPickDisplayParts identity shape (rosterbot-uc3's precedent) must be
// named, at any nesting depth and inside arrays, so the diag harness
// (diag_pending_trades_test.go) can point Jon at exactly the JSON path to
// read.
func TestScanForDraftPickKeys_FindsCandidateFields(t *testing.T) {
	raw := []byte(`{
		"responses": [
			{
				"data": {
					"pendingTransactions": {
						"pendingTransactionSets": [
							{
								"id": "abc",
								"transactions": [
									{
										"scorerId": "*99",
										"draftPickDisplayParts": {
											"year": "2027 1st",
											"roundInfo": "(via BOS)"
										}
									}
								]
							}
						],
						"scorerMap": {
							"*99": {
								"name": "",
								"posShortNames": "",
								"round": 1
							}
						}
					}
				}
			}
		]
	}`)

	got := scanForDraftPickKeys(raw)
	want := []string{
		"responses[0].data.pendingTransactions.pendingTransactionSets[0].transactions[0].draftPickDisplayParts",
		"responses[0].data.pendingTransactions.pendingTransactionSets[0].transactions[0].draftPickDisplayParts.roundInfo",
		"responses[0].data.pendingTransactions.scorerMap.*99.round",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scanForDraftPickKeys() =\n%v\nwant\n%v", got, want)
	}
}

// TestScanForDraftPickKeys_HealthyPayloadFindsNothing pins the absence case:
// an ordinary pending-trade payload carrying only player rows (the shape
// fetchPendingTrades already parses) must report no candidates, not a false
// hit off some unrelated key. Without this half, a scanner that always
// returns something would look equally "working" on every payload.
func TestScanForDraftPickKeys_HealthyPayloadFindsNothing(t *testing.T) {
	raw := []byte(`{
		"responses": [
			{
				"data": {
					"pendingTransactions": {
						"pendingTransactionSets": [
							{
								"id": "abc",
								"transactions": [
									{"scorerId": "123", "sourceTeamId": "t1", "destinationTeamId": "t2"}
								]
							}
						],
						"scorerMap": {
							"123": {"name": "Mookie Betts", "posShortNames": "OF"}
						}
					},
					"fantasyTeams": [
						{"id": "t1", "name": "Team One"},
						{"id": "t2", "name": "Team Two"}
					]
				}
			}
		]
	}`)

	got := scanForDraftPickKeys(raw)
	if len(got) != 0 {
		t.Fatalf("scanForDraftPickKeys() = %v, want none", got)
	}
}

// TestScanForDraftPickKeys_MalformedJSONReturnsNil guards the caller: the
// diag harness must not panic on an unexpected envelope shape (e.g. Fantrax
// changing pendingTransactions to an array).
func TestScanForDraftPickKeys_MalformedJSONReturnsNil(t *testing.T) {
	if got := scanForDraftPickKeys([]byte("not json")); got != nil {
		t.Fatalf("scanForDraftPickKeys(invalid) = %v, want nil", got)
	}
}
