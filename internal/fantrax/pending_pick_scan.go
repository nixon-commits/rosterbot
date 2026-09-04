package fantrax

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// draftPickKeyNeedles are the key-name substrings (case-insensitive) that
// would indicate a JSON field carries draft-pick identity, on the precedent
// set by rosterbot-uc3: the TRADE-view history endpoint encodes pick identity
// in a "draftPickDisplayParts" object with "year"/"roundInfo" fields (see
// go-fantrax's auth_client/parser/transaction_parser.go). The pending-offer
// payload (internal/fantrax.fetchPendingTrades' pendingTransactions envelope)
// has never been observed to carry a pick, so this scans broadly rather than
// assuming the same field name is reused verbatim.
var draftPickKeyNeedles = []string{"draftpick", "pick", "round", "displayparts"}

// scanForDraftPickKeys walks a raw JSON blob (typically the response of
// auth_client.GetLeagueHomeInfoRaw) and returns the sorted, deduplicated
// dotted paths of every object key whose name contains one of
// draftPickKeyNeedles, case-insensitively. It is the pure helper behind
// TestDiagPendingTradesDraftPickIdentity (diag_pending_trades_test.go,
// //go:build diag) — that harness needs live Fantrax credentials and a real
// pending trade offer to run, so the key-matching logic it depends on is kept
// here, outside the diag build tag, so it has a hermetic, always-run test.
//
// Malformed JSON returns nil rather than panicking, since the diag harness
// runs this against a live API response whose shape is exactly what's in
// question.
func scanForDraftPickKeys(raw []byte) []string {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}

	var found []string
	var walk func(node any, path string)
	walk = func(node any, path string) {
		switch v := node.(type) {
		case map[string]any:
			for key, child := range v {
				childPath := key
				if path != "" {
					childPath = path + "." + key
				}
				if keyLooksLikeDraftPick(key) {
					found = append(found, childPath)
				}
				walk(child, childPath)
			}
		case []any:
			for i, child := range v {
				walk(child, path+"["+strconv.Itoa(i)+"]")
			}
		}
	}
	walk(doc, "")

	sort.Strings(found)
	return found
}

// keyLooksLikeDraftPick reports whether a single JSON object key name matches
// one of draftPickKeyNeedles, case-insensitively.
func keyLooksLikeDraftPick(key string) bool {
	lower := strings.ToLower(key)
	for _, needle := range draftPickKeyNeedles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}
