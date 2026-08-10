package tradeboard

import (
	"fmt"
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
	"github.com/nixon-commits/rosterbot/internal/playername"
	"github.com/nixon-commits/rosterbot/internal/tradevalue"
)

// offersFilename is the leaf object name in every partition.
const offersFilename = "offers.ndjson"

// Outcome is what became of an offer.
type Outcome string

const (
	// OutcomeOpen means the offer was still pending when last observed.
	OutcomeOpen Outcome = "open"
	// OutcomeAccepted means the offer left the pending list and a matching
	// trade appeared in the executed feed.
	OutcomeAccepted Outcome = "accepted"
	// OutcomeNotExecuted means the offer left the pending list with no
	// matching executed trade.
	//
	// It is deliberately NOT called "rejected". Fantrax reports nothing
	// about why an offer disappeared, and declining it, the other team
	// withdrawing it, and it expiring are indistinguishable in the data.
	// Naming this "rejected" would attribute a decision to you that you may
	// never have made.
	OutcomeNotExecuted Outcome = "not-executed"
)

// LogRow is one observed offer in the durable log.
//
// The log exists because a pending offer is unrecoverable once it resolves:
// Fantrax keeps no history of trades that did not happen, so an offer not
// captured while open is gone. That is the same property that makes the Team
// Value Store NoBackfill (docs/adr/0002). Executed trades, by contrast, are
// re-fetchable forever and are not logged here.
//
// The valuation is stored as-of observation rather than recomputed on read,
// because HKB values move daily and the question the log answers is "was that
// a good offer at the time", not "what would it be worth now".
type LogRow struct {
	Dt        string             `json:"dt"`
	TradeID   string             `json:"trade_id"`
	MyTeam    string             `json:"my_team"`
	FirstSeen time.Time          `json:"first_seen"`
	LastSeen  time.Time          `json:"last_seen"`
	Outcome   Outcome            `json:"outcome"`
	Sides     []Side             `json:"sides"`
	Verdict   tradevalue.Verdict `json:"verdict"`
}

// ExecutedAsset is one row of an executed trade, used only to decide whether
// a vanished offer was accepted.
type ExecutedAsset struct {
	TradeGroupID string
	Player       string
	To           string
}

// InferOutcome decides what happened to an offer that is no longer pending.
//
// The match is on the offer's set of (normalized player, receiving team)
// pairs against each executed trade group's. Fantrax assigns the executed
// trade a different id from the pending one, so there is no key to join on
// and the asset set is the only available evidence.
//
// The rule is deliberately strict: an exact set match means accepted,
// anything else means not-executed. A loose match would let an unrelated
// trade between the same two teams claim the offer, which turns a missing
// record into a wrong one.
func InferOutcome(offer Offer, executed []ExecutedAsset) Outcome {
	want := assetSet(offerAssets(offer))
	if len(want) == 0 {
		return OutcomeNotExecuted
	}

	groups := map[string][]ExecutedAsset{}
	for _, e := range executed {
		groups[e.TradeGroupID] = append(groups[e.TradeGroupID], e)
	}
	for _, g := range groups {
		if setsEqual(want, assetSet(g)) {
			return OutcomeAccepted
		}
	}
	return OutcomeNotExecuted
}

func offerAssets(o Offer) []ExecutedAsset {
	var out []ExecutedAsset
	for _, s := range o.Sides {
		for _, a := range s.Assets {
			name := a.Name
			if a.IsPick {
				name = "" // matches the empty name Fantrax gives pick rows
			}
			out = append(out, ExecutedAsset{Player: name, To: s.Team})
		}
	}
	return out
}

func assetSet(as []ExecutedAsset) map[string]int {
	m := map[string]int{}
	for _, a := range as {
		m[playername.Normalize(a.Player)+"|"+a.To]++
	}
	return m
}

func setsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// MergeLog folds the currently-pending offers into the previously logged
// rows, returning the full set for today's partition.
//
// Rows are matched by TradeID, which is stable while an offer is open.
// FirstSeen is preserved from the earliest observation; LastSeen advances
// while the offer is still pending. An offer that has dropped off the pending
// list gets its outcome inferred once and then stops moving.
func MergeLog(now time.Time, myTeam string, prior []LogRow, current []Offer, executed []ExecutedAsset) []LogRow {
	dt := now.UTC().Format("2006-01-02")

	byID := make(map[string]LogRow, len(prior))
	for _, r := range prior {
		byID[r.TradeID] = r
	}
	open := make(map[string]Offer, len(current))
	for _, o := range current {
		open[o.TradeID] = o
	}

	for _, o := range current {
		r, seen := byID[o.TradeID]
		if !seen {
			r = LogRow{TradeID: o.TradeID, MyTeam: myTeam, FirstSeen: now}
		}
		r.Dt = dt
		r.LastSeen = now
		r.Outcome = OutcomeOpen
		r.Sides = o.Sides
		r.Verdict = o.Verdict
		byID[o.TradeID] = r
	}

	for id, r := range byID {
		if _, stillOpen := open[id]; stillOpen || r.Outcome != OutcomeOpen {
			continue // still pending, or already resolved on an earlier run
		}
		r.Outcome = InferOutcome(Offer{TradeID: id, Sides: r.Sides}, executed)
		byID[id] = r
	}

	out := make([]LogRow, 0, len(byID))
	for _, r := range byID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TradeID < out[j].TradeID })
	return out
}

// Latest collapses the log to one row per TradeID, keeping the most recently
// observed.
//
// Each daily partition is a cumulative snapshot -- MergeLog carries every
// known offer forward -- so the newest partition alone is the complete state
// and reading the whole series yields each offer once per day it was known.
// The volume makes that a non-issue (a season is a few dozen offers), and the
// property earned is that any single partition is self-describing rather than
// a delta that only means something applied to its predecessors.
func Latest(rows []LogRow) []LogRow {
	byID := map[string]LogRow{}
	for _, r := range rows {
		if prev, ok := byID[r.TradeID]; ok && prev.LastSeen.After(r.LastSeen) {
			continue
		}
		byID[r.TradeID] = r
	}
	out := make([]LogRow, 0, len(byID))
	for _, r := range byID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TradeID < out[j].TradeID })
	return out
}

// Writer persists a day's offer log rows.
type Writer interface {
	WriteOffers(date time.Time, rows []LogRow) error
}

// Reader loads the whole offer log.
type Reader interface {
	ReadAll() ([]LogRow, error)
}

// MarshalNDJSON serializes rows as newline-delimited JSON (one row per line).
func MarshalNDJSON(rows []LogRow) ([]byte, error) { return ndjsonstore.Marshal(rows) }

// UnmarshalNDJSON parses newline-delimited JSON (one LogRow per line).
func UnmarshalNDJSON(b []byte) ([]LogRow, error) { return ndjsonstore.Unmarshal[LogRow](b) }

func objectKey(date time.Time) string {
	return fmt.Sprintf("dt=%s/%s", date.UTC().Format("2006-01-02"), offersFilename)
}

// ObjectKey is the store-relative partition key (dt=YYYY-MM-DD/offers.ndjson).
func ObjectKey(date time.Time) string { return objectKey(date) }

type writer struct{ store ndjsonstore.Store }

// NewWriter returns a Writer persisting rows to store.
func NewWriter(store ndjsonstore.Store) Writer { return writer{store: store} }

// NewFileWriter returns a Writer over a local directory root.
func NewFileWriter(root string) Writer { return NewWriter(ndjsonstore.NewFileStore(root)) }

func (w writer) WriteOffers(date time.Time, rows []LogRow) error {
	return ndjsonstore.Write(w.store, objectKey(date), rows)
}

type reader struct{ store ndjsonstore.Store }

// NewReader returns a Reader over rows in store.
func NewReader(store ndjsonstore.Store) Reader { return reader{store: store} }

// NewFileReader returns a Reader over a local directory root.
func NewFileReader(root string) Reader { return NewReader(ndjsonstore.NewFileStore(root)) }

func (r reader) ReadAll() ([]LogRow, error) {
	// Empty prefix: every dt= partition sits directly under the store root.
	return ndjsonstore.ReadAll[LogRow](r.store, "", offersFilename, nil)
}
